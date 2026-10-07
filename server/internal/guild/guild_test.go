package guild

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/storage"
)

func TestNewAndClone(t *testing.T) {
	g := New("测试会", "张三")
	if len(g.Ranks) != 1 || g.Ranks[0].No != ChiefRankNo {
		t.Fatalf("新建行会应只有 rank 1，得到 %+v", g.Ranks)
	}
	if !IsChief(g, "张三") {
		t.Fatal("张三应是掌门")
	}

	c := Clone(g)
	c.Ranks[0].Members[0] = "李四"
	if IsMember(c, "张三") == false && g.Ranks[0].Members[0] != "张三" {
		t.Fatal("Clone 应深拷贝成员切片")
	}
	if g.Ranks[0].Members[0] != "张三" {
		t.Fatal("Clone 修改不应影响原对象")
	}
}

func TestAddDelMember(t *testing.T) {
	g := New("会", "掌门")
	AddMember(g, "甲")
	AddMember(g, "乙")

	if Count(g) != 3 {
		t.Fatalf("人数 = %d，期望 3", Count(g))
	}
	if RankNoOf(g, "甲") != MemberRankNo {
		t.Fatalf("新成员应在 rank %d", MemberRankNo)
	}
	if !DelMember(g, "甲") {
		t.Fatal("删除应成功")
	}
	if DelMember(g, "甲") {
		t.Fatal("重复删除应失败")
	}
	if RankOf(g, "甲") != nil || Count(g) != 2 {
		t.Fatal("甲应已移除")
	}
}

func TestParseRankData(t *testing.T) {
	// 客户端从成员列表拼出的职务表（FState.pas:6514-6565）：
	// "#1 <掌门人>" + 成员行 + "#99 <行会成员>" + 成员行。
	text := "#1 <掌门人>\r张三\r\r#99 <行会成员>\r李四 王五\r赵六"
	ranks := ParseRankData(text)
	if len(ranks) != 2 {
		t.Fatalf("应解析出 2 个职务，得到 %+v", ranks)
	}
	if ranks[0].No != 1 || ranks[0].Name != "掌门人" || len(ranks[0].Members) != 1 {
		t.Fatalf("掌门职务解析错误: %+v", ranks[0])
	}
	if ranks[1].No != 99 || ranks[1].Name != "行会成员" {
		t.Fatalf("成员职务解析错误: %+v", ranks[1])
	}
	if got := ranks[1].Members; len(got) != 3 || got[0] != "李四" || got[2] != "赵六" {
		t.Fatalf("成员解析错误: %v", got)
	}
}

func TestParseRankDataLimit(t *testing.T) {
	// 一行超过 10 个名字时截断（Guild.pas:977）。
	line := ""
	for i := 0; i < 15; i++ {
		line += "甲 "
	}
	ranks := ParseRankData("#1 <掌门>\r" + line)
	if len(ranks) != 1 || len(ranks[0].Members) != maxMembersPerLine {
		t.Fatalf("应截断到 %d 人，得到 %d", maxMembersPerLine, len(ranks[0].Members))
	}

	// 职务名超长截断。
	long := ""
	for i := 0; i < 40; i++ {
		long += "长"
	}
	ranks = ParseRankData("#1 <" + long + ">")
	if got := []rune(ranks[0].Name); len(got) != MaxRankNameLen {
		t.Fatalf("职务名应截断到 %d 字符，得到 %d", MaxRankNameLen, len(got))
	}
}

func TestValidateRanks(t *testing.T) {
	g := New("会", "张三")
	AddMember(g, "李四")
	AddMember(g, "王五")

	tests := []struct {
		name   string
		text   string
		code   int
		online func(string) bool
	}{
		{"无变化", "#1 <" + ChiefRankName + ">\r张三\r#99 <行会成员>\r李四 王五", RankNoChange, nil},
		{"改职务名", "#1 <会长>\r张三\r#99 <行会成员>\r李四 王五", RankOK, nil},
		{"换职务", "#1 <掌门人>\r张三\r#50 <精英>\r李四\r#99 <行会成员>\r王五", RankOK, nil},
		{"掌门名空", "#1 <>\r张三\r#99 <行会成员>\r李四 王五", RankChiefNameEmpty, nil},
		{"首职务非掌门", "#2 <长老>\r张三\r#99 <行会成员>\r李四 王五", RankBadChief, nil},
		{"加人", "#1 <掌门人>\r张三\r#99 <行会成员>\r李四 王五 赵六", RankMemberChanged, nil},
		{"减人", "#1 <掌门人>\r张三\r#99 <行会成员>\r李四", RankMemberChanged, nil},
		{"职务号重复", "#1 <掌门人>\r张三\r#1 <另一个>\r李四\r#99 <行会成员>\r王五", RankBadRankNo, nil},
		{"职务号越界", "#1 <掌门人>\r张三\r#100 <精英>\r李四\r#99 <行会成员>\r王五", RankBadRankNo, nil},
		{"掌门超2人", "#1 <掌门人>\r张三 李四 王五\r#99 <行会成员>", RankTooManyChiefs, nil},
		{
			// ⚠️ 必须同时改职务名：职务表与现状完全一致时原版先判 -1
			// （Guild.pas:987-1018），根本走不到在线检查。
			"掌门全离线",
			"#1 <会长>\r张三\r#99 <行会成员>\r李四 王五",
			RankChiefOffline,
			func(string) bool { return false },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ranks := ParseRankData(tt.text)
			if got := ValidateRanks(g, ranks, tt.online); got != tt.code {
				t.Fatalf("返回码 = %d，期望 %d", got, tt.code)
			}
		})
	}
}

// ---------- Manager ----------

type fakeStore struct {
	mu sync.Mutex
	m  map[string]*storage.Guild
}

func newFakeStore() *fakeStore { return &fakeStore{m: make(map[string]*storage.Guild)} }

func (f *fakeStore) Create(_ context.Context, g *storage.Guild) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.m[g.Name]; ok {
		return storage.ErrExists
	}
	f.m[g.Name] = Clone(g)
	return nil
}

func (f *fakeStore) GetByName(_ context.Context, name string) (*storage.Guild, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := f.m[name]
	if g == nil {
		return nil, storage.ErrNotFound
	}
	return Clone(g), nil
}

func (f *fakeStore) List(_ context.Context) ([]*storage.Guild, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*storage.Guild, 0, len(f.m))
	for _, g := range f.m {
		out = append(out, Clone(g))
	}
	return out, nil
}

func (f *fakeStore) Save(_ context.Context, g *storage.Guild) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.m[g.Name]; !ok {
		return storage.ErrNotFound
	}
	f.m[g.Name] = Clone(g)
	return nil
}

func (f *fakeStore) Delete(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.m[name]; !ok {
		return storage.ErrNotFound
	}
	delete(f.m, name)
	return nil
}

func TestManagerLifecycle(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	m := NewManager(fs)

	// 建会
	g, err := m.Create(ctx, "测试会", "张三")
	if err != nil {
		t.Fatalf("建会失败: %v", err)
	}
	if g.Name != "测试会" || !IsChief(g, "张三") {
		t.Fatalf("建会结果异常: %+v", g)
	}
	if _, err := m.Create(ctx, "测试会", "甲"); !errors.Is(err, ErrExists) {
		t.Fatalf("重名建会应报 ErrExists，得到 %v", err)
	}
	if _, err := m.Create(ctx, "别的会", "张三"); !errors.Is(err, ErrAlreadyInGuild) {
		t.Fatalf("已入会者建会应报 ErrAlreadyInGuild，得到 %v", err)
	}

	// 反查
	got, no, name := m.OfMember("张三")
	if got == nil || no != ChiefRankNo || name != ChiefRankName {
		t.Fatalf("反查异常: %v %d %q", got, no, name)
	}
	if got, _, _ := m.OfMember("查无此人"); got != nil {
		t.Fatal("未入会者应返回 nil")
	}

	// 成员
	if err := m.AddMember(ctx, "测试会", "李四"); err != nil {
		t.Fatalf("加人失败: %v", err)
	}
	if err := m.AddMember(ctx, "测试会", "李四"); !errors.Is(err, ErrAlreadyInGuild) {
		t.Fatalf("重复加人应报错，得到 %v", err)
	}
	if _, no, _ := m.OfMember("李四"); no != MemberRankNo {
		t.Fatalf("李四职务号 = %d，期望 %d", no, MemberRankNo)
	}

	// 重新载入（模拟重启）后索引仍正确
	m2 := NewManager(fs)
	if err := m2.Load(ctx); err != nil {
		t.Fatalf("载入失败: %v", err)
	}
	if _, no, _ := m2.OfMember("李四"); no != MemberRankNo {
		t.Fatal("重启后应按成员名反查到行会")
	}
	if m2.Count() != 1 {
		t.Fatalf("行会数 = %d，期望 1", m2.Count())
	}

	// 改职务表（掌门改名成功；加人失败）
	if code, err := m2.UpdateRanks(ctx, "测试会", "#1 <会长>\r张三\r#99 <行会成员>\r李四", nil); err != nil || code != RankOK {
		t.Fatalf("改职务表: code=%d err=%v", code, err)
	}
	if code, _ := m2.UpdateRanks(ctx, "测试会", "#1 <会长>\r张三\r#99 <行会成员>\r李四 王五", nil); code != RankMemberChanged {
		t.Fatalf("加人应被拒（-6），得到 %d", code)
	}

	// 退会 / 解散
	if removed, err := m2.DelMember(ctx, "测试会", "李四"); err != nil || !removed {
		t.Fatalf("退会失败: %v %v", removed, err)
	}
	// ⚠️ 退会只删成员、保留空职务（Guild.pas:872-894），
	// 空职务要靠改职务表提交一份不含它的表格来清掉（成员总数不变，-6 不触发）。
	if ok := CanCancel(m2.Find("测试会"), "张三"); ok {
		t.Fatal("还有空职务时不应能解散")
	}
	if code, err := m2.UpdateRanks(ctx, "测试会", "#1 <会长>\r张三", nil); err != nil || code != RankOK {
		t.Fatalf("清空职务失败: code=%d err=%v", code, err)
	}
	if ok := CanCancel(m2.Find("测试会"), "张三"); !ok {
		t.Fatal("只剩掌门时应可解散")
	}
	if err := m2.Delete(ctx, "测试会"); err != nil {
		t.Fatalf("解散失败: %v", err)
	}
	if m2.Find("测试会") != nil || m2.Count() != 0 {
		t.Fatal("解散后不应还能查到")
	}
}

func TestManagerAllyWar(t *testing.T) {
	ctx := context.Background()
	m := NewManager(newFakeStore())
	if _, err := m.Create(ctx, "A会", "甲"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(ctx, "B会", "乙"); err != nil {
		t.Fatal(err)
	}

	if err := m.AddAlly(ctx, "A会", "B会"); err != nil {
		t.Fatalf("结盟失败: %v", err)
	}
	if !HasAlly(m.Find("A会"), "B会") || !HasAlly(m.Find("B会"), "A会") {
		t.Fatal("结盟应双向写入")
	}
	if ok, err := m.BreakAlly(ctx, "A会", "B会"); err != nil || !ok {
		t.Fatalf("解盟失败: %v %v", ok, err)
	}
	if HasAlly(m.Find("A会"), "B会") || HasAlly(m.Find("B会"), "A会") {
		t.Fatal("解盟应双向移除")
	}

	until := time.Now().Add(time.Hour).Truncate(time.Second)
	if err := m.AddWar(ctx, "A会", "B会", until); err != nil {
		t.Fatalf("宣战失败: %v", err)
	}
	ga := m.Find("A会")
	if len(ga.Wars) != 1 || ga.Wars[0].Name != "B会" || !ga.Wars[0].EndAt.Equal(until) {
		t.Fatalf("行会战记录异常: %+v", ga.Wars)
	}
	// 重复宣战应刷新到期时间而不是叠加。
	until2 := until.Add(time.Hour)
	if err := m.AddWar(ctx, "A会", "B会", until2); err != nil {
		t.Fatal(err)
	}
	if wars := m.Find("A会").Wars; len(wars) != 1 || !wars[0].EndAt.Equal(until2) {
		t.Fatalf("重复宣战应刷新时间: %+v", wars)
	}
}

// TestGuildWar 覆盖宣战的双向写入、回滚与到期清理。
func TestGuildWar(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	m := NewManager(fs)
	_, _ = m.Create(ctx, "A会", "甲")
	_, _ = m.Create(ctx, "B会", "乙")

	// 双向：宣战成功后两边互为敌对
	until := time.Now().Add(30 * time.Minute)
	if err := m.AddWar(ctx, "A会", "B会", until); err != nil {
		t.Fatalf("AddWar: %v", err)
	}
	ga, gb := m.Find("A会"), m.Find("B会")
	if len(ga.Wars) != 1 || ga.Wars[0].Name != "B会" {
		t.Errorf("A 会的敌对行会 = %+v", ga.Wars)
	}
	if len(gb.Wars) != 1 || gb.Wars[0].Name != "A会" {
		t.Errorf("B 会的敌对行会 = %+v", gb.Wars)
	}
	if got := m.ActiveWars(ga, time.Now()); len(got) != 1 || got[0] != "B会" {
		t.Errorf("ActiveWars = %v", got)
	}
	// 落库检查：存档里也有
	if reloaded, err := fs.GetByName(ctx, "A会"); err != nil || len(reloaded.Wars) != 1 {
		t.Errorf("行会战未落库: %+v (%v)", reloaded, err)
	}

	// 幂等：重复宣战只更新到期时间，不重复添加
	if err := m.AddWar(ctx, "A会", "B会", until.Add(time.Hour)); err != nil {
		t.Fatalf("重复 AddWar: %v", err)
	}
	ga = m.Find("A会")
	if len(ga.Wars) != 1 {
		t.Errorf("重复宣战产生重复条目: %+v", ga.Wars)
	}

	// 回滚：双方都清掉
	if err := m.RemoveWar(ctx, "A会", "B会"); err != nil {
		t.Fatalf("RemoveWar: %v", err)
	}
	ga = m.Find("A会")
	if len(ga.Wars) != 0 {
		t.Errorf("回滚后 A 会仍有战争记录: %+v", ga.Wars)
	}

	// 到期清理：过期项被剔除
	_ = m.AddWar(ctx, "A会", "B会", time.Now().Add(-time.Second))
	dropped := m.PruneWars(ctx, time.Now())
	if len(dropped) != 1 {
		t.Errorf("PruneWars 应清掉 1 条，实际 %d 条（%+v）", len(dropped), dropped)
	}
	ga = m.Find("A会")
	if len(m.ActiveWars(ga, time.Now())) != 0 {
		t.Errorf("过期战争仍被视作有效: %+v", ga.Wars)
	}
}

// TestAtWar 单向语义：原版 IsNotWarGuild 遍历的是**自己**的敌对列表，
// 所以 AtWar(a,b) 与 AtWar(b,a) 必须分别判断，InWar 才是两侧的 and。
func TestAtWar(t *testing.T) {
	now := time.Now()
	later := now.Add(time.Hour)
	past := now.Add(-time.Hour)

	a := &storage.Guild{Name: "A会"}
	b := &storage.Guild{Name: "B会"}
	c := &storage.Guild{Name: "C会"}

	// 无宣战记录
	if AtWar(a, b, now) || InWar(a, b, now) {
		t.Fatalf("未宣战却判成战争中: a=%+v", a.Wars)
	}

	// 只有 A 记着 B（单向，模拟落库中途失败）
	a.Wars = []storage.GuildWar{{Name: "B会", EndAt: later}}
	if !AtWar(a, b, now) {
		t.Error("A 记着 B，AtWar(a,b) 应为 true")
	}
	if AtWar(b, a, now) {
		t.Error("B 没记着 A，AtWar(b,a) 应为 false（方向性是真的）")
	}
	if !InWar(a, b, now) {
		t.Error("InWar 是两侧 or，单向记录也应命中")
	}
	if AtWar(a, c, now) {
		t.Error("A 与 C 无战争记录")
	}

	// 双向
	b.Wars = []storage.GuildWar{{Name: "A会", EndAt: later}}
	if !AtWar(b, a, now) || !InWar(a, b, now) {
		t.Error("双向宣战后两侧都应为 true")
	}

	// 过期即失效（原版靠 dwWarTick 递减摘掉条目）
	// ⚠️ 要两侧都换成过期记录：InWar 是 or，只改一侧的话另一侧仍命中。
	a.Wars = []storage.GuildWar{{Name: "B会", EndAt: past}}
	b.Wars = []storage.GuildWar{{Name: "A会", EndAt: past}}
	if AtWar(a, b, now) || InWar(a, b, now) {
		t.Error("已过期的战争记录不应算作战争中")
	}

	// nil 安全
	if AtWar(nil, b, now) || AtWar(a, nil, now) || InWar(nil, nil, now) {
		t.Error("nil 行会不应判为战争中")
	}
}

// TestAddAllyIdempotent 守住"重复结盟也算成功"这条原版语义：
// AllyGuild 内部去重（Guild.pas:1191），ClientGuildAlly 丢弃返回值直接 n8:=0。
func TestAddAllyIdempotent(t *testing.T) {
	g := &storage.Guild{Name: "A会"}
	AddAlly(g, "B会")
	AddAlly(g, "B会")
	if len(g.Allies) != 1 {
		t.Fatalf("重复结盟应去重，得到 %+v", g.Allies)
	}
	if !HasAlly(g, "B会") {
		t.Error("HasAlly 应为 true")
	}
	if !DelAlly(g, "B会") || HasAlly(g, "B会") {
		t.Error("DelAlly 应删掉并返回 true")
	}
	if DelAlly(g, "B会") {
		t.Error("重复 DelAlly 应返回 false")
	}
}
