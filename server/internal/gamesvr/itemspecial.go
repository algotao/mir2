// 物品**形状**触发的特殊效果（原版那一批 `m_boXxx` 标志）的消费侧。
//
// 形状→标志的判定在 itemabil.go 的 playerItemSpecials；这里放它们的**效果**：
//
//	复活戒指(114)  死亡前拦一次，60 秒冷却，每次扣 1000 耐久   ObjBase.pas:3755
//	虹魔套(136-138) 命中吸血 `伤害 × ΣAniCount / 100`（≥2 才回） :22273
//	麻痹戒指(113)  命中按"目标抗毒 + 5"抽签，把目标石化 5 秒    :22265
//
// ⚠️ 三处数值都取官方/出厂配置，不自己编：
//
//	dwRevivalTime    = 60 * 1000（M2Share.pas:2033，!setup.txt 的 RevivalTime，官方文件没写）
//	nAttackPosionRate= 5（nAttackPosionRate）
//	nAttackPosionTime= 5（nAttackPosionTime，单位秒）
package gamesvr

import (
	"fmt"
	"github.com/algotao/mir2/server/internal/delphi"
	"strconv"
	"strings"
	"time"

	"github.com/algotao/mir2/server/internal/entity"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/world"
)

// 复活戒指
const (
	// revivalCooldown 是复活戒指的冷却（原版 dwRevivalTime = 60 秒）。
	revivalCooldown = 60 * time.Second
	// revivalRingDuraCost 是每次复活扣的耐久。原版 `Dec(nDura, 1000)`
	//（ItemDamageRevivalRing，:3644），耐久单位是 1/1000 ⇒ 即扣 1 点。
	revivalRingDuraCost = 1000
	// revivalRingShape 是复活戒指的形状号（Identify 时的 `pSItem.Shape in [114, ...]`）。
	revivalRingShape = 114
)

// 传送戒指
const (
	// userMoveTime 是 `@move` 的冷却（原版 `g_Config.dwUserMoveTime`，
	// 官方 !setup.txt:848 `UserMoveTime=10`，单位秒）。
	userMoveTime = 10 * time.Second
	// teleportRingShape 是传送戒指的形状号（`m_boTeleport` 来自 Shape 112）。
	teleportRingShape = 112
)

// 戒指授予的临时技能（原版 `AddItemSkill` / `DelItemSkill`，ObjBase.pas:18652/18907）
const (
	// itemSkillFireBall / itemSkillHeal 是 `AddItemSkill(1/2)` 的目标技能号：
	// 1 → 火球术（`SKILL_FIREBALL`）、2 → 治愈术（`SKILL_HEALLING`）。
	itemSkillFireBall = 1
	itemSkillHeal     = 2
	// itemSkillLevel 是授予时的等级（原版 `UserMagic.btLevel := 1`）。
	itemSkillLevel = 1
)

// 探测项链
const (
	// permissionProbe / permissionProbeCooldown 是探测项链的两个 GM 权限后门
	//（原版 ObjBase.pas:14358 的 `m_btPermission >= 6` 与 :14363 的 `>= 3`）。
	// ⚠️ 数字是**逐字**照抄的，别按"看起来差不多"改成别处用过的等级。
	permissionProbe         = 6 // 权限 ≥ 6：不戴项链也能用 @searchhuman
	permissionProbeCooldown = 3 // 权限 ≥ 3：跳过 10 秒冷却

	// probeCooldown 是 `@searchhuman` 的冷却（原版 `(GetTickCount - m_dwProbeTick) > 10000`）。
	probeCooldown = 10 * time.Second
)

// 麻痹戒指
const (
	// attackPoisonRate 是原版 `g_Config.nAttackPosionRate`（默认 5）。
	attackPoisonRate = 5
	// attackPoisonTime 是石化时长（`nAttackPosionTime` = 5 秒）。
	attackPoisonTime = 5 * time.Second
)

// tryRevivalRing 在玩家 HP 归零、**真正死亡之前**拦一次。
//
// 对应 ObjBase.pas:3755-3765：
//
//	if ((m_LastHiter = nil) or not m_LastHiter.m_boUnRevival) and m_boRevival
//	   and (GetTickCount - m_dwRevivalTick > g_Config.dwRevivalTime) then
//	  m_dwRevivalTick := GetTickCount; ItemDamageRevivalRing;
//	  m_WAbil.HP := m_WAbil.MaxHP; HealthSpellChanged; 提示"复活戒指生效，体力恢复"
//
// ⚠️ 原版还有"攻击者带 m_boUnRevival（防复活）"这一条 —— 那是 GM/特殊怪才有的标志，
// 我们没实现，故不判。
//
// 返回 true 表示"已复活，别走死亡流程"。
func (s *Server) tryRevivalRing(p *Player) bool {
	if p == nil || p.Char == nil || p.Char.Data == nil || p.Char.Data.Abil == nil {
		return false
	}
	if !p.equipSpecials.revival {
		return false
	}
	if !p.revivalAt.IsZero() && time.Since(p.revivalAt) < revivalCooldown {
		return false
	}
	// 消耗戒指耐久（≤0 就销毁这一格）
	used := false
	for _, u := range p.Char.Data.HumItems {
		if u == nil || u.Index == 0 {
			continue
		}
		it := s.data.tables.Items.Get(int(u.Index) - 1)
		if it == nil || it.Shape != revivalRingShape {
			continue
		}
		if u.Dura > revivalRingDuraCost {
			u.Dura -= revivalRingDuraCost
		} else {
			*u = pb.UserItem{}
		}
		used = true
		break
	}
	if !used {
		return false
	}
	p.revivalAt = time.Now()
	hp := p.refillHP() // 复活戒指：回满血（持锁）
	s.sendHealthChanged(p, p.Obj.ID, hp, p.mp(), p.maxHP())
	s.sysMsg(p.conn, "复活戒指生效，体力恢复")
	s.sendUseItems(p.conn, p) // 耐久变了，刷新装备栏
	s.refreshSpecials(p)
	logpvp("%s 的复活戒指生效（体力恢复，冷却 %v）", p.Char.Name, revivalCooldown)
	return true
}

// hongMoLeech 结算虹魔套吸血，返回实际回血量（HP 已加好）。
//
// 对应 ObjBase.pas:22273-22280：
//
//	m_db3B0 := nPower / 100 * m_nHongMoSuite;      // nPower = 这一击的伤害
//	if m_db3B0 >= 2.0 then DamageHealth(-Trunc(m_db3B0));
//
// 其中 `m_nHongMoSuite` 是**每件虹魔装备的 AniCount 之和**（不是件数，见 :3255）。
func (s *Server) hongMoLeech(p *Player, target *entity.Monster, dmg uint32) int {
	if p == nil || p.Char == nil || p.Char.Data == nil || p.Char.Data.Abil == nil {
		return 0
	}
	suite := p.equipSpecials.hongMo
	if suite <= 0 || dmg == 0 {
		return 0
	}
	heal := int(dmg) * suite / 100
	if heal < 2 { // 原版 `if m_db3B0 >= 2.0`
		return 0
	}
	before := p.hp()
	after := p.addHP(int64(heal)) // 治疗：夹到上限（持锁）
	gained := int(after - before)
	if gained <= 0 {
		return 0
	}
	s.sendHealthChanged(p, p.Obj.ID, after, p.mp(), p.maxHP())
	logpvp("%s 的虹魔套吸了 %d 点体力（伤害 %d × %d%%）", p.Char.Name, gained, dmg, suite)
	return gained
}

// paralyzeOnHit 是麻痹戒指的公共核心，逐句对照 ObjBase.pas:22265-22268：
//
//	if not AttackTarget.m_boUnParalysis and m_boParalysis
//	   and (Random(AttackTarget.m_btAntiPoison + nAttackPosionRate) = 0) then
//	  AttackTarget.MakePosion(POISON_STONE, nAttackPosionTime, 0);
//
// ⚠️ 目标**可以是怪物也可以是玩家**（原版 `AttackTarget` 是 `TBaseObject`）。
// anti 是目标抗毒：我们的怪物表没有这一列 ⇒ 怪物一律 0（`Random(5) = 0` ⇒ 20%）；
// 玩家走装备汇总（`playerAddAbil(victim).antiPoison`），与中毒判定同一份数据。
func (s *Server) paralyzeOnHit(p *Player, obj *entity.Object, anti int, label string) bool {
	if p == nil || obj == nil || !p.equipSpecials.paralysis {
		return false
	}
	if delphi.Random(anti+attackPoisonRate) != 0 {
		return false
	}
	obj.Stone(attackPoisonTime)
	logpvp("%s 的麻痹戒指生效：%s 被石化 %v", p.Char.Name, label, attackPoisonTime)
	return true
}

// paralysisOnHit 是打怪时的麻痹。
//
// ⚠️ 石化走 `Object.Stone`（原版 `POISON_STONE`），**不是** `Monster.Seize`
// （那是困魔咒 `m_boHolySeize`，被打会解除）。两者都进 `CanAct` 的行动门。
func (s *Server) paralysisOnHit(p *Player, target *entity.Monster) bool {
	if target == nil {
		return false
	}
	return s.paralyzeOnHit(p, target.Object, 0, target.Name)
}

// paralysisPlayerOnHit 是打玩家时的麻痹（PvP）。原版 `_Attack` 对任何
// `TBaseObject` 都生效 ⇒ 戴着麻痹戒指打人同样有 1/5（按对方抗毒）概率石化对方。
//
// 石化后按原版补两件事（`MakePosion` 末尾，ObjBase.pas:22744-22748）：
// 状态位变了就 `StatusChanged()`（⇒ SM_CHARSTATUSCHANGED），
// 玩家还会收到 `SysMsg(Format(sYouPoisoned, [nTime, nPoint]))` 那条文案。
func (s *Server) paralysisPlayerOnHit(attacker, victim *Player) bool {
	if attacker == nil || victim == nil || victim.Obj == nil || victim.Char == nil {
		return false
	}
	anti := s.playerAddAbil(victim).antiPoison
	if !s.paralyzeOnHit(attacker, victim.Obj, anti, victim.Char.Name) {
		return false
	}
	s.broadcastStatus(victim)
	s.sysMsg(victim.conn, fmt.Sprintf("你中毒了[时间:%d秒，点数:%d点]",
		int(attackPoisonTime.Seconds()), 0))
	return true
}

// cmdUserMove 实现传送戒指开放的 `@move X Y`（原版 TPlayObject.CmdUserMoveXY，
// ObjBase.pas:15195-15240）：
//
//	if m_boTeleport then begin
//	  nX := Str_ToInt(sX, -1);  nY := Str_ToInt(sY, -1);
//	  if not m_PEnvir.Flag.boNOPOSITIONMOVE then begin
//	    if m_PEnvir.CanWalkOfItem(nX, nY, boUserMoveCanDupObj{0}, boUserMoveCanOnItem{1}) then begin
//	      if (GetTickCount - m_dwTeleportTick) > dwUserMoveTime * 1000 then
//	        SendRefMsg(RM_SPACEMOVE_FIRE, ...); SpaceMove(m_sMapName, nX, nY, 0)
//	      else SysMsg('N秒之后才可以再使用此功能！！！')
//	    end else SysMsg(无法移动到地图: ...)
//	  end else SysMsg('此地图禁止使用此命令！！！')
//	end
//
// 我们的对应物：`switchMap` 就是"同图落点"（瞬息移动(21) 也走它）。
// 三处**有意不实现**（都因为缺数据/机制，不是漏）：
//   - 地图的 `boNOPOSITIONMOVE` 标志（我们的地图没有这个 flag）
//   - `CanWalkOfItem` 的两个开关：官方 !setup.txt:846-847 是 `UserMoveCanDupObj=0`
//     / `UserMoveCanOnItem=1` ⇒ 等价于"可走 **且** 该格没有别的对象"（我们本来就
//     一格一对象）；"允许落在物品上"我们没有地面物品，天然满足
//   - `RM_SPACEMOVE_FIRE` 起手特效（我们没实现这条 RM）
func (s *Server) cmdUserMove(p *Player, args []string) bool {
	if p == nil || p.Obj == nil {
		return true
	}
	if !p.equipSpecials.teleport {
		// 原版这个分支在后面（else 之后被截断），语义就是"没有戒指什么都别做"。
		return false
	}
	// 地图标记 `NOPOSITIONMOVE`：官方 `ObjBase.pas:15205` 的 `if not …Flag.boNOPOSITIONMOVE`
	// ⇒ 该图禁止定位传送（静默不移动；原版 else 分支未细读）。
	if mi := s.mapFlagOf(p.Obj.MapRef()); mi != nil && mi.NoPositionMove {
		return true
	}
	if len(args) < 2 {
		s.sysMsg(p.conn, "命令格式: @move 座标X 座标Y")
		return true
	}
	x, err1 := strconv.Atoi(args[0])
	y, err2 := strconv.Atoi(args[1])
	if err1 != nil || err2 != nil || x < 0 || y < 0 {
		s.sysMsg(p.conn, "命令格式: @move 座标X 座标Y")
		return true
	}
	if !p.lastTeleportAt.IsZero() && time.Since(p.lastTeleportAt) < userMoveTime {
		left := userMoveTime - time.Since(p.lastTeleportAt)
		s.sysMsg(p.conn, fmt.Sprintf("%d秒之后才可以再使用此功能！！！", int(left.Seconds())+1))
		return true
	}
	mp := p.Obj.MapRef()
	bad := fmt.Sprintf("无法移动到地图: %s X:%s Y:%s", mapNameOf(mp), args[0], args[1])
	if mp == nil || !mp.InBounds(x, y) || !mp.CanWalk(x, y) {
		s.sysMsg(p.conn, bad)
		return true
	}
	// `UserMoveCanDupObj=0` ⇒ 落点不能站着别的对象（我们本来就是"每格一个对象"）。
	// cellFreeLocked 要求**调用方持锁**，所以这里自己 RLock。
	s.mu.RLock()
	free := s.cellFreeLocked(mp, x, y)
	s.mu.RUnlock()
	if !free {
		s.sysMsg(p.conn, bad)
		return true
	}
	p.lastTeleportAt = time.Now()
	if err := s.switchMap(p.conn, p, p.Obj.MapRef().Name, x, y); err != nil {
		logpvp("%s 的 @move(%d,%d) 失败: %v", p.Char.Name, x, y, err)
		return true
	}
	logpvp("%s 用传送戒指移动到 %s(%d,%d)", p.Char.Name, p.Obj.MapRef().Name, x, y)
	return true
}

// mapNameOf 取地图名（nil 安全，只为拼文案）。
func mapNameOf(m *world.Map) string {
	if m == nil {
		return ""
	}
	return m.Name
}

// syncItemSkills 同步"装备授予的临时技能"（火焰戒指 ⇒ 火球术、治愈戒指 ⇒ 治愈术）。
//
// 原版在 `RecalcAbilitys` 末尾无条件地调（ObjBase.pas:3453-3457）：
//
//	if m_boFlameRing then AddItemSkill(1) else DelItemSkill(1);
//	if m_boRecoveryRing then AddItemSkill(2) else DelItemSkill(2);
//
// `AddItemSkill`（:18652-18678）：目标技能**已经在列表里就不重复加**
// （`if not IsTrainingSkill(Magic.wMagicId)`），加了就按**等级 1** 加并下发。
// `DelItemSkill`（:18907-18933）有个**按职业的保护**：
//
//	1: if m_btJob <> jWizard then DELETESKILL(sFireBallSkill);   // 法师不删火球术
//	2: if m_btJob <> jTaos   then DELETESKILL(sHealSkill);       // 道士不删治愈术
//
// 我们照抄这两条 —— 也就是说：战士戴了火焰戒指、再摘下来，会把"火球术"删掉
// （原版就是这个行为；法师不受影响，因为那个职业本来就该有火球术）。
func (s *Server) syncItemSkills(p *Player) {
	if p == nil || p.Char == nil || p.Char.Data == nil {
		return
	}
	d := p.Char.Data
	sp := p.equipSpecials
	changed := false

	ensure := func(id uint32, want bool) {
		idx := -1
		for i, um := range d.Magics {
			if um != nil && um.MagicId == id {
				idx = i
				break
			}
		}
		if want {
			if idx >= 0 {
				return // 已在列表里：原版不重复加（等级也不动）
			}
			d.Magics = append(d.Magics, &pb.UserMagic{MagicId: id, Level: itemSkillLevel})
			changed = true
			logpvp("%s 的%s授予了 %s（等级 %d）", p.Char.Name,
				map[bool]string{true: "火焰戒指", false: "治愈戒指"}[id == itemSkillFireBall],
				map[uint32]string{itemSkillFireBall: "火球术", itemSkillHeal: "治愈术"}[id], itemSkillLevel)
			return
		}
		// 回收：照原版按职业保护（法师/道士各自的"本命技能"不删）
		if idx < 0 {
			return
		}
		if id == itemSkillFireBall && d.Job == entity.JobWizard {
			return
		}
		if id == itemSkillHeal && d.Job == entity.JobTaos {
			return
		}
		d.Magics = append(d.Magics[:idx], d.Magics[idx+1:]...)
		changed = true
		logpvp("%s 摘下%s，收回了 %s", p.Char.Name,
			map[bool]string{true: "火焰戒指", false: "治愈戒指"}[id == itemSkillFireBall],
			map[uint32]string{itemSkillFireBall: "火球术", itemSkillHeal: "治愈术"}[id])
	}
	ensure(itemSkillFireBall, sp.flameRing)
	ensure(itemSkillHeal, sp.healRing)

	if changed && p.conn != nil {
		s.sendMyMagics(p.conn, p) // 全量重发技能列表
	}
}

// cmdSearchHuman 实现探测项链开放的 `@searchhuman <角色名>`
// （原版 TPlayObject.CmdSearchHuman，ObjBase.pas:14356-14380）。
//
//	if m_boProbeNecklace or (m_btPermission >= 6) then begin
//	  ... 空名字 ⇒ 提示用法
//	  if (GetTickCount - m_dwProbeTick) > 10000 or (m_btPermission >= 3) then begin
//	    PlayObject := UserEngine.GetPlayObject(sHumanName);
//	    if PlayObject <> nil then SysMsg('X 现在位于 <地图> X:Y', 蓝)
//	    else SysMsg('X 现在不在线，或位于其它服务器上！！！', 红)
//	  end
//	end
//
// 两个 `m_btPermission` 后门照原版保留（`OBJ_BASE_PERMISSION_PROBE` = 6、
// `…_COOLDOWN` = 3）：
//
//	权限 ≥ 6 的 GM：**不戴项链也能用**；
//	权限 ≥ 3：**跳过 10 秒冷却**（原版把冷却判定写成
//	`(GetTickCount - m_dwProbeTick) > 10000 or (m_btPermission >= 3)`）。
func (s *Server) cmdSearchHuman(p *Player, args []string) bool {
	if p == nil {
		return true
	}
	// 原版：`if m_boProbeNecklace or (m_btPermission >= 6) then … else SysMsg('您现在还无法使用此功能！！！')`
	if !p.equipSpecials.probeNecklace && p.permission < permissionProbe {
		s.sysMsg(p.conn, "您现在还无法使用此功能！！！")
		return true
	}
	if len(args) < 1 || strings.TrimSpace(args[0]) == "" {
		s.sysMsg(p.conn, "命令格式: @searchhuman 人物名称")
		return true
	}
	name := strings.TrimSpace(args[0])
	// 冷却：权限 ≥ 3 直接跳过
	if p.permission < permissionProbeCooldown &&
		!p.lastProbeAt.IsZero() && time.Since(p.lastProbeAt) < probeCooldown {
		left := probeCooldown - time.Since(p.lastProbeAt)
		s.sysMsg(p.conn, fmt.Sprintf("%d秒之后才可以再使用此功能！！！", int(left.Seconds())+1))
		return true
	}
	p.lastProbeAt = time.Now()
	target := s.playerByName(name)
	if target != nil && target.Obj != nil {
		s.sysMsg(p.conn, fmt.Sprintf("%s 现在位于 %s %d:%d", name, mapDescOf(target.Obj.MapRef()),
			target.Obj.PosX(), target.Obj.PosY()))
		return true
	}
	s.sysMsg(p.conn, name+" 现在不在线，或位于其它服务器上！！！")
	return true
}

// mapDescOf 取地图显示名（nil 安全）。
func mapDescOf(m *world.Map) string {
	if m == nil {
		return ""
	}
	return m.Name
}
