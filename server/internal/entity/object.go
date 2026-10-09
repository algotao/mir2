// Package entity 是游戏中的可见实体。
//
// 原版 ObjBase.pas 用一个 26821 行的 TBaseObject 承载坐标、属性、背包、魔法、
// 视野、战斗等全部职责（约 370 个字段）。这里按**组合**拆分：
//
//	Object  坐标 / 地图 / 方向 / 外观（本文件）
//	Stats   战斗属性（待 P4）
//	Inventory 背包与装备（待 P3 后续）
//	Vision  视野对象（待 P3 后续）
//
// 用组合而非继承：Go 没有继承，且原版那棵继承树（TBaseObject → TAnimalObject
// → TPlayObject / TMonster）本身就是为 Delphi 的虚方法分派服务的。
package entity

import (
	"github.com/algotao/mir2/server/internal/tscale"
	"sync"
	"time"

	"github.com/algotao/mir2/server/internal/world"
)

// 方向常量（Common/Grobal2.pas:19-26）。
const (
	DirUp = iota
	DirUpRight
	DirRight
	DirDownRight
	DirDown
	DirDownLeft
	DirLeft
	DirUpLeft
)

// DirDelta 是各方向的坐标增量 [dx, dy]。
//
// 屏幕坐标系：y 向下增大，故 DirUp 的 dy 为 -1。
var DirDelta = [8][2]int{
	{0, -1},  // 0 上
	{1, -1},  // 1 右上
	{1, 0},   // 2 右
	{1, 1},   // 3 右下
	{0, 1},   // 4 下
	{-1, 1},  // 5 左下
	{-1, 0},  // 6 左
	{-1, -1}, // 7 左上
}

// Object 是所有可见实体的公共部分。
//
// ⚠️ 并发（审计 P1-5）：本类型的可变态**天生跨 goroutine** —— 坐标/朝向由
// "本人 goroutine"（玩家）或 AI ticker（怪物）改，而别人的 goroutine 在读
// （视野差集、目标判定、广播、脚本条件、行会/组队距离…）。所以这里把它做成
// **自带锁的对象**：
//
//  1. 可变态一律**不导出**，读写只经下面的访问器；`mu` 是**叶子锁**
//     （访问器不回调外部、不嵌套别的锁 ⇒ 不可能死锁，也没有锁序问题）；
//  2. 要一次读多个字段（"地图 + 坐标"必须自洽）用 `Place()` / `Pos()` /
//     `Appearance()`，别分几次调 `PosX()`/`PosY()`；
//  3. `Poison` / 几个"限时状态"同理，走 `PoisonSnapshot`/`WithPoison`/`Stone`…
//
// 这样做的代价是每次访问多一次 `Mutex`（可忽略），收益是"谁在哪个 goroutine
// 读"不再需要逐个论证——`-race` 也不会再报这一类。
type Object struct {
	ID   uint32 // ActorId，客户端用它识别实体
	Name string

	// mu 保护下面**全部**可变态。⚠️ 除本文件与 poison.go 的访问器外，谁都不许碰。
	mu sync.Mutex

	// mapRef/posX/posY/facing 是位置与朝向（原版 TBaseObject 的 m_Map/m_nX/m_nY/m_btDirection）。
	mapRef     *world.Map
	posX, posY int
	facing     uint8

	// feature 是外观位域（Race/Weapon/Hair/Dress），见 proto.MakeFeature。
	feature int32
	// status 是状态位（中毒/隐身/石化的组合）。
	status int32

	// poison 是中毒状态（原版 TBaseObject 的 m_wStatusTimeArr + m_btGreenPoisoningPoint）。
	//
	// 放在 Object 上而不是分别放 Player/Monster：原版就在 TBaseObject 上，
	// 而我们这里 Monster 直接嵌入 *Object、Player 也持有 *Object
	// ⇒ 一份实现同时覆盖"施毒术打怪"与"怪/PK 毒人"。语义见 poison.go。
	poison PoisonState

	// stoneUntil 是石化/麻痹的到期时刻（原版 `m_wStatusTimeArr[POISON_STONE]`，
	// 由 `MakePosion(POISON_STONE, nTime, 0)` 设置）。
	//
	// ⚠️ 与 Monster.SeizedUntil（困魔咒 `m_boHolySeize`）**是两回事**：
	// 困魔咒被攻击会解除（BreakHolySeizeMode），石化不会；两者都禁止行动。
	// 放在 Object 上是因为**玩家也会被石化**（麻痹戒指打人，ObjBase.pas:22265 的
	// `AttackTarget` 是 TBaseObject）。
	stoneUntil time.Time
	// showHPUntil 是"心灵启示"（SKILL_SHOWHP，id=28）给本对象开的血条到期时刻
	//（原版 m_dwShowHPInterval，ObjBase.pas:4029）。
	showHPUntil time.Time

	// castleAggroUntil 是"打过城堡单位"的仇恨窗口到期时刻。
	//
	// 对应原版 TBaseObject 的 `m_bo2B0` + `m_dw2B4Tick`（ObjMon2.pas:822-823
	// 由 TGuardUnit.Struck 置位；:836 判定 `(GetTickCount - m_dw2B4Tick) < 2*60*1000`）。
	// ⚠️ 它放在**攻击者**身上（原版就是这样）：任何打过城门/城墙/守卫的人，
	// 在 2 分钟内都是守卫的合法目标 —— 哪怕他不是攻城方、甚至就是守方行会的人。
	castleAggroUntil time.Time
}

// ---------- 位置 / 朝向 ----------

// NewObject 创建一个实体，并把位置/朝向/外观一次设好。
//
// ⚠️ 可变态一律不导出（见类型注释），所以**跨包**构造必须走这里；
// 包内构造可以直接写字段（见 NewMonster）。
func NewObject(id uint32, name string, m *world.Map, x, y int, facing uint8, feature int32) *Object {
	return &Object{
		ID: id, Name: name,
		mapRef: m, posX: x, posY: y, facing: facing,
		feature: feature,
	}
}

// Pos 返回坐标，满足 world.Positioned 接口。
func (o *Object) Pos() (int, int) {
	if o == nil {
		return 0, 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.posX, o.posY
}

// PosX / PosY 是单字段读（需要"坐标 + 地图自洽"时请用 Place）。
func (o *Object) PosX() int {
	if o == nil {
		return 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.posX
}

func (o *Object) PosY() int {
	if o == nil {
		return 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.posY
}

// Facing 返回朝向。
func (o *Object) Facing() uint8 {
	if o == nil {
		return 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.facing
}

// MapRef 返回所在地图。
func (o *Object) MapRef() *world.Map {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.mapRef
}

// Place 一次取回"地图 + 坐标 + 朝向"的**自洽快照**。
func (o *Object) Place() (m *world.Map, x, y int, facing uint8) {
	if o == nil {
		return nil, 0, 0, 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.mapRef, o.posX, o.posY, o.facing
}

// SetPos 直接把对象放到 (m, x, y)（不改朝向）。
func (o *Object) SetPos(m *world.Map, x, y int) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.mapRef, o.posX, o.posY = m, x, y
	o.mu.Unlock()
}

// SetPlace 同时设地图/坐标/朝向（换图、传送到点位用）。
func (o *Object) SetPlace(m *world.Map, x, y int, facing uint8) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.mapRef, o.posX, o.posY, o.facing = m, x, y, facing
	o.mu.Unlock()
}

// SetFacing 只改朝向（不校验合法性；校验版见 Turn）。
func (o *Object) SetFacing(dir uint8) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.facing = dir
	o.mu.Unlock()
}

// SetMapRef 只换地图，坐标不动（跨图搬运对象时用；同时换坐标请用 SetPos/SetPlace）。
func (o *Object) SetMapRef(m *world.Map) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.mapRef = m
	o.mu.Unlock()
}

// SetPosX / SetPosY 单轴改坐标（另一轴与地图不动）。
//
// ⚠️ "两条轴一起改"请用 SetPos/SetPlace —— 分两次调会留下"x 已是新值、y 还是旧值"
// 的中间态（虽然每次读都是自洽的，但两个瞬间的坐标合起来不是你想要的那一格）。
func (o *Object) SetPosX(x int) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.posX = x
	o.mu.Unlock()
}

func (o *Object) SetPosY(y int) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.posY = y
	o.mu.Unlock()
}

// ---------- 外观 / 状态位 ----------

// FeatureBits 返回外观位域。
func (o *Object) FeatureBits() int32 {
	if o == nil {
		return 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.feature
}

// SetFeatureBits 设置外观位域（换装、变身）。
func (o *Object) SetFeatureBits(v int32) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.feature = v
	o.mu.Unlock()
}

// Appearance 一次取回"外观 + 状态位"的自洽快照（TCharDesc 要的就是这两个）。
func (o *Object) Appearance() (feature, status int32) {
	if o == nil {
		return 0, 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.feature, o.status
}

// StatusBits 返回"上次发出去的状态位"快照。
func (o *Object) StatusBits() int32 {
	if o == nil {
		return 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.status
}

// SetStatusBits 更新状态位快照。
func (o *Object) SetStatusBits(v int32) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.status = v
	o.mu.Unlock()
}

// ---------- 石化 / 血条 / 城堡仇恨窗口 ----------

// StoneUntil 返回石化到期时刻。
func (o *Object) StoneUntil() time.Time {
	if o == nil {
		return time.Time{}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.stoneUntil
}

// Stone 石化 d 时长（原版 `MakePosion(POISON_STONE, nTime, 0)`）。
//
// ⚠️ 原版 `MakePosion` 只**延长**不缩短：已石化且剩余时间更长时保持原值。
// ⚠️ "判定 + 置位"必须在**同一次持锁**里做 —— 否则并发延长会互相覆盖
// （审计 P1-5 里唯一"会丢更新"的一处，2026-10-07 收口）。
func (o *Object) Stone(d time.Duration) {
	if o == nil {
		return
	}
	o.mu.Lock()
	if until := time.Now().Add(d); until.After(o.stoneUntil) {
		o.stoneUntil = until
	}
	o.mu.Unlock()
}

// SetStoneUntil 直接把石化到期时刻设成给定值（`time.Time{}` 即清除）。
//
// ⚠️ 与 `Stone(d)` 的区别：`Stone` 是**只延长**的游戏语义（原版 MakePosion），
// 这个是"原始赋值"，只在需要精确造前提的测试/编辑器路径上用。
func (o *Object) SetStoneUntil(t time.Time) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.stoneUntil = t
	o.mu.Unlock()
}

// ClearStone 清掉石化（到期/免疫时用）。
func (o *Object) ClearStone() {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.stoneUntil = time.Time{}
	o.mu.Unlock()
}

// Stoned 报告此刻是否处于石化状态。
func (o *Object) Stoned(now time.Time) bool {
	if o == nil {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return now.Before(o.stoneUntil)
}

// ShowHPUntil 返回心灵启血条的到期时刻。
func (o *Object) ShowHPUntil() time.Time {
	if o == nil {
		return time.Time{}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.showHPUntil
}

// SetShowHPUntil 开/关血条（零值即关闭）。
func (o *Object) SetShowHPUntil(t time.Time) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.showHPUntil = t
	o.mu.Unlock()
}

// CastleAggroUntil 返回"打过城堡单位"的仇恨窗口到期时刻。
func (o *Object) CastleAggroUntil() time.Time {
	if o == nil {
		return time.Time{}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.castleAggroUntil
}

// SetCastleAggroUntil 设置/清零城堡仇恨窗口。
func (o *Object) SetCastleAggroUntil(t time.Time) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.castleAggroUntil = t
	o.mu.Unlock()
}

// ---------- 移动 ----------

// SetMap 切换地图并重置坐标。
func (o *Object) SetMap(m *world.Map, x, y int) { o.SetPos(m, x, y) }

// CanMove 校验能否朝 dir 移动一格，返回目标坐标。
//
// 校验项：方向合法、目标在地图内、目标格可通行。
func (o *Object) CanMove(dir uint8) (nx, ny int, ok bool) {
	if o == nil || dir > DirUpLeft {
		return 0, 0, false
	}
	d := DirDelta[dir]
	o.mu.Lock()
	nx, ny, m := o.posX+d[0], o.posY+d[1], o.mapRef
	o.mu.Unlock()
	if m == nil || !m.CanWalk(nx, ny) {
		return 0, 0, false
	}
	return nx, ny, true
}

// MoveTo 朝 dir 移动一格；不可通行则不移动并返回 false。
func (o *Object) MoveTo(dir uint8) bool {
	nx, ny, ok := o.CanMove(dir)
	if !ok {
		return false
	}
	o.mu.Lock()
	o.posX, o.posY, o.facing = nx, ny, dir
	o.mu.Unlock()
	return true
}

// Turn 只改朝向不移动。
func (o *Object) Turn(dir uint8) bool {
	if o == nil || dir > DirUpLeft {
		return false
	}
	o.mu.Lock()
	o.facing = dir
	o.mu.Unlock()
	return true
}

// Distance 返回与另一个坐标的切比雪夫距离。
//
// 传奇的"视野范围"用切比雪夫距离（正方形视野），不是欧氏距离。
func (o *Object) Distance(x, y int) int {
	if o == nil {
		return 0
	}
	o.mu.Lock()
	dx, dy := o.posX-x, o.posY-y
	o.mu.Unlock()
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	if dx > dy {
		return dx
	}
	return dy
}

// MoveLimiter 控制移动频率。
//
// 原版靠网关的"发送窗口 + '*' 确认"反变速齿轮（SENDCHECKSIZE=512 等），
// 那是 2002 年拨号/ADSL 条件下的产物。现代做法直接在服务端按时间戳限流，
// 逻辑更直白，也更容易调参。
type MoveLimiter struct {
	// last 是**走跑共用**的"上一次移动时刻"。
	//
	// ⚠️ 原版走与跑读的是**同一个** `m_dwMoveTick`（`ObjBase.pas:9604` 的
	// `ClientWalkXY` 与 `:9521` 的 `ClientRunXY` 都在查它）⇒ 走一步之后紧接着跑
	// 也要等满一个间隔。我们原来分开记两个时间戳 ⇒ "走→跑→走"交替能把平均压到
	// 约 266ms/格，等于送了个变速齿轮。
	last time.Time

	// MinWalk / MinRun 是两次移动之间的最小间隔。
	//
	// ⚠️ 原版两个都是 **600ms**（`GameConfig.pas:1038-1052` 的 `dwWalkIntervalTime`
	// 与 `dwRunIntervalTime` 同为 600）—— 跑是"600ms 走 2 格"（300ms/格），
	// 不是"间隔更短"。我们原来写 MinRun=400 ⇒ 跑比原版**快 50%**，
	// 视觉上就是"滑步"（`RUN_STEP_MS` 也是按错的值推的）。
	MinWalk time.Duration
	MinRun  time.Duration
}

// NewMoveLimiter 创建限流器，零值会被替换为典型值。
func NewMoveLimiter() *MoveLimiter {
	// ⚠️ 限流跟随全局倍速（tscale）：e2e 加速时服务端与客户端必须同速，
	// 否则客户端按键节奏跟不上，会大量丢步（表现为"追不上怪"）。
	return &MoveLimiter{
		MinWalk: tscale.D(600 * time.Millisecond),
		MinRun:  tscale.D(600 * time.Millisecond), // 原版同为 600：跑 = 600ms / 2 格
	}
}

// Allow 报告此刻是否允许移动；允许则同时更新时间戳。
func (l *MoveLimiter) Allow(running bool, now time.Time) bool {
	min := l.MinWalk
	if running {
		min = l.MinRun
	}
	if min <= 0 {
		return true
	}
	// 走跑共用 `l.last`（见字段说明）
	if !l.last.IsZero() && now.Sub(l.last) < min {
		return false
	}
	l.last = now
	return true
}

// Reset 重置时间戳（切换地图时使用，避免刚传送就被限流）。
func (l *MoveLimiter) Reset() {
	l.last = time.Time{}
}
