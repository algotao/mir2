package script

// NPC 脚本第二批条件（按 1.76 官方脚本使用频率）。
//
// 命名以 OpenMir2 的 ConditionCode.cs 为蓝本（与 Delphi 的 sSC_/nSC_ 表
// 一一对应）。本批覆盖：属性比较、背包/等级变体、地图统计、变量检查、
// 以及城堡相关条件。
//
// 城堡条件对应 Delphi ObjNpc.pas 的：
//   ISCASTLEGUILD       ConditionOfCheckIsCastleGuild   :5062
//   ISATTACKGUILD       ConditionOfCheckIsAttackGuild   :4928
//   ISDEFENSEGUILD      ConditionOfCheckIsDefenseGuild  :5048
//   ISATTACKALLYGUILD   ConditionOfCheckIsAttackAlly    :5020
//   ISDEFENSEALLYGUILD  ConditionOfCheckIsDefenceAlly   :5033
//   INCASTLEWARAREA     InCastleWarArea 判定
//
// ⚠️ OpenMir2 的 ISUNDERWAR 是**空壳**（ConditionProcessingSys.cs:3707-3732
// 整体被注释），但 Delphi 侧有对应的 nSC_ 命令与我们已实现的
// internal/castle，所以这里按 Delphi 实现——不是自创。

import "strings"

// CastleInfo 是条件求值需要的城堡状态。
//
// 由 gamesvr 适配（internal/castle.Castle 刚好有这些方法）。
type CastleInfo interface {
	// InWarArea 是否在城堡战区（含皇宫/密道/战场半径）。
	InWarArea(mapName string, x, y int) bool
	// UnderWar 是否正在攻城。
	UnderWar() bool
	// IsMember 是否占领方成员。
	IsMember(guildName string) bool
	// IsAttackGuild 是否本场攻城方。
	IsAttackGuild(guildName string) bool
	// IsDefenseGuild 是否守方（攻城期外为 false）。
	IsDefenseGuild(guildName string) bool
}

// EvalCondCastle 求值城堡相关条件。
//
// 独立于 EvalCond，因为需要 CastleInfo —— 不能塞进 Context 接口
// （会让所有调用方都被迫实现城堡方法，而多数场景没有城堡）。
func EvalCondCastle(cond string, ctx Context, ci CastleInfo) bool {
	f := splitArgs(cond)
	if len(f) == 0 {
		return false
	}
	// ⚠️ 无参条件（ISUNDERWAR / INCASTLEWARAREA）只有 1 个 token，
	// 不能用 `len(f) < 2` 一刀切挡掉。
	switch strings.ToUpper(f[0]) {
	case "ISCASTLEGUILD":
		return len(f) >= 1 && ci != nil && ci.IsMember(ctx.GuildName())
	case "ISATTACKGUILD":
		return len(f) >= 1 && ci != nil && ci.IsAttackGuild(ctx.GuildName())
	case "ISDEFENSEGUILD":
		return len(f) >= 1 && ci != nil && ci.IsDefenseGuild(ctx.GuildName())
	case "ISUNDERWAR", "INCASTLEWARAREA":
		if ci == nil {
			return false
		}
		// INCASTLEWARAREA 看位置，ISUNDERWAR 只看战期标志。
		if strings.EqualFold(f[0], "ISUNDERWAR") {
			return ci.UnderWar()
		}
		return ci.InWarArea(ctx.MapName(), 0, 0)
	}
	return false
}

// ---------- 属性类条件 ----------

// EvalCond2 是第二批条件（属性/背包/地图统计/变量）。
//
// 单独一个函数而不是塞进 EvalCond：这些条件需要"遍历视野/统计人数"
// 之类重操作，gamesvr 侧要走真实数据；放在 internal/script 里只做
// **纯计算**（给定数值判定），数据由调用方喂。
func EvalCond2(cond string, ctx Context, v Vals) bool {
	f := splitArgs(cond)
	if len(f) == 0 {
		return true
	}
	switch strings.ToUpper(f[0]) {
	case "CHECKHP":
		return cmp2(f[1:], v.HP)
	case "CHECKMP":
		return cmp2(f[1:], v.MP)
	case "CHECKDC":
		return cmp2(f[1:], v.DC)
	case "CHECKMC":
		return cmp2(f[1:], v.MC)
	case "CHECKSC":
		return cmp2(f[1:], v.SC)
	case "CHECKEXP":
		return cmp2(f[1:], v.Exp)
	case "CHECKLEVELEX":
		// CHECKLEVELEX <op> <n>：与 CHECKLEVEL 同义（OpenMir2 的别名）。
		return EvalCond("CHECKLEVEL "+strings.Join(f[1:], " "), ctx)
	case "CHECKBAGSIZE":
		// CHECKBAGSIZE <op> <n>：背包**剩余**格数比较（可省略 op，默认 >=）。
		return cmp2(f[1:], int64(v.BagFree))
	case "CHECKMAPHUMANCOUNT":
		// CHECKMAPHUMANCOUNT <地图> <op> <n>
		if len(f) < 4 || !strings.EqualFold(f[1], v.MapName) {
			return false
		}
		op, ok := parseOp(f[2])
		if !ok {
			return false
		}
		n, ok := parseNum(f[3], ctx)
		if !ok {
			return false
		}
		return compare(int64(v.MapHumanCount), n, op)
	case "CHECKMAPMONCOUNT":
		// CHECKMAPMONCOUNT <地图> <op> <n>
		if len(f) < 4 || !strings.EqualFold(f[1], v.MapName) {
			return false
		}
		op, ok := parseOp(f[2])
		if !ok {
			return false
		}
		n, ok := parseNum(f[3], ctx)
		if !ok {
			return false
		}
		return compare(int64(v.MapMonCount), n, op)
	case "CHECKVAR":
		// CHECKVAR <n> <op> <值>：CHECK 的别名。
		return EvalCond("CHECK "+strings.Join(f[1:], " "), ctx)
	case "CHECKSLAVECOUNT":
		// CHECKSLAVECOUNT <op> <n>：宠物/召唤兽数量（可省略 op）。
		return cmp2(f[1:], int64(v.SlaveCount))
	case "CHECKGROUPCOUNT":
		// CHECKGROUPCOUNT <op> <n>：组队人数（可省略 op，默认 >=）。
		//
		// 对应 ObjNpc.pas:4877-4898 ConditionOfCheckGroupCount。
		// ⚠️ 原版第一行就是 `if m_GroupOwner = nil then Exit` ⇒ **不在队恒为假**，
		// 哪怕写 `CHECKGROUPCOUNT < 5` 也不过。我们用 GroupCount==0 表达"不在队"。
		//
		// ⚠️ 人数取的是队长的 m_GroupMembers.Count（最多 11），不是"我看到几个人"。
		return v.GroupCount > 0 && cmp2(f[1:], int64(v.GroupCount))
	case "CHECKGROUPCLASS":
		// CHECKGROUPCLASS <职业> <op> <n>：队里该职业的人数。
		//
		// 对应 ObjNpc.pas:5602-5641 ConditionOfCheckGroupClass。
		// 职业名沿用原版三个串：WARRIOR/WIZARD/TAOSIST（jWarr/jWizard/jTaoS）。
		if len(f) < 4 {
			return false
		}
		job, ok := jobOfName(f[1])
		if !ok {
			return false
		}
		op, ok := parseOp(f[2])
		if !ok {
			return false
		}
		n, ok := parseNum(f[3], ctx)
		if !ok {
			return false
		}
		return compare(int64(v.GroupJobCount[job]), n, op)
	case "CHECKSIGNMAP", "CHECKMAPNAME":
		// CHECKSIGNMAP <地图名>：当前地图等于给定地图。
		return len(f) >= 2 && strings.EqualFold(f[1], v.MapName)
	}
	return false
}

// Vals 是第二批条件需要的数值快照（由调用方从真实数据取）。
type Vals struct {
	HP, MP        int64
	DC, MC, SC    int64 // 攻击/魔法/道术
	Exp           int64
	BagFree       int
	MapName       string
	MapHumanCount int
	MapMonCount   int
	SlaveCount    int
	// GroupCount 是所在队伍的人数（0 = 没组队）。
	//
	// ⚠️ 上限是 **11** 而不是 10：原版检查 `Count > GroupMembersMax`
	// 才拒绝（ObjBase.pas:17591），见 internal/group 的 HardMax。
	GroupCount int
	// GroupJobCount 按职业统计队内人数，下标 0=战士 1=法师 2=道士。
	GroupJobCount [3]int
}

// jobOfName 把职业名转成 0/1/2（对应 jWarr/jWizard/jTaoS）。
//
// 用的是原版三个串（Warrior/Wizard/Taoist），大小写不敏感。
func jobOfName(s string) (int, bool) {
	switch {
	case strings.EqualFold(s, "WARRIOR"), strings.EqualFold(s, "战士"):
		return 0, true
	case strings.EqualFold(s, "WIZARD"), strings.EqualFold(s, "法师"):
		return 1, true
	case strings.EqualFold(s, "TAOIST"), strings.EqualFold(s, "道士"):
		return 2, true
	}
	return 0, false
}

// cmp2 处理 `<指令> <op> <n>`，也允许省略 op（默认 >=）。
//
// 省略 op 是官方脚本的常见写法（`checkgold 20000` 就是），
// 行为必须与 EvalCond 里的 CHECKGOLD/CHECKLEVEL 一致。
func cmp2(f []string, cur int64) bool {
	// 传进来的是去掉指令名后的参数，所以只剩 1 个 token 时
	// 就是"省略 op 的单值写法"（如 CHECKSLAVECOUNT 1）。
	if len(f) == 0 {
		return false
	}
	// 显式三段：<op> <n>（op 必在首位）
	if len(f) >= 2 {
		if op, ok := parseOp(f[0]); ok {
			if n, err := parseInt64(f[1]); err == nil {
				return compare(cur, n, op)
			}
			return false
		}
	}
	// 省略 op：默认 >=
	n, err := parseInt64(f[len(f)-1])
	if err != nil {
		return false
	}
	return compare(cur, n, OpGE)
}
