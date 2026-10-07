package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/algotao/mir2/server/internal/storage"
	"github.com/algotao/mir2/server/internal/storage/pb"
)

// newTestStore 在临时目录打开一个数据库。
//
// ⚠️ 不要用 ":memory:"：它会绕过文件锁与 WAL 的真实行为，
// 而"文件锁在 NFS 上不可靠"正是我们要防的坑（见 store.go 注释）。
func newTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestPingAndMigrate(t *testing.T) {
	s := newTestStore(t)
	if err := s.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	// 重复 Open 同一文件应走 IF NOT EXISTS 而不报错
	if err := migrate(s.db); err != nil {
		t.Fatalf("重复建表: %v", err)
	}
}

func TestAccountLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	hash, salt, err := storage.HashPassword("secret")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	a := &storage.Account{
		Name:         "algotao",
		PasswordHash: hash,
		Salt:         salt,
		Data:         &pb.AccountData{Email: "a@example.com", UserName: "张三"},
	}
	if err := s.Accounts().Create(ctx, a); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if a.ID == 0 {
		t.Fatal("Create 后 ID 未回填")
	}

	got, err := s.Accounts().GetByName(ctx, "algotao")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if !storage.VerifyPassword("secret", got.PasswordHash, got.Salt) {
		t.Error("正确口令应通过校验")
	}
	if storage.VerifyPassword("wrong", got.PasswordHash, got.Salt) {
		t.Error("错误口令不应通过校验")
	}
	if got.Data.Email != "a@example.com" || got.Data.UserName != "张三" {
		t.Errorf("AccountData 往返不符: %+v", got.Data)
	}

	// 重名应报 ErrExists
	dup := &storage.Account{Name: "algotao", PasswordHash: hash, Salt: salt}
	if err := s.Accounts().Create(ctx, dup); !errors.Is(err, storage.ErrExists) {
		t.Errorf("重名 = %v, want ErrExists", err)
	}

	// 不存在应报 ErrNotFound
	if _, err := s.Accounts().GetByName(ctx, "nobody"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("查无此人 = %v, want ErrNotFound", err)
	}
}

func TestPasswordHash(t *testing.T) {
	h1, s1, err := storage.HashPassword("same")
	if err != nil {
		t.Fatal(err)
	}
	h2, s2, _ := storage.HashPassword("same")
	if string(h1) == string(h2) || string(s1) == string(s2) {
		t.Error("相同口令的盐/哈希不应相同（防彩虹表）")
	}
	if !storage.VerifyPassword("same", h1, s1) {
		t.Error("校验失败")
	}
	// 异常输入必须判否，不能 panic
	if storage.VerifyPassword("", h1, s1) {
		t.Error("空口令不应通过")
	}
	if storage.VerifyPassword("same", h1, []byte("short")) {
		t.Error("盐长度异常不应通过")
	}
	if _, _, err := storage.HashPassword(""); !errors.Is(err, storage.ErrEmptyPassword) {
		t.Errorf("空口令 = %v, want ErrEmptyPassword", err)
	}
}

// TestCharacterRoundTrip 校验整块存档的 protobuf 往返一致性。
// 这是存档层的正确性底线——一旦错位就是丢装备/丢技能。
func TestCharacterRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	want := &pb.CharacterData{
		ChrName: "勇士",
		Account: "algotao",
		CurMap:  "0",
		CurX:    289,
		CurY:    618,
		Dir:     4,
		Hair:    1,
		Sex:     0,
		Job:     0,
		Gold:    123456,
		Abil: &pb.Ability{
			Level: 35, Hp: 100, Mp: 50, MaxHp: 200, MaxMp: 80,
			Exp: 1234567, MaxExp: 7654321,
			Dc: &pb.MinMax{Min: 5, Max: 15},
			Ac: &pb.MinMax{Min: 0, Max: 3},
		},
		StatusTime: []uint32{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11},
		HomeMap:    "0",
		HomeX:      289,
		HomeY:      618,
		BonusAbil:  &pb.NakedAbility{Hit: 5, Speed: 15},
		BonusPoint: 42,
		StoragePwd: "1234",
		GameGold:   99,
		PkPoint:    100,
		AllowGroup: true,
		AttackMode: 3,
		HumItems: []*pb.UserItem{
			{MakeIndex: 1001, Index: 5, Dura: 1000, DuraMax: 2000, Value: []byte{1, 2, 3}},
		},
		BagItems: []*pb.UserItem{
			{MakeIndex: 2001, Index: 3, Dura: 1, DuraMax: 1},
			{MakeIndex: 2002, Index: 3, Dura: 1, DuraMax: 1},
		},
		Magics: []*pb.UserMagic{
			{MagicId: 1, Level: 3, TranPoint: 500, Key: 'F'},
			{MagicId: 26, Level: 2, TranPoint: 300},
		},
		StorageItems: []*pb.UserItem{
			{MakeIndex: 3001, Index: 10, Dura: 500, DuraMax: 500},
		},
		QuestFlag: make([]byte, 128),
	}
	want.QuestFlag[0] = 1
	want.QuestFlag[127] = 0xFF

	c := &storage.Character{Data: proto.Clone(want).(*pb.CharacterData)}
	c.SyncFromData()

	if err := s.Characters().Create(ctx, c); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if c.Level != 35 || c.Gold != 123456 {
		t.Fatalf("索引列投影错误: level=%d gold=%d", c.Level, c.Gold)
	}

	got, err := s.Characters().GetByName(ctx, "勇士")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if !proto.Equal(got.Data, want) {
		t.Errorf("protobuf 往返不一致:\n got=%v\nwant=%v", got.Data, want)
	}
	if got.Level != 35 || got.Gold != 123456 || got.Job != 0 {
		t.Errorf("索引列不符: %+v", got)
	}
}

func TestCharacterListAndDelete(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	for _, name := range []string{"甲", "乙"} {
		c := &storage.Character{Data: &pb.CharacterData{ChrName: name, Account: "algotao", Abil: &pb.Ability{Level: 1}}}
		c.SyncFromData()
		if err := s.Characters().Create(ctx, c); err != nil {
			t.Fatalf("Create %s: %v", name, err)
		}
	}

	list, err := s.Characters().ListByAccount(ctx, "algotao")
	if err != nil {
		t.Fatalf("ListByAccount: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("列表长度 = %d, want 2", len(list))
	}

	// 软删后不应出现在列表里，但记录仍在（可恢复）
	if err := s.Characters().MarkDeleted(ctx, list[0].ID); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}
	list, _ = s.Characters().ListByAccount(ctx, "algotao")
	if len(list) != 1 {
		t.Errorf("软删后列表长度 = %d, want 1", len(list))
	}
	if _, err := s.Characters().GetByName(ctx, "甲"); err != nil {
		t.Errorf("软删后记录仍应可取: %v", err)
	}

	// 删不存在的 id 应报 ErrNotFound
	if err := s.Characters().MarkDeleted(ctx, 999999); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("删不存在 = %v, want ErrNotFound", err)
	}
}

// TestSerializedSize 记录存档体积指标。
//
// 原版 THumData 是**定长** ~3628 字节（Grobal2.pas:891，含 13+46+50 个物品槽与
// 20 个技能槽，无论是否装满）。改用 protobuf 后未填满的角色显著更小，
// 这是换掉定长结构的直接收益之一。
func TestSerializedSize(t *testing.T) {
	empty := &pb.CharacterData{ChrName: "空角色", Abil: &pb.Ability{Level: 1}}
	b1, err := proto.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}

	full := &pb.CharacterData{
		ChrName: "满载角色", Account: "algotao", CurMap: "3", Job: 0, Gold: 2000000000,
		Abil:          &pb.Ability{Level: 500, Exp: 1 << 40, Dc: &pb.MinMax{Min: 100, Max: 200}},
		StatusTime:    make([]uint32, 12),
		QuestFlag:     make([]byte, 128),
		QuestUnit:     make([]byte, 128),
		QuestUnitOpen: make([]byte, 128),
	}
	for i := 0; i < 13; i++ {
		full.HumItems = append(full.HumItems, &pb.UserItem{MakeIndex: int32(i), Index: uint32(i + 1), Dura: 2000, DuraMax: 2000, Value: make([]byte, 14)})
	}
	for i := 0; i < 46; i++ {
		full.BagItems = append(full.BagItems, &pb.UserItem{MakeIndex: int32(100 + i), Index: uint32(i + 1), Dura: 1000, DuraMax: 1000, Value: make([]byte, 14)})
	}
	for i := 0; i < 50; i++ {
		full.StorageItems = append(full.StorageItems, &pb.UserItem{MakeIndex: int32(200 + i), Index: uint32(i + 1), Dura: 500, DuraMax: 500, Value: make([]byte, 14)})
	}
	for i := 0; i < 20; i++ {
		full.Magics = append(full.Magics, &pb.UserMagic{MagicId: uint32(i + 1), Level: 3, TranPoint: 1000})
	}
	b2, err := proto.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("空角色 %d 字节 | 满载角色 %d 字节 | 原版定长 THumData 3628 字节", len(b1), len(b2))
	// 满载时不应超过原版定长结构太多，否则说明有字段被无谓放大
	if len(b2) > 4096 {
		t.Errorf("满载存档 %d 字节，超过原版 3628 字节太多", len(b2))
	}
	if len(b1) > 512 {
		t.Errorf("空角色 %d 字节，proto 未生效？", len(b1))
	}
}

// TestGuildRoundTrip 校验行会存档往返（JSON 序列化 + 索引）。
//
// 行会每次变更都立即落库（原版 UpdateGuildInfoFile 同样立即写盘），
// 这里覆盖 Create → Save → GetByName/List 的完整路径与时间戳字段。
func TestGuildRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	until := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	want := &storage.Guild{
		Name:   "测试行会",
		Notice: []string{"第一条公告", "第二条公告"},
		Allies: []string{"友军行会"},
		Wars:   []storage.GuildWar{{Name: "敌对行会", EndAt: until}},
		Ranks: []storage.GuildRank{
			{No: 1, Name: "行会掌门人", Members: []string{"张三"}},
			{No: 50, Name: "精英", Members: []string{"李四", "王五"}},
			{No: 99, Name: "行会成员", Members: []string{"赵六"}},
		},
		EnableAuthAlly: true,
	}
	if err := s.Guilds().Create(ctx, want); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Guilds().Create(ctx, &storage.Guild{Name: "测试行会"}); !errors.Is(err, storage.ErrExists) {
		t.Errorf("重名建会 = %v, want ErrExists", err)
	}

	got, err := s.Guilds().GetByName(ctx, "测试行会")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if got.Name != want.Name || !got.EnableAuthAlly || len(got.Ranks) != 3 {
		t.Fatalf("读回不符: %+v", got)
	}
	if len(got.Notice) != 2 || got.Notice[1] != "第二条公告" {
		t.Errorf("公告往返不符: %v", got.Notice)
	}
	if len(got.Wars) != 1 || got.Wars[0].Name != "敌对行会" || !got.Wars[0].EndAt.Equal(until) {
		t.Errorf("行会战往返不符: %+v", got.Wars)
	}
	if got.Ranks[1].Members[1] != "王五" {
		t.Errorf("成员往返不符: %+v", got.Ranks[1])
	}

	// Save：加人 + 改公告
	got.Ranks[2].Members = append(got.Ranks[2].Members, "钱七")
	got.Notice = []string{"新公告"}
	if err := s.Guilds().Save(ctx, got); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got2, err := s.Guilds().GetByName(ctx, "测试行会")
	if err != nil {
		t.Fatal(err)
	}
	if len(got2.Ranks[2].Members) != 2 || got2.Notice[0] != "新公告" {
		t.Errorf("Save 后不符: %+v %v", got2.Ranks[2], got2.Notice)
	}

	list, err := s.Guilds().List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Name != "测试行会" {
		t.Errorf("List = %+v", list)
	}

	if err := s.Guilds().Delete(ctx, "测试行会"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Guilds().GetByName(ctx, "测试行会"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("删除后查询 = %v, want ErrNotFound", err)
	}
	if err := s.Guilds().Delete(ctx, "测试行会"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("重复删除 = %v, want ErrNotFound", err)
	}
}

// TestCharacterUpdate 校验存盘路径：改详情 → 索引列同步更新。
func TestCharacterUpdate(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	c := &storage.Character{Data: &pb.CharacterData{
		ChrName: "丙", Account: "algotao", Abil: &pb.Ability{Level: 1}, Gold: 100,
	}}
	c.SyncFromData()
	if err := s.Characters().Create(ctx, c); err != nil {
		t.Fatal(err)
	}

	c.Data.Abil.Level = 50
	c.Data.Gold = 999999
	c.Data.BagItems = append(c.Data.BagItems, &pb.UserItem{MakeIndex: 7, Index: 3, Dura: 1, DuraMax: 1})
	if err := s.Characters().Update(ctx, c); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := s.Characters().GetByName(ctx, "丙")
	if err != nil {
		t.Fatal(err)
	}
	// 索引列必须跟着更新，否则按等级排序会读到旧值
	if got.Level != 50 || got.Gold != 999999 {
		t.Errorf("索引列未同步: level=%d gold=%d", got.Level, got.Gold)
	}
	if len(got.Data.BagItems) != 1 {
		t.Errorf("背包未保存: %d", len(got.Data.BagItems))
	}
}
