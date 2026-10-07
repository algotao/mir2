package data

import (
	"bufio"
	"os"
	"strings"
)

// AdminList 是 `Envir/AdminList.txt`：谁能用 `@` 命令（GM 名单）。
//
// 为什么必须做：此前 gamesvr **完全不校验**权限，任何玩家都能 `@give 屠龙`
// `@level 60` `@spawn` —— 等于人人都是 GM。
//
// 行格式（LocalDB.pas:82-152 `TFrmDB.LoadAdminList`）：
//
//	<权限字符><分隔符><角色名>[<分隔符><IP>]
//
// 分隔符是 `/`、`\`、空格、制表符里的**任意一个**（GetValidStrCap 的字符集）。
// 官方包的样例是 `* GM01` … `* GM09`。
//
// 权限字符 → 等级（LocalDB.pas:110-129）：
//
//	'*'=10  '1'=9  '2'=8  '3'=7  '4'=6  '5'=5  '6'=4  '7'=3  '8'=2  '9'=1
//
// ⚠️ 这是个**反向编码**（`*` 最高 10），别按"1 最大"去理解。
//
// 匹配规则（UsrEngn.pas:2176-2197 `GetAdminInfo`）：按**角色名**比较
// （CompareText ⇒ 不区分大小写）；条目里写了 IP 时还要求登录 IP 相同。
type AdminList struct {
	entries []AdminEntry
}

// AdminEntry 是一条授权。
type AdminEntry struct {
	// Level 是权限等级（1..10；0 表示该行无效）。
	Level uint8
	// Name 是角色名。
	Name string
	// IP 是可选的白名单 IP（空 = 不限）。
	IP string
}

// LoadAdminList 解析 AdminList.txt。文件不存在时返回空名单（不报错）：
// 没有名单 = 谁都不是 GM，这是安全的默认。
func LoadAdminList(path string) (*AdminList, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &AdminList{}, nil
		}
		return nil, err
	}
	defer f.Close()

	out := &AdminList{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		// 权限字符必须在**行首**（原版看的是 sLineText[1]）。
		lv, ok := adminLevel(line[0])
		if !ok {
			continue
		}
		// 权限字符与角色名之间那个分隔符要先剥掉：官方既有 `* GM01`
		//（空格）也有 `3/战士甲/10.0.0.5`（斜杠），不剥的话 cutField 会切出
		// 一个空字段、整行被当成无效行丢掉。
		rest := strings.TrimLeft(line[1:], "/\\ \t")
		name, rest := cutField(rest)
		if name == "" {
			continue
		}
		ip, _ := cutField(rest)
		out.entries = append(out.entries, AdminEntry{Level: lv, Name: name, IP: ip})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Len 返回名单条数。
func (a *AdminList) Len() int {
	if a == nil {
		return 0
	}
	return len(a.entries)
}

// LevelFor 返回该角色名/登录 IP 的权限等级（0 = 不是 GM）。
//
// ip 允许带端口（`127.0.0.1:55754`），比较时会剥掉端口。
func (a *AdminList) LevelFor(name, ip string) uint8 {
	if a == nil {
		return 0
	}
	for _, e := range a.entries {
		if !strings.EqualFold(e.Name, name) {
			continue
		}
		if e.IP != "" && !sameIP(e.IP, ip) {
			continue
		}
		return e.Level
	}
	return 0
}

// Entries 返回名单的副本（供日志/诊断）。
func (a *AdminList) Entries() []AdminEntry {
	if a == nil {
		return nil
	}
	out := make([]AdminEntry, len(a.entries))
	copy(out, a.entries)
	return out
}

// adminLevel 把权限字符翻成等级（LocalDB.pas:110-129 的对照表）。
func adminLevel(c byte) (uint8, bool) {
	switch c {
	case '*':
		return 10, true
	case '1':
		return 9, true
	case '2':
		return 8, true
	case '3':
		return 7, true
	case '4':
		return 6, true
	case '5':
		return 5, true
	case '6':
		return 4, true
	case '7':
		return 3, true
	case '8':
		return 2, true
	case '9':
		return 1, true
	}
	return 0, false
}

// cutField 按原版的分隔符集切出第一个字段，返回 (字段, 剩余)。
func cutField(s string) (string, string) {
	i := strings.IndexAny(s, "/\\ \t")
	if i < 0 {
		return s, ""
	}
	return s[:i], strings.TrimSpace(s[i+1:])
}

// sameIP 比较两个 IP，允许任意一端带端口。
func sameIP(a, b string) bool {
	return hostOnly(a) == hostOnly(b)
}

// hostOnly 剥掉 `host:port` 的端口；IPv6 带方括号的也一并处理。
func hostOnly(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "[") { // [::1]:7000
		if i := strings.Index(s, "]"); i >= 0 {
			return s[1:i]
		}
	}
	if i := strings.LastIndex(s, ":"); i >= 0 && !strings.Contains(s[:i], ":") {
		return s[:i]
	}
	return s
}
