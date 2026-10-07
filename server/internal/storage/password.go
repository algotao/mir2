package storage

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
)

// 口令哈希参数。
//
// 原版是明文存储 + 明文比较 + 明文写日志（LoginSrv/LMain.pas:1211、WriteLogMsg:1862），
// 重写时改为 PBKDF2-SHA256。旧档迁移需一次性把所有明文口令转为哈希。
const (
	pwIterations = 100_000
	pwKeyLen     = 32
	pwSaltLen    = 16
)

// Params 是存储侧当前使用的口令派生参数。
//
// ⚠️ 必须与 `HashPassword` 里用的**同一组常量** —— 客户端靠它算出与服务端存储
// 一致的 `K`（D-24① 挑战应答的第一步：服务端把盐与这两个数下发）。
// 盐本身是**每账号一份**（在 `Account.Salt` 里），不在这个结构里。
type Params struct {
	Iterations int
	KeyLen     int
	SaltLen    int
}

// KDFParams 返回当前的派生参数。
func KDFParams() Params {
	return Params{Iterations: pwIterations, KeyLen: pwKeyLen, SaltLen: pwSaltLen}
}

// ErrEmptyPassword 表示口令为空。
var ErrEmptyPassword = errors.New("storage: 口令不能为空")

// HashPassword 生成口令哈希与随机盐。
//
// crypto/pbkdf2.Key 的签名是 Key[Hash](h func() Hash, password, salt, iter, keyLen)，
// 哈希构造函数作为首参，且返回 ([]byte, error)。
func HashPassword(pw string) (hash, salt []byte, err error) {
	if pw == "" {
		return nil, nil, ErrEmptyPassword
	}
	salt = make([]byte, pwSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, nil, err
	}
	hash, err = pbkdf2.Key(sha256.New, pw, salt, pwIterations, pwKeyLen)
	if err != nil {
		return nil, nil, err
	}
	return hash, salt, nil
}

// VerifyPassword 校验口令，使用常量时间比较防时序侧信道。
func VerifyPassword(pw string, hash, salt []byte) bool {
	if pw == "" || len(hash) != pwKeyLen || len(salt) != pwSaltLen {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, pwIterations, pwKeyLen)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, hash) == 1
}
