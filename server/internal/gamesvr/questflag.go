package gamesvr

import (
	"fmt"
	"log"
	"net"
	"strconv"

	"github.com/algotao/mir2/server/internal/data"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
)

// 任务旗标（quest flag）与地图的 `NEEDSET_ON/OFF` 进图门。
//
// 旗标本体：官方 `TQuestFlag = array[0..127] of Byte`（Grobal2.pas:832）——
// 每个角色 128 个字节，存在角色档里。我们的 `CharacterData.QuestFlag`（pb 字段 43）
// 一直是**只存不用**（只有存储层测试碰过它）⇒ 这里补上读写与消费点。
//
// 消费点（也就是这个文件存在的理由）：官方 `TBaseObject.EnterAnotherMap`
// （ObjBase.pas:20306-20310）：
//
//	if Envir.Flag.nNEEDSETONFlag >= 0 then
//	  if GetQuestFalgStatus(Envir.Flag.nNEEDSETONFlag) <> Envir.Flag.nNeedONOFF then Exit;
//
// 即"目标地图要求 1 号旗标 = 1（NEEDSET_ON）或 = 0（NEEDSET_OFF）才让进"，
// 不满足就**静默失败**（原版只 Exit，没有提示 —— 我们照样静默，只写日志）。
//
// ⚠️ 原版往旗标里写值有两条路：**任务系统**（`nSET` 动作，
// ObjNpc.pas:7762-7766）与 **GM 命令**（`SETFLAG`，ObjBase.pas:8290-8305）。
// 我们的任务系统按审计 §4 的决定**不做** ⇒ 只做 GM 那条（`@setflag`），
// 所以这条门目前是可手工开关、可测试的，数据侧则**一张 NEEDSET 图都没有**
//（出厂 mapinfo 里 `NEEDSET_ON`/`NEEDSET_OFF` 各 0 处，跟 DECHP 一样是"空转但完整"）。

// questFlagMax 是旗标数量（官方 `array[0..127]`）。
const questFlagMax = 128

// errNeedSetNotMet 是"进图被 NEEDSET 门挡下"。
//
// ⚠️ 原版是**静默失败**（`Exit` 完事，客户端那边就是"没动"）。我们返回错误是
// 为了让日志/上层能区分"门上不去"与"地图加载失败"；调用方（`mapTo`/走路）
// 会把错误文案显示给玩家 ⇒ 这一点比原版多一句话，属已知差异。
var errNeedSetNotMet = fmt.Errorf("该地图需要满足开关条件")

// questFlag 读第 n 号旗标（原版 `TPlayObject.GetQuestFalgStatus` —— 注意官方把
// Flag 拼成了 Falg，我们函数名用正确的拼写）。
//
// 越界或没分配一律当 0（原版数组越界会读到别的字段，不值得复刻）。
func questFlag(d *pb.CharacterData, n int) int {
	if d == nil || n < 0 || n >= questFlagMax || n >= len(d.QuestFlag) {
		return 0
	}
	return int(d.QuestFlag[n])
}

// setQuestFlag 写第 n 号旗标（原版 `SetQuestFlagStatus`）。
func setQuestFlag(d *pb.CharacterData, n, v int) {
	if d == nil || n < 0 || n >= questFlagMax {
		return
	}
	// 角色档里的字段可能还没分配（老档案只有 0 长度）⇒ 按需补齐到 128
	for len(d.QuestFlag) < questFlagMax {
		d.QuestFlag = append(d.QuestFlag, 0)
	}
	d.QuestFlag[n] = byte(v)
}

// needSetAllows 是进入某张图前的"开关位"门（原版 `EnterAnotherMap` 那两句）。
//
// `NEEDSET_ON(n)` ⇒ 要求第 n 号旗标 = 1；`NEEDSET_OFF(n)` ⇒ 要求 = 0。
// 没标这两种的地图一律放行。
func needSetAllows(d *pb.CharacterData, mi *data.MapInfo) bool {
	if mi == nil || mi.NeedSetID < 0 {
		return true
	}
	want := 0
	switch {
	case mi.NeedSetOn:
		want = 1
	case mi.NeedSetOff:
		want = 0
	default:
		return true
	}
	return questFlag(d, mi.NeedSetID) == want
}

// gmSetFlag 复刻原版 `SETFLAG` 命令（ObjBase.pas:8290-8305）：
// 给**指定玩家**设旗标，并回一句 `名字: [n] = ON/OFF`。
//
// ⚠️ 原版命令名来自 `g_GameCommand.SETFLAG.sCmd`（配置在 Command.ini 里，
// 出厂数据没带），这里取 `@setflag`（行为一致即可）。
func (s *Server) gmSetFlag(c net.Conn, p *Player, args []string) {
	if len(args) < 3 {
		s.sysMsg(c, "用法: @setflag <玩家名> <旗标号> <值>")
		return
	}
	target := s.playerByName(args[0])
	if target == nil || target.Char == nil || target.Char.Data == nil {
		s.sysMsg(c, "该玩家不在线")
		return
	}
	flag, err1 := strconv.Atoi(args[1])
	value, err2 := strconv.Atoi(args[2])
	if err1 != nil || err2 != nil || flag < 0 || flag >= questFlagMax {
		s.sysMsg(c, "旗标号要 0..127，值要整数")
		return
	}
	setQuestFlag(target.Char.Data, flag, value)
	// 原版回执：`if GetQuestFalgStatus(nFlag) = 1 then '... [n] = ON' else '... = OFF'`
	state := "OFF"
	if questFlag(target.Char.Data, flag) == 1 {
		state = "ON"
	}
	msg := fmt.Sprintf("%s: [%d] = %s", target.Char.Name, flag, state)
	s.sysMsg(c, msg)
	log.Printf("%s 用 GM 设置 %s 的旗标 %d = %d", p.Char.Name, target.Char.Name, flag, value)
}

// gmGetFlag 是原版那条"查询某玩家旗标"的命令（ObjBase.pas:11762-11780，
// 回 `ShowHumanFlagONMsg/OFFMsg`）。命令名同样来自配置，这里取 `@getflag`。
func (s *Server) gmGetFlag(c net.Conn, p *Player, args []string) {
	if len(args) < 2 {
		s.sysMsg(c, "用法: @getflag <玩家名> <旗标号>")
		return
	}
	target := s.playerByName(args[0])
	if target == nil || target.Char == nil || target.Char.Data == nil {
		s.sysMsg(c, "该玩家不在线")
		return
	}
	flag, err := strconv.Atoi(args[1])
	if err != nil || flag < 0 || flag >= questFlagMax {
		s.sysMsg(c, "旗标号要 0..127")
		return
	}
	if questFlag(target.Char.Data, flag) == 1 {
		s.sysMsg(c, fmt.Sprintf("%s 的旗标 [%d] = ON", target.Char.Name, flag))
		return
	}
	s.sysMsg(c, fmt.Sprintf("%s 的旗标 [%d] = OFF", target.Char.Name, flag))
}
