// Package script 解析 1.76 的 NPC 脚本（Envir\market_def\*.txt）。
//
// 文件结构：
//
//	;科目  肉块  , 干肉产品
//	(@trading @buy @sell ...)            ← 该 NPC 支持的命令
//	%100                                 ← 买入价比例
//	+40                                  ← 卖出价比例
//	+1
//
//	[@main]
//	欢迎光临,有什么事情需要我帮忙吗？\ \
//	 \
//	<打开/@trading> 交易市场\
//	<买/@buy>肉\
//	<退出/@exit>
//
//	[@sell]
//	...
//	#if
//	#act
//	mapmove H001 73 67
//
// ⚠️ 行尾的 `\` 是续行符（原版约定），显示时应去掉。
package script

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// Link 是一个可点击选项：`<显示文本/@目标标签>`。
type Link struct {
	Text  string
	Label string
}

// Label 是脚本中的一个段（[@xxx]）。
type Label struct {
	Name string
	// Say 是要显示的文本（已去掉续行符）。
	Say string
	// Links 是本段的选项。
	Links []Link
	// Conds 是 #if 下的条件表达式（暂不求值，仅保留）。
	Conds []string
	// Acts 是 #act 条件成立时执行的指令。
	Acts []string
	// ElseActs 是 #elseact 条件不成立时执行的指令。
	ElseActs []string
}

// Script 是一个 NPC 的完整脚本。
type Script struct {
	// Name 取自文件名（对应 merchant.txt 的 ID）。
	Name string
	// CmdLine 是开头的 (@trading @buy ...) 一行，标明支持的命令。
	CmdLine string
	// Labels 按段名索引。
	Labels map[string]*Label
	// Order 保持段出现的顺序，便于确定入口段。
	Order []string
}

// linkRe 匹配 `<文本/@标签>`。
var linkRe = regexp.MustCompile(`<([^<>/]*)/@([^<>]+)>`)

// ParseFile 解析一个脚本文件。
func ParseFile(path string) (*Script, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	name := strings.TrimSuffix(base(path), ".txt")
	return Parse(name, f)
}

func base(p string) string {
	if i := strings.LastIndexAny(p, `/\\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// Parse 从 reader 解析脚本，name 用作脚本名。
func Parse(name string, r io.Reader) (*Script, error) {
	s := &Script{Name: name, Labels: make(map[string]*Label)}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var cur *Label
	// mode 标记当前处在哪一类指令块中
	mode := ""

	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r\n")

		if strings.HasPrefix(strings.TrimSpace(line), "[") {
			inner := strings.TrimSpace(line)
			inner = strings.TrimPrefix(inner, "[")
			if i := strings.Index(inner, "]"); i >= 0 {
				inner = inner[:i]
			}
			inner = strings.TrimPrefix(strings.TrimSpace(inner), "@")
			l := &Label{Name: inner}
			s.Labels[inner] = l
			s.Order = append(s.Order, inner)
			cur = l
			mode = ""
			continue
		}

		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "(") && strings.HasSuffix(trimmed, ")") {
			s.CmdLine = trimmed
			continue
		}
		if strings.HasPrefix(trimmed, ";") {
			continue // 注释
		}
		switch strings.ToLower(trimmed) {
		case "#if":
			mode = "if"
			continue
		case "#act":
			mode = "act"
			continue
		case "#say":
			mode = "say"
			continue
		case "#elseact":
			mode = "elseact"
			continue
		}

		if cur == nil {
			continue
		}
		if trimmed == "" {
			continue
		}

		switch mode {
		case "if":
			cur.Conds = append(cur.Conds, trimmed)
		case "act":
			cur.Acts = append(cur.Acts, trimmed)
		case "elseact":
			cur.ElseActs = append(cur.ElseActs, trimmed)
		default:
			// 正文：先抽选项，剩下的作为显示文本
			rest := linkRe.ReplaceAllStringFunc(trimmed, func(m string) string {
				g := linkRe.FindStringSubmatch(m)
				cur.Links = append(cur.Links, Link{Text: g[1], Label: g[2]})
				return ""
			})
			rest = strings.TrimRight(rest, ` \`) // 去掉续行符
			rest = strings.TrimSpace(rest)
			if rest != "" {
				if cur.Say != "" {
					cur.Say += "\n"
				}
				cur.Say += rest
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("读脚本 %s: %w", name, err)
	}
	if len(s.Labels) == 0 {
		return nil, fmt.Errorf("脚本 %s 没有任何段", name)
	}
	return s, nil
}

// Entry 返回入口段（[@main]，没有则取第一个段）。
func (s *Script) Entry() *Label {
	if l, ok := s.Labels["main"]; ok {
		return l
	}
	return s.Labels[s.Order[0]]
}

// Label 按名取段。
func (s *Script) Label(name string) *Label {
	return s.Labels[strings.TrimPrefix(name, "@")]
}
