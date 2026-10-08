// Package authn —— 账号名规则（建号用，D-32）。
//
// 放在 `authn` 而不是 `gamesvr`：账号名是**存储主键**，规范化的那一份必须与
// 校验它的那一份是同一个（同 R-7 的"安全策略只允许有一份"）。将来 legacy 那条
// 路（`accountsvc` 的 `CM_ADDNEWUSER`）要接，也得走这里。
package authn

import (
	"fmt"
	"strings"
)

// 账号名长度上下限。
//
// 下界 3 来自原版客户端（`IntroScn.pas:990`「账号至少 3 位」）；上界 14 与我们的
// 输入框一致（`client/core/src/login_ui.rs` 的 `Art::MAX_ACCOUNT`）。
const (
	AccountMinLen = 3
	AccountMaxLen = 14
)

// CanonicalAccount 把账号名**规范化到存储用的形状**：去空白 + 转小写。
//
// 用途：**所有按账号查库的地方**都必须先过它（取盐、登录、建号）—— 账号在库里
// 一律是小写（建号时归一化），查的时候不归一化就会出现"建了 newbie、输 NewBie
// 就查不到"这种客服级问题。
//
// ⚠️ 它**不校验**（不合法也照原样返回小写）：取盐/登录对这种名字只该"查不到"，
// 不该报"名字非法"——那等于告诉对方"这个名字格式有问题"，与 D-24① 的
// "不区分账号是否存在"是同一类泄露。
//
// ⚠️ D-24 的证明是 `HMAC(K, nonce ‖ account)`，那个 `account` 用的是**客户端发来的
// 原样字符串**（两端要对齐），别拿这个函数的结果去替换它。
func CanonicalAccount(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// NormalizeAccount 规范化并校验账号名：返回**小写**账号，非法则报错。
//
// 规则：3..14 位、仅 `[a-z0-9_]`，统一转小写（`A` 与 `a` 是同一个号）。
//
// ⚠️ 与原版 `CheckAccountName`（`LoginSrv/LSShare.pas:169-192`）的差别，写清楚
// 免得以后被当成"漏抄"：
//
//   - 原版放行 `'0'..'z'` 这一整段（含 `:` `;` `?` 等标点）以及一段 GBK 汉字
//     （`#$B0..#$C8` + `#$A1..#$FE`），长度靠定长字段 `String[10]` 天然截断；
//   - 我们是 UTF-8（D-02），账号是存储主键，而客户端本来就做 `LowerCase`
//     （`IntroScn.pas:1035`）⇒ 只收 `[a-z0-9_]`、大小写统一。
//     汉字节那一段**有意不收**：它与 UTF-8 下的"汉字"不是一回事（那是 GBK 双字节，
//     按字节过滤在 UTF-8 下会切坏字符）。
func NormalizeAccount(name string) (string, error) {
	s := CanonicalAccount(name)
	if len(s) < AccountMinLen || len(s) > AccountMaxLen {
		return "", fmt.Errorf("账号名要 %d~%d 位", AccountMinLen, AccountMaxLen)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_'
		if !ok {
			return "", fmt.Errorf("账号名只能用字母、数字与下划线")
		}
	}
	return s, nil
}
