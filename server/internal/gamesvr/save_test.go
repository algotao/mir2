package gamesvr

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/storage"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
)

// 自动存档的并发回归测试（见 saveAll / saveSnapshotOf 的注释）。
//
// 病：自动存档跑在 ticker goroutine 上，却直接序列化**在线对象** `p.Char`
// ⇒ 玩家此刻正在吃药/捡钱/换装/交易，读到的既是半截状态、又全是 data race。
// 修：把"做快照"投给玩家自己的 goroutine（`Player.snapReq`），
// 拿到深拷贝再落盘。

// captureStore 是只实现存档那一条路的假 Store（其余方法留空）。
type captureStore struct {
	mu    sync.Mutex
	saved []*storage.Character
}

func (c *captureStore) Characters() storage.CharacterStore { return &captureCharStore{c: c} }
func (c *captureStore) Accounts() storage.AccountStore     { return nil }
func (c *captureStore) Sessions() storage.SessionStore     { return nil }
func (c *captureStore) Guilds() storage.GuildStore         { return nil }
func (c *captureStore) Castles() storage.CastleStore       { return nil }
func (c *captureStore) Ping(context.Context) error         { return nil }
func (c *captureStore) Close() error                       { return nil }

func (c *captureStore) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.saved)
}

func (c *captureStore) last() *storage.Character {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.saved) == 0 {
		return nil
	}
	return c.saved[len(c.saved)-1]
}

type captureCharStore struct{ c *captureStore }

func (cs *captureCharStore) Update(_ context.Context, ch *storage.Character) error {
	cs.c.mu.Lock()
	cs.c.saved = append(cs.c.saved, ch)
	cs.c.mu.Unlock()
	return nil
}
func (cs *captureCharStore) UpdateForSession(ctx context.Context, ch *storage.Character, _ int32) error {
	return cs.Update(ctx, ch)
}
func (cs *captureCharStore) Create(context.Context, *storage.Character) error { return nil }
func (cs *captureCharStore) GetByName(context.Context, string) (*storage.Character, error) {
	return nil, nil
}
func (cs *captureCharStore) ListByAccount(context.Context, string) ([]*storage.Character, error) {
	return nil, nil
}
func (cs *captureCharStore) MarkDeleted(context.Context, int64) error { return nil }

// saveTestPlayer 造一个挂了快照通道的在线玩家（够 saveAll 用）。
func saveTestPlayer(id uint32, name string) *Player {
	p := newTestPlayer(id, name, entity.JobWarr)
	p.Char.Data.BagItems = make([]*pb.UserItem, entity.MaxBagSize)
	p.snapReq = make(chan chan *storage.Character, 1)
	return p
}

// ownerLoop 模拟玩家派发循环里"服务快照请求"那一半（见 handleConn）。
// serve=false 表示"忙得没空回"（模拟卡在门禁延时里）。
func ownerLoop(p *Player, serve bool, stop <-chan struct{}) {
	for {
		select {
		case reply := <-p.snapReq:
			if serve {
				reply <- saveSnapshotOf(p)
			}
		case <-stop:
			return
		}
	}
}

// TestSaveAllTakesSnapshotFromOwner 钉住两件事：
//
//  1. 存档线程**不会自己去读**在线对象 —— 玩家 goroutine 不交快照时，
//     这一轮就不存（而不是"照存不误"地读一遍）；
//  2. 拿到的是**深拷贝** —— 之后玩家继续变，已落盘的那份不受影响。
func TestSaveAllTakesSnapshotFromOwner(t *testing.T) {
	s := testSlaveServer()
	cs := &captureStore{}
	s.store = cs
	s.snapTimeout = 30 * time.Millisecond // 测试里把超时调小

	p := saveTestPlayer(1, "存档测试")
	p.Char.Data.Gold = 100
	p.Char.Data.Abil.Hp = 50
	s.world.players[p.Obj.ID] = p
	p.allowGroup = true // 会话态开关应当被同步进存档（原 savePlayer 那两行）
	p.allowGroupRecall = false

	// ① 玩家 goroutine 忙（不交快照）⇒ 本轮一个都不存
	stop := make(chan struct{})
	go ownerLoop(p, false, stop)
	s.saveAll()
	if n := cs.count(); n != 0 {
		t.Fatalf("玩家没交快照时存档线程不该自己去读状态，却存了 %d 个", n)
	}
	close(stop)

	// ② 玩家 goroutine 正常服务 ⇒ 存下去，且是深拷贝
	stop2 := make(chan struct{})
	go ownerLoop(p, true, stop2)
	s.saveAll()
	if n := cs.count(); n != 1 {
		t.Fatalf("交得起快照时该存 1 个，实际 %d 个", n)
	}
	close(stop2)

	snap := cs.last()
	if snap == nil || snap.Data == nil {
		t.Fatal("存档里没有详情")
	}
	if snap.Name != "存档测试" || snap.Data.Gold != 100 || snap.Data.Abil.Hp != 50 {
		t.Fatalf("快照内容不对：name=%q gold=%d hp=%d", snap.Name, snap.Data.Gold, snap.Data.Abil.Hp)
	}
	if !snap.Data.AllowGroup || snap.Data.AllowGroupRecall {
		t.Errorf("会话态开关没同步进存档：AllowGroup=%v AllowGroupRecall=%v",
			snap.Data.AllowGroup, snap.Data.AllowGroupRecall)
	}
	if snap.Data == p.Char.Data {
		t.Fatal("快照的 Data 不该还是在线对象本身（必须深拷贝）")
	}
	// 玩家继续改变 ⇒ 已落盘的那份纹丝不动
	p.setGold(999)
	p.Char.Data.Abil.Hp = 1
	p.withBag(func(d *pb.CharacterData) {
		d.BagItems[0] = &pb.UserItem{MakeIndex: 77, Index: 1, Dura: 1, DuraMax: 1}
	})
	// ⚠️ 空槽的表示：`proto.Marshal` 往返与 `proto.Clone` 都会把 nil 格规范化成
	// "非 nil 的空物品"（实测：3 格里只有 1 格有物，往返后三格都非 nil、下标不错位）
	// ⇒ 仓库里一律按 `Index == 0` 判空，这里也照那个口径断言。
	if snap.Data.Gold != 100 || snap.Data.Abil.Hp != 50 || snap.Data.BagItems[0].GetIndex() != 0 {
		t.Errorf("落盘后的快照被在线状态改动了：gold=%d hp=%d 背包首格index=%d",
			snap.Data.Gold, snap.Data.Abil.Hp, snap.Data.BagItems[0].GetIndex())
	}
}

// TestSaveAllConcurrentWithGameplay 存档线程与玩家自己的 goroutine 并发跑。
//
// 配合 `-race`：修好之后是干净的；把 saveAll 改回"直接存在线对象"
// （`s.savePlayer(p)`）应当立刻报 `WARNING: DATA RACE`（本轮做过变异验证）。
func TestSaveAllConcurrentWithGameplay(t *testing.T) {
	s := testSlaveServer()
	cs := &captureStore{}
	s.store = cs
	s.snapTimeout = time.Second

	p := saveTestPlayer(1, "并发存档")
	s.world.players[p.Obj.ID] = p

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(10 * time.Microsecond)
		defer tick.Stop()
		for {
			select {
			case reply := <-p.snapReq:
				reply <- saveSnapshotOf(p)
			case <-tick.C:
				// 玩家自己的 goroutine 在改自己的状态：本人字段直接写，
				// 随身财物走加锁助手（真实玩法就是这样）
				p.Char.Data.Abil.Hp++
				p.addGold(1)
			case <-stop:
				return
			}
		}
	}()

	for i := 0; i < 100; i++ {
		s.saveAll()
	}
	close(stop)
	wg.Wait()

	if cs.count() == 0 {
		t.Fatal("跑了 100 轮一次都没存下去")
	}
}
