package script

// 条件求值用到的解析小工具。

import (
	"math/rand/v2"
	"strconv"
	"strings"
)

// RandomSource 是 RANDOM 条件的随机源。
//
// 抽成变量是为了让单测能确定性地覆盖 RANDOM 分支；生产用 rand.Intn。
var RandomSource = func(n int) int { return rand.IntN(n) }

// parseOp 解析比较运算符。
func parseOp(s string) (CompareOp, bool) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "=", "==":
		return OpEQ, true
	case "<>", "!=":
		return OpNE, true
	case "<":
		return OpLT, true
	case ">":
		return OpGT, true
	case "<=":
		return OpLE, true
	case ">=":
		return OpGE, true
	}
	return "", false
}

// parseVarIndex 解析脚本变量名 → 索引（0..98）。
//
// ⚠️ **两种前缀都要认**：
//   - n1..n99 —— 9Bsender-0.txt 等脚本用的数值变量
//   - S1..S99 —— QFunction-0.txt 用的（:231-234 的 GetItemFieldValue
//     读的就是 S1/S2/S3/S4）
//
// 早期只认 n，导致 QFunction 那批脚本的变量全部读写失败。
//
// 两种前缀映射到**同一索引空间**（原版 n 与 S 是分开的两套：n 是数值、
// S 是字符串）。我们的 P 变量只有整数一种，所以合并；
// 真需要字符串变量时再拆开。
func parseVarIndex(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return 0, false
	}
	head := s[0]
	if head != 'n' && head != 'N' && head != 's' && head != 'S' {
		return 0, false
	}
	n, err := strconv.Atoi(s[1:])
	if err != nil || n < 1 || n > 99 {
		return 0, false
	}
	return n - 1, true
}

// isPlainVarName 报告 s 是否是"纯变量名"（n1..n99，不含任何运算符）。
func isPlainVarName(s string) bool {
	if strings.ContainsAny(strings.TrimSpace(s), "+-*/") {
		return false
	}
	_, ok := parseVarIndex(s)
	return ok
}

// parseNum 解析一个数值：字面量、P 变量（n1）或已解析的表达式。
func parseNum(s string, ctx Context) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	// `<$Str(x)>` 插值：解开一层再按普通值解析。
	// 解开后 inner 不再是插值形式（脚本里不会嵌套），不会无限递归。
	//
	// ⚠️ 必须**不区分大小写**：官方 QFunction-0.txt 写的是 `<$Str(S4)>`
	//（小写 tr），只认大写 STR 会让这批脚本的变量全部读不到。
	if len(s) >= 7 && strings.EqualFold(s[:6], "<$str(") && s[len(s)-2:] == ")>" {
		return parseNum(s[6:len(s)-2], ctx)
	}
	// P 变量
	if idx, ok := parseVarIndex(s); ok {
		return ctx.Var(idx), true
	}
	// 字面量
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, true
	}
	// 简单算式（QFunction-0.txt 大量用 `MOV n1 <$STR(n2)+1>` 这类）
	if v, ok := evalArith(s, ctx); ok {
		return v, true
	}
	return 0, false
}

// evalArith 求值 `a+b` / `a-b` / `a*b` / `a/b` 这类单层算式。
//
// 只支持**单层**（左值 op 右值），不做优先级与括号——
// 1.76 官方脚本里的算式都是这种形式，够用且不会有歧义。
func evalArith(expr string, ctx Context) (int64, bool) {
	expr = strings.TrimSpace(expr)
	for _, op := range []string{"+", "-", "*", "/"} {
		i := strings.Index(expr, op)
		if i <= 0 {
			continue
		}
		// 跳过符号本身（可能出现在负数里）
		lhs := strings.TrimSpace(expr[:i])
		rhs := strings.TrimSpace(expr[i+len(op):])
		a, ok1 := parseAtom(lhs, ctx)
		b, ok2 := parseAtom(rhs, ctx)
		if !ok1 || !ok2 {
			continue
		}
		switch op {
		case "+":
			return a + b, true
		case "-":
			return a - b, true
		case "*":
			return a * b, true
		case "/":
			if b == 0 {
				return 0, false
			}
			return a / b, true
		}
	}
	return 0, false
}

// parseAtom 解析算式的原子（`<$STR()>` 的解包已在 parseNum 里做）。
func parseAtom(s string, ctx Context) (int64, bool) { return parseNum(s, ctx) }

// splitArgs 按空白切分，但**保留引号内的空格**。
//
// 脚本里物品名可能带空格（`强效太阳水包`），也可能有引号
// （`checknamelist "D:\Mir2\Envir\名单.txt"`）。
func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
		case (r == ' ' || r == '\t') && !inQuote:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// joinName 把 args[from:to] 拼回一个名字（物品名可能含空格）。
func joinName(args []string, from, to int) string {
	if from >= len(args) {
		return ""
	}
	if to > len(args) {
		to = len(args)
	}
	return strings.Join(args[from:to], " ")
}

// Str 便于调试：把条件列表拼回可读形式。
func Str(conds []string) string { return fmt2(conds) }

func fmt2(conds []string) string {
	if len(conds) == 0 {
		return "(无)"
	}
	return strings.Join(conds, " && ")
}

// ParseValue 解析一个值表达式（字面量 / P 变量 / 算式 / `<$STR()>` 插值）。
//
// 供外部（MOV/SET 等动作指令）复用条件侧的解析能力，避免两套实现。
func ParseValue(s string, ctx Context) (int64, bool) { return parseNum(s, ctx) }

// parseInt64 解析纯字面量（不查变量）。
func parseInt64(s string) (int64, error) { return strconv.ParseInt(strings.TrimSpace(s), 10, 64) }

// ParseVarIndex 暴露变量名解析（供 gamesvr 侧共用同一套规则）。
func ParseVarIndex(s string) (int, bool) { return parseVarIndex(s) }
