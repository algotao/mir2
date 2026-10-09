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
	// Say 是要显示的文本（已去掉续行符、**已抽掉行内选项**）。
	//
	// ⚠️ 它丢掉了"选项在行内的位置"，只适合 legacy 那种"正文 + 底部选项列表"的画法。
	// 要在原位置画行内可点文字，用 [`Label.Lines`]。
	Say string
	// Lines 是**原样的显示行**（去掉行尾续行符，保留行首空白）。
	//
	// 行内的选项标记已经**改写为 `<文字/@序号>`**（序号 1 起，与 `Links` 次序一致）——
	// 客户端据此把「打开」画成可点文字、点了回 `NpcSelect{index}`。
	//
	// 用户 2026-10-09：「交易窗口渲染不对，应该为『打开 交易市场』在一行，其中『打开』可点击」
	// —— 脚本是 ` <打开/@trading> 交易市场\`，就是这一行要原样渲染。
	Lines []string
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
			// 正文：先把行内选项**编号**，再抽出来当显示文本
			//
			// ⚠️ 编号是给客户端用的（见 `Label.Lines`）：脚本里的
			// ` <打开/@trading> 交易市场\` 要渲染成**一行**、其中「打开」可点，
			// 客户端回包时只需要一个序号 ⇒ 把标记改写成 `<打开/@1>`（1 起，
			// 与 `Links` 的次序一致）。
			marked := linkRe.ReplaceAllStringFunc(trimmed, func(m string) string {
				g := linkRe.FindStringSubmatch(m)
				cur.Links = append(cur.Links, Link{Text: g[1], Label: g[2]})
				return fmt.Sprintf("<%s/@%d>", g[1], len(cur.Links))
			})
			// 整行保留（去掉行尾续行符 `\`，**保留行首空白** —— 脚本靠它缩进对齐）
			//
			// ⚠️ **空行也保留**：脚本用 ` \` 这种行当段落间隔（`7Gst-0.txt` 的
			// `[@main]` 里就有一条），官方是照原样换行画的。只丢"连续行符都没有"的空行。
			line := strings.TrimRight(marked, ` \`)
			if line != "" || strings.TrimSpace(marked) != "" {
				cur.Lines = append(cur.Lines, line)
			}
			rest := linkRe.ReplaceAllString(trimmed, "")
			rest = strings.TrimRight(rest, ` \`)
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
