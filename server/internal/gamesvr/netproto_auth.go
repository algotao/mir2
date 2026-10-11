package gamesvr

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/algotao/mir2/server/internal/authn"
	"github.com/algotao/mir2/server/internal/storage"
	"github.com/algotao/mir2/server/protocol"
)

func (ps *protoSession) onLoginSaltRequest(m *protocol.LoginSaltRequest) bool {
	// ⚠️ 查库与留盐都用**规范化**后的名字：库里一律小写（建号时归一化），
	// 建号那边同样按规范化比对 `saltFor`（见 authn.CanonicalAccount）。
	account := authn.CanonicalAccount(m.GetAccount())
	params := storage.KDFParams()

	out := &protocol.LoginSalt{
		Iterations: uint32(params.Iterations),
		KeyLen:     uint32(params.KeyLen),
	}
	// 先用**随机盐**打底：账号不存在/已停用也回它 —— 不在这里露出"账号存不存在"
	//（与 accountsvc 那套"不存在也按口令错误回"是同一条纪律）。
	out.Salt = make([]byte, params.SaltLen)
	if _, err := rand.Read(out.Salt); err != nil {
		log.Printf("%s: 生成随机盐失败: %v", ps.clientIP, err)
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), protoStoreTimeout)
	defer cancel()
	if acc, err := ps.srv.store.Accounts().GetByName(ctx, account); err == nil && !acc.Deleted {
		out.Salt = acc.Salt
	}
	// 记下来：建号（D-32）要用**同一个盐** —— 客户端就是拿它算 K 的，换一个盐
	// 存库 ⇒ 以后登录算出的 K 对不上（这条链最容易踩的坑，所以存在会话上）。
	ps.saltFor, ps.salt = account, out.Salt
	return ps.send(&protocol.Envelope{Body: &protocol.Envelope_LoginSalt{LoginSalt: out}}) == nil
}

// onLogin 校验口令证明，成功则开一条会话（与 accountsvc 登录成功后做的是同一件事）。
func (ps *protoSession) onLogin(m *protocol.Login) bool {
	if ps.rec != nil {
		// 这条连接已经认领过会话：当幂等回执（与 onReconnect 同一纪律），
		// 但**不重发 token**（那等于把已经给过的东西再喊一遍）。
		return ps.sendLoginResult(protocol.LoginCode_LOGIN_OK, "已登录", nil) == nil
	}
	if len(ps.nonce) == 0 {
		// 握手里的 nonce 没留下来 ⇒ 证明没法验。这属服务器内部错误，不是客户端的错。
		return ps.failLogin(protocol.LoginCode_LOGIN_SERVER_FULL, "服务器缺少握手 nonce")
	}
	proof, err := hex.DecodeString(strings.TrimSpace(m.GetPasswordHash()))
	if err != nil || len(proof) != sha256.Size {
		return ps.failLogin(protocol.LoginCode_LOGIN_BAD_CREDENTIALS, "口令证明格式非法")
	}
	// ⚠️ 证明里的 account 用**客户端原样**发来的那个（两端要对齐，D-24）；
	// 查库用规范化后的（库里一律小写）。两者**刻意分开**，别合成一个变量。
	account := m.GetAccount()

	ctx, cancel := context.WithTimeout(context.Background(), protoStoreTimeout)
	defer cancel()
	acc, err := ps.srv.store.Accounts().GetByName(ctx, authn.CanonicalAccount(account))
	if err != nil || acc.Deleted {
		// ⚠️ 不存在与已停用**都按口令错误回**：不告诉对方账号是否存在
		return ps.failLogin(protocol.LoginCode_LOGIN_BAD_CREDENTIALS, "账号或口令不正确")
	}
	now := time.Now()
	lock := authn.DefaultLockPolicy()
	if lock.Locked(acc, now) {
		return ps.failLogin(protocol.LoginCode_LOGIN_LOCKED, "口令错误次数过多，请稍后再试")
	}
	if !authn.CheckProof(acc.PasswordHash, ps.nonce, account, proof) {
		lock.NoteFailure(acc, now)
		if err := ps.srv.store.Accounts().Update(ctx, acc); err != nil {
			log.Printf("%s: 写回失败计数出错（不影响本次拒绝）: %v", ps.clientIP, err)
		}
		return ps.failLogin(protocol.LoginCode_LOGIN_BAD_CREDENTIALS, "账号或口令不正确")
	}
	if authn.NoteSuccess(acc) {
		_ = ps.srv.store.Accounts().Update(ctx, acc)
	}

	// 成功：开一条**已认证**的会话，并把会话号当 token 回给客户端。
	// ⚠️ 会话号是 31 位随机值（不可猜测）；唯一性由存储主键兜底（Create 失败就换个号）。
	rec := &storage.SessionRecord{
		// 用库里那份名字（规范化过的），别用客户端原样发来的 —— 会话后面要按它
		// 查角色，两种写法混着用就会出现"登录成功但角色列表是空的"。
		Account:   acc.Name,
		IP:        ps.clientIP,
		Stage:     sessionStageAuthed,
		ExpiresAt: now.Add(loginSessionTTL),
	}
	if _, err := ps.newSession(ctx, rec); err != nil {
		log.Printf("%s: 账号 %s 登录后开会话失败: %v", ps.clientIP, account, err)
		return ps.failLogin(protocol.LoginCode_LOGIN_SERVER_FULL, "服务器暂时无法分配会话")
	}
	if err := ps.srv.store.Sessions().Activate(ctx, rec); err != nil {
		log.Printf("%s: 账号 %s 会话接管失败: %v", ps.clientIP, account, err)
		return ps.failLogin(protocol.LoginCode_LOGIN_SERVER_FULL, "服务器暂时无法分配会话")
	}
	ps.rec = rec

	var tok [4]byte
	binary.LittleEndian.PutUint32(tok[:], uint32(rec.SessionID))
	log.Printf("%s: 账号 %s 登录成功（会话 %d）", ps.clientIP, account, rec.SessionID)
	return ps.sendLoginResult(protocol.LoginCode_LOGIN_OK, "", tok[:]) == nil
}

// ---------- 建号：D-32 ----------

// 建号结果码 —— 与 `protocol/account.proto` 里 CreateAccountResult 的注释一一对应。
const (
	createAccountOK       = 0
	createAccountExists   = 1
	createAccountBadName  = 2
	createAccountDisabled = 3
	createAccountTooFast  = 4
	createAccountStoreErr = 5
	createAccountNoSalt   = 6
)

// newAccountCooldown 是建号节流窗口。
//
// 原版是**每连接** 5 秒（`LoginSrv/LMain.pas:977-986` 的 `GetTickCount -
// dwClientTick > 5000`），我们按 **IP** 限 —— 每连接限速在公网上毫无意义。
const newAccountCooldown = 5 * time.Second

// onCreateAccount 建号（D-32）。三条纪律照原版：**开关**、**节流**、**重名拒绝**。
//
// 与原版的差别（有意为之，写在协议注释里）：
//
//   - 原版收两个 16 字节定长结构（`TUserEntry` + `TUserEntryAdd`：账号/口令/姓名/
//     身份证/密保…全落库），我们只收 `account + verifier` —— 存储没有那些列；
//   - **口令长度只有客户端把关**：服务端只收到 `verifier`，从来见不到口令
//     （原版服务端同样不查口令，见 `LMain.pas:1019-1079`）。
//
// 失败**不断连接**（与登录不同）：建号没成只是这次没成，用户还要接着重填或去登录。
func (ps *protoSession) onCreateAccount(m *protocol.CreateAccount) bool {
	if !ps.srv.cfg.allowNewAccount {
		return ps.sendCreateAccountResult(createAccountDisabled, "本服未开放注册")
	}
	account, err := authn.NormalizeAccount(m.GetAccount())
	if err != nil {
		return ps.sendCreateAccountResult(createAccountBadName, err.Error())
	}
	verifier, err := hex.DecodeString(strings.TrimSpace(m.GetVerifier()))
	if err != nil || len(verifier) != sha256.Size {
		return ps.sendCreateAccountResult(createAccountBadName, "口令校验值格式非法")
	}
	if len(ps.salt) == 0 || ps.saltFor != account {
		// 没先取盐（或取的是别的账号的盐）⇒ 不知道客户端拿哪个盐算的 K，
		// 落库以后登录必然对不上。宁可拒绝，也不要存一个"永远登不上"的号。
		return ps.sendCreateAccountResult(createAccountNoSalt, "请先取盐再建号")
	}
	now := time.Now()
	if !ps.srv.allowAccountCreate(ps.clientIP, now) {
		log.Printf("%s: 建号过快（%s）", ps.clientIP, account)
		return ps.sendCreateAccountResult(createAccountTooFast, "操作过快，请稍后再试")
	}

	ctx, cancel := context.WithTimeout(context.Background(), protoStoreTimeout)
	defer cancel()
	// 先查一次只为给出"已存在"这个明确回执；真正的唯一性由存储主键兜底
	//（并发建同名时 Create 会回 ErrExists，见下面那一支）。
	if acc, err := ps.srv.store.Accounts().GetByName(ctx, account); err == nil && !acc.Deleted {
		return ps.sendCreateAccountResult(createAccountExists, "这个账号已经被使用了")
	}
	salt := append([]byte(nil), ps.salt...)
	rec := &storage.Account{Name: account, PasswordHash: verifier, Salt: salt}
	if err := ps.srv.store.Accounts().Create(ctx, rec); err != nil {
		if errors.Is(err, storage.ErrExists) {
			return ps.sendCreateAccountResult(createAccountExists, "这个账号已经被使用了")
		}
		log.Printf("%s: 建号 %s 落库失败: %v", ps.clientIP, account, err)
		return ps.sendCreateAccountResult(createAccountStoreErr, "服务器暂时无法创建账号")
	}
	log.Printf("%s: 建号成功 %s", ps.clientIP, account)
	// ⚠️ 建完**不清** salt：同一条连接再建一个号时（先取新盐）会被覆盖，这里不清
	// 只是省一次判断；真正的把关是上面的 `saltFor == account`。
	return ps.sendCreateAccountResult(createAccountOK, "账号已建立，请登录")
}

// sendCreateAccountResult 回一条建号结果。
//
// ⚠️ **拒绝也要留一行日志**：成功那条本来就有 `建号成功`，而"已存在/未开放/过快"
// 这些被拒的分支原先一行都不打 —— 用户那次"点了建号什么都没发生"就无处可查
// （服务端日志里只有 `握手完成`，排查时只能靠客户端那行 `提交建号` 去猜）。
// 拒绝是**正常结果**，但必须看得见。
func (ps *protoSession) sendCreateAccountResult(code uint32, msg string) bool {
	if code != createAccountOK {
		// 账号名用会话上留的那个（取盐时记的）—— 走到这里一定有过一次取盐。
		log.Printf("%s: 建号被拒 %s：%s", ps.clientIP, ps.saltFor, msg)
	}
	out := &protocol.CreateAccountResult{Result: &protocol.ActionResult{
		Ok:      code == createAccountOK,
		Code:    code,
		Message: msg,
	}}
	return ps.send(&protocol.Envelope{
		Body: &protocol.Envelope_CreateAccountResult{CreateAccountResult: out},
	}) == nil
}

// allowAccountCreate 是建号节流：同一 IP 在 `newAccountCooldown` 内只放行一次。
//
// ⚠️ 表会随不同 IP 增长 ⇒ 超过一定规模就顺手清掉过期项（不改语义，只防内存）。
// 用一把粗锁：建号是低频动作，热点在别处。
func (s *Server) allowAccountCreate(ip string, now time.Time) bool {
	s.createMu.Lock()
	defer s.createMu.Unlock()
	if s.createLast == nil {
		s.createLast = make(map[string]time.Time)
	}
	if len(s.createLast) > 4096 {
		for k, t := range s.createLast {
			if now.Sub(t) >= newAccountCooldown {
				delete(s.createLast, k)
			}
		}
	}
	if last, ok := s.createLast[ip]; ok && now.Sub(last) < newAccountCooldown {
		return false
	}
	s.createLast[ip] = now
	return true
}

// newSession 分配一个不可猜测的会话号并落库（形状与 accountsvc.SessionStore.Create 一致）。
func (ps *protoSession) newSession(ctx context.Context, rec *storage.SessionRecord) (int32, error) {
	for i := 0; i < 8; i++ {
		var raw [4]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return 0, err
		}
		rec.SessionID = int32(binary.BigEndian.Uint32(raw[:]) & 0x7fffffff)
		if rec.SessionID < 2 {
			continue
		}
		if err := ps.srv.store.Sessions().Create(ctx, rec); err == nil {
			return rec.SessionID, nil
		}
	}
	return 0, errors.New("无法分配唯一会话号")
}

// sendLoginResult 回一条登录结果。
func (ps *protoSession) sendLoginResult(code protocol.LoginCode, msg string, token []byte) error {
	return ps.send(&protocol.Envelope{Body: &protocol.Envelope_LoginResult{
		LoginResult: &protocol.LoginResult{Code: code, Message: msg, SessionToken: token}}})
}

// failLogin 回一条失败并断开：登录失败时**没有**可继续的状态（与 failReconnect 同一纪律）。
func (ps *protoSession) failLogin(code protocol.LoginCode, reason string) bool {
	log.Printf("%s: 新协议登录失败（%s）: %s", ps.clientIP, code, reason)
	_ = ps.sendLoginResult(code, reason, nil)
	_ = ps.send(&protocol.Envelope{Body: &protocol.Envelope_Disconnect{
		Disconnect: &protocol.Disconnect{Reason: reason}}})
	return false
}

// onReconnect 用会话号认领会话（v0 的入口；语义与 §5 的重连一致）。
//
// ⚠️ v0 的 `session_token` 就是 4 字节小端会话号 —— 这是**临时编码**：
// 正式 token 应由 accountsvc 在登录后签发（不可猜测、可换发、可失效），见 protocol.md §11。
func (ps *protoSession) onReconnect(m *protocol.Reconnect) bool {
	if ps.rec != nil {
		// 已经认领过：当幂等回执，不重复认领（也绝不再申请一次租约）。
		return ps.sendReconnectResult(protocol.ReconnectStatus_RECONNECT_BACK_TO_SELECT) == nil
	}
	tok := m.GetSessionToken()
	if len(tok) != 4 {
		return ps.failReconnect(fmt.Sprintf("session_token 长度非法（%d 字节）", len(tok)))
	}
	sid := int32(binary.LittleEndian.Uint32(tok))

	ctx, cancel := context.WithTimeout(context.Background(), protoStoreTimeout)
	defer cancel()
	rec, err := ps.srv.store.Sessions().Get(ctx, sid)
	if err != nil {
		return ps.failReconnect(fmt.Sprintf("会话 %d 不存在", sid))
	}
	now := time.Now()
	if !rec.ExpiresAt.After(now) {
		return ps.failReconnect("会话已过期")
	}
	ps.rec = rec

	// §5 的重连语义：租约仍在 ⇒ 直接回世界；否则回落到选角阶段。
	// 「租约仍在」的判据 = 这条会话还是该账号的当前登录者，且已经选好角色。
	status := protocol.ReconnectStatus_RECONNECT_BACK_TO_SELECT
	if rec.Stage == sessionStageSelected && rec.CharacterName != "" {
		if ok, err := ps.srv.store.Sessions().IsCurrent(ctx, rec.Account, sid, now); err == nil && ok {
			status = protocol.ReconnectStatus_RECONNECT_OK_IN_WORLD
		}
	}
	if err := ps.sendReconnectResult(status); err != nil {
		return false
	}
	if status != protocol.ReconnectStatus_RECONNECT_OK_IN_WORLD {
		log.Printf("%s: 新协议认领会话 %d（账号 %s）→ 回选角", ps.clientIP, sid, rec.Account)
		return true
	}
	// 回世界：与"选完角"是同一条尾巴。
	chr, err := ps.srv.store.Characters().GetByName(ctx, rec.CharacterName)
	if err != nil || chr.Deleted {
		return ps.failReconnect(fmt.Sprintf("角色 %s 不可用", rec.CharacterName))
	}
	return ps.enterWorld(chr)
}

// failReconnect 回一条 FAILED 并断开：token 无效时**没有**可继续的状态。
func (ps *protoSession) failReconnect(reason string) bool {
	log.Printf("%s: 新协议重连失败: %s", ps.clientIP, reason)
	_ = ps.sendReconnectResult(protocol.ReconnectStatus_RECONNECT_FAILED)
	_ = ps.send(&protocol.Envelope{Body: &protocol.Envelope_Disconnect{
		Disconnect: &protocol.Disconnect{Code: 1004, Reason: reason}}})
	return false
}

func (ps *protoSession) sendReconnectResult(s protocol.ReconnectStatus) error {
	return ps.send(&protocol.Envelope{Body: &protocol.Envelope_ReconnectResult{
		ReconnectResult: &protocol.ReconnectResult{Status: s}}})
}
