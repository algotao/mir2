package gamesvr

// NPC 脚本引擎接线：#if 条件求值 + P 变量 + P0 动作指令。
//
// 指令集以 OpenMir2 的 ScriptEngine（ExecutionCode.cs 380 条 /
// ConditionCode.cs 290 条）为蓝本——它与 Delphi 的 sSC_/nSC_ 命令表
// 一一对应，是忠实移植。**不采用** Crystal 的 NPCActions.cs/NPCChecks.cs：
// 那份自创了 Conquest（征服）/Hero（英雄）/GT（守卫）/Mail（邮件）/
// Buff 五大子系统，1.76 原版里根本不存在，照抄会引入一堆假功能。
//
// 本轮只实现 1.76 官方脚本（Envir/market_def，448 个文件）里
// **按文件数统计的高频指令**：
//
//	MAPMOVE(130+) MAP(61) BREAK(59) TAKE(51) GIVE(46) RANDOM(46)
//	CHECKITEM(38) CHECKGOLD(23) CHECKLEVEL(14) MOV(8) SENDMSG(3)
//
// 数据来自调研时的 grep 统计（按"含该指令的文件数"）。

import (
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"

	"github.com/algotao/mir2/server/internal/script"
	"time"
)

// scriptCtx 把 Player 适配成 script.Context。
type scriptCtx struct {
	s *Server
	p *Player
	// vars 是 P 变量 n1..n99（索引 0..98）。
	vars [99]int64
}

func newScriptCtx(s *Server, p *Player) *scriptCtx {
	return &scriptCtx{s: s, p: p}
}

// GameTimeName 返回当前昼夜相位名，供脚本条件 `DAYTIME` 用（见 daynight.go）。
func (c *scriptCtx) GameTimeName() string {
	return gameTimeName(gameTimePhase(time.Now()))
}

func (c *scriptCtx) Gold() int64 {
	if c.p.Char == nil || c.p.Char.Data == nil {
		return 0
	}
	return c.p.gold() // 持锁读（脚本条件里的 $GOLD 之类）
}

func (c *scriptCtx) Level() int32 {
	if c.p.Char == nil || c.p.Char.Data == nil || c.p.Char.Data.Abil == nil {
		return 1
	}
	return int32(c.p.level())
}

func (c *scriptCtx) Job() int32 {
	if c.p.Char == nil || c.p.Char.Data == nil {
		return 0
	}
	return int32(c.p.Char.Data.Job)
}

func (c *scriptCtx) MapName() string {
	if c.p.Obj == nil || c.p.Obj.MapRef() == nil {
		return ""
	}
	return c.p.Obj.MapRef().Name
}

func (c *scriptCtx) PKPoint() int32 {
	if c.p.Char == nil || c.p.Char.Data == nil {
		return 0
	}
	return int32(c.p.Char.Data.PkPoint)
}

func (c *scriptCtx) CountItem(name string) int {
	if c.p.Char == nil || c.p.Char.Data == nil {
		return 0
	}
	n := 0
	for _, ui := range c.p.Char.Data.BagItems {
		if ui == nil || ui.Index == 0 {
			continue
		}
		if it := c.s.data.tables.Items.Get(int(ui.Index) - 1); it != nil && it.Name == name {
			// 可堆叠物品的 Dura 是数量
			if it.StackLimit() > 0 {
				n += int(ui.Dura)
			} else {
				n++
			}
		}
	}
	return n
}

// GuildName 返回所在行会名（供城堡类条件用）。
func (c *scriptCtx) GuildName() string { return c.s.guildNameOf(c.p) }

func (c *scriptCtx) Var(n int) int64 {
	if n < 0 || n >= len(c.vars) {
		return 0
	}
	return c.vars[n]
}

func (c *scriptCtx) SetVar(n int, v int64) {
	if n < 0 || n >= len(c.vars) {
		return
	}
	c.vars[n] = v
}

func (c *scriptCtx) CheckSkill(name string) bool {
	if c.p.Char == nil || c.p.Char.Data == nil {
		return false
	}
	for _, m := range c.p.Char.Data.Magics {
		if m == nil {
			continue
		}
		if it := c.s.data.tables.Magics.GetByID(uint16(m.MagicId)); it != nil && it.Name == name {
			return true
		}
	}
	return false
}

// evalLabelConds 求值一段的 #if 条件（走完整判定，含第二批与城堡条件）。
func (s *Server) evalLabelConds(p *Player, l *script.Label) bool {
	return s.evalLabelCondsFull(p, l)
}

// ---------- P0 动作指令的实现 ----------

// actMapMove 是 MAP / MAPMOVE 的共同实现。
//
// 官方脚本两种都高频：MAPMOVE(130+ 文件) 与 MAP(61 文件)。
// 不带坐标时用该图出生点/中心（与 GM 的 @map 同口径）。
func (s *Server) actMapMove(c net.Conn, p *Player, args []string) {
	if len(args) == 0 {
		return
	}
	mapID := args[0]
	x, y := -1, -1
	if len(args) >= 3 {
		x, _ = strconv.Atoi(args[1])
		y, _ = strconv.Atoi(args[2])
	}
	if x < 0 || y < 0 {
		if sp := s.startPointOf(mapID); sp != nil {
			x, y = sp.X, sp.Y
		} else if mp, err := s.world.maps.Get(mapID); err == nil {
			x, y = mp.Width()/2, mp.Height()/2
		} else {
			s.sysMsg(c, "传送失败: 地图 "+mapID+" 不存在")
			return
		}
	}
	if err := s.switchMap(c, p, mapID, x, y); err != nil {
		log.Printf("脚本传送 %s 失败: %v", mapID, err)
	}
}

// varIndex 解析脚本变量名 → 索引。非法返回 -1。
//
// n1..n99 与 S1..S99 都认（QFunction-0.txt 用的是 S 前缀），
// 详见 internal/script.parseVarIndex 的说明。
func varIndex(name string) int {
	if i, ok := script.ParseVarIndex(name); ok {
		return i
	}
	return -1
}

// actSetVar 实现 MOV / SET：把值写进 P 变量。
//
// 值可以是字面量、P 变量（n2）、算式（n2+1）、`<$STR(...)>` 插值——
// QFunction-0.txt 里这四种都用。解析复用 script 包的算式求值。
func (s *Server) actSetVar(p *Player, name, value string) {
	idx := varIndex(name)
	if idx < 0 {
		return
	}
	ctx := newScriptCtx(s, p)
	if v, ok := script.ParseValue(value, ctx); ok {
		ctx.SetVar(idx, v)
	}
}

// actIncrVar 实现 INC / DEC。
func (s *Server) actIncrVar(p *Player, name string, delta int64) {
	idx := varIndex(name)
	if idx < 0 {
		return
	}
	ctx := newScriptCtx(s, p)
	ctx.SetVar(idx, ctx.Var(idx)+delta)
}

// actSetHP 实现 HUMANHP / HUMANMP：直接设当前值。
//
// 原版 HUMANHP 是"设为指定值"（ActionOfHumanHP，ObjNpc.pas:9367），
// 传 0 就是清空。同时支持 `HUMANHP +100` 的增量写法（部分脚本在用）。
func (s *Server) actSetHP(c net.Conn, p *Player, args []string, isHP bool) {
	if len(args) == 0 || p.Char.Data == nil || p.Char.Data.Abil == nil {
		return
	}
	raw := strings.TrimSpace(args[0])
	rel := strings.HasPrefix(raw, "+") || strings.HasPrefix(raw, "-")
	v, err := strconv.Atoi(raw)
	if err != nil {
		return
	}
	abil := p.Char.Data.Abil
	if isHP {
		if rel {
			abil.Hp = clampHP(int(abil.Hp)+v, int(abil.MaxHp))
		} else {
			abil.Hp = clampHP(v, int(abil.MaxHp))
		}
	} else {
		if rel {
			abil.Mp = clampHP(int(abil.Mp)+v, int(abil.MaxMp))
		} else {
			abil.Mp = clampHP(v, int(abil.MaxMp))
		}
	}
	s.sendHealthChanged(p, p.Obj.ID, abil.Hp, abil.Mp, abil.MaxHp)
	s.sysMsg(c, fmt.Sprintf("HP=%d MP=%d", abil.Hp, abil.Mp))
}

func clampHP(v, max int) uint32 {
	if v < 0 {
		v = 0
	}
	if v > max {
		v = max
	}
	return uint32(v)
}
