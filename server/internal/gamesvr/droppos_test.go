package gamesvr

import (
	"testing"

	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/world"
)

// dropTestServer 造一个"全图可走"的服务器 + 地图，用于钉死落点算法。
//
// ⚠️ `world.Generate(..., border=false)` 的每一格都可走（只有 border=true 才挡四周），
// 所以下面每个用例的"哪几格不可用"完全由用例自己控制 ⇒ 断言是确定的。
func dropTestServer() (*Server, *world.Map) {
	s := testSlaveServer() // 已有 world.players / monsters / monsterIdx / social
	m := world.Generate("droptest", 20, 20, false)
	mm := world.NewMapManager("", 4)
	mm.Put(m)
	s.world.maps = mm
	if s.world.index == nil {
		s.world.index = world.NewSpatialIndex(32)
	}
	if s.world.ground == nil {
		s.world.ground = make(map[uint32]*GroundItem)
	}
	return s, m
}

// addGround 在该格放 n 件地面物品（只为占格，内容不重要）。
func addGround(s *Server, m *world.Map, x, y, n int) {
	for i := 0; i < n; i++ {
		id := s.world.groundSeq.Add(1)
		s.world.ground[id] = &GroundItem{ID: id, Map: m, X: x, Y: y}
	}
}

// TestDropPositionSpiralOrder 钉住扫描顺序：空地上落**螺旋的第一个格** (-1,-1)。
//
// ⚠️ 这条正是最容易搞错的地方：原版不是"随机散落"，顺序是固定的
// （ObjBase.pas:1545-1560 的三层 for）⇒ 同一格重复掉落会落在同一处。
func TestDropPositionSpiralOrder(t *testing.T) {
	s, m := dropTestServer()
	x, y := 10, 10
	if dx, dy := s.dropPosition(m, x, y, dropRangePlayerDie, 0); dx != x-1 || dy != y-1 {
		t.Errorf("空地落点 = (%d,%d)，期望 (%d,%d)（螺旋第一格）", dx, dy, x-1, y-1)
	}
}

// TestDropPositionSkipsBlockedAndOccupied 钉住"可走 + 该格没东西"两条前提。
func TestDropPositionSkipsBlockedAndOccupied(t *testing.T) {
	s, m := dropTestServer()
	x, y := 10, 10

	// ① 把螺旋第一格改成不可走 ⇒ 应落到第二个 (x, y-1)
	if !m.SetBlock(x-1, y-1, true) {
		t.Fatal("SetBlock 失败")
	}
	if dx, dy := s.dropPosition(m, x, y, dropRangePlayerDie, 0); dx != x || dy != y-1 {
		t.Errorf("第一格被阻挡后落点 = (%d,%d)，期望 (%d,%d)", dx, dy, x, y-1)
	}

	// ② 再在第二格放一件地面物品 ⇒ 应落到第三个 (x+1, y-1)
	addGround(s, m, x, y-1, 1)
	if dx, dy := s.dropPosition(m, x, y, dropRangePlayerDie, 0); dx != x+1 || dy != y-1 {
		t.Errorf("前两格不可用后落点 = (%d,%d)，期望 (%d,%d)", dx, dy, x+1, y-1)
	}
}

// TestDropPositionOriginWhenRingOneBlocked 钉住"原点在扫描范围内、但排第 5"。
//
// 把 i=1 那一圈里排在原点之前的四格全挡掉 ⇒ 原点被选中。
// 顺带验证 ignoreID：站在这格上的那个对象（自己）不算阻挡。
func TestDropPositionOriginWhenRingOneBlocked(t *testing.T) {
	s, m := dropTestServer()
	x, y := 10, 10
	for _, c := range [][2]int{{x - 1, y - 1}, {x, y - 1}, {x + 1, y - 1}, {x - 1, y}} {
		m.SetBlock(c[0], c[1], true)
	}

	// 自己站在原点上：原版看 m_boDeath，所以不该挡自己
	me := newTestPlayer(7, "自己", 1)
	me.Obj.SetPlace(m, x, y, me.Obj.Facing())
	s.world.players[7] = me
	s.world.index.Add(me)

	if dx, dy := s.dropPosition(m, x, y, dropRangePlayerDie, 7); dx != x || dy != y {
		t.Errorf("前四格不可用时落点 = (%d,%d)，期望原点 (%d,%d)", dx, dy, x, y)
	}
	// 不忽略自己的话，原点被自己占着 ⇒ 只能往外找
	if dx, dy := s.dropPosition(m, x, y, dropRangePlayerDie, 0); dx == x && dy == y {
		t.Error("ignoreID=0 时原点被活人占着，不该还落原点")
	}
}

// TestDropPositionDensityFallback 钉住"一圈都没空格"时的两条退路：
// 物品**最少**的那格（< 8），否则原地。
func TestDropPositionDensityFallback(t *testing.T) {
	s, m := dropTestServer()
	x, y := 10, 10
	// 除原点外，i=1 这一圈的 8 格全铺上物品：各 2 件，**只有 (0,-1) 是 1 件**
	ring := [][2]int{{-1, -1}, {1, -1}, {1, 0}, {1, 1}, {0, 1}, {-1, 1}, {-1, 0}}
	for _, c := range ring {
		addGround(s, m, x+c[0], y+c[1], 2)
	}
	addGround(s, m, x, y-1, 1)
	m.SetBlock(x, y, true) // 原点不可走 ⇒ 逼它走"最少件数"那条退路

	if dx, dy := s.dropPosition(m, x, y, 1, 0); dx != x || dy != y-1 {
		t.Errorf("一圈无空格时落点 = (%d,%d)，期望件数最少的 (%d,%d)", dx, dy, x, y-1)
	}

	// 把那一圈**全部**堆到 8 件 ⇒ 最小值也不满足 < 8 ⇒ 落原地
	addGround(s, m, x, y-1, 7)
	for _, c := range ring {
		addGround(s, m, x+c[0], y+c[1], 6)
	}
	if dx, dy := s.dropPosition(m, x, y, 1, 0); dx != x || dy != y {
		t.Errorf("最少件数 ≥ 8 时落点 = (%d,%d)，期望原地 (%d,%d)", dx, dy, x, y)
	}
}

// TestDropPositionLivingBlocksButCorpseDoesNot 钉住原版那两条：
// 活着的对象挡格（OS_MOVINGOBJECT + not m_boDeath），尸体不挡。
func TestDropPositionLivingBlocksButCorpseDoesNot(t *testing.T) {
	s, m := dropTestServer()
	x, y := 10, 10

	alive := newTestMonster(21, "鸡", 10)
	alive.SetPlace(m, x-1, y-1, alive.Facing())
	s.world.monsters[21] = alive
	s.world.monsterIdx.Add(alive)

	// 活怪占着第一格 ⇒ 落到第二格
	if dx, dy := s.dropPosition(m, x, y, 1, 0); dx != x || dy != y-1 {
		t.Errorf("活怪挡格时落点 = (%d,%d)，期望 (%d,%d)", dx, dy, x, y-1)
	}

	// 同一只怪改成死的 ⇒ 尸体不挡 ⇒ 回到第一格（叠在尸体上）
	alive.HP = 0
	if !alive.IsDead() {
		t.Fatal("HP=0 应算死亡")
	}
	if dx, dy := s.dropPosition(m, x, y, 1, 0); dx != x-1 || dy != y-1 {
		t.Errorf("尸体不该挡格：落点 = (%d,%d)，期望 (%d,%d)", dx, dy, x-1, y-1)
	}
}

// TestDropPositionIgnoresOtherMap 钉住"索引是全局的"这条我们自己引入的坑：
// 别的地图上同坐标的玩家不能挡住落点。
func TestDropPositionIgnoresOtherMap(t *testing.T) {
	s, m := dropTestServer()
	other := world.Generate("other", 20, 20, false)
	x, y := 10, 10

	far := newTestPlayer(31, "别处的", 1)
	far.Obj.SetPlace(other, x-1, y-1, far.Obj.Facing())
	s.world.players[31] = far
	s.world.index.Add(far)

	if dx, dy := s.dropPosition(m, x, y, 1, 0); dx != x-1 || dy != y-1 {
		t.Errorf("别的地图的玩家不该挡格：落点 = (%d,%d)，期望 (%d,%d)", dx, dy, x-1, y-1)
	}
}

// TestDropPositionRange 钉住范围实参：玩家死亡 2 / 怪物死亡 3。
func TestDropPositionRange(t *testing.T) {
	s, m := dropTestServer()
	x, y := 10, 10
	// 把 i=1 整圈挡住 ⇒ 只能往外找
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			m.SetBlock(x+dx, y+dy, true)
		}
	}
	if dx, dy := s.dropPosition(m, x, y, dropRangePlayerDie, 0); dx != x-2 || dy != y-2 {
		t.Errorf("范围 2 的落点 = (%d,%d)，期望 (%d,%d)", dx, dy, x-2, y-2)
	}
	if dropRangePlayerDie != 2 || dropRangeMonsterDie != 3 {
		t.Errorf("范围常量 = %d/%d，期望 2/3（ObjBase.pas:20570/20641）",
			dropRangePlayerDie, dropRangeMonsterDie)
	}
}

// 编译期确认：索引里确实放的是这两种类型（canDropAt 的类型断言依赖它）。
var (
	_ world.Positioned = (*Player)(nil)
	_ world.Positioned = (*entity.Monster)(nil)
)
