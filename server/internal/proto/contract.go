// 客户端 ↔ 服务端之间的**契约常量**。
//
// 为什么放在 proto 包：这些值不是"服务端内部的调优参数"，而是**两边必须一致**的约定 ——
// 客户端会直接把它们当索引用（槽位）、当编号规则用（ActorId 区间）、当包长用（技能列表）。
// 之前它们散在 cmd/gamesvr 的各个文件里、`cmd/mir2cli` 里还各自抄了一份（ID 基址），
// 一旦哪边改了另一边不会报错，只会在线上表现为"怪打不着/物品放错槽"。
package proto

// 装备槽位索引（原版 `Grobal2.pas:29-38` 的 `U_*`）。
//
// ⚠️ 客户端在 `CM_TAKEONITEM` / `CM_TAKEOFFITEM` 里**直接发这些下标**
// （`MakeDefaultMsg(CM_TAKEONITEM, 装备槽位, 背包位置, …)`），所以编号必须与原版逐字一致。
// 注意 3=项链、4=头盔 —— 与某些版本的写法相反，以本工程源码为准。
const (
	SlotDress     = 0  // U_DRESS      衣服
	SlotWeapon    = 1  // U_WEAPON     武器
	SlotRightHand = 2  // U_RIGHTHAND  右手（原版有；我们未建模：盾/蜡烛类物品不在 1.76 物品表里）
	SlotNecklace  = 3  // U_NECKLACE   项链
	SlotHelmet    = 4  // U_HELMET     头盔
	SlotArmRingL  = 5  // U_ARMRINGL   左手镯
	SlotArmRingR  = 6  // U_ARMRINGR   右手镯
	SlotRingL     = 7  // U_RINGL      左戒指
	SlotRingR     = 8  // U_RINGR      右戒指
	SlotBujuk     = 9  // U_BUJUK      护身符
	SlotBelt      = 10 // U_BELT       腰带
	SlotBoots     = 11 // U_BOOTS      靴子
	// MaxEquipSlot 是已穿戴装备的格数（原版 `THumItems = array[0..12]`）。
	// ⚠️ 它比上面最大的槽位号大：原版把 12 号位留给"马/坐骑"一类扩展槽。
	MaxEquipSlot = 13
)

// ActorId 的空间划分。
//
// 玩家、怪物、NPC **共用一套 ActorId 空间**（客户端不区分来源，只按 ID 索引对象），
// 所以用区间隔离，避免 ID 撞车导致视野集合串味。
//
// ⚠️ 客户端（`cmd/mir2cli` 的用例）也按这两条线判断"这是怪/NPC 还是玩家"，
// 所以它们是契约、不是服务端内部约定。
const (
	// MonsterIDBase 是怪物 ActorId 的起始值。
	MonsterIDBase = 1_000_000
	// NpcIDBase 是 NPC ActorId 的起始值。
	NpcIDBase = 2_000_000
)

// MagicBodySize 是 `SM_SENDMYMAGIC` body 里**每条技能**占的字节数。
//
// 客户端按 `body / MagicBodySize` 切分技能条目，两边必须同一个值。
const MagicBodySize = 12
