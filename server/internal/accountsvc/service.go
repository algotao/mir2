package accountsvc

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/algotao/mir2/server/internal/authn"
	"github.com/algotao/mir2/server/internal/chargen"
	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/storage"
	"github.com/algotao/mir2/server/internal/wire"
)

// 客户端线路正文格式（均已对照 Delphi 源码核准）：
//
//	CM_PROTOCOL      无正文，Recog = 客户端版本号
//	CM_IDPASSWORD    "<account>/<password>"
//	CM_SELECTSERVER  "<serverName>"
//	CM_QUERYCHR      "<account>/<sessionID>"          (UsrSoc.pas:615)
//	CM_NEWCHR        "<account>/<chrName>/<hair>/<job>/<sex>"  (UsrSoc.pas:737-741)
//	CM_DELCHR        "<chrName>"                      (UsrSoc.pas:693，无分隔符)
//	CM_SELCHR        "<account>/<chrName>"            (UsrSoc.pas:869)
//
// 响应正文：
//
//	SM_PASSOK_SELECTSERVER  "<服务器名>/<状态>/" 重复   状态：1空闲 2良好 3繁忙 4满员
//	SM_SELECTSERVER_OK      "<selGateIP>/<selGatePort>/<sessionID>"   (LMain.pas:1411)
//	SM_QUERYCHR             "[*]<名>/<job>/<hair>/<level>/<sex>/" 重复，'*' 表示上次选中
//	SM_STARTPLAY            "<runGateIP>/<port>"，port = 网关端口 + 地图索引 (UsrSoc.pas:923)

// Config 是服务配置。
type Config struct {
	// ClientVersion 是要求的客户端版本号，0 表示不校验。
	ClientVersion int32
	// VersionDate 下发给客户端的版本日期，原版默认 20011006（LSShare.pas:150）。
	VersionDate int32

	ServerName  string
	ServerTitle string

	// SelGateAddr / SelGatePort 是角色网关地址，选服后下发给客户端。
	SelGateAddr string
	SelGatePort int
	// RunGateAddr / RunGatePort 是游戏网关地址，选角后下发。
	RunGateAddr string
	RunGatePort int

	// MaxChrPerAccount 每账号角色上限，原版为 2（UsrSoc.pas:652）。
	MaxChrPerAccount int
	// NewChrIntervalMs 建/删角色的最小间隔，原版 1000ms（UsrSoc.pas:548,563）。
	NewChrIntervalMs int64
	// QueryChrIntervalMs 查询角色的最小间隔，原版 200ms（UsrSoc.pas:536）。
	QueryChrIntervalMs int64

	// MaxPasswordErrors 密码错误上限，原版 5；超限锁定 PasswordLockMs。
	MaxPasswordErrors int
	PasswordLockMs    int64

	// HomeMap / HomeX / HomeY 新建角色的出生点。
	HomeMap string
	HomeX   uint32
	HomeY   uint32
}

// DefaultConfig 返回默认配置。
func DefaultConfig() Config {
	return Config{
		VersionDate:        20011006,
		ServerName:         "mir2go",
		ServerTitle:        "mir2go",
		SelGateAddr:        "127.0.0.1",
		SelGatePort:        7100,
		RunGateAddr:        "127.0.0.1",
		RunGatePort:        7200,
		MaxChrPerAccount:   2,
		NewChrIntervalMs:   1000,
		QueryChrIntervalMs: 200,
		// ⚠️ 默认值来自 `authn`（唯一来源）：gamesvr 的挑战应答走的是同一套策略
		MaxPasswordErrors: authn.DefaultLockPolicy().MaxErrors,
		PasswordLockMs:    authn.DefaultLockPolicy().LockForMs,
		HomeMap:           "0",
		HomeX:             289,
		HomeY:             618,
	}
}

// Service 是账号服务。
type Service struct {
	store    storage.Store
	sessions *SessionStore
	cfg      Config

	// tables 是静态数据表（可选）。用于建角色时查询初始物品的模板索引。
	// 为 nil 时不发放初始物品。
	tables *data.Tables
	// itemSeq 分配物品唯一 ID（MakeIndex）。
	//
	// 原版从 g_Config.nItemNumber 读取并递增（M2Share.pas:3610-3628），
	// 持久化在配置里。此处用内存计数器，重启后从高位继续以避免撞号。
	itemSeq atomic.Int64
}

// SetTables 注入静态数据表。
func (s *Service) SetTables(t *data.Tables) { s.tables = t }

// New 创建账号服务。
func New(store storage.Store, cfg Config) *Service {
	if cfg.MaxChrPerAccount <= 0 {
		cfg.MaxChrPerAccount = DefaultConfig().MaxChrPerAccount
	}
	// 负值视为"未设置"→ 用默认值；0 表示显式关闭限速（便于测试与单机部署）。
	if cfg.NewChrIntervalMs < 0 {
		cfg.NewChrIntervalMs = DefaultConfig().NewChrIntervalMs
	}
	if cfg.QueryChrIntervalMs < 0 {
		cfg.QueryChrIntervalMs = DefaultConfig().QueryChrIntervalMs
	}
	if cfg.MaxPasswordErrors <= 0 {
		cfg.MaxPasswordErrors = DefaultConfig().MaxPasswordErrors
	}
	if cfg.PasswordLockMs <= 0 {
		cfg.PasswordLockMs = DefaultConfig().PasswordLockMs
	}
	if cfg.VersionDate == 0 {
		cfg.VersionDate = DefaultConfig().VersionDate
	}
	return &Service{store: store, sessions: NewSessionStore(), cfg: cfg}
}

const loginSessionTTL = 10 * time.Minute

// Sessions 返回本进程的会话索引（权威所有权以共享存储为准）。
func (s *Service) Sessions() *SessionStore { return s.sessions }

// NewSession 建立登录连接会话并持久化。随机 SessionID 避免重启复用及顺序枚举。
func (s *Service) NewSession(ip string) (*Session, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.store.Sessions().CleanupExpired(ctx, time.Now()); err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 8; attempt++ {
		sess, err := s.sessions.Create(ip)
		if err != nil {
			return nil, err
		}
		sess.ExpiresAt = time.Now().Add(loginSessionTTL)
		err = s.store.Sessions().Create(ctx, &storage.SessionRecord{
			SessionID: sess.SessionID, IP: ip, ExpiresAt: sess.ExpiresAt,
		})
		if err == nil {
			return sess, nil
		}
		s.sessions.Forget(sess.SessionID)
		if !errors.Is(err, storage.ErrExists) {
			return nil, err
		}
	}
	return nil, errors.New("accountsvc: SessionID 冲突次数过多")
}

// persist 将当前登录连接的阶段状态原子写回，并刷新登录租约。
func (s *Service) persist(sess *Session) error {
	if sess == nil || sess.Account == "" {
		return storage.ErrLeaseLost
	}
	sess.ExpiresAt = time.Now().Add(loginSessionTTL)
	return s.store.Sessions().Update(context.Background(), &storage.SessionRecord{
		SessionID: sess.SessionID, Account: sess.Account, IP: sess.IP,
		Stage: int(sess.Stage), ServerName: sess.ServerName,
		CharacterName: sess.CharacterName, ExpiresAt: sess.ExpiresAt,
	})
}

// IsCurrentSession reports whether this accountsvc connection still owns the account lease.
func (s *Service) IsCurrentSession(sess *Session) bool {
	if sess == nil || sess.Account == "" || sess.SessionID == 0 {
		return true
	}
	ok, err := s.store.Sessions().IsCurrent(context.Background(), sess.Account, sess.SessionID, time.Now())
	return err == nil && ok
}

// DropSession invalidates one session without disturbing a newer login for the same account.
func (s *Service) DropSession(sessionID int32) {
	local, _ := s.sessions.Get(sessionID)
	s.sessions.Forget(sessionID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if local != nil && local.Account != "" {
		_ = s.store.Sessions().ReleaseCurrent(ctx, local.Account, sessionID)
		return
	}
	_ = s.store.Sessions().Delete(ctx, sessionID)
}

// NewConnSession 建立游离会话（选角网关 7100 使用，需经一次性 CM_QUERYCHR 绑定）。
func (s *Service) NewConnSession(ip string) *Session { return s.sessions.Unbound(ip) }

// Handle 处理一条客户端消息，返回需要下发的包（可能为空）。
func (s *Service) Handle(ctx context.Context, sess *Session, p wire.Packet) []wire.Packet {
	if sess == nil {
		return nil
	}
	if sess.Account != "" && !s.IsCurrentSession(sess) {
		return staleSessionResponse(p.Head.Ident)
	}
	switch p.Head.Ident {
	case proto.CM_PROTOCOL:
		return s.onProtocol(sess, p)
	case proto.CM_IDPASSWORD:
		return s.onLogin(ctx, sess, p)
	case proto.CM_SELECTSERVER:
		return s.onSelectServer(ctx, sess, p)
	case proto.CM_QUERYCHR:
		return s.onQueryChr(ctx, sess, p)
	case proto.CM_NEWCHR:
		return s.onNewChr(ctx, sess, p)
	case proto.CM_DELCHR:
		return s.onDelChr(ctx, sess, p)
	case proto.CM_SELCHR:
		return s.onSelChr(ctx, sess, p)
	default:
		return nil
	}
}

// ---------- 各阶段处理 ----------

// onProtocol 版本握手。
func (s *Service) onProtocol(_ *Session, p wire.Packet) []wire.Packet {
	if s.cfg.ClientVersion > 0 && p.Head.Recog != s.cfg.ClientVersion {
		return one(proto.SM_CERTIFICATION_FAIL, 0, "")
	}
	return one(proto.SM_CERTIFICATION_SUCCESS, s.cfg.VersionDate, "")
}

// onLogin 口令认证。
//
// 失败码沿用原版语义（客户端 ClMain.pas:4270-4294）：
// -1 密码错误 / -2 错误过多锁定 / -3 已登录 / -4 需付费 / -5 账号锁定。
func (s *Service) onLogin(ctx context.Context, sess *Session, p wire.Packet) []wire.Packet {
	if sess == nil || sess.SessionID == 0 || sess.Account != "" || sess.Stage != StageNew {
		return one(proto.SM_PASSWD_FAIL, -3, "")
	}
	i := strings.IndexByte(p.Body, '/')
	if i < 0 {
		return one(proto.SM_PASSWD_FAIL, -1, "")
	}
	account, password := p.Body[:i], p.Body[i+1:]

	acc, err := s.store.Accounts().GetByName(ctx, account)
	if errors.Is(err, storage.ErrNotFound) {
		// 账号不存在也按"密码错误"返回，避免暴露账号是否注册
		return one(proto.SM_PASSWD_FAIL, -1, "")
	}
	if err != nil {
		return one(proto.SM_PASSWD_FAIL, -1, "")
	}
	if acc.Deleted {
		return one(proto.SM_PASSWD_FAIL, -5, "")
	}

	// 锁定策略与"错误计数怎么记"都在 `authn`（**唯一一份**，见那个包的文件头）——
	// gamesvr 的挑战应答走的是同一套策略，两边不许各写一遍。
	now := time.Now()
	lock := authn.LockPolicy{MaxErrors: s.cfg.MaxPasswordErrors, LockForMs: s.cfg.PasswordLockMs}
	if lock.Locked(acc, now) {
		return one(proto.SM_PASSWD_FAIL, -2, "")
	}

	if !authn.CheckPassword(password, acc) {
		lock.NoteFailure(acc, now)
		_ = s.store.Accounts().Update(ctx, acc)
		return one(proto.SM_PASSWD_FAIL, -1, "")
	}

	// 成功：清零错误计数
	if authn.NoteSuccess(acc) {
		_ = s.store.Accounts().Update(ctx, acc)
	}
	sess.Account = account
	sess.AccountID = acc.ID
	sess.Stage = StageAuthed
	sess.ServerName = ""
	sess.CharacterName = ""
	sess.ExpiresAt = time.Now().Add(loginSessionTTL)
	if err := s.store.Sessions().Activate(ctx, &storage.SessionRecord{
		SessionID: sess.SessionID, Account: account, IP: sess.IP,
		Stage: int(sess.Stage), ExpiresAt: sess.ExpiresAt,
	}); err != nil {
		sess.Account = ""
		sess.AccountID = 0
		sess.Stage = StageNew
		log.Printf("账号 %s 登录会话接管失败: %v", account, err)
		return one(proto.SM_PASSWD_FAIL, -1, "")
	}

	list := fmt.Sprintf("%s/%d/", s.cfg.ServerName, serverStatusGood)
	return one(proto.SM_PASSOK_SELECTSERVER, 0, list)
}

// 服务器状态值（MasSock.pas:436）。
const (
	serverStatusFree = 1
	serverStatusGood = 2
	serverStatusBusy = 3
	serverStatusFull = 4
)

// onSelectServer 选服，下发角色网关地址。
func (s *Service) onSelectServer(_ context.Context, sess *Session, p wire.Packet) []wire.Packet {
	if sess == nil || sess.Stage != StageAuthed {
		return one(proto.SM_STARTFAIL, 0, "")
	}
	if err := checkSession(sess); err != nil {
		return one(proto.SM_STARTFAIL, 0, "")
	}
	name := strings.TrimSpace(p.Body)
	if name != "" && name != s.cfg.ServerName {
		return one(proto.SM_STARTFAIL, 0, "")
	}
	sess.ServerName = s.cfg.ServerName
	sess.Stage = StageServerSelected
	if err := s.persist(sess); err != nil {
		log.Printf("账号 %s 选服会话更新失败: %v", sess.Account, err)
		return one(proto.SM_STARTFAIL, 0, "")
	}

	body := fmt.Sprintf("%s/%d/%d", s.cfg.SelGateAddr, s.cfg.SelGatePort, sess.SessionID)
	return one(proto.SM_SELECTSERVER_OK, sess.SessionID, body)
}

// onQueryChr 查询角色列表。
func (s *Service) onQueryChr(ctx context.Context, sess *Session, p wire.Packet) []wire.Packet {
	// 限速放在认证之前，避免未绑定连接刷接口
	if !sess.passQueryLimit(s.cfg.QueryChrIntervalMs) {
		return nil
	}

	i := strings.IndexByte(p.Body, '/')
	if i < 0 {
		return one(proto.SM_QUERYCHR_FAIL, 0, "")
	}
	account := p.Body[:i]
	sid, err := strconv.Atoi(p.Body[i+1:])
	if err != nil || sid < 2 || int64(sid) > 1<<31-1 {
		return one(proto.SM_QUERYCHR_FAIL, 0, "")
	}

	// 7100 的游离连接通过共享存储原子消费一次性 handoff；复制/重放 SID 的
	// 第二条连接不能绑定同一登录会话。
	if sess.Account == "" {
		acc, err := s.store.Accounts().GetByName(ctx, account)
		if err != nil {
			return one(proto.SM_QUERYCHR_FAIL, 0, "")
		}
		auth, err := s.store.Sessions().BindHandoff(ctx, account, int32(sid), time.Now())
		if err != nil {
			return one(proto.SM_QUERYCHR_FAIL, 0, "")
		}
		sess.SessionID = auth.SessionID
		sess.Account = auth.Account
		sess.AccountID = acc.ID
		sess.Stage = Stage(auth.Stage)
		sess.ServerName = auth.ServerName
		sess.CharacterName = auth.CharacterName
		sess.ExpiresAt = auth.ExpiresAt
	}
	if sess.Stage != StageServerSelected && sess.Stage != StageQueryed && sess.Stage != StagePlaying {
		return one(proto.SM_QUERYCHR_FAIL, 0, "")
	}
	if err := checkSession(sess); err != nil {
		return one(proto.SM_QUERYCHR_FAIL, 0, "")
	}
	if account != sess.Account || int32(sid) != sess.SessionID {
		return one(proto.SM_QUERYCHR_FAIL, 0, "")
	}

	chars, err := s.store.Characters().ListByAccount(ctx, account)
	if err != nil {
		return one(proto.SM_QUERYCHR_FAIL, 0, "")
	}

	var sb strings.Builder
	n := 0
	for _, c := range chars {
		if n >= s.cfg.MaxChrPerAccount {
			break
		}
		var hair, sex uint32
		if c.Data != nil {
			hair = c.Data.Hair
			sex = c.Data.Sex
		}
		sb.WriteString(c.Name)
		sb.WriteByte('/')
		sb.WriteString(strconv.FormatUint(uint64(c.Job), 10))
		sb.WriteByte('/')
		sb.WriteString(strconv.FormatUint(uint64(hair), 10))
		sb.WriteByte('/')
		sb.WriteString(strconv.FormatUint(uint64(c.Level), 10))
		sb.WriteByte('/')
		sb.WriteString(strconv.FormatUint(uint64(sex), 10))
		sb.WriteByte('/')
		n++
	}
	sess.chrQueryed = true
	sess.Stage = StageQueryed

	// 原版：MakeDefaultMsg(SM_QUERYCHR, nChrCount, 0, 1, 0)
	return []wire.Packet{{
		Head: proto.MakeDefaultMsg(proto.SM_QUERYCHR, int32(n), 0, 1, 0),
		Body: sb.String(),
	}}
}

// onNewChr 创建角色。
//
// 失败码沿用原版（UsrSoc.pas:724-728）：0 非法字符 / 2 重名 / 3 超过上限 / 4 创建失败。
func (s *Service) onNewChr(ctx context.Context, sess *Session, p wire.Packet) []wire.Packet {
	if sess == nil || (sess.Stage != StageServerSelected && sess.Stage != StageQueryed && sess.Stage != StagePlaying) {
		return one(proto.SM_NEWCHR_FAIL, 0, "")
	}
	if err := checkSession(sess); err != nil {
		return one(proto.SM_NEWCHR_FAIL, 0, "")
	}
	if !sess.passModifyLimit(s.cfg.NewChrIntervalMs) {
		return nil
	}

	parts := strings.Split(p.Body, "/")
	if len(parts) != 5 {
		return one(proto.SM_NEWCHR_FAIL, 0, "")
	}
	account := parts[0]
	chrName := strings.TrimSpace(parts[1])
	hair, err1 := strconv.Atoi(parts[2])
	job, err2 := strconv.Atoi(parts[3])
	sex, err3 := strconv.Atoi(parts[4])
	if err1 != nil || err2 != nil || err3 != nil || account != sess.Account {
		return one(proto.SM_NEWCHR_FAIL, 0, "")
	}
	if !chargen.ValidName(chrName) {
		return one(proto.SM_NEWCHR_FAIL, 0, "")
	}

	// 重名
	if _, err := s.store.Characters().GetByName(ctx, chrName); err == nil {
		return one(proto.SM_NEWCHR_FAIL, 2, "")
	} else if !errors.Is(err, storage.ErrNotFound) {
		return one(proto.SM_NEWCHR_FAIL, 4, "")
	}

	// 角色数上限
	chars, err := s.store.Characters().ListByAccount(ctx, account)
	if err != nil {
		return one(proto.SM_NEWCHR_FAIL, 4, "")
	}
	if len(chars) >= s.cfg.MaxChrPerAccount {
		return one(proto.SM_NEWCHR_FAIL, 3, "")
	}

	// ⚠️ 组装一律走 `chargen`：**与 gamesvr 的新协议建角是同一份实现**（R-7）。
	// 初始物品、13 槽装备位、初始 HP/MP 都在那边，这里不再自己拼一遍。
	var items chargen.ItemSource
	if s.tables != nil {
		items = s.tables.Items
	}
	c := chargen.Build(
		account, chrName, uint32(job), uint32(sex), uint32(hair),
		chargen.Home{
			Map: s.cfg.HomeMap,
			X:   s.cfg.HomeX,
			Y:   s.cfg.HomeY,
		},
		items, &s.itemSeq,
	)
	if err := s.store.Characters().Create(ctx, c); err != nil {
		return one(proto.SM_NEWCHR_FAIL, 4, "")
	}

	// 建/删角色后需重新查询列表（原版 boChrQueryed := False）。
	// 同时放行紧随的查询，否则客户端会卡在 200ms 限流上。
	sess.chrQueryed = false
	sess.resetQueryLimit()
	return one(proto.SM_NEWCHR_SUCCESS, 0, "")
}

// onDelChr 删除角色（软删）。
func (s *Service) onDelChr(ctx context.Context, sess *Session, p wire.Packet) []wire.Packet {
	if sess == nil || (sess.Stage != StageServerSelected && sess.Stage != StageQueryed) {
		return one(proto.SM_DELCHR_FAIL, 0, "")
	}
	if err := checkSession(sess); err != nil {
		return one(proto.SM_DELCHR_FAIL, 0, "")
	}
	if !sess.passModifyLimit(s.cfg.NewChrIntervalMs) {
		return nil
	}
	chrName := strings.TrimSpace(p.Body)
	if chrName == "" {
		return one(proto.SM_DELCHR_FAIL, 0, "")
	}
	c, err := s.store.Characters().GetByName(ctx, chrName)
	if err != nil {
		return one(proto.SM_DELCHR_FAIL, 0, "")
	}
	// 只能删自己的角色
	if c.Account != sess.Account {
		return one(proto.SM_DELCHR_FAIL, 0, "")
	}
	if err := s.store.Characters().MarkDeleted(ctx, c.ID); err != nil {
		return one(proto.SM_DELCHR_FAIL, 0, "")
	}
	sess.chrQueryed = false
	sess.resetQueryLimit()
	return one(proto.SM_DELCHR_SUCCESS, 0, "")
}

// onSelChr 选中角色，下发游戏网关地址。
//
// ⚠️ 原版 UsrSoc.pas:576 的守卫是 "if not UserInfo.boChrQueryed"，
// 而 QueryChr 成功后恰恰把该标志置 True（:541）——等于"查过就不能选角"，
// 与注释要求的"必须先 Query"完全相反。此处按正确语义实现：
// 必须先查询过角色列表才能选角。
func (s *Service) onSelChr(ctx context.Context, sess *Session, p wire.Packet) []wire.Packet {
	if sess == nil || sess.Stage != StageQueryed {
		return one(proto.SM_STARTFAIL, 0, "")
	}
	if err := checkSession(sess); err != nil {
		return one(proto.SM_STARTFAIL, 0, "")
	}
	if !sess.chrQueryed {
		return one(proto.SM_STARTFAIL, 0, "")
	}
	i := strings.IndexByte(p.Body, '/')
	if i < 0 {
		return one(proto.SM_STARTFAIL, 0, "")
	}
	account, chrName := p.Body[:i], p.Body[i+1:]
	if account != sess.Account {
		return one(proto.SM_STARTFAIL, 0, "")
	}

	c, err := s.store.Characters().GetByName(ctx, chrName)
	if err != nil || c.Deleted || c.Account != sess.Account {
		return one(proto.SM_STARTFAIL, 0, "")
	}

	sess.CharacterName = c.Name
	sess.Stage = StagePlaying
	if err := s.persist(sess); err != nil {
		log.Printf("账号 %s 选角会话更新失败: %v", sess.Account, err)
		return one(proto.SM_STARTFAIL, 0, "")
	}
	if err := s.store.Sessions().IssueGameTicket(ctx, sess.Account, sess.SessionID,
		c.Name, time.Now(), sess.ExpiresAt); err != nil {
		log.Printf("账号 %s 角色 %s 游戏票据签发失败: %v", sess.Account, c.Name, err)
		return one(proto.SM_STARTFAIL, 0, "")
	}
	// 原版端口 = 网关端口 + 地图索引（UsrSoc.pas:923），实现分区。
	// 地图索引依赖 MapFile，暂缺该数据，先用 0。
	port := s.cfg.RunGatePort
	body := fmt.Sprintf("%s/%d", s.cfg.RunGateAddr, port)
	return one(proto.SM_STARTPLAY, 0, body)
}

// ---------- 辅助 ----------

func staleSessionResponse(ident uint16) []wire.Packet {
	switch ident {
	case proto.CM_IDPASSWORD:
		return one(proto.SM_PASSWD_FAIL, -3, "")
	case proto.CM_SELECTSERVER, proto.CM_SELCHR:
		return one(proto.SM_STARTFAIL, 0, "")
	case proto.CM_QUERYCHR:
		return one(proto.SM_QUERYCHR_FAIL, 0, "")
	case proto.CM_NEWCHR:
		return one(proto.SM_NEWCHR_FAIL, 0, "")
	case proto.CM_DELCHR:
		return one(proto.SM_DELCHR_FAIL, 0, "")
	default:
		return nil
	}
}

// one 构造单条响应。
func one(ident uint16, recog int32, body string) []wire.Packet {
	return []wire.Packet{{Head: proto.MakeDefaultMsg(ident, recog, 0, 0, 0), Body: body}}
}
