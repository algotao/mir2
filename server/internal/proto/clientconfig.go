package proto

import "encoding/binary"

// ClientConf 是下发给客户端的"服务端配置"（对应 Delphi `TClientConf`）。
//
// 客户端侧定义：`GameOfMir/Client/Grobal2.pas:1664-1686`；服务端初始化字面量：
// `M2Share.pas:2160-2181` 的 `g_Config.ClientConf`。
//
// ⚠️ 字段顺序与类型**必须逐字对齐**：客户端是
// `sBody := DecodeString(sBody); DecodeBuffer(sBody, @ClientConf, SizeOf(ClientConf))`
// —— 把这段字节**直接当记录读**（`ClMain.pas:6364-6365`）。记录**不是 packed**，
// `boolean` 是 1 字节、`word` 落在偶数偏移 ⇒ 一共 **24 字节**：
//
//	偏移 0  boClientCanSet    1  boRunHuman      2  boRunMon       3  boRunNpc
//	偏移 4  boWarRunAll       5  btDieColor      6  wSpellTime(2)  8  wHitIime(2)
//	偏移 10 wItemFlashTime(2) 12 btItemSpeed    13  boCanStartRun 14  boParalyCanRun
//	偏移 15 boParalyCanWalk  16  boParalyCanHit 17  boParalyCanSpell
//	偏移 18 boShowRedHPLable 19  boShowHPNumber 20  boShowJobLevel
//	偏移 21 boDuraAlert     22  boMagicLock    23  boAutoPuckUpItem
type ClientConf struct {
	ClientCanSet   bool   // 客户端是否可以自己改配置（原版恒 True）
	RunHuman       bool   // 该图人能不能"跑"（穿人）
	RunMon         bool   // 该图怪能不能"跑"
	RunNpc         bool   // NPC 能不能"跑"
	WarRunAll      bool   // 攻城战里全体可跑
	DieColor       uint8  // 死亡特效颜色（客户端 _MIN(8, btDieColor)）
	SpellTime      uint16 // 施法间隔（客户端动画用）
	HitTime        uint16 // 攻击间隔（客户端动画用）
	ItemFlashTime  uint16 // 物品闪烁
	ItemSpeed      uint8  // 物品速度
	CanStartRun    bool
	ParalyCanRun   bool // 被麻痹时还能跑（`!setup.txt ParalyCanRun=0`）
	ParalyCanWalk  bool
	ParalyCanHit   bool
	ParalyCanSpell bool
	ShowRedHPLabel bool
	ShowHPNumber   bool
	ShowJobLevel   bool
	DuraAlert      bool
	MagicLock      bool
	AutoPickUpItem bool
}

// ClientConfSize 是 `TClientConf` 的字节数（Delphi 非 packed 的对齐结果，见上表）。
const ClientConfSize = 24

// Bytes 按 Delphi 的内存布局写出这 24 字节（小端）。
func (c ClientConf) Bytes() []byte {
	b := make([]byte, 0, ClientConfSize)
	b = append(b,
		boolByte(c.ClientCanSet), boolByte(c.RunHuman), boolByte(c.RunMon), boolByte(c.RunNpc),
		boolByte(c.WarRunAll), c.DieColor)
	b = binary.LittleEndian.AppendUint16(b, c.SpellTime)
	b = binary.LittleEndian.AppendUint16(b, c.HitTime)
	b = binary.LittleEndian.AppendUint16(b, c.ItemFlashTime)
	b = append(b, c.ItemSpeed,
		boolByte(c.CanStartRun), boolByte(c.ParalyCanRun), boolByte(c.ParalyCanWalk),
		boolByte(c.ParalyCanHit), boolByte(c.ParalyCanSpell),
		boolByte(c.ShowRedHPLabel), boolByte(c.ShowHPNumber), boolByte(c.ShowJobLevel),
		boolByte(c.DuraAlert), boolByte(c.MagicLock), boolByte(c.AutoPickUpItem))
	return b
}

// DecodeClientConf 解析这 24 字节（单测与客户端对拍用）。
func DecodeClientConf(b []byte) (ClientConf, bool) {
	if len(b) < ClientConfSize {
		return ClientConf{}, false
	}
	var c ClientConf
	at := func(i int) bool { return b[i] != 0 }
	c.ClientCanSet, c.RunHuman, c.RunMon, c.RunNpc = at(0), at(1), at(2), at(3)
	c.WarRunAll, c.DieColor = at(4), b[5]
	c.SpellTime = binary.LittleEndian.Uint16(b[6:])
	c.HitTime = binary.LittleEndian.Uint16(b[8:])
	c.ItemFlashTime = binary.LittleEndian.Uint16(b[10:])
	c.ItemSpeed = b[12]
	c.CanStartRun, c.ParalyCanRun, c.ParalyCanWalk = at(13), at(14), at(15)
	c.ParalyCanHit, c.ParalyCanSpell = at(16), at(17)
	c.ShowRedHPLabel, c.ShowHPNumber, c.ShowJobLevel = at(18), at(19), at(20)
	c.DuraAlert, c.MagicLock, c.AutoPickUpItem = at(21), at(22), at(23)
	return c, true
}

func boolByte(v bool) byte {
	if v {
		return 1
	}
	return 0
}
