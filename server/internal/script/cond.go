package script

// #if 条件求值。
//
// 对应原版 ObjNpc.pas 的 ConditionOfCheck* 家族与 M2Share.pas:190-1200 的
// sSC_/nSC_ 命令表。命名以 OpenMir2 的 ConditionCode.cs 为蓝本
// （它与 Delphi 的 sSC_ 表一一对应，是忠实移植），**不采用** Crystal 的
// NPCChecks.cs——后者自创了 Conquest/Hero/GT/Mail/Buff 五大子系统，
// 1.76 原版里不存在。
//
// 只实现 1.76 官方脚本（Envir/market_def，448 个文件）里**实际在用**的：
// 按文件数排序是 CHECKITEM(38) > CHECKGOLD(23) > CHECKLEVEL(14)，
// 以及 QFunction-0.txt 依赖的 CHECKJOB / CHECKMAP / RANDOM / CHECK。
//
// 数值来源：CHECKGOLD 等支持 P 变量（n1..n99）与 `<$STR(n)>` 插值，
// 由 Context 提供。

import (
	"strings"
)

// Context 是条件求值需要的外部状态。
//
// 刻意用接口而不是 Player：internal/script 不该依赖 gamesvr 的类型。
type Context interface {
	// Gold 返回玩家金币。
	Gold() int64
	// Level 返回玩家等级。
	Level() int32
	// Job 返回职业（0 战 1 法 2 道）。
	Job() int32
	// MapName 返回当前地图号。
	MapName() string
	// CountItem 返回背包里该物品的数量；不存在返回 0。
	CountItem(name string) int
	// Var 返回 P 变量 n1..n99 的值。
	Var(n int) int64
	// SetVar 写 P 变量。
	SetVar(n int, v int64)
	// CheckSkill 是否已学会某技能。
	CheckSkill(name string) bool
	// PKPoint 返回 PK 点。
	PKPoint() int32
	// GuildName 返回所在行会名（无行会返回空串）。
	// 供城堡类条件（ISCASTLEGUILD 等）使用。
	GuildName() string
	// GameTimeName 返回当前"游戏时间"相位的脚本名：
	// SUNRAISE（日出）/ DAY（白天）/ SUNSET（日落）/ NIGHT（夜晚）。
	// 供 `DAYTIME` 条件使用（原版 `g_nGameTime`，见 gamesvr/daynight.go）。
	GameTimeName() string
}

// CompareOp 是比较运算符。
type CompareOp string

const (
	// OpEQ 等于。
	OpEQ CompareOp = "="
	// OpNE 不等于。
	OpNE CompareOp = "<>"
	// OpLT 小于。
	OpLT CompareOp = "<"
	// OpGT 大于。
	OpGT CompareOp = ">"
	// OpLE 小于等于。
	OpLE CompareOp = "<="
	// OpGE 大于等于。
	OpGE CompareOp = ">="
)

// compare 按 op 比较 a 与 b。
func compare(a, b int64, op CompareOp) bool {
	switch op {
	case OpEQ:
		return a == b
	case OpNE:
		return a != b
	case OpLT:
		return a < b
	case OpGT:
		return a > b
	case OpLE:
		return a <= b
	case OpGE:
		return a >= b
	}
	return false
}

// EvalCond 求值一条 #if 条件。
//
// 全部满足才为真（多个条件行之间是 AND，与原版一致）。
func EvalCond(cond string, ctx Context) bool {
	fields := splitArgs(cond)
	if len(fields) == 0 {
		return true // 空条件视为通过
	}
	cmd := strings.ToUpper(fields[0])
	rest := fields[1:]

	switch cmd {
	case "CHECKGOLD":
		// CHECKGOLD <op> <n>，也允许省略 op（默认 >）。
		// 实测写法：checkgold 20000 / checkgold n1
		if len(rest) == 0 {
			return false
		}
		want, ok1 := parseNum(rest[len(rest)-1], ctx)
		if !ok1 {
			return false
		}
		op := OpGE
		if len(rest) >= 2 {
			if o, ok := parseOp(rest[0]); ok {
				op = o
			}
		}
		return compare(ctx.Gold(), want, op)

	case "CHECKITEM":
		// CHECKITEM <物品名> [数量]（数量缺省 1）
		if len(rest) == 0 {
			return false
		}
		name := joinName(rest, 0, 1)
		need := 1
		if len(rest) >= 2 {
			if n, ok := parseNum(rest[1], ctx); ok {
				need = int(n)
			}
		}
		return ctx.CountItem(name) >= need

	case "CHECKITEMW":
		// CHECKITEMW <物品名> [数量]：同上（带 W 的变体，检查装备+背包）
		return EvalCond("CHECKITEM "+strings.Join(rest, " "), ctx)

	case "CHECKLEVEL":
		// CHECKLEVEL <op> <n>
		if len(rest) == 0 {
			return false
		}
		want, ok1 := parseNum(rest[len(rest)-1], ctx)
		if !ok1 {
			return false
		}
		op := OpGE
		if len(rest) >= 2 {
			if o, ok := parseOp(rest[0]); ok {
				op = o
			}
		}
		return compare(int64(ctx.Level()), want, op)

	case "CHECKJOB", "CHECKCLASS":
		// CHECKJOB <职业>（0 战 1 法 2 道）
		if len(rest) == 0 {
			return false
		}
		want, ok := parseNum(rest[0], ctx)
		if !ok {
			return false
		}
		return int64(ctx.Job()) == want

	case "CHECKMAP":
		return len(rest) > 0 && strings.EqualFold(rest[0], ctx.MapName())

	case "CHECKPKPOINT":
		if len(rest) == 0 {
			return false
		}
		want, ok1 := parseNum(rest[len(rest)-1], ctx)
		if !ok1 {
			return false
		}
		op := OpGE
		if len(rest) >= 2 {
			if o, ok := parseOp(rest[0]); ok {
				op = o
			}
		}
		return compare(int64(ctx.PKPoint()), want, op)

	case "DAYTIME":
		// DAYTIME SUNRAISE|DAY|SUNSET|NIGHT（ObjNpc.pas:7013-7030 的 nDAYTIME）：
		// 比的是引擎的全局游戏时间相位（`g_nGameTime`，由宿主机小时推进）。
		// 官方脚本里有 1 处在用（market_def/4Reagent_store-0119.txt）。
		// 原版用 CompareText ⇒ **不区分大小写**。
		if len(rest) == 0 {
			return false
		}
		return strings.EqualFold(rest[0], ctx.GameTimeName())

	case "CHECKSKILL":
		return len(rest) > 0 && ctx.CheckSkill(rest[0])

	case "RANDOM":
		// RANDOM <n>：1..n 之间随机为真（n<=1 恒真）。
		// 注：确定性测试里靠 ctx 注入，见 RandomSource。
		if len(rest) == 0 {
			return false
		}
		n, ok := parseNum(rest[0], ctx)
		if !ok || n <= 1 {
			return true
		}
		return RandomSource(int(n)) == 0

	case "CHECK", "EQUAL", "LARGE", "SMALL":
		// CHECK [n] <op> <值>：读 P 变量比较
		// EQUAL/LARGE/SMALL 是 CHECK 的语法糖（QFunction-0.txt 在用）。
		if cmd == "CHECK" {
			// CHECK 有三种写法（QFunction-0.txt 三种都用）：
			//   1. CHECK <op> <值>        → 隐含 P 变量 n1
			//   2. CHECK <变量> <op> <值>  → 如 CHECK n2 > 100
			//   3. CHECK <算式> <op> <值>  → 如 CHECK n1*2 = 20
			if len(rest) < 2 {
				return false
			}
			if op, ok := parseOp(rest[0]); ok {
				// 形式 1
				want, ok := parseNum(rest[1], ctx)
				if !ok {
					return false
				}
				return compare(ctx.Var(0), want, op)
			}
			// 形式 2 / 3：第一个 token 是变量名或算式，当作左值
			lhs, ok := parseNum(rest[0], ctx)
			if !ok || len(rest) < 3 {
				return false
			}
			op, ok := parseOp(rest[1])
			if !ok {
				return false
			}
			want, ok := parseNum(rest[2], ctx)
			if !ok {
				return false
			}
			return compare(lhs, want, op)
		}
		// EQUAL n1 <值> / LARGE n1 <值> / SMALL n1 <值>
		if len(rest) < 2 {
			return false
		}
		idx, ok := parseVarIndex(rest[0])
		if !ok {
			return false
		}
		want, ok := parseNum(rest[1], ctx)
		if !ok {
			return false
		}
		got := ctx.Var(idx)
		switch cmd {
		case "EQUAL":
			return got == want
		case "LARGE":
			return got >= want
		default:
			return got <= want
		}

	case "CHECKGAMEGOLD", "CHECKGAMEPOINT":
		return false // 未实现：元宝/点数系统未接入

	default:
		// 未识别的条件一律**不通过**，而不是静默通过。
		// 与原版一致（原版遇到未知命令会提示并按 False 处理）。
		return false
	}
}

// EvalConds 求值一组条件，全部满足才为真（AND）。
func EvalConds(conds []string, ctx Context) bool {
	for _, c := range conds {
		if !EvalCond(c, ctx) {
			return false
		}
	}
	return true
}
