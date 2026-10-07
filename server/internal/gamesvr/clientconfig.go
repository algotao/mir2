package gamesvr

import (
	"net"

	"github.com/algotao/mir2/server/internal/codec"
	"github.com/algotao/mir2/server/internal/proto"
)

// 服务端配置（`SM_SERVERCONFIG=5007`）—— 逐句对照 `TPlayObject.SendServerConfig`
// （ObjBase.pas:16803-16843），客户端侧处理见 `ClMain.pas:6349-6385`。
//
// 这条包以前**只有消息号、没有实现**（proto 里躺着 5007）⇒ 真客户端拿不到
// "该图能不能跑 / 攻击与施法间隔 / 死亡特效颜色"这些参数，mapinfo 的
// `RUNHUMAN`/`RUNMON` 也因此一直没有下游。
//
// 值的三处来源（原版同）：
//
//	① **该图**的 `RUNHUMAN`/`RUNMON`（mapinfo 标记，`m_PEnvir.Flag.boRUNHUMAN`）；
//	② 全局的 `RUNHUMAN`/`RUNMON`/`RunNpc`/`WarDisHumRun`（`g_Config.boXXX`）；
//	③ 其余客户端侧开关：出厂文件里没有 ⇒ 用代码默认值
//	   （`M2Share.pas:2160-2181` 的 `g_Config.ClientConf` 字面量）。
const (
	// 攻击/施法间隔：官方 `!setup.txt:996-997`（`HitIntervalTime=520`、
	// `MagicHitIntervalTime=450`），下发的是 **+500 / +300**
	//（原版 `ClientConf.wHitIime := dwHitIntervalTime + 500`）。
	clientConfHitTime   = 520 + 500
	clientConfSpellTime = 450 + 300

	// 官方 `!setup.txt:303 RunNpc=0`、`:872-875 ParalyCanRun/Walk/Hit/Spell=0`
	//（出厂全部关闭）⇒ 我们照文件取值，不再另设开关。
	clientConfRunNpc       = false
	clientConfWarRunAll    = false
	clientConfParalyCanRun = false
	clientConfParalyWalk   = false
	clientConfParalyHit    = false
	clientConfParalySpell  = false

	// 出厂文件里没有这几项 ⇒ 代码默认值：死亡特效颜色 5、物品闪烁 500、物品速度 25、
	// 显示职业等级 True、耐久提醒 True，其余 False。
	clientConfDieColor      = 5
	clientConfItemFlashTime = 5 * 100
	clientConfItemSpeed     = 25
)

// sendServerConfig 下发服务端配置（原版在 `RM_LOGON` 里调，ObjBase.pas:5622）。
//
// ⚠️ 版本门：原版首行是 `if m_nSoftVersionDateEx = 0 then Exit`（只有上报过版本号的
// "Ex 客户端"才发）—— 那个版本号由 **LoginSrv 的 load-record** 带进 M2Server
// （`FrnEngn.pas:35/216/227` 的 `AddToLoadRcdList`，`GetExVersionNO` 把 >1e8 的部分
// 拆成"Ex 版本"）。我们的登录链在 accountsvc，gamesvr 这一层拿不到它
// ⇒ **在"进入游戏"这一步无条件发**（我们的客户端都是现代客户端）。
// 差异记在 docs/progress §3.31，将来若要做严格版本门，入口是 accountsvc 的登录包。
func (s *Server) sendServerConfig(c net.Conn, p *Player) {
	conf, ok := s.serverConf(p)
	if !ok {
		return
	}
	// ⚠️ 字段打包照原版：`nRecog := MakeLong(MakeWord(nRunHuman, nRunMon),
	// MakeWord(nRunNpc, nWarRunAll))`、`nParam := MakeWord(5, 0)` —— 客户端按
	// `LoByte(LoWord(Recog))` 依次取这 4 个开关（ClMain.pas:6354-6357），
	// `LoByte(Param)` 是死亡特效颜色；正文是 6bit 编码的 24 字节记录。
	recog := int32(b2i(conf.RunHuman) | b2i(conf.RunMon)<<8 | b2i(conf.RunNpc)<<16 | b2i(conf.WarRunAll)<<24)
	body := string(codec.EncodeBuffer(conf.Bytes()))
	s.send(c, proto.SM_SERVERCONFIG, recog, uint16(conf.DieColor), 0, 0, body)
}

// serverConf 按**当前地图**组装一份客户端配置（原版 `SendServerConfig` 里那段赋值）。
// 单测直接断言它，不必去截包。
func (s *Server) serverConf(p *Player) (proto.ClientConf, bool) {
	if p == nil || p.Obj == nil || p.Obj.MapRef() == nil {
		return proto.ClientConf{}, false
	}
	mi := s.mapFlagOf(p.Obj.MapRef())
	return proto.ClientConf{
		ClientCanSet:   true,
		RunHuman:       mi != nil && mi.RunHuman,
		RunMon:         mi != nil && mi.RunMon,
		RunNpc:         clientConfRunNpc,
		WarRunAll:      clientConfWarRunAll,
		DieColor:       clientConfDieColor,
		SpellTime:      clientConfSpellTime,
		HitTime:        clientConfHitTime,
		ItemFlashTime:  clientConfItemFlashTime,
		ItemSpeed:      clientConfItemSpeed,
		CanStartRun:    false,
		ParalyCanRun:   clientConfParalyCanRun,
		ParalyCanWalk:  clientConfParalyWalk,
		ParalyCanHit:   clientConfParalyHit,
		ParalyCanSpell: clientConfParalySpell,
		ShowJobLevel:   true,
		DuraAlert:      true,
	}, true
}

func b2i(v bool) int32 {
	if v {
		return 1
	}
	return 0
}
