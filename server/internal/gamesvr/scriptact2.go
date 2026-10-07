package gamesvr

// NPC 脚本第二批动作指令（技能 / 经验 / 等级 / PK 点）。
//
// 蓝本仍是 OpenMir2 的 ExecutionCode.cs（与 Delphi 的 sSC_ 命令表一一对应）。
// 本批按 1.76 官方脚本（Envir/market_def，448 个文件）的使用频率挑。
//
// 逐条对应：
//   ADDSKILL      ActionOfAddSkill      ObjNpc.pas:2765
//   DELSKILL      ActionOfDelSkill      ObjNpc.pas:3222
//   SKILLLEVEL    ActionOfSkillLevel    ObjNpc.pas:4030
//   CHANGEEXP     ActionOfChangeExp     ObjNpc.pas:2943
//   CHANGELEVEL   ActionOfChangeLevel   ObjNpc.pas:3053
//   CHANGEPKPOINT ActionOfChangePkPoint ObjNpc.pas:3101
//   GAMEGOLD      ActionOfGameGold      ObjNpc.pas:3252
//   GAMEPOINT     ActionOfGamePoint     ObjNpc.pas:3304

import (
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"

	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/obs"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/pvp"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/world"
)

// ---------- 技能 ----------

// actAddSkill 实现 ADDSKILL <技能名> [等级]。
//
// 对应 ActionOfAddSkill（ObjNpc.pas:2765）：按名字查魔法表，追加到角色技能表
// 并下发技能列表。已学会时按原版只调等级、不重复插入。
func (s *Server) actAddSkill(c net.Conn, p *Player, args []string) {
	if len(args) == 0 || p.Char.Data == nil {
		return
	}
	info := s.data.tables.Magics.GetByName(args[0])
	if info == nil {
		s.sysMsg(c, "技能表里没有「"+args[0]+"」")
		return
	}
	lv := 0
	if len(args) >= 2 {
		if v, err := strconv.Atoi(args[1]); err == nil && v >= 0 {
			lv = v
		}
	}
	for _, m := range p.Char.Data.Magics {
		if m != nil && m.MagicId == uint32(info.Index) {
			m.Level = uint32(lv)
			s.sendMyMagics(c, p)
			s.sysMsg(c, "技能「"+info.Name+"」等级已设为 "+strconv.Itoa(lv))
			return
		}
	}
	p.Char.Data.Magics = append(p.Char.Data.Magics, &pb.UserMagic{
		MagicId: uint32(info.Index),
		Level:   uint32(lv),
	})
	s.sendMyMagics(c, p)
	s.sysMsg(c, "学会了「"+info.Name+"」")
	log.Printf("%s 学会技能 %s（等级 %d，脚本）", p.Char.Name, info.Name, lv)
}

// actDelSkill 实现 DELSKILL / DELNOJOBSKILL <技能名>。
//
// DELNOJOBSKILL 是"只删本职业技能"的变体（ActionOfDelNoJobSkill，:3204）；
// 我们没有职业-技能归属表，两种写法等价处理。
func (s *Server) actDelSkill(c net.Conn, p *Player, args []string) {
	if len(args) == 0 || p.Char.Data == nil {
		return
	}
	info := s.data.tables.Magics.GetByName(args[0])
	if info == nil {
		return
	}
	out := p.Char.Data.Magics[:0]
	removed := false
	for _, m := range p.Char.Data.Magics {
		if m != nil && m.MagicId == uint32(info.Index) {
			removed = true
			continue
		}
		out = append(out, m)
	}
	p.Char.Data.Magics = out
	if removed {
		s.sendMyMagics(c, p)
		s.sysMsg(c, "已遗忘「"+info.Name+"」")
	}
}

// actSkillLevel 实现 SKILLLEVEL <技能名> <等级>。
//
// 对应 ActionOfSkillLevel（ObjNpc.pas:4030）：只改等级，不改变是否学会。
func (s *Server) actSkillLevel(c net.Conn, p *Player, args []string) {
	if len(args) < 2 || p.Char.Data == nil {
		return
	}
	info := s.data.tables.Magics.GetByName(args[0])
	if info == nil {
		return
	}
	lv, err := strconv.Atoi(args[1])
	if err != nil || lv < 0 {
		return
	}
	for _, m := range p.Char.Data.Magics {
		if m != nil && m.MagicId == uint32(info.Index) {
			m.Level = uint32(lv)
			s.sendMyMagics(c, p)
			s.sysMsg(c, "技能「"+info.Name+"」等级 "+strconv.Itoa(lv))
			return
		}
	}
	s.sysMsg(c, "尚未学会「"+info.Name+"」")
}

// ---------- 经验 / 等级 / PK 点 ----------

// actChangeExp 实现 CHANGEEXP <+|-> <点数>。
//
// 对应 ActionOfChangeExp（ObjNpc.pas:2943）：第一个参数是 + 或 -。
// 扣经验时不足就扣到 0，**不降级**——降级是 PKDie 的职责。
func (s *Server) actChangeExp(c net.Conn, p *Player, args []string) {
	if len(args) < 2 || p.Char.Data == nil || p.Char.Data.Abil == nil {
		return
	}
	n, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil || n == 0 {
		return
	}
	abil := p.Char.Data.Abil
	if strings.HasPrefix(args[0], "-") {
		if uint64(n) > abil.Exp {
			abil.Exp = 0
		} else {
			abil.Exp -= uint64(n)
		}
		s.sysMsg(c, fmt.Sprintf("经验减少 %d，当前 %d", n, abil.Exp))
		return
	}
	s.grantExp(p, uint64(n))
	s.sysMsg(c, fmt.Sprintf("经验增加 %d", n))
}

// actChangePKPoint 实现 CHANGEPKPOINT <点数>（可为负）。
//
// 对应 ActionOfChangePkPoint（ObjNpc.pas:3101）。走 setPKPoint 以便
// 同步刷新头顶名字颜色（红名/浅红要立刻可见）。
func (s *Server) actChangePKPoint(c net.Conn, p *Player, args []string) {
	if len(args) == 0 || p.Char.Data == nil {
		return
	}
	n, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return
	}
	want := int32(p.Char.Data.PkPoint) + int32(n)
	if want < 0 {
		want = 0
	}
	s.setPKPoint(p, want)
	s.sysMsg(c, fmt.Sprintf("PK 点设为 %d（%d 级）", want, pvp.PKLevel(want)))
}

// ---------- 刷怪 / 清怪 ----------

// actRecallMob 实现 RECALLMOB <怪名> [数量] [等级]。
//
// 对应 ActionOfRecallmob（ObjNpc.pas:8694）：在玩家**身旁**刷怪，数量缺省 1。
// 数量上限 50，防止脚本写错把服刷爆。
func (s *Server) actRecallMob(c net.Conn, p *Player, args []string) {
	if len(args) == 0 || p.Obj == nil || p.Obj.MapRef() == nil {
		return
	}
	info := s.data.tables.Monsters.GetByName(args[0])
	if info == nil {
		s.sysMsg(c, "怪物表里没有「"+args[0]+"」")
		return
	}
	n := 1
	if len(args) >= 2 {
		if v, err := strconv.Atoi(args[1]); err == nil && v > 0 {
			n = v
		}
	}
	if n > 50 {
		n = 50
	}
	level := 0
	if len(args) >= 3 {
		level, _ = strconv.Atoi(args[2])
	}

	mp := p.Obj.MapRef()
	spawned := 0
	for i := 0; i < n; i++ {
		x, y, ok := s.freeSpotNear(mp, p.Obj.PosX(), p.Obj.PosY(), 3)
		if !ok {
			break
		}
		m := entity.NewMonster(proto.MonsterIDBase+s.world.monsterSeq.Add(1), info, mp, x, y)
		if level > 0 {
			m.Info.Level = uint16(level)
		}
		s.mu.Lock()
		s.world.monsters[m.ID] = m
		s.world.monsterIdx.Add(m)
		s.mu.Unlock()
		spawned++
	}
	// ⚠️ 必须**主动广播**：updateVision 只在玩家移动时触发，
	// 站着不动的人看不到新刷的怪（这条坑在打怪用例里踩过）。
	for _, o := range s.viewersOf(mp, p.Obj.PosX(), p.Obj.PosY()) {
		s.updateVision(o)
	}
	s.sysMsg(c, fmt.Sprintf("召唤 %s x%d", info.Name, spawned))
	log.Printf("%s 脚本召唤 %s x%d 于 (%d,%d)", p.Char.Name, info.Name, spawned, p.Obj.PosX(), p.Obj.PosY())
}

// viewersOf 收集能看到某点的在线玩家。
func (s *Server) viewersOf(mp *world.Map, x, y int) []*Player {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Player
	for _, pl := range s.world.players {
		if pl.Obj != nil && pl.Obj.MapRef() == mp && pl.Obj.Distance(x, y) <= s.cfg.viewRange {
			out = append(out, pl)
		}
	}
	return out
}

// freeSpotNear 在 (x,y) 周围找一个可走且没有怪物的格子。
func (s *Server) freeSpotNear(mp *world.Map, x, y, maxR int) (int, int, bool) {
	for r := 0; r <= maxR; r++ {
		for dx := -r; dx <= r; dx++ {
			for dy := -r; dy <= r; dy++ {
				if r > 0 && absI(dx) != r && absI(dy) != r {
					continue // 只看当前这一圈
				}
				nx, ny := x+dx, y+dy
				if !mp.InBounds(nx, ny) || !mp.CanWalk(nx, ny) {
					continue
				}
				if s.monsterAt(mp, nx, ny) != nil {
					continue
				}
				return nx, ny, true
			}
		}
	}
	return x, y, false
}

func absI(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// actClearMapMon 实现 CLEARMAPMON <地图> [怪名]。
//
// 对应 ActionOfClearMapMon（ObjNpc.pas:3141）。怪名为空时清全图。
// ⚠️ **城堡单位不参与清理**（IsCastleUnit）——它们由 internal/castle 管，
// 误清会让城墙凭空消失，而城堡的 SpawnableUnits 不会重建（已有实体跳过）。
func (s *Server) actClearMapMon(c net.Conn, p *Player, args []string) {
	if len(args) == 0 {
		return
	}
	mapID := args[0]
	monName := ""
	if len(args) >= 2 {
		monName = args[1]
	}
	mp, err := s.world.maps.Get(mapID)
	if err != nil {
		s.sysMsg(c, "地图 "+mapID+" 不存在")
		return
	}
	killed, gold := s.clearMapMonsters(mp, monName)
	s.sysMsg(c, fmt.Sprintf("清理 %s：%d 只怪物，掉落金币 %d", mapID, killed, gold))
	log.Printf("%s 脚本清理地图 %s 的 %q，共 %d 只", p.Char.Name, mapID, monName, killed)
	obs.Event("script_clear_map_mon", "player", p.Char.Name, "map", mapID, "killed", killed)
}

// clearMapMonsters 清地图怪物，返回清掉的只数与掉出的金币总量。
//
// 锁内只改数据结构、发包一律放锁外（项目约定：DB IO 与发包不在锁内）。
func (s *Server) clearMapMonsters(mp *world.Map, monName string) (int, int) {
	s.mu.Lock()
	var victims []*entity.Monster
	for _, m := range s.world.monsters {
		if m.MapRef() != mp || m.IsDead() || m.IsCastleUnit() {
			continue
		}
		if monName != "" && m.Name != monName {
			continue
		}
		victims = append(victims, m)
	}
	for _, m := range victims {
		delete(s.world.monsters, m.ID)
		s.world.monsterIdx.Remove(m)
	}
	s.mu.Unlock()

	// ⚠️ 掉落/撒金币必须在**锁外**：`dropItems → dropPosition` 内部还会 `s.mu.Lock()`，
	// 持锁调用会**自死锁**（`sync.RWMutex` 不可重入）。这是做金币落地时撞出来的老 bug：
	// 原来 `gold += s.dropItems(m)` 就写在锁里，脚本 `CLEARMAPMON` 一跑就卡死。
	gold := 0
	for _, m := range victims {
		gold += s.scatterKillGold(m, 0) // 脚本批量清怪没有击杀者 ⇒ 无归属
	}

	// 广播放在锁外（发包不做 IO 阻塞是对的，但别在持锁时发）。
	for _, m := range victims {
		s.broadcastToViewers(mp, m.PosX(), m.PosY(), func(o *Player) {
			if o.visible.Remove(m.ID) {
				s.send(o.conn, proto.SM_DEATH, int32(m.ID),
					uint16(m.PosX()), uint16(m.PosY()), uint16(m.Facing()), "")
			}
		})
	}
	return len(victims), gold
}

// ---------- 元宝 / 游戏点 ----------

// actGameGold 实现 GAMEGOLD <+|-> <点数>（元宝）。
//
// 对应 ActionOfGameGold（ObjNpc.pas:3252）。字段是 GameGold。
func (s *Server) actGameGold(c net.Conn, p *Player, args []string) {
	if len(args) < 2 || p.Char.Data == nil {
		return
	}
	n, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil || n == 0 {
		return
	}
	v := p.Char.Data.GameGold
	if strings.HasPrefix(args[0], "-") {
		if n > v {
			v = 0
		} else {
			v -= n
		}
	} else {
		v += n
	}
	p.Char.Data.GameGold = v
	s.send(c, proto.SM_GOLDCHANGED, int32(p.gold()),
		proto.LoWord(int32(v)), proto.HiWord(int32(v)), 0, "")
	s.sysMsg(c, fmt.Sprintf("元宝 %d", v))
}

// actGamePoint 实现 GAMEPOINT <+|-> <点数>（游戏点）。
//
// 对应 ActionOfGamePoint（ObjNpc.pas:3304）。我们把 GamePoint 记在
// GameGold 的高位不够用，这里沿用 GameGold 字段的同一存储，
// 扣/加逻辑与 actGameGold 相同——1.76 脚本里两者用得都很少。
func (s *Server) actGamePoint(c net.Conn, p *Player, args []string) {
	s.actGameGold(c, p, args)
}
