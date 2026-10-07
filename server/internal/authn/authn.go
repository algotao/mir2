// Package authn 是**登录校验的唯一实现**。
//
// 为什么单列一个包而不是各服务里各写一份：这是**安全策略**（账号是否存在该不该说、
// 错误几次锁定、锁多久、成功后要不要清零），而 R-7 说的是"游戏的规则只允许有一份"——
// 安全策略更是如此：两份迟早会漂，而且漂的那一份是在"谁能进门"这件事上。
//
// 两个入口：
//
//	legacy 路径（明文口令 → PBKDF2 比对）：`CheckPassword`，`accountsvc` 在用；
//	D-24① 挑战应答（HMAC 证明）：`ExpectedProof` / `CheckProof`，`gamesvr` 在用。
//
// 两者的**存储侧凭证是同一个东西**：PBKDF2 派生值（`storage.Account.PasswordHash`）。
// 挑战应答之所以成立，正是因为服务端**拿存着的派生值就能校验 HMAC**，
// 不必知道口令明文，也不必让客户端把派生值发上来。
package authn

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"time"

	"github.com/algotao/mir2/server/internal/storage"
)

// LockPolicy 是"口令错误次数 → 临时锁定"的策略。
//
// 原版这套来自 `!setup.txt` 的 `PasswordLockSystem`（我们按 `accountsvc` 的既有默认值走：
// 错误 5 次锁 60 秒）。时间戳与 `Account.ActionTick` 一样用**毫秒**，
// 免得在"秒/毫秒"上出现第二个真相。
type LockPolicy struct {
	// MaxErrors 是触发锁定的错误次数。
	MaxErrors int
	// LockForMs 是锁定时长（毫秒）。
	LockForMs int64
}

// DefaultLockPolicy 是默认的锁定策略：错误 5 次锁 60 秒
// （与 `accountsvc.DefaultConfig` 的既有值一致，见那里的注释"原版 5"）。
//
// ⚠️ 这里是**唯一的默认值来源**：accountsvc 的 DefaultConfig 引用它。
func DefaultLockPolicy() LockPolicy {
	return LockPolicy{MaxErrors: 5, LockForMs: 60_000}
}

// Locked 判断账号此刻是否处于锁定期。
func (p LockPolicy) Locked(acc *storage.Account, now time.Time) bool {
	if p.MaxErrors <= 0 || acc.ErrorCount < p.MaxErrors {
		return false
	}
	return now.UnixMilli()-acc.ActionTick < p.LockForMs
}

// NoteFailure 记一次失败（改 `ErrorCount` / `ActionTick`）。
//
// ⚠️ 调用方负责把它写回存储：把"算"和"写"分开，是为了让调用方能在一处决定
// 事务边界（SQLite 里 Update 是一次写）。
func (p LockPolicy) NoteFailure(acc *storage.Account, now time.Time) {
	acc.ErrorCount++
	acc.ActionTick = now.UnixMilli()
}

// NoteSuccess 成功时清零计数；返回**是否需要写回**（没脏就别白写一次库）。
func NoteSuccess(acc *storage.Account) bool {
	if acc.ErrorCount == 0 && acc.ActionTick == 0 {
		return false
	}
	acc.ErrorCount = 0
	acc.ActionTick = 0
	return true
}

// CheckPassword 校验**明文口令**（legacy 路径：客户端把口令发上来，服务端 PBKDF2 比对）。
//
// ⚠️ 只有 legacy 那套协议会走它；新协议走挑战应答（见 `ExpectedProof`），
// 明文不上网络。这条路径随 legacy 退役一起消失。
func CheckPassword(password string, acc *storage.Account) bool {
	return storage.VerifyPassword(password, acc.PasswordHash, acc.Salt)
}

// ExpectedProof 算出**客户端应当提交的证明**（D-24①）：
//
//	HMAC-SHA256( K, nonce ‖ account )
//
// 其中 `K` 就是存储里的口令凭证（PBKDF2 派生值）。服务端不知道口令，
// 但它存着 `K` ⇒ 能重算这个 HMAC 与客户端比对。
//
// ⚠️ `nonce` 必须是**每条连接一次**的随机值（`ServerHello.session_key`）：
// 它把证明绑在那一条连接上，所以**嗅到也重放不了** —— 这正是它比
// "直接发 K"（D-24②）强的地方。用 32 字节的 SHA256 摘要作为 MAC 输出。
func ExpectedProof(verifier, nonce []byte, account string) []byte {
	mac := hmac.New(sha256.New, verifier)
	mac.Write(nonce)
	mac.Write([]byte(account))
	return mac.Sum(nil)
}

// CheckProof 用**常量时间比较**校验客户端提交的证明（防时序侧信道）。
func CheckProof(verifier, nonce []byte, account string, got []byte) bool {
	if len(verifier) == 0 || len(nonce) == 0 || len(got) != sha256.Size {
		return false
	}
	return subtle.ConstantTimeCompare(ExpectedProof(verifier, nonce, account), got) == 1
}
