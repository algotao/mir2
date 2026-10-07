package gamesvr

import (
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/wire"
	"github.com/algotao/mir2/server/internal/world"
)

// 本文件守住审计 P1-5（共享实体状态的未同步读写）与 P1-6（持 s.mu 写 socket）。
//
// ⚠️ 其中三条并发用例的**判定力来自 `-race`**：
//
//	go test -race -run 'LockDomain|RaceFree|DoesNotWriteUnderWorldLock|FireCharge|ViewTracker' \
//	    ./internal/gamesvr ./internal/entity
//
// 不带 `-race` 时它们只验证"功能没崩"，不验证有没有竞态。

// ---------- P1-6：持 s.mu 时不做 socket I/O ----------

// blockingConn 是"写会一直阻塞"的假连接：`Write` 先通知测试"我开始写了"，
// 然后卡在 release 上，直到测试放行。
//
// ⚠️ `SetWriteDeadline` 是**空实现**（不生效）：超时策略不该掩盖
// "发送时是否仍持有世界锁"这条断言 —— 那条断言只关心锁有没有被放开。
type blockingConn struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingConn() *blockingConn {
	return &blockingConn{started: make(chan struct{}), release: make(chan struct{})}
}

func (c *blockingConn) Write(b []byte) (int, error) {
	c.once.Do(func() { close(c.started) })
	<-c.release
	return len(b), nil
}
func (c *blockingConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *blockingConn) Close() error                     { return nil }
func (c *blockingConn) LocalAddr() net.Addr              { return recordingAddr{} }
func (c *blockingConn) RemoteAddr() net.Addr             { return recordingAddr{} }
func (c *blockingConn) SetDeadline(time.Time) error      { return nil }
func (c *blockingConn) SetReadDeadline(time.Time) error  { return nil }
func (c *blockingConn) SetWriteDeadline(time.Time) error { return nil }

// newLockDomainServer 造一个只够跑"视野/坐标/属性"这几条路径的最小 Server。
func newLockDomainServer() *Server {
	return &Server{
		cfg: configState{viewRange: 12},
		world: worldState{
			players:    map[uint32]*Player{},
			index:      world.NewSpatialIndex(32),
			monsters:   map[uint32]*entity.Monster{},
			monsterIdx: world.NewSpatialIndex(32),
		},
	}
}

// TestUpdateVisionDoesNotWriteUnderWorldLock 守住 P1-6：
// `updateVision` 发包时**不得**持有 `s.mu`。
//
// 手法：让**收件人**的 `Write` 一直阻塞，然后看**另一个 goroutine 能不能拿到
// s.mu**。修复前，第二段"别人对 p 的可见性"是持 s.mu 调 `sendPlayerAppear`
// ⇒ 写卡住时锁也卡住 ⇒ 这条断言超时失败。
//
// ⚠️ 用例的**前提**很讲究：必须让唯一那一包落在"第二段"里。
//
//	`entered`（p 自己的视野差集）要先填好 `p.visible`，否则第一段就会先给
//	p 发包 —— 那一段本来就在锁外，写阻塞了也照样能拿到锁 ⇒ 用例会白过。
//
//	所以：p 已经"看见"other（entered 为空），而 other 还没"看见"p
//	（第二段要给他补一条 SM_TURN）。阻塞的正是 other 那条连接。
func TestUpdateVisionDoesNotWriteUnderWorldLock(t *testing.T) {
	m := world.Generate("锁域图", 40, 40, false)
	s := newLockDomainServer()

	p := newTestPlayer(1, "主角", entity.JobWarr)
	p.Obj.SetPlace(m, 10, 10, p.Obj.Facing())
	p.visible = entity.NewViewTracker()
	p.conn = &recordingConn{}
	s.world.players[p.Obj.ID] = p

	// 旁观者在 p 的视野里（p 已"看见"他 ⇒ 第一段不发包）
	other := newTestPlayer(2, "旁观者", entity.JobWarr)
	other.Obj.SetPlace(m, 11, 10, other.Obj.Facing())
	other.visible = entity.NewViewTracker()
	bc := newBlockingConn()
	other.conn = bc
	s.world.players[other.Obj.ID] = other
	s.world.index.Add(other)
	p.visible.Add(other.Obj.ID)

	done := make(chan struct{})
	go func() {
		s.updateVision(p)
		close(done)
	}()

	select {
	case <-bc.started:
	case <-time.After(3 * time.Second):
		close(bc.release)
		t.Fatal("updateVision 没有给旁观者发包（用例前提不成立）")
	}

	// 关键断言：此刻世界锁必须还能拿到
	locked := make(chan struct{})
	go func() {
		s.mu.Lock()
		s.mu.Unlock()
		close(locked)
	}()
	select {
	case <-locked:
	case <-time.After(2 * time.Second):
		close(bc.release)
		<-done
		t.Fatal("updateVision 在持有 s.mu 时写 socket（审计 P1-6）")
	}

	close(bc.release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("updateVision 没有在写完后返回")
	}
}

// ---------- P1-5：共享实体状态 ----------

// TestMoveAndVisionShareWorldLock 守住 P1-5 的第一条：坐标的写（移动）与
// 跨 goroutine 的读（视野同步 / 广播过滤）必须在**同一个锁域**里。
//
// ⚠️ 走的是**真实入口** `handleMove`（不是直接调 `movePlayer`）：这样若有人
// 把 handleMove 改回"先 MoveTo 改坐标、再只锁索引"，`-race` 会立刻报出来。
func TestMoveAndVisionShareWorldLock(t *testing.T) {
	m := world.Generate("锁域图", 200, 200, false)
	s := newLockDomainServer()

	owner := newTestPlayer(1, "走位者", entity.JobWarr)
	owner.Obj.SetPlace(m, 100, 100, owner.Obj.Facing())
	owner.visible = entity.NewViewTracker()
	owner.conn = &recordingConn{}
	owner.logonDone = true
	owner.Limiter = &entity.MoveLimiter{} // 零值 ⇒ 不限流，方便循环走位
	s.world.players[owner.Obj.ID] = owner
	s.world.index.Add(owner)

	watcher := newTestPlayer(2, "旁观者", entity.JobWarr)
	watcher.Obj.SetPlace(m, 101, 100, watcher.Obj.Facing())
	watcher.visible = entity.NewViewTracker()
	watcher.conn = &recordingConn{}
	s.world.players[watcher.Obj.ID] = watcher
	s.world.index.Add(watcher)

	var wg sync.WaitGroup
	wg.Add(3)
	// ① 写坐标：真实移动入口（必须持 s.mu，并同步空间索引）
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			pkt := wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_WALK, 0, 0, uint16(i%8), 0)}
			s.handleMove(owner.conn, owner, pkt, false)
		}
	}()
	// ② 读坐标：视野差集（锁内读 other 的 Map/Distance）
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			s.updateVision(watcher)
		}
	}()
	// ③ 读坐标：广播收件人过滤（锁内读候选玩家的 Map/Distance）
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			s.broadcastToViewers(m, 100, 100, func(o *Player) {
				_ = s.statusBit(o)
			})
		}
	}()
	wg.Wait()
}

// TestStatusBitSharesWorldLock 守住 P1-5 里 `Object.Status` 那一条：
// 本人 goroutine 写（broadcastStatus）与 ticker 读（tickPlayerStatus）同锁域。
func TestStatusBitSharesWorldLock(t *testing.T) {
	s := newLockDomainServer()
	p := newTestPlayer(1, "中毒者", entity.JobWarr)
	p.visible = entity.NewViewTracker()
	p.conn = &recordingConn{}
	s.world.players[p.Obj.ID] = p

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			s.setStatusBit(p, uint32(i))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			_ = s.statusBit(p)
		}
	}()
	wg.Wait()
	if got := s.statusBit(p); got != 499 {
		t.Fatalf("状态快照 = %d，期望最后一次写入 499", got)
	}
}

// TestRegenAndHurtShareAbilLock 守住 P1-5 的第二条：ticker 读 `Abil`
// （`tickMapHP` / `regenOnce`）必须走**持锁快照**，不能裸读字段。
func TestRegenAndHurtShareAbilLock(t *testing.T) {
	m := world.Generate("扣血图", 20, 20, false)
	s := newLockDomainServer()
	// DECHP/INCHP 都打开：两个分支都会被 ticker 走到
	s.indexMapInfos([]*data.MapInfo{{
		ID:       m.Name,
		DecHPSet: true, DecHPPoint: 1, DecHPTime: 1,
		IncHPSet: true, IncHPPoint: 1, IncHPTime: 1,
	}})

	p := newTestPlayer(1, "挨打者", entity.JobWarr)
	p.Obj.SetPlace(m, 5, 5, p.Obj.Facing())
	p.visible = entity.NewViewTracker()
	p.conn = &recordingConn{}
	s.world.players[p.Obj.ID] = p

	var wg sync.WaitGroup
	wg.Add(4)
	// ① 别的 goroutine 扣血（实战里是攻击者）
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			p.hurt(3)
		}
	}()
	// ② ticker：地图级扣血/加血（原来在这里裸读 ab.Hp/ab.Mp/ab.MaxHp 组包）
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			s.tickMapHP(p, time.Now())
		}
	}()
	// ③ ticker：自然恢复
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			s.regenOnce()
		}
	}()
	// ④ 本人 goroutine：吃药/放技能也会改血蓝
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			p.addHP(2)
			p.addMP(1)
		}
	}()
	wg.Wait()

	if hp := p.hp(); hp > p.maxHP() {
		t.Fatalf("血量越界: %d > %d", hp, p.maxHP())
	}
}

// TestFireChargeIsRaceFree 守住 P1-5 的第三条：`fireHit/fireHitAt`
// 由玩家 goroutine（点燃/消费）与 regenLoop 的 ticker（到期作废）同时读写。
func TestFireChargeIsRaceFree(t *testing.T) {
	p := newTestPlayer(1, "战士", entity.JobWarr)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			p.armFireCharge(time.Now(), 0)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			p.consumeFireCharge(time.Now())
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			p.expireFireCharge(time.Now(), 20*time.Second)
			_, _ = p.fireChargeSnapshot()
		}
	}()
	wg.Wait()
}

// TestFireChargeAccessors 固定这四个访问器的语义（原版 ObjBase.pas:9784-9791 /
// :6425-6433 / :22131）：冷却内不可重复点燃、消费会刷新计时、超期才作废且只报一次。
func TestFireChargeAccessors(t *testing.T) {
	p := newTestPlayer(1, "战士", entity.JobWarr)
	now := time.Now()

	if armed, _ := p.fireChargeSnapshot(); armed {
		t.Fatal("初始不该已点燃")
	}
	if !p.armFireCharge(now, fireHitArmGap) {
		t.Fatal("首次点燃应当成功")
	}
	if armed, at := p.fireChargeSnapshot(); !armed || !at.Equal(now) {
		t.Fatalf("点燃后快照 = (%v, %v)，期望 (true, %v)", armed, at, now)
	}
	if p.armFireCharge(now.Add(time.Second), fireHitArmGap) {
		t.Fatal("冷却内不该再次点燃")
	}
	if p.consumeFireCharge(now.Add(2*time.Second)) == false {
		t.Fatal("应消费到充能")
	}
	// 消费即刷新计时（"Jacky 禁止双烈火"）
	if armed, at := p.fireChargeSnapshot(); armed || !at.Equal(now.Add(2*time.Second)) {
		t.Fatalf("消费后快照 = (%v, %v)，期望 (false, %v)", armed, at, now.Add(2*time.Second))
	}
	if p.consumeFireCharge(now) {
		t.Fatal("没有充能时不该消费成功")
	}

	armedAt := now.Add(time.Minute)
	if !p.armFireCharge(armedAt, 0) {
		t.Fatal("冷却是节奏类时间，隔了一分钟应当可以再点燃")
	}
	if p.expireFireCharge(armedAt.Add(time.Second), fireHitLife) {
		t.Fatal("未到有效期不该作废")
	}
	if !p.expireFireCharge(armedAt.Add(fireHitLife+time.Second), fireHitLife) {
		t.Fatal("超过有效期应当作废")
	}
	if p.expireFireCharge(armedAt.Add(2*fireHitLife), fireHitLife) {
		t.Fatal("已经作废过就不该再报一次")
	}
}
