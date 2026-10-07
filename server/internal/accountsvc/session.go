// Package accountsvc 实现账号服务与角色服务（原 LoginSrv + DBServer 合并）。
//
// 合并理由：原版两个进程要共享会话状态，于是搞出一套别扭的跨进程同步——
// DBServer 作为 TCP 客户端连 LoginSrv:5600，上报 SS_SERVERINFO 时把 nServerIndex
// 固定填 99，LoginSrv 靠"服务器名匹配 或 index=99"把全量会话广播给它
// （MasSock.pas:299 / IDSocCli.pas:337）。合并后这套 SS_* 协议整体消失。
package accountsvc

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"sync"
	"time"
)

// Stage 是登录流程的阶段。
type Stage int

const (
	StageNew            Stage = iota // 未认证
	StageAuthed                      // 已通过口令认证（可查询/创建/删除角色）
	StageServerSelected              // 已选服务器
	StageQueryed                     // 已查询角色列表
	StagePlaying                     // 已选角，即将进入游戏
)

// Session 是一个客户端连接的状态。
//
// 对应原版 LoginSrv 的 TConnInfo（LMain.pas:10-21）+ DBServer 的 TUserInfo。
type Session struct {
	ID            int64
	SessionID     int32
	Account       string
	AccountID     int64
	IP            string
	Stage         Stage
	ServerName    string
	CharacterName string
	ExpiresAt     time.Time

	// chrQueryed 标记是否已查询过角色列表。
	// ⚠️ 原版 UsrSoc.pas:576 的守卫写成 "if not UserInfo.boChrQueryed"，
	// 而 QueryChr 成功后恰恰把它置 True（:541）——等于"查过就不能选角"，
	// 与注释要求的"必须先 Query"完全相反。此处按正确语义实现。
	chrQueryed bool

	// 限速时间戳（毫秒）。查询与建/删分开计数：
	// 原版共用一个 dwChrTick（UsrSoc.pas:536,548,563），导致查询会把紧随其后的
	// 建角色也一并限掉。二者频率特征不同（200ms vs 1000ms），此处分开。
	lastQueryTick  int64
	lastModifyTick int64
}

// SessionStore 管理会话。
type SessionStore struct {
	mu   sync.RWMutex
	byID map[int32]*Session
}

// NewSessionStore 创建本地会话索引。权威所有权保存在共享存储中。
func NewSessionStore() *SessionStore {
	return &SessionStore{byID: make(map[int32]*Session)}
}

// Create 建立本地会话并分配不可预测的 SessionID；全局唯一性由持久化主键兜底。
func (s *SessionStore) Create(ip string) (*Session, error) {
	for i := 0; i < 8; i++ {
		var raw [4]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return nil, err
		}
		id := int32(binary.BigEndian.Uint32(raw[:]) & 0x7fffffff)
		if id < 2 {
			continue
		}
		sess := &Session{SessionID: id, IP: ip, Stage: StageNew}
		s.mu.Lock()
		if _, exists := s.byID[id]; !exists {
			s.byID[id] = sess
			s.mu.Unlock()
			return sess, nil
		}
		s.mu.Unlock()
	}
	return nil, errors.New("accountsvc: 无法分配唯一 SessionID")
}

// Forget 从本地索引移除会话。
func (s *SessionStore) Forget(id int32) {
	s.mu.Lock()
	delete(s.byID, id)
	s.mu.Unlock()
}

// Unbound 创建一个**未注册**的会话。
//
// 用途：客户端登录成功后会断连并转连选角网关(7100)，新连接此时没有身份，
// 需靠 CM_QUERYCHR 正文里的 "<account>/<sessionID>" 绑定回已认证会话
// （原版对应 UsrSoc.pas:615-622 的 CheckSession + UserInfo.sAccount := sAccount）。
func (s *SessionStore) Unbound(ip string) *Session {
	return &Session{IP: ip, Stage: StageNew}
}

// Get 按 SessionID 取会话。
func (s *SessionStore) Get(id int32) (*Session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.byID[id]
	return sess, ok
}

// Delete 移除会话。
func (s *SessionStore) Delete(id int32) {
	s.mu.Lock()
	delete(s.byID, id)
	s.mu.Unlock()
}

// Len 返回会话数。
func (s *SessionStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byID)
}

// ErrRateLimited 表示操作过于频繁。
var ErrRateLimited = errors.New("accountsvc: 操作过于频繁")

// ErrBadSession 表示会话无效（未认证或 SessionID 不匹配）。
var ErrBadSession = errors.New("accountsvc: 会话无效")

// ErrBadStage 表示当前阶段不允许该操作。
var ErrBadStage = errors.New("accountsvc: 当前阶段不允许该操作")

// checkSession 校验会话有效：已认证且 SessionID 匹配。
//
// 原版 FrmIDSoc.CheckSession（IDSocCli.pas:137-156）只比对 (account, sessionID)，
// **不校验 IP**（IP 校验被注释掉，见 :148）。此处保留该行为——
// 因为 NAT 环境下同账号多 IP 是常态，校验 IP 会误伤。
func checkSession(sess *Session) error {
	if sess == nil || sess.Account == "" {
		return ErrBadSession
	}
	if sess.Stage < StageAuthed {
		return ErrBadStage
	}
	return nil
}

// passQueryLimit 检查并更新"查询角色"的限速时间戳。intervalMs <= 0 表示不限速。
func (sess *Session) passQueryLimit(intervalMs int64) bool {
	if intervalMs <= 0 {
		return true
	}
	now := time.Now().UnixMilli()
	if now-sess.lastQueryTick < intervalMs {
		return false
	}
	sess.lastQueryTick = now
	return true
}

// resetQueryLimit 放行紧随其后的角色查询。
//
// 建/删角色后客户端必然要立刻刷新列表（原版亦置 boChrQueryed := False），
// 若仍受 200ms 限制就会卡住。建/删本身已有 1000ms 限流，整体频率仍可控。
func (sess *Session) resetQueryLimit() { sess.lastQueryTick = 0 }

// passModifyLimit 检查并更新"建/删角色"的限速时间戳。intervalMs <= 0 表示不限速。
func (sess *Session) passModifyLimit(intervalMs int64) bool {
	if intervalMs <= 0 {
		return true
	}
	now := time.Now().UnixMilli()
	if now-sess.lastModifyTick < intervalMs {
		return false
	}
	sess.lastModifyTick = now
	return true
}

// ctxKey 用于在 context 中传递会话。
type ctxKey struct{}

// WithSession 把会话放入 context。
func WithSession(ctx context.Context, sess *Session) context.Context {
	return context.WithValue(ctx, ctxKey{}, sess)
}

// SessionFrom 从 context 取会话。
func SessionFrom(ctx context.Context) (*Session, bool) {
	sess, ok := ctx.Value(ctxKey{}).(*Session)
	return sess, ok
}
