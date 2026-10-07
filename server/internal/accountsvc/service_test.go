package accountsvc

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/storage"
	"github.com/algotao/mir2/server/internal/storage/sqlite"
	"github.com/algotao/mir2/server/internal/wire"
)

// newTestService 创建测试用服务。
//
// 把限速间隔压到 1ms，否则连续建/删角色会被 1000ms 的间隔拦下；
// 限速行为本身由 TestRateLimit 单独用默认配置验证。
func newTestService(t *testing.T) (*Service, *Session) {
	t.Helper()
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := DefaultConfig()
	cfg.NewChrIntervalMs = 0 // 关闭限速，避免测试因时序抖动不稳定
	cfg.QueryChrIntervalMs = 0
	svc := New(st, cfg)
	return svc, mustNewSession(t, svc, "127.0.0.1")
}

func mustNewSession(t *testing.T, svc *Service, ip string) *Session {
	t.Helper()
	sess, err := svc.NewSession(ip)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return sess
}

// addAccount 建一个测试账号。
func addAccount(t *testing.T, svc *Service, name, pw string) {
	t.Helper()
	hash, salt, err := storage.HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.store.Accounts().Create(context.Background(),
		&storage.Account{Name: name, PasswordHash: hash, Salt: salt}); err != nil {
		t.Fatalf("建账号: %v", err)
	}
}

func pkt(ident uint16, body string) wire.Packet {
	return wire.Packet{Head: proto.MakeDefaultMsg(ident, 0, 0, 0, 0), Body: body}
}

func identOf(t *testing.T, out []wire.Packet) uint16 {
	t.Helper()
	if len(out) == 0 {
		t.Fatal("无响应包")
	}
	return out[0].Head.Ident
}

// TestFullLoginFlow 走完整流程：握手 → 登录 → 选服 → 查角色 → 建角色 → 选角。
// 这是 P2 的核心验收：客户端能走完这条链路才说明协议对齐。
func TestFullLoginFlow(t *testing.T) {
	ctx := context.Background()
	svc, sess := newTestService(t)
	addAccount(t, svc, "tester", "pw123")

	// 1. 版本握手
	out := svc.Handle(ctx, sess, pkt(proto.CM_PROTOCOL, ""))
	if got := identOf(t, out); got != proto.SM_CERTIFICATION_SUCCESS {
		t.Fatalf("握手响应 = %d, want SM_CERTIFICATION_SUCCESS(%d)", got, proto.SM_CERTIFICATION_SUCCESS)
	}

	// 2. 登录
	out = svc.Handle(ctx, sess, pkt(proto.CM_IDPASSWORD, "tester/pw123"))
	if got := identOf(t, out); got != proto.SM_PASSOK_SELECTSERVER {
		t.Fatalf("登录响应 = %d, want SM_PASSOK_SELECTSERVER(%d)", got, proto.SM_PASSOK_SELECTSERVER)
	}
	if sess.Stage != StageAuthed {
		t.Errorf("登录后 Stage = %v, want StageAuthed", sess.Stage)
	}
	if !strings.HasPrefix(out[0].Body, "mir2go/2/") {
		t.Errorf("服务器列表 = %q", out[0].Body)
	}

	// 3. 选服，应拿到角色网关地址
	out = svc.Handle(ctx, sess, pkt(proto.CM_SELECTSERVER, "mir2go"))
	if got := identOf(t, out); got != proto.SM_SELECTSERVER_OK {
		t.Fatalf("选服响应 = %d", got)
	}
	want := fmt.Sprintf("127.0.0.1/7100/%d", sess.SessionID)
	if out[0].Body != want {
		t.Errorf("选服正文 = %q, want %q", out[0].Body, want)
	}

	// 4. 查角色（此时为空）
	q := fmt.Sprintf("tester/%d", sess.SessionID)
	out = svc.Handle(ctx, sess, pkt(proto.CM_QUERYCHR, q))
	if got := identOf(t, out); got != proto.SM_QUERYCHR {
		t.Fatalf("查角色响应 = %d", got)
	}
	if out[0].Head.Recog != 0 || out[0].Body != "" {
		t.Errorf("空角色列表: Recog=%d Body=%q", out[0].Head.Recog, out[0].Body)
	}
	// 原版固定 Param=0 Tag=1 Series=0
	if out[0].Head.Param != 0 || out[0].Head.Tag != 1 || out[0].Head.Series != 0 {
		t.Errorf("SM_QUERYCHR 参数不符: %+v", out[0].Head)
	}

	// 5. 建角色（正文 account/chrName/hair/job/sex）
	out = svc.Handle(ctx, sess, pkt(proto.CM_NEWCHR, "tester/勇士/1/0/0"))
	if got := identOf(t, out); got != proto.SM_NEWCHR_SUCCESS {
		t.Fatalf("建角色响应 = %d", got)
	}

	// 6. 再查角色
	out = svc.Handle(ctx, sess, pkt(proto.CM_QUERYCHR, q))
	if got := identOf(t, out); got != proto.SM_QUERYCHR {
		t.Fatalf("查询响应 = %d", got)
	}
	if out[0].Head.Recog != 1 {
		t.Errorf("角色数 = %d, want 1", out[0].Head.Recog)
	}
	if out[0].Body != "勇士/0/1/1/0/" {
		t.Errorf("角色串 = %q, want %q", out[0].Body, "勇士/0/1/1/0/")
	}

	// 7. 选角，应拿到游戏网关地址
	out = svc.Handle(ctx, sess, pkt(proto.CM_SELCHR, "tester/勇士"))
	if got := identOf(t, out); got != proto.SM_STARTPLAY {
		t.Fatalf("选角响应 = %d, want SM_STARTPLAY(%d)", got, proto.SM_STARTPLAY)
	}
	if out[0].Body != "127.0.0.1/7200" {
		t.Errorf("选角正文 = %q", out[0].Body)
	}
	if sess.Stage != StagePlaying {
		t.Errorf("选角后 Stage = %v", sess.Stage)
	}
}

// TestCrossPortSessionBinding 模拟真实的跨端口流程。
//
// 客户端登录成功后会**断连**并转连选角网关(7100)，新连接没有身份，
// 必须靠 CM_QUERYCHR 正文里的 SessionID 绑定回已认证会话。
func TestCrossPortSessionBinding(t *testing.T) {
	ctx := context.Background()
	svc, sess := newTestService(t) // 登录网关(7000)上的会话
	addAccount(t, svc, "tester", "pw123")

	svc.Handle(ctx, sess, pkt(proto.CM_IDPASSWORD, "tester/pw123"))
	svc.Handle(ctx, sess, pkt(proto.CM_SELECTSERVER, "mir2go"))
	svc.Handle(ctx, sess, pkt(proto.CM_NEWCHR, "tester/勇士/1/0/0"))
	sid := sess.SessionID

	// 客户端断连，转连 7100：新连接是游离会话
	conn := svc.NewConnSession("127.0.0.1")
	if conn.Account != "" {
		t.Fatal("游离会话不应已绑定账号")
	}

	// 错误的 SessionID → 拒绝
	out := svc.Handle(ctx, conn, pkt(proto.CM_QUERYCHR, fmt.Sprintf("tester/%d", sid+1000)))
	if got := identOf(t, out); got != proto.SM_QUERYCHR_FAIL {
		t.Fatalf("错误 SessionID → %d, want SM_QUERYCHR_FAIL", got)
	}
	if conn.Account != "" {
		t.Error("失败的绑定不应写入账号")
	}

	// 正确的 SessionID → 绑定成功
	out = svc.Handle(ctx, conn, pkt(proto.CM_QUERYCHR, fmt.Sprintf("tester/%d", sid)))
	if got := identOf(t, out); got != proto.SM_QUERYCHR {
		t.Fatalf("绑定响应 = %d, want SM_QUERYCHR", got)
	}
	if conn.Account != "tester" || conn.SessionID != sid {
		t.Errorf("绑定结果: account=%q sid=%d, want %q/%d", conn.Account, conn.SessionID, "tester", sid)
	}
	if out[0].Body != "勇士/0/1/1/0/" {
		t.Errorf("角色串 = %q", out[0].Body)
	}

	// 同一票据不能再绑定到第二条 7100 连接。
	replay := svc.NewConnSession("127.0.0.1")
	out = svc.Handle(ctx, replay, pkt(proto.CM_QUERYCHR, fmt.Sprintf("tester/%d", sid)))
	if got := identOf(t, out); got != proto.SM_QUERYCHR_FAIL {
		t.Fatalf("重放 handoff = %d, want SM_QUERYCHR_FAIL", got)
	}

	// 已绑定的连接仍可正常选角。
	out = svc.Handle(ctx, conn, pkt(proto.CM_SELCHR, "tester/勇士"))
	if got := identOf(t, out); got != proto.SM_STARTPLAY {
		t.Fatalf("选角 = %d, want SM_STARTPLAY", got)
	}
}

func TestNewLoginTakesOverPreviousSession(t *testing.T) {
	ctx := context.Background()
	svc, first := newTestService(t)
	addAccount(t, svc, "tester", "pw123")
	if got := identOf(t, svc.Handle(ctx, first, pkt(proto.CM_IDPASSWORD, "tester/pw123"))); got != proto.SM_PASSOK_SELECTSERVER {
		t.Fatalf("first login=%d", got)
	}
	if got := identOf(t, svc.Handle(ctx, first, pkt(proto.CM_SELECTSERVER, "mir2go"))); got != proto.SM_SELECTSERVER_OK {
		t.Fatalf("first select=%d", got)
	}
	oldSID := first.SessionID

	second := mustNewSession(t, svc, "127.0.0.2")
	if second.SessionID == oldSID {
		t.Fatal("random SessionID collision")
	}
	if got := identOf(t, svc.Handle(ctx, second, pkt(proto.CM_IDPASSWORD, "tester/pw123"))); got != proto.SM_PASSOK_SELECTSERVER {
		t.Fatalf("replacement login=%d", got)
	}
	if svc.IsCurrentSession(first) {
		t.Fatal("old account session remained current")
	}
	if got := identOf(t, svc.Handle(ctx, first, pkt(proto.CM_SELECTSERVER, "mir2go"))); got != proto.SM_STARTFAIL {
		t.Fatalf("stale connection select=%d, want SM_STARTFAIL", got)
	}
	if got := identOf(t, svc.Handle(ctx, second, pkt(proto.CM_SELECTSERVER, "mir2go"))); got != proto.SM_SELECTSERVER_OK {
		t.Fatalf("replacement select=%d", got)
	}

	oldConn := svc.NewConnSession("127.0.0.1")
	if got := identOf(t, svc.Handle(ctx, oldConn, pkt(proto.CM_QUERYCHR, fmt.Sprintf("tester/%d", oldSID)))); got != proto.SM_QUERYCHR_FAIL {
		t.Fatalf("stale handoff=%d, want SM_QUERYCHR_FAIL", got)
	}
	newConn := svc.NewConnSession("127.0.0.2")
	if got := identOf(t, svc.Handle(ctx, newConn, pkt(proto.CM_QUERYCHR, fmt.Sprintf("tester/%d", second.SessionID)))); got != proto.SM_QUERYCHR {
		t.Fatalf("new handoff=%d, want SM_QUERYCHR", got)
	}
}

// TestSessionPersisted 校验会话落盘。
//
// 这是 gamesvr 能独立校验客户端的前提：它凭 SessionID 在共享库里查到账号与阶段，
// 不必再与 accountsvc 做进程间通信。
func TestSessionPersisted(t *testing.T) {
	ctx := context.Background()
	svc, sess := newTestService(t)
	addAccount(t, svc, "tester", "pw123")

	// 建会话时即落盘，但尚无账号身份
	rec, err := svc.store.Sessions().Get(ctx, sess.SessionID)
	if err != nil {
		t.Fatalf("会话未落盘: %v", err)
	}
	if rec.Account != "" {
		t.Errorf("认证前 Account = %q, want 空", rec.Account)
	}

	svc.Handle(ctx, sess, pkt(proto.CM_IDPASSWORD, "tester/pw123"))
	svc.Handle(ctx, sess, pkt(proto.CM_SELECTSERVER, "mir2go"))

	rec, err = svc.store.Sessions().Get(ctx, sess.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Account != "tester" {
		t.Errorf("落盘 Account = %q, want %q", rec.Account, "tester")
	}
	if rec.Stage < int(StageAuthed) {
		t.Errorf("落盘 Stage = %d, want >= %d", rec.Stage, StageAuthed)
	}
	if rec.ServerName != "mir2go" {
		t.Errorf("落盘 ServerName = %q", rec.ServerName)
	}

	// 断连清理
	svc.DropSession(sess.SessionID)
	if _, err := svc.store.Sessions().Get(ctx, sess.SessionID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("清理后仍可查到: %v", err)
	}
}

// TestLoginFailures 校验登录失败码与锁定逻辑。
func TestLoginFailures(t *testing.T) {
	ctx := context.Background()
	svc, sess := newTestService(t)
	addAccount(t, svc, "tester", "pw123")

	// 密码错误
	out := svc.Handle(ctx, sess, pkt(proto.CM_IDPASSWORD, "tester/wrong"))
	if got := identOf(t, out); got != proto.SM_PASSWD_FAIL {
		t.Fatalf("响应 = %d, want SM_PASSWD_FAIL", got)
	}
	if out[0].Head.Recog != -1 {
		t.Errorf("Recog = %d, want -1", out[0].Head.Recog)
	}
	if sess.Stage != StageNew {
		t.Error("失败后不应改变 Stage")
	}

	// 账号不存在也返回 -1（不暴露账号是否存在）
	out = svc.Handle(ctx, sess, pkt(proto.CM_IDPASSWORD, "nobody/x"))
	if out[0].Head.Recog != -1 {
		t.Errorf("不存在账号 Recog = %d, want -1", out[0].Head.Recog)
	}

	// 连续错误达上限后锁定
	cfg := svc.cfg
	for i := 1; i < cfg.MaxPasswordErrors; i++ {
		svc.Handle(ctx, sess, pkt(proto.CM_IDPASSWORD, "tester/wrong"))
	}
	out = svc.Handle(ctx, sess, pkt(proto.CM_IDPASSWORD, "tester/pw123"))
	if out[0].Head.Recog != -2 {
		t.Errorf("锁定后 Recog = %d, want -2", out[0].Head.Recog)
	}
}

// TestRequiresAuth 校验未认证时角色操作一律被拒。
func TestRequiresAuth(t *testing.T) {
	ctx := context.Background()
	svc, sess := newTestService(t)

	cases := map[uint16]uint16{
		proto.CM_SELECTSERVER: proto.SM_STARTFAIL,
		proto.CM_QUERYCHR:     proto.SM_QUERYCHR_FAIL,
		proto.CM_NEWCHR:       proto.SM_NEWCHR_FAIL,
		proto.CM_DELCHR:       proto.SM_DELCHR_FAIL,
		proto.CM_SELCHR:       proto.SM_STARTFAIL,
	}
	for in, want := range cases {
		out := svc.Handle(ctx, sess, pkt(in, "x"))
		if len(out) == 0 {
			t.Errorf("CM_%d 未认证时无响应", in)
			continue
		}
		if out[0].Head.Ident != want {
			t.Errorf("CM_%d → %d, want %d", in, out[0].Head.Ident, want)
		}
	}
}

// TestSelChrRequiresQuery 固化"必须查过角色才能选角"。
//
// 原版此处逻辑是反的（UsrSoc.pas:576 用 "not boChrQueryed"，
// 而 QueryChr 成功后置 True），我们按正确语义实现。
func TestSelChrRequiresQuery(t *testing.T) {
	ctx := context.Background()
	svc, sess := newTestService(t)
	addAccount(t, svc, "tester", "pw123")

	svc.Handle(ctx, sess, pkt(proto.CM_IDPASSWORD, "tester/pw123"))
	svc.Handle(ctx, sess, pkt(proto.CM_SELECTSERVER, "mir2go"))
	svc.Handle(ctx, sess, pkt(proto.CM_NEWCHR, "tester/勇士/1/0/0"))

	// 未查询过 → 拒绝
	out := svc.Handle(ctx, sess, pkt(proto.CM_SELCHR, "tester/勇士"))
	if got := identOf(t, out); got != proto.SM_STARTFAIL {
		t.Fatalf("未查询就选角 = %d, want SM_STARTFAIL", got)
	}

	// 查询过 → 允许
	svc.Handle(ctx, sess, pkt(proto.CM_QUERYCHR, fmt.Sprintf("tester/%d", sess.SessionID)))
	out = svc.Handle(ctx, sess, pkt(proto.CM_SELCHR, "tester/勇士"))
	if got := identOf(t, out); got != proto.SM_STARTPLAY {
		t.Fatalf("查询后选角 = %d, want SM_STARTPLAY", got)
	}
}

// TestNewChrErrors 校验建角色的错误码：0 非法 / 2 重名 / 3 超限。
func TestNewChrErrors(t *testing.T) {
	ctx := context.Background()
	svc, sess := newTestService(t)
	addAccount(t, svc, "tester", "pw123")
	svc.Handle(ctx, sess, pkt(proto.CM_IDPASSWORD, "tester/pw123"))
	svc.Handle(ctx, sess, pkt(proto.CM_SELECTSERVER, "mir2go"))

	// 名字过短
	out := svc.Handle(ctx, sess, pkt(proto.CM_NEWCHR, "tester/ab/1/0/0"))
	if out[0].Head.Recog != 0 {
		t.Errorf("短名 Recog = %d, want 0", out[0].Head.Recog)
	}
	// 含非法字符
	out = svc.Handle(ctx, sess, pkt(proto.CM_NEWCHR, "tester/ab cd/1/0/0"))
	if out[0].Head.Recog != 0 {
		t.Errorf("含空格 Recog = %d, want 0", out[0].Head.Recog)
	}
	// 字段数不对
	out = svc.Handle(ctx, sess, pkt(proto.CM_NEWCHR, "tester/abcd"))
	if out[0].Head.Recog != 0 {
		t.Errorf("字段数不符 Recog = %d", out[0].Head.Recog)
	}

	// 正常建两个
	for _, n := range []string{"甲勇士", "乙勇士"} {
		out = svc.Handle(ctx, sess, pkt(proto.CM_NEWCHR, "tester/"+n+"/1/0/0"))
		if out[0].Head.Ident != proto.SM_NEWCHR_SUCCESS {
			t.Fatalf("建 %s 失败: %d", n, out[0].Head.Ident)
		}
	}

	// 第三个 → 超过上限
	out = svc.Handle(ctx, sess, pkt(proto.CM_NEWCHR, "tester/丙勇士/1/0/0"))
	if out[0].Head.Recog != 3 {
		t.Errorf("超限 Recog = %d, want 3", out[0].Head.Recog)
	}

	// 重名（换一个会话，避免账号下已满）——用另一账号
	addAccount(t, svc, "other", "pw")
	sess2 := mustNewSession(t, svc, "127.0.0.1")
	svc.Handle(ctx, sess2, pkt(proto.CM_IDPASSWORD, "other/pw"))
	svc.Handle(ctx, sess2, pkt(proto.CM_SELECTSERVER, "mir2go"))
	out = svc.Handle(ctx, sess2, pkt(proto.CM_NEWCHR, "other/甲勇士/1/0/0"))
	if out[0].Head.Recog != 2 {
		t.Errorf("重名 Recog = %d, want 2", out[0].Head.Recog)
	}
}

// TestDelChr 校验删角色为软删，且不能删别人的角色。
func TestDelChr(t *testing.T) {
	ctx := context.Background()
	svc, sess := newTestService(t)
	addAccount(t, svc, "tester", "pw123")
	svc.Handle(ctx, sess, pkt(proto.CM_IDPASSWORD, "tester/pw123"))
	svc.Handle(ctx, sess, pkt(proto.CM_SELECTSERVER, "mir2go"))
	svc.Handle(ctx, sess, pkt(proto.CM_NEWCHR, "tester/勇士/1/0/0"))

	// 别人不能删
	addAccount(t, svc, "other", "pw")
	sess2 := mustNewSession(t, svc, "127.0.0.1")
	svc.Handle(ctx, sess2, pkt(proto.CM_IDPASSWORD, "other/pw"))
	svc.Handle(ctx, sess2, pkt(proto.CM_SELECTSERVER, "mir2go"))
	out := svc.Handle(ctx, sess2, pkt(proto.CM_DELCHR, "勇士"))
	if out[0].Head.Ident != proto.SM_DELCHR_FAIL {
		t.Error("不应能删别人的角色")
	}

	// 本人删除
	out = svc.Handle(ctx, sess, pkt(proto.CM_DELCHR, "勇士"))
	if out[0].Head.Ident != proto.SM_DELCHR_SUCCESS {
		t.Fatalf("删除失败: %d", out[0].Head.Ident)
	}
	// 软删：记录仍在但不在列表里
	list, _ := svc.store.Characters().ListByAccount(ctx, "tester")
	if len(list) != 0 {
		t.Errorf("软删后列表 = %d, want 0", len(list))
	}
	if _, err := svc.store.Characters().GetByName(ctx, "勇士"); err != nil {
		t.Errorf("软删后记录应仍可取: %v", err)
	}
}

// TestRateLimit 校验建角色的限速（默认 1000ms）。
func TestRateLimit(t *testing.T) {
	ctx := context.Background()
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(st, DefaultConfig()) // 使用默认 1000ms 间隔
	sess := mustNewSession(t, svc, "127.0.0.1")
	addAccount(t, svc, "tester", "pw123")
	svc.Handle(ctx, sess, pkt(proto.CM_IDPASSWORD, "tester/pw123"))
	svc.Handle(ctx, sess, pkt(proto.CM_SELECTSERVER, "mir2go"))

	if out := svc.Handle(ctx, sess, pkt(proto.CM_NEWCHR, "tester/甲勇士/1/0/0")); len(out) == 0 {
		t.Fatal("首次建角色应有响应")
	}
	// 立刻再建 → 静默丢弃
	if out := svc.Handle(ctx, sess, pkt(proto.CM_NEWCHR, "tester/乙勇士/1/0/0")); len(out) != 0 {
		t.Errorf("限速内应无响应, got %d 个包", len(out))
	}
}

// TestSessionStore 校验随机会话 ID 分配与本地索引回收。
func TestSessionStore(t *testing.T) {
	s := NewSessionStore()
	a, err := s.Create("1.1.1.1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Create("1.1.1.2")
	if err != nil {
		t.Fatal(err)
	}
	if a.SessionID == b.SessionID {
		t.Fatal("SessionID 应唯一")
	}
	if a.SessionID < 2 {
		t.Errorf("SessionID = %d, 应为正且跳过保留值", a.SessionID)
	}
	if _, ok := s.Get(a.SessionID); !ok {
		t.Error("应能取到会话")
	}
	if s.Len() != 2 {
		t.Errorf("Len = %d", s.Len())
	}
	s.Forget(a.SessionID)
	if _, ok := s.Get(a.SessionID); ok {
		t.Error("删除后不应取到")
	}
}

func TestValidChrName(t *testing.T) {
	cases := map[string]bool{
		"勇士甲": true,
		"ab":  false, // 过短
		"a b": false, // 空格
		"a/b": false, // 分隔符
		"a@b": false,
		"a?b": false,
		"a'b": false,
	}
	for name, want := range cases {
		if got := validChrName(name); got != want {
			t.Errorf("validChrName(%q) = %v, want %v", name, got, want)
		}
	}
}
