package gamesvr

import (
	"log"
	"net"
	"time"

	"github.com/algotao/mir2/server/internal/delphi"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/wire"
	"github.com/algotao/mir2/server/internal/world"
)

// 挖肉（取肉）与挖矿。
//
// 原版分三处：
//
//	取肉 `TPlayObject.ClientGetButchItem`（ObjBase.pas:17458-17504），入口 `CM_BUTCH`（:4706）
//	挖矿 `AttackDir` 的"挖矿"分支（:8819-8852）+ `TPlayObject.PileStones`（:21880-21925）
//	     + `MakeMine` / `MakeMine2`（:24352 / :24476）
//	矿脉 `TFrmMain.MakeStoneMines`（svMain.pas:867-883）：**MINE/MINE2 地图的每一格**
//	     都建一个 `TStoneMineEvent`（`MineCount = Random(200)`、`AddStoneCount = Random(80)`）
//
// ⚠️ 两个容易搞错的点：
//
//  1. **挖矿不是 `CM_BUTCH`**：它是 `CM_HEAVYHIT`（客户端的"重击"键）+ 手里是**鹤嘴锄**
//     （`StdItem.Shape = 19`）+ 正前方那格**不可走**（`not CanWalk(前格)`）⇒ 打的是矿脉。
//     打不到矿脉事件时**照样**走正常攻击流程（原版 `Exit` 只在认成矿脉时才提前退）。
//  2. **动物死亡时什么都不掉**：原版 `Die` 里整段掉落被 `(not m_boAnimal)` 挡着
//     （:20983-20999）⇒ 鸡/鹿/狼的东西要**取肉**的时候才进背包（`TakeBagItems`）。
//     见 `scatterKillGold` 里的门。
const (
	// 以下全部取自官方 `!setup.txt`（括号里是代码里的默认值，**以文件为准**）：
	makeMineHitRate      = 2   // MakeMineHitRate（代码默认 4）：几分之一"挖中"
	makeMineRate         = 8   // MakeMineRate（代码默认 12）：挖中之后再几分之一真出矿
	stoneTypeRate        = 120 // StoneTypeRate：矿石种类抽签上界
	goldStoneMin         = 1   // 金矿区间
	goldStoneMax         = 2
	silverStoneMin       = 3 // 银矿区间（文件里只有 3）
	silverStoneMax       = 3
	steelStoneMin        = 4 // 铁矿区间
	steelStoneMax        = 20
	blackStoneMin        = 21 // 黑铁矿区间（代码默认 46..56）
	blackStoneMax        = 99
	stoneMinDura         = 3000 // 矿石基础耐久
	stoneGeneralDuraRate = 13000
	stoneAddDuraRate     = 20
	stoneAddDuraMax      = 10000
	// 矿脉补货间隔（原版 `10 * 60 * 1000`）与矿脉初始储量上界（`Random(200)`）
	mineRefillInterval = 10 * time.Minute
	// 首次挖矿时的初始储量/补货量上界（原版 `Random(200)` / `Random(80)`，右开）
	mineCountInitMax = 200
	mineAddCountMax  = 80
	// turnIntervalTime 是转身/取肉的**共用**间隔（官方 `TurnIntervalTime=100`，毫秒）。
	// 原版用的是同一个 `m_dwTurnTick`（ClientGetButchItem 首行就复用它）。
	turnIntervalTime = 100 * time.Millisecond
	// 取肉：每次削掉的"皮革度/肉质量"
	leatheryCutMin, leatheryCutRange = 5, 16    // Random(16)+5   ⇒ 5..20
	meatCutMin, meatCutRange         = 100, 201 // Random(201)+100 ⇒ 100..300
	// leatheryReset 是"把肉挖出来之后"重置的皮革度（原版 `m_nBodyLeathery := 50`）
	leatheryReset = 50
	// animalRaceLo/Hi 是 `RC_ANIMAL`/`RC_MONSTER`（Grobal2.pas:1103-1104）：
	// 原版判"能不能变成骷髅"用的是这个区间，而 `m_boAnimal` 只在
	// 鸡/鹿/狼三个 race 上被置真（UsrEngn.pas:1835-1865）⇒ 两者对我们是等价的。
	animalRaceLo = 50
	animalRaceHi = 80
)

// 矿石名（官方 `!setup.txt:3468-3472` 的 `Names` 段）。
const (
	goldStoneName   = "金矿"
	silverStoneName = "银矿"
	steelStoneName  = "铁矿"
	copperStoneName = "铜矿"
)

// String.ini 的文案。
const (
	sYouFoundNothing = "未获取任何物品!"
	sBagFullNoItems  = "背包已满！无法携带更多的东西!"
)

// 动物（`m_boAnimal`）的三个 race：鸡 51 / 鹿 52 / 狼 53（M2Share.pas:148-150）。
// ⚠️ 我们的数据里 52 还被"牛/羊/护卫"共用、55 是练功师 ⇒ 用**集合**而不是区间，
// 与原版 `m_boAnimal` 的置真范围（UsrEngn 只给这三个 race 置真）保持一致。
var animalRaces = map[uint16]bool{51: true, 52: true, 53: true}

// isAnimalRace 判定 race 是不是动物。
func isAnimalRace(race uint16) bool { return animalRaces[race] }

// isAnimal 判定这只怪是不是"要取肉"的动物。
func (s *Server) isAnimal(m *entity.Monster) bool {
	return m != nil && m.Info != nil && isAnimalRace(m.Info.Race)
}

// animalInit 按 race 给出肉质/皮革度的初值（原版 `UsrEngn.pas:1835-1865`）：
//
//	鸡 51：MeatQuality = Random(3500)+3000，Leathery = 50
//	鹿 52：1/30 出"鸡鹿"（Random(20000)+10000 / 150），否则 Random(8000)+8000 / 150
//	狼 53：Random(8000)+8000，Leathery = 150
//
// 幂等：只在第一次（`AnimalSet` 为假）时掷一次。
func (s *Server) animalInit(m *entity.Monster) {
	if m == nil || m.Info == nil || m.AnimalSet || !isAnimalRace(m.Info.Race) {
		return
	}
	switch m.Info.Race {
	case 51: // 鸡
		m.MeatQuality = delphi.Random(3500) + 3000
		m.Leathery = 50
	case 52: // 鹿（含共用 52 的羊/牛）
		if delphi.Random(30) == 0 {
			m.MeatQuality = delphi.Random(20000) + 10000
		} else {
			m.MeatQuality = delphi.Random(8000) + 8000
		}
		m.Leathery = 150
	default: // 53 狼
		m.MeatQuality = delphi.Random(8000) + 8000
		m.Leathery = 150
	}
	m.AnimalSet = true
}

// handleButch 处理 `CM_BUTCH`（取肉）。
//
// 客户端 `SendButchAnimal(x, y, dir, actorid)`（ClMain.pas:3078-3084）发
// `MakeDefaultMsg(CM_BUTCH, actorid, x, y, dir)` ⇒ Recog=目标、Param=x、Tag=y、Series=dir。
// / protoActionButch 新协议里"挖肉"这个动作的编号。
// /
// / ⚠️ 编号是**我们自己定的**（协议的动作表里 1..8 是攻击、51 受击、52 死亡，
// / 原版的挖肉走的是 `SM_BUTCH` 消息而不是动作号）⇒ 客户端 `world::action` 里
// / 有同名常量，两处必须一致（改一处要改另一处）。
const protoActionButch uint32 = 53

func (s *Server) handleButch(c net.Conn, p *Player, pkt wire.Packet) {
	s.doButch(c, p, uint32(pkt.Head.Recog), int(pkt.Head.Param), int(pkt.Head.Tag),
		proto.LoByte(pkt.Head.Series))
}

// doButch 挖一次肉（legacy `CM_BUTCH` 与新协议的 `Butch` 共用这一份）。
//
// 原版流程见注释里的 `ObjBase.pas:17476` 那几步：转身间隔 → 目标校验（死了、没变骷髅、
// 是动物、且在自己 2 格内）→ 削皮革度/肉质量 → 皮革度归零就变骷髅并把东西给挖肉的人。
func (s *Server) doButch(c net.Conn, p *Player, targetID uint32, x, y int, dir uint8) {
	if p == nil || p.Obj == nil || !p.logonDone {
		return
	}
	// ① 转身/取肉的间隔（原版首行：`GetTickCount - m_dwTurnTick < TurnIntervalTime`
	//    就把 dwDelayTime 报给调用方然后退出）。我们用同一个时间戳给转身与取肉共用。
	now := time.Now()
	if now.Sub(p.turnAt) < tickDur(turnIntervalTime) {
		return
	}
	p.turnAt = now

	target := s.monsterByID(targetID)
	// ② 目标必须在自己**2 格以内**，且那个格子上就是它
	if target == nil || target.MapRef() != p.Obj.MapRef() ||
		absi(x-p.Obj.PosX()) > 2 || absi(y-p.Obj.PosY()) > 2 || target.PosX() != x || target.PosY() != y {
		return
	}
	// ③ 只有"死了、还没变成骷髅、而且是动物"的尸体能取肉（:17476）
	if target.Alive || target.Skeleton || !s.isAnimal(target) {
		return
	}
	s.animalInit(target)

	// ④ 削皮革度与肉质量（被击时也会削，见 hitMonsterMeat）
	target.Leathery -= delphi.Random(leatheryCutRange) + leatheryCutMin
	target.MeatQuality -= delphi.Random(meatCutRange) + meatCutMin
	if target.MeatQuality < 0 {
		target.MeatQuality = 0
	}

	if target.Leathery <= 0 {
		// ⑤ 皮革度归零 ⇒ 变骷髅 + 把身上的东西给取肉的人
		if target.Info != nil && int(target.Info.Race) >= animalRaceLo && int(target.Info.Race) < animalRaceHi {
			target.Skeleton = true
			s.broadcastSkeleton(target)
		}
		if !s.butchTakeItems(p, target) {
			s.sysMsg(c, sYouFoundNothing)
		}
		target.Leathery = leatheryReset
	}

	// 朝向属于 s.mu 域（statelock.go 第二节）。
	s.turnPlayer(p, dir)
	s.broadcastButch(p)
}

// butchTakeItems 把尸体的"携带物"给取肉的人（原版 `TBaseObject.TakeBagItems`，:20537）：
// 逐件往背包里塞，塞不进就停（剩下的留在尸体上）。
//
// ⚠️ 我们和原版的差别（有意为之）：原版在**怪物出生时**就把掉落表掷进 `m_ItemList`，
// 我们改成"取肉这一刻"掷 —— 对外表现一致（不被取肉就永远看不到），而且省掉一份
// 要跟着尸体清理的中间状态；代价是背包满时重试会**重新掷一次**。
// 另外 `ApplyMeatQuality`（:20517）那步在这里等价实现：`StdMode = 40`（肉/鸡肉）的
// 持久度直接取 `MeatQuality`。
func (s *Server) butchTakeItems(p *Player, m *entity.Monster) bool {
	if m.LootTaken {
		return false
	}
	hits := s.rollLoot(m)
	took := 0
	for _, it := range hits {
		if it.IsGold() {
			// 原版 `TakeBagItems` 只处理 `m_ItemList`，金币不在这条路上
			//（动物的金币在 `Die` 里就被 `(not m_boAnimal)` 一起挡掉了）⇒ 丢掉。
			continue
		}
		tmpl := s.data.tables.Items.GetByName(it.ItemName)
		if tmpl == nil {
			continue
		}
		ui := &pb.UserItem{
			MakeIndex: int32(s.itemSeq.Add(1)),
			Index:     uint32(tmpl.Index),
			Dura:      initialDura(tmpl),
			DuraMax:   tmpl.DuraMax,
		}
		if tmpl.StdMode == 40 {
			// `ApplyMeatQuality`：肉的质量就是它的耐久
			ui.Dura = uint32(m.MeatQuality)
		}
		if ui.Dura == 0 && tmpl.DuraMax > 0 {
			ui.Dura = 1
		}
		if s.addToBag(p, ui) < 0 {
			s.sysMsg(p.conn, sBagFullNoItems)
			return took > 0
		}
		s.sendAddItem(p, ui)
		took++
	}
	if took > 0 {
		s.sendBagItems(p.conn, p)
		s.applyWeights(p)
		m.LootTaken = true
		log.Printf("%s 从 %s 身上取下 %d 件（肉质量 %d）", p.Char.Name, m.Name, took, m.MeatQuality)
	}
	return took > 0
}

// hitMonsterMeat 是"被击时肉质量衰减"。原版有三条，各削各的：
//
//	`TAnimalObject.Struck`（:2807-2811）：挨打一下 ⇒ `Dec(m_nMeatQuality, Random(300))`
//	魔法伤害（:4517）              ：`Dec(m_nMeatQuality, nDamage * 1000)`
//	绿毒每 2.5 秒扣血（:4259）      ：`Dec(m_nMeatQuality, 1000)`
//
// magic 为真走魔法那条（按伤害量），否则走"挨一下"那条。
func (s *Server) hitMonsterMeat(m *entity.Monster, dmg uint32, magic bool) {
	if !s.isAnimal(m) {
		return
	}
	s.animalInit(m)
	if magic {
		m.MeatQuality -= int(dmg) * 1000
	} else {
		m.MeatQuality -= delphi.Random(300)
	}
	if m.MeatQuality < 0 {
		m.MeatQuality = 0
	}
}

// hitMonsterMeatPoison 是绿毒那条（每 2.5 秒扣血时削 1000）。
func (s *Server) hitMonsterMeatPoison(m *entity.Monster) {
	if !s.isAnimal(m) {
		return
	}
	s.animalInit(m)
	m.MeatQuality -= 1000
	if m.MeatQuality < 0 {
		m.MeatQuality = 0
	}
}

// ---------- 挖矿 ----------

// mineEvent 是矿脉（原版 `TStoneMineEvent`）：MINE/MINE2 地图**每一格**一个。
type mineEvent struct {
	count    int       // 还能挖几下（初始 Random(200)）
	addCount int       // 挖空后补成多少（原版 AddStoneMine 用 Random(80)）
	addTick  time.Time // 上次补货时刻（隔 10 分钟才补）
}

// mineAt 取某一格的矿脉，没有就按原版初值建一个。
//
// ⚠️ 原版是**启动时**给 MINE/MINE2 图的每一格都建好（`MakeStoneMines`）；我们是
// 第一次挖到这一格时才建 —— 矿脉对客户端不可见（`m_boVisible := False`），
// 唯一的观测差别是"初始储量"的掷点时机，可以忽略。
func (s *Server) mineAt(m *world.Map, x, y int) *mineEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.world.mines == nil {
		s.world.mines = make(map[*world.Map]map[[2]int]*mineEvent)
	}
	cells := s.world.mines[m]
	if cells == nil {
		cells = make(map[[2]int]*mineEvent)
		s.world.mines[m] = cells
	}
	k := [2]int{x, y}
	ev := cells[k]
	if ev == nil {
		ev = &mineEvent{
			count:    delphi.Random(mineCountInitMax),
			addCount: delphi.Random(mineAddCountMax),
			addTick:  time.Now(),
		}
		cells[k] = ev
	}
	return ev
}

// handleMine 是 `AttackDir` 里的"挖矿"分支（ObjBase.pas:8819-8852）。
//
// 认成挖矿要三件事同时成立：**重击包** + 手里是**鹤嘴锄**（`Shape = 19`）+ **正前方不可走**。
// 返回 true 表示"这一下是挖矿，别再走普通攻击"。
//
// ⚠️ 原版只在"正前方那格存在矿脉事件"时才 `Exit`，否则继续走正常攻击；
// 我们把这个判断放进 pileStones 的返回值里（挖不动 ⇒ false ⇒ 继续普通攻击）。
func (s *Server) handleMine(p *Player, pkt wire.Packet) bool {
	if p == nil || p.Obj == nil || p.Obj.MapRef() == nil {
		return false
	}
	if pkt.Head.Ident != proto.CM_HEAVYHIT {
		return false
	}
	w := s.equipAt(p, proto.SlotWeapon)
	if w == nil || w.Dura == 0 {
		return false
	}
	tmpl := s.data.tables.Items.Get(int(w.Index) - 1)
	if tmpl == nil || tmpl.Shape != 19 { // Shape = 19 ⇒ 鹤嘴锄
		return false
	}
	d := entity.DirDelta[p.Obj.Facing()&7]
	fx := p.Obj.PosX() + d[0]
	fy := p.Obj.PosY() + d[1]
	// 只有正前方**不可走**（= 是墙/石头）才可能是矿
	if p.Obj.MapRef().CanWalk(fx, fy) {
		return false
	}
	_, ok := s.pileStones(p, fx, fy)
	// ⚠️ 原版挖矿还扣"回复计时/回复百分比"（`Dec(m_nHealthTick, 30)` …，:8837-8841），
	// 我们的自然恢复没有那套计数器 ⇒ 这一段没做（不影响出矿）。
	return ok
}

// pileStones 复刻 `TPlayObject.PileStones`（ObjBase.pas:21880-21925）。
//
// 返回值 _dig 与 ok 分别对应原版的 `s1C`（"挖中"标记，随 `RM_HEAVYHIT` 发下去）与 `Result`。
func (s *Server) pileStones(p *Player, x, y int) (bool, bool) {
	mp := p.Obj.MapRef()
	ev := s.mineAt(mp, x, y)
	if ev.count > 0 {
		ev.count--
		if delphi.Random(makeMineHitRate) != 0 {
			return false, false // 挖空了手
		}
		// 挖中：在自己脚下铺/叠一个"碎石堆"事件（原版 TPileStones，5 分钟、最多 5 层）。
		// ⚠️ 位置是**挖矿者脚下**、重复挖**不发包**（客户端自己把层数 +1）——
		// 细节与依据都写在 piles.go 的文件头。
		s.ensurePileAt(mp, p.Obj.PosX(), p.Obj.PosY())
		if delphi.Random(makeMineRate) == 0 {
			if mi := s.mapFlagOf(mp); mi != nil {
				if mi.Mine {
					s.makeMine(p)
				} else if mi.Mine2 {
					s.makeMine2(p)
				}
			}
		}
		// 扣武器耐久 5..19（`DoDamageWeapon(Random(15) + 5)`）
		s.damageWeapon(p, delphi.Random(15)+5)
		return true, true
	}
	// 矿脉空了：隔 10 分钟补一次
	if time.Since(ev.addTick) > mineRefillInterval {
		ev.count = ev.addCount
		ev.addTick = time.Now()
	}
	return false, false
}

// stoneDura 是矿石的持久度（原版 `MakeMine` 里的 `RandomDrua`）：
//
//	Random(StoneGeneralDuraRate{13000}) + StoneMinDura{3000}
//	外加 1/StoneAddDuraRate{20} 的概率再 + Random(StoneAddDuraMax{10000})
func stoneDura() uint32 {
	d := uint32(delphi.Random(stoneGeneralDuraRate) + stoneMinDura)
	if delphi.Random(stoneAddDuraRate) == 0 {
		d += uint32(delphi.Random(stoneAddDuraMax))
	}
	return d
}

// giveStone 挖到一块矿：进背包 + `SendAddItem`（原版 `MakeMine` 的收尾三行）。
func (s *Server) giveStone(p *Player, name string) bool {
	tmpl := s.data.tables.Items.GetByName(name)
	if tmpl == nil {
		return false // 物品表里没有这块矿 ⇒ 原版 `CopyToUserItemFromName` 失败、Dispose
	}
	ui := &pb.UserItem{
		MakeIndex: int32(s.itemSeq.Add(1)),
		Index:     uint32(tmpl.Index),
		Dura:      stoneDura(),
		DuraMax:   tmpl.DuraMax,
	}
	if s.addToBag(p, ui) < 0 {
		s.sysMsg(p.conn, sBagFullNoItems)
		return false
	}
	s.sendAddItem(p, ui)
	s.sendBagItems(p.conn, p)
	s.applyWeights(p)
	log.Printf("%s 挖到 %s（耐久 %d）", p.Char.Name, name, ui.Dura)
	return true
}

// makeMine 复刻 `TPlayObject.MakeMine`（ObjBase.pas:24352）：`MINE` 图的矿石种类抽签。
//
//	Random(StoneTypeRate{120}) 落在哪个区间：
//	  1..2 金矿 / 3..3 银矿 / 4..20 铁矿 / 21..99 黑铁矿石 / 其余 铜矿
func (s *Server) makeMine(p *Player) {
	if s.bagFull(p) {
		return // 原版 `if m_ItemList.Count >= MAXBAGITEM then Exit`
	}
	n := delphi.Random(stoneTypeRate)
	switch {
	case n >= goldStoneMin && n <= goldStoneMax:
		s.giveStone(p, goldStoneName)
	case n >= silverStoneMin && n <= silverStoneMax:
		s.giveStone(p, silverStoneName)
	case n >= steelStoneMin && n <= steelStoneMax:
		s.giveStone(p, steelStoneName)
	case n >= blackStoneMin && n <= blackStoneMax:
		s.giveStone(p, blackStoneName)
	default:
		s.giveStone(p, copperStoneName)
	}
}

// makeMine2 复刻 `TPlayObject.MakeMine2`（:24476）：`MINE2` 图的宝石抽签。
//
// ⚠️ 官方 `!setup.txt` 里**没有** `GemStone*` 段（那四个名字是代码默认值
// `RubyOre`/`AmethystOre`/`NephriteOre`/`PlatinumOre`），而我们的物品表里也没有它们
// ⇒ 这条分支在**当前数据**下等价于"什么都没挖到"（原版 `CopyToUserItemFromName`
// 失败也会 Dispose）。我们家的 mapinfo.txt 里也**一张 MINE2 图都没有**（19 张都是 MINE）。
func (s *Server) makeMine2(p *Player) {
	if s.bagFull(p) {
		return
	}
	n := delphi.Random(stoneTypeRate)
	switch {
	case n >= 1 && n <= 2:
		s.giveStone(p, "RubyOre")
	case n >= 3 && n <= 20:
		s.giveStone(p, "AmethystOre")
	case n >= 21 && n <= 45:
		s.giveStone(p, "NephriteOre")
	default:
		s.giveStone(p, "PlatinumOre")
	}
}

// ---------- 广播 ----------

// broadcastButch 广播"挖"的动作（原版 `SendRefMsg(RM_BUTCH, Dir, X, Y, 0, ”)`）。
//
// 客户端 SM_BUTCH（637）按 Recog=对象, Param=x, Tag=y, Series=dir + body=TCharDesc 解，
// 非自己时让那个 actor 播"挖"的动作（ClMain.pas:4059-4071）。
func (s *Server) broadcastButch(p *Player) {
	body := proto.MessageBodyWL{Param1: p.Obj.FeatureBits(), Param2: p.Obj.StatusBits(), Tag1: proto.MakeFeatureEx(true)}
	raw := body.Bytes()
	s.broadcastToViewers(p.Obj.MapRef(), p.Obj.PosX(), p.Obj.PosY(), func(o *Player) {
		// **新协议**：发一条"挖肉"动作（原版 `SM_BUTCH` 那一路只走 legacy，
		// 而 proto 玩家的 legacy 下行会被丢 ⇒ 不补这条，别人就看不见挖的动作）。
		if o.protoOut != nil {
			o.protoOut.action(p.Obj.ID, protoActionButch)
			return
		}
		s.send(o.conn, proto.SM_BUTCH, int32(p.Obj.ID), uint16(p.Obj.PosX()), uint16(p.Obj.PosY()),
			uint16(p.Obj.Facing()), string(raw[:]))
	})
}

// broadcastSkeleton 广播"尸体变成骷髅"（原版 `SendRefMsg(RM_SKELETON, …)`）。
//
// ⚠️ 客户端按 Recog=对象, Param=HP, Tag=MaxHP, Series=伤害 + body=TCharDesc 解
// （ClMain.pas:4187-4191）⇒ 死透了就发 0/0/0。
// ⚠️ 别照旧注释以为"缺了骷髅外观"：**原版也不改外观**（`ClientGetButchItem`
// 只置 `m_boSkeleton := True` 再 `SendRefMsg(RM_SKELETON, dir, x, y, 0, ”)`，
// ObjBase.pas:17494-17508）；`GetFeature` 里**没有**骷髅分支。
// 客户端拿这条包靠的是 `desc.Feature/desc.Status`（`TCharDesc` 只有 8 字节：
// Feature + Status，Grobal2.pas:719-722）—— 我们发的 16 字节 `MessageBodyWL`
// 前 8 字节正好是 (Feature, Status)，客户端只读 8 字节 ⇒ 形状是对上的。
func (s *Server) broadcastSkeleton(m *entity.Monster) {
	body := proto.MessageBodyWL{Param1: m.FeatureBits(), Param2: m.StatusBits(), Tag1: proto.MakeFeatureEx(true)}
	raw := body.Bytes()
	s.broadcastToViewers(m.MapRef(), m.PosX(), m.PosY(), func(o *Player) {
		s.send(o.conn, proto.SM_SKELETON, int32(m.ID), uint16(m.HP), uint16(m.MaxHP), 0, string(raw[:]))
	})
}

// keepCorpseOrRemove 是"怪死了"的收尾，**调用方持 `s.mu`**：
//
//	城堡单位（城门/城墙）：**就地移除**（原版结构物的 Die 与生物不同，重建归城堡系统）。
//	其余（含动物）        ：**留成尸体**。原版 `Die` 之后对象仍在世界里，
//	  直到 `GetTickCount - m_dwDeathTick > dwMakeGhostTime{3 分钟}` 才收走（ObjBase.pas:3769）。
//
// ⚠️ 我们原来在这五个击杀点**立刻** `delete(s.world.monsters, …)` ⇒ 地图上从来没有
// 尸体，于是"取肉 / 变骷髅"整条链路在游戏里**根本够不着**（只有单测能造出尸体来）。
// 尸体留在 `world.monsters` 里是安全的：目标选择（`monsterAt` / 宠物 / 施毒）、
// 占位（`cellFreeLocked`）、掉落落点（`dropPosition`）都已经用 `IsDead()` 排除尸体；
// `updateVision` 也不会把尸体发给刚进视野的人（原版会发，这是我们已知的简化）。
func (s *Server) keepCorpseOrRemove(m *entity.Monster) {
	if m == nil {
		return
	}
	if m.IsCastleUnit() {
		delete(s.world.monsters, m.ID)
		s.world.monsterIdx.Remove(m)
		return
	}
	m.DeathAt = time.Now()
}

// corpseLifetime 是尸体的存活时长（官方代码默认 `dwMakeGhostTime = 3 * 60 * 1000`）。
const corpseLifetime = 3 * time.Minute

// sweepCorpses 收走"躺够 [`corpseLifetime`]"的尸体（原版 `ObjBase.pas:3769`：
// `GetTickCount - m_dwDeathTick > dwMakeGhostTime{3 分钟}`）。
//
// ⚠️ 这个函数是 2026-10-09 补的：`corpseLifetime` 早就定义着，但**全仓库没有第二个引用**
// ⇒ 尸体永远留在地图上（用户问的"尸体应在一段时间后消失，现在这功能有没有？"）。
// 现在挂在 `spawnLoop`（5 秒一档）里跑。
//
// 发包在**锁外**：先删（锁内），再给"看得见它的人"补一条 `EntityDisappear`，
// 并把 id 从他们的 `visible` 里摘掉 —— 不摘的话下一轮视野 diff 会再发一次。
func (s *Server) sweepCorpses(now time.Time) {
	type corpse struct {
		id uint32
		m  *entity.Monster
	}
	var gone []corpse

	s.mu.Lock()
	for id, m := range s.world.monsters {
		if m == nil || !m.IsDead() {
			continue
		}
		if m.DeathAt.IsZero() || now.Sub(m.DeathAt) < corpseLifetime {
			continue // 刚死的（或没记死亡时刻的）先留着
		}
		delete(s.world.monsters, id)
		s.world.monsterIdx.Remove(m)
		gone = append(gone, corpse{id, m})
	}
	viewers := make(map[*Player][]corpse)
	for _, p := range s.world.players {
		for _, g := range gone {
			if p.visible.Remove(g.id) {
				viewers[p] = append(viewers[p], g)
			}
		}
	}
	s.mu.Unlock()

	for p, got := range viewers {
		for _, g := range got {
			// 坐标只是报文要求的填充（原版消失包也带位置）；客户端按 id 摘实体。
			s.sendDisappear(p, g.id, g.m.PosX(), g.m.PosY())
		}
	}
}

// ---------- 小工具 ----------

// absi 取绝对值（本包内多处要用）。
func absi(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// bagFull 报告背包是不是已经满了（原版 `IsEnoughBag` 的反面）。
func (s *Server) bagFull(p *Player) bool {
	if p == nil || p.Char == nil || p.Char.Data == nil {
		return true
	}
	return !s.bagHasRoom(p.Char.Data)
}

// monsterByID 按 ActorId 取怪（调用方不持 s.mu）。
func (s *Server) monsterByID(id uint32) *entity.Monster {
	if id == 0 {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, m := range s.world.monsters {
		if m.ID == id {
			return m
		}
	}
	return nil
}

// damageWeapon 扣武器耐久 n 点（原版 `DoDamageWeapon`）。
func (s *Server) damageWeapon(p *Player, n int) {
	if p == nil || p.Char == nil || p.Char.Data == nil || n <= 0 {
		return
	}
	if proto.SlotWeapon >= len(p.Char.Data.HumItems) {
		return
	}
	w := p.Char.Data.HumItems[proto.SlotWeapon]
	if w == nil || w.Index == 0 {
		return
	}
	if tmpl := s.data.tables.Items.Get(int(w.Index) - 1); tmpl != nil && tmpl.StackLimit() > 0 {
		return
	}
	if uint32(n) >= w.Dura {
		w.Dura = 0
	} else {
		w.Dura -= uint32(n)
	}
	s.sendUseItems(p.conn, p)
	s.applyWeights(p)
}
