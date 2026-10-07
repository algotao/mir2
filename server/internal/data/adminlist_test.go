package data

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAdminLevelMapping 守住权限字符 → 等级的反向编码（LocalDB.pas:110-129）。
//
// ⚠️ 这里是**反向**的：`*` 才是最高（10），`9` 最低（1）。写成"字符数字=等级"
// 会让 `*` 变成 0（没权限）、`9` 变成 9（超级管理员）——完全反过来。
func TestAdminLevelMapping(t *testing.T) {
	want := map[byte]uint8{'*': 10, '1': 9, '2': 8, '3': 7, '4': 6, '5': 5, '6': 4, '7': 3, '8': 2, '9': 1}
	for c, lv := range want {
		got, ok := adminLevel(c)
		if !ok || got != lv {
			t.Errorf("adminLevel(%q) = (%d, %v)，期望 (%d, true)", c, got, ok, lv)
		}
	}
	for _, c := range []byte{'/', ' ', '0', 'a', 'A'} {
		if _, ok := adminLevel(c); ok {
			t.Errorf("adminLevel(%q) 不该被接受", c)
		}
	}
}

// TestLoadAdminList 解析官方格式：`* GM01`、带 IP 的、以及各种分隔符。
func TestLoadAdminList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AdminList.txt")
	body := "; 注释行\n" +
		"* GM01\n" + // 官方样例：空格分隔、不限 IP
		"3/战士甲/10.0.0.5\n" + // 斜杠分隔 + IP 白名单（权限 7）
		"5\t法师乙\n" + // 制表符分隔
		"\n" +
		"0 无效行\n" + // 权限字符非法 → 跳过
		"* 无名氏\n" +
		"  \n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	al, err := LoadAdminList(path)
	if err != nil {
		t.Fatalf("LoadAdminList: %v", err)
	}
	if al.Len() != 4 {
		t.Fatalf("名单条数 = %d，期望 4（无效行应被跳过）", al.Len())
	}

	cases := []struct {
		name, ip string
		want     uint8
	}{
		{"GM01", "1.2.3.4:5678", 10}, // 不限 IP，端口要被剥掉
		{"gm01", "1.2.3.4:5678", 10}, // 大小写不敏感（CompareText）
		{"战士甲", "10.0.0.5:1234", 7},  // IP 命中
		{"战士甲", "10.0.0.9:1234", 0},  // IP 不命中 ⇒ 不是 GM
		{"法师乙", "", 5},               // 制表符分隔
		{"路人", "10.0.0.5:1", 0},      // 不在名单
	}
	for _, c := range cases {
		if got := al.LevelFor(c.name, c.ip); got != c.want {
			t.Errorf("LevelFor(%q, %q) = %d，期望 %d", c.name, c.ip, got, c.want)
		}
	}
}

// TestLoadAdminListMissing 文件缺失 = 空名单（谁都不是 GM），不是报错。
func TestLoadAdminListMissing(t *testing.T) {
	al, err := LoadAdminList(filepath.Join(t.TempDir(), "nope.txt"))
	if err != nil {
		t.Fatalf("文件缺失不该报错：%v", err)
	}
	if al.Len() != 0 {
		t.Errorf("条数 = %d，期望 0", al.Len())
	}
	if al.LevelFor("GM01", "") != 0 {
		t.Error("空名单里任何人都该是 0 级权限")
	}
}

// TestLoadOfficialAdminList 用仓库内的官方名单守住解析（`* GM01` … `* GM09`）。
func TestLoadOfficialAdminList(t *testing.T) {
	path := filepath.Join("..", "..", "data", "envir", "AdminList.txt")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("官方 AdminList.txt 不可用（%v），跳过", err)
	}
	al, err := LoadAdminList(path)
	if err != nil {
		t.Fatalf("LoadAdminList(%s): %v", path, err)
	}
	if al.Len() == 0 {
		t.Fatal("官方名单不该是空的")
	}
	// 官方样例是 `* GM01` ⇒ 10 级。
	if got := al.LevelFor("GM01", "127.0.0.1:1"); got != 10 {
		t.Errorf("GM01 权限 = %d，期望 10（`*`）", got)
	}
	t.Logf("官方名单 %d 条", al.Len())
}
