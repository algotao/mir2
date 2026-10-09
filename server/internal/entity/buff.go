// 增益（Buff）类型与**状态位**。
//
// 为什么放 entity：
//
//   - 增益是**对象自身的状态**（原版散在 `TBaseObject` 的多个布尔字段 + 各类
//     `m_dwXXXTime` 上）；这里只收敛"有哪些增益"这一层，
//     **时长与效果**仍留在 cmd/gamesvr/buff.go（那是服务端的调优值）。
//   - 状态位是 `Object.Status` 的各位，随 `SM_CHARSTATUSCHANGED` 下发，
//     `Object` 又就在本包 ⇒ 掩码和状态字段放一起才自洽。
package entity

// BuffType 增益类型。
type BuffType uint8

const (
	BuffInvisible   BuffType = iota // 隐身术(18)
	BuffMagicShield                 // 魔法盾(31)
	BuffSoulShield                  // 幽灵盾(14)：加魔法防御
	BuffHolyArmor                   // 神圣战甲术(15)：加物理防御
)

// 状态位：`Object.Status` 的各位（32 位，随 SM_CHARSTATUSCHANGED / TCharDesc 下发）。
//
// ⚠️ 位分配**照原版**（`TBaseObject.GetCharStatus`，ObjBase.pas:20074-20088）：
// 每一位对应状态计时器数组的下标 i，取 `$80000000 shr i` —— 即**下标越大位越低**，
// 下标 0 是最高位。客户端 `Grobal2.pas:75-88` 给这些下标起了名字：
//
//	POISON_DECHEALTH      = 0  ⇒ 0x80000000  绿毒
//	POISON_DAMAGEARMOR    = 1  ⇒ 0x40000000  红毒
//	POISON_LOCKSPELL      = 2  ⇒ 0x20000000  封魔
//	POISON_DONTMOVE       = 4  ⇒ 0x08000000  定身
//	POISON_STONE          = 5  ⇒ 0x04000000  石化/麻痹
//	STATE_TRANSPARENT     = 8  ⇒ 0x00800000  隐身（透明）
//	STATE_DEFENCEUP       = 9  ⇒ 0x00400000  防御上升（神圣战甲）
//	STATE_MAGDEFENCEUP    = 10 ⇒ 0x00200000  魔御上升（幽灵盾）
//	STATE_BUBBLEDEFENCEUP = 11 ⇒ 0x00100000  魔法盾（泡泡）
//
// （以前我们自排低位 `1<<0..1<<4`，还把状态塞在 SM_CHARSTATUSCHANGED 的 Series 里
//
//	—— 真客户端读的是 Param/Tag 拼出来的 32 位，那样它拿到的状态**恒为 0**。
//	2026-10-06 的审计 §3.5 就是这条。）
const (
	StatePoisonGreen = 0x80000000 // 绿毒（下标 0）
	StatePoisonRed   = 0x40000000 // 红毒（下标 1）
	StateStone       = 0x04000000 // 石化/麻痹（下标 5）
	StateInvisible   = 0x00800000 // 隐身（下标 8）
	StateHolyArmor   = 0x00400000 // 防御上升（下标 9）
	StateSoulShield  = 0x00200000 // 魔御上升（下标 10）
	StateShield      = 0x00100000 // 魔法盾（下标 11）

	// StateRedName 是**红名**（我们自定的位，原版靠 SM_CHANGENAMECOLOR 单独下发名字颜色）。
	//
	// 为什么借状态位：新协议只有 `status_bits` 这一条通道能把"这人是红名"告诉客户端
	//（见 `protocol/scene.proto` 的说明），而名字颜色本来就是**服务端说了算**
	//（原版 `m_nNameColor` 默认 `clWhite`，服务器可改，见 `DrawScrn.pas` 的画名那段）。
	//
	// ⚠️ 必须与客户端 `mir2_core::world::STATE_RED_NAME` **同值**（0x00008000）。
	StateRedName = 0x00008000
)
