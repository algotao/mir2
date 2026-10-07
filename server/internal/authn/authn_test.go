package authn

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/storage"
)

// `ExpectedProof` 就是 HMAC-SHA256(verifier, nonce‖account)，所以把 RFC 4231 的
// key/data 拆成 (verifier, nonce, account) 就能对上**公开测试向量** ——
// 自己实现的密码学构造必须有这一条。
func TestExpectedProofRFC4231(t *testing.T) {
	// case 1: key = 20 个 0x0b，data = "Hi There"
	key := make([]byte, 20)
	for i := range key {
		key[i] = 0x0b
	}
	got := ExpectedProof(key, []byte("Hi T"), "here")
	const want1 = "b0344c61d8db38535ca8afceaf0bf12b881dc200c9833da726e9376c2e32cff7"
	if hex.EncodeToString(got) != want1 {
		t.Fatalf("RFC 4231 #1 不符\n got %s\nwant %s", hex.EncodeToString(got), want1)
	}
	// case 2: key = "Jefe"，data = "what do ya want for nothing?"
	got = ExpectedProof([]byte("Jefe"), []byte("what do ya want "), "for nothing?")
	const want2 = "5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843"
	if hex.EncodeToString(got) != want2 {
		t.Fatalf("RFC 4231 #2 不符\n got %s\nwant %s", hex.EncodeToString(got), want2)
	}
}

// TestProofVector 钉住本项目的登录证明口径：**客户端与服务端对同一组输入必须给出同一组字节**。
//
// 客户端那一侧在 `client/core/src/auth.rs` 的 `proof_vector_matches_server`
// —— 两处钉的是同一组十六进制串。任一边改了口径，会有一边红。
func TestProofVector(t *testing.T) {
	salt, err := hex.DecodeString("000102030405060708090a0b0c0d0e0f")
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := hex.DecodeString("101112131415161718191a1b1c1d1e1f")
	if err != nil {
		t.Fatal(err)
	}
	// 客户端算的是 K（PBKDF2），服务端存的就是它
	k, err := pbkdf2.Key(sha256.New, "pw", salt, 100_000, 32)
	if err != nil {
		t.Fatal(err)
	}
	const wantK = "fbe79903d759b826e1d54bd9e4f8beb2de5a87ac86037c76328ad485fd894671"
	if hex.EncodeToString(k) != wantK {
		t.Fatalf("K 不符\n got %s\nwant %s", hex.EncodeToString(k), wantK)
	}
	const wantProof = "07f7a0c926c9ec65e55c6a6ad631e92bd2ab97c0ee61b90317bb25ec75dd9a89"
	if got := hex.EncodeToString(ExpectedProof(k, nonce, "tester")); got != wantProof {
		t.Fatalf("proof 不符\n got %s\nwant %s", got, wantProof)
	}
	// 校验入口：对的过、错的不过、长度不对直接不过（不 panic）
	if !CheckProof(k, nonce, "tester", ExpectedProof(k, nonce, "tester")) {
		t.Fatal("自己的证明该过")
	}
	if CheckProof(k, nonce, "tester", ExpectedProof(k, nonce, "tester2")) {
		t.Fatal("换个账号就不该过（account 参与 MAC）")
	}
	if CheckProof(k, nonce, "tester", []byte{1, 2, 3}) {
		t.Fatal("长度不对该直接拒")
	}
	if CheckProof(nil, nonce, "tester", ExpectedProof(k, nonce, "tester")) {
		t.Fatal("没有凭证该直接拒")
	}
}

// TestLockPolicy 锁定策略（**唯一一份**，accountsvc 的 legacy 登录也走它）。
func TestLockPolicy(t *testing.T) {
	p := LockPolicy{MaxErrors: 3, LockForMs: 1000}
	now := time.Now()
	acc := &storage.Account{}

	if p.Locked(acc, now) {
		t.Fatal("没错过就不该锁")
	}
	for i := 0; i < 3; i++ {
		p.NoteFailure(acc, now)
	}
	if acc.ErrorCount != 3 || acc.ActionTick != now.UnixMilli() {
		t.Fatalf("失败计数没记对: %+v", acc)
	}
	if !p.Locked(acc, now) {
		t.Fatal("错了 3 次该锁")
	}
	if !p.Locked(acc, now.Add(999*time.Millisecond)) {
		t.Fatal("锁定期内该一直锁")
	}
	if p.Locked(acc, now.Add(1001*time.Millisecond)) {
		t.Fatal("过了锁定期就不该锁")
	}
	// 成功清零：脏了要写回，干净的不再写一次（别白写库）
	if !NoteSuccess(acc) {
		t.Fatal("有脏计数时应返回真（要写回）")
	}
	if acc.ErrorCount != 0 || acc.ActionTick != 0 {
		t.Fatal("清零没做干净")
	}
	if NoteSuccess(acc) {
		t.Fatal("干净的账号不该再写一次库")
	}
}
