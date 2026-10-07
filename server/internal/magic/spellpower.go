// Package magic 是**技能威力公式**（逐条对照 Delphi 1.76 原版）。
//
// 这里只放"输入数值 → 输出数值"的纯计算；与玩家/服务器状态打交道的那一层
// （`playerPowerAttr`）留在 cmd 侧的 internal/gamesvr 里。

package magic

import (
	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/delphi"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
)

// TrainDivisor 是等级缩放的分母 `btTrainLv + 1`。
//
// 原版 btTrainLv := 3 是**硬编码**（LocalDB.pas:382），我们用同一个常量
// （MagicMaxLevel = 3），所以除数恒为 4：
//
//	技能 0 级 → 1/4    1 级 → 2/4    2 级 → 3/4    3 级 → 4/4
const TrainDivisor = entity.MagicMaxLevel + 1

// MPow 对齐 MPow（Magic.pas:55-57）。
//
// 注意它取的是 **wPower/wMaxPower**（3 级满级区间），不是 DefPower。
// "0 级威力"由 GetPower 另一头的 btDefPower 项负责——两者是**相加**关系，
// 这一点最容易被误读成"在 Def→Power 之间插值"。
func MPow(info *data.MagicInfo) int {
	if info == nil {
		return 0
	}
	return int(info.Power) + delphi.Random(int(info.MaxPower)-int(info.Power))
}

// GetPower 对齐 GetPower（Magic.pas:59-62）：
//
//	ROUND(nPower / (btTrainLv+1) * (btLevel+1)) + (btDefPower + Random(btDefMaxPower - btDefPower))
//
// ⚠️ ROUND 只包住第一项（第二项本来就是整数，包不包一样，但读原版时别看错）。
func GetPower(nPower int, info *data.MagicInfo, level uint32) int {
	if info == nil {
		return 0
	}
	scale := delphi.Round(float64(nPower) / TrainDivisor * float64(level+1))
	return scale + int(info.DefPower) +
		delphi.Random(int(info.DefMaxPower)-int(info.DefPower))
}

// GetPower13 对齐 GetPower13（Magic.pas:64-71）：
//
//	d10 = nInt/3;  d18 = nInt - d10
//	ROUND(d18/(btTrainLv+1)*(btLevel+1) + d10 + (btDefPower + Random(btDefMax-btDefPower)))
//
// 即 nInt 的 **2/3 按技能等级缩放、1/3 固定**（与 GetPower 的整体缩放不同）。
// 用在幽灵盾/神圣战甲(60)、困魔咒(40)、隐身术(30)、心灵启示(×2+30) 上——
// 这些技能的"强度"是秒数或防御值，不该被技能等级整体缩放。
//
// ⚠️ 这里的 ROUND 包住**整个和**（与 GetPower 不同）。
func GetPower13(nInt int, info *data.MagicInfo, level uint32) int {
	d10 := float64(nInt) / 3.0
	d18 := float64(nInt) - d10
	fixed := float64(0)
	if info != nil {
		fixed = float64(int(info.DefPower) +
			delphi.Random(int(info.DefMaxPower)-int(info.DefPower)))
	}
	return delphi.Round(d18/TrainDivisor*float64(level+1) + d10 + fixed)
}

// SpellPoint 是一次施放消耗的 MP（原版 `GetSpellPoint`）。
//
//	ROUND(wSpell / (btTrainLv + 1) * (btLevel + 1)) + btDefSpell
//
// 交叉验证：OpenMir2 `PlayObject.Base.cs:1369`
// `Round(Magic.Spell / 4.0 * (Level + 1)) + Magic.DefSpell`（/4 就是 btTrainLv+1）。
//
// ⚠️ 我们此前写的是 `Spell + DefSpell*level`——**固定项与等级项搞反了**：
// 原版固定加的是 **DefSpell**，等级缩放的才是 **Spell**。
// 影响面不小（火墙 20→30、魔法盾 20→35、冰咆哮 12→33 是涨；
// 治愈术 7→2、隐身 5→1 是降），不是"数值近似"而是另一条曲线。
func SpellPoint(info *data.MagicInfo, um *pb.UserMagic) uint32 {
	if info == nil || um == nil {
		return 0
	}
	n := delphi.Round(float64(info.Spell)/TrainDivisor*float64(um.Level+1)) + int(info.DefSpell)
	if n <= 0 {
		return 0
	}
	return uint32(n)
}

// GetRPow 对齐 GetRPow（Magic.pas:73-78）：从打包的 [Lo,Hi] 随机取值。
//
// 与 delphi.Random 不同，**这里是闭区间**（Random(Hi-Lo+1)），能取到 Hi。
// 高低相等时直接返回低值，不摇随机。
//
// ⚠️ 入参是 **DWord** 不是 Word：原版签名是 `GetRPow(wInt: Integer)`，
// 用 HiWord/LoWord 拆 32 位（MinMax 打包进 DWord，Grobal2.pas:736）。
// 收 uint16 会把上限整个丢掉，退化成"永远取下限"。
func GetRPow(v uint32) int {
	lo, hi := int(v&0xFFFF), int(v>>16)
	if hi > lo {
		return lo + delphi.Random(hi-lo+1)
	}
	return lo
}

// AttackPower 对齐 TBaseObject.GetAttackPower（ObjBase.pas:2416-2427）的
// **无幸运分支**（m_nLuck <= 0，nPower > 0 时走这里）：
//
//	Result := nBasePower + Random(nPower + 1)
//
// 即"基础值 + [0, nPower] 闭区间随机"。传进来的 spread 就是原版的第二参数，
// 多数技能给 `(HiWord(attr) - LoWord(attr)) + 1`——注意 GetAttackPower 内部
// 又 +1，所以实际随机宽度是 `Hi-Lo+2`（原版就多算这一格，照抄）。
//
// m_nLuck > 0 的"幸运必出上限"分支与 m_nPowerRate 未实现（见下）。
func AttackPower(base, spread int) int {
	if spread < 0 {
		spread = 0
	}
	return base + delphi.Random(spread+1)
}

// MagStruckDamage 对齐 TBaseObject.GetMagStruckDamage（ObjBase.pas:22441-22461）
// 的减伤部分：减的是**受害者自己的 MAC**，且**没有保底 1**——打不动就是 0 伤害。
//
// ⚠️ 别拿 rollDamage（那是物理路 GetHitStruckDamage，吃 AC 且保底 1）。
func MagStruckDamage(mac uint32, damage int) int {
	return max(0, damage-GetRPow(mac))
}

// MagAnimalDiscount 是原版对动物目标 magic 伤害的折扣
// （ObjBase.pas:4576 `if m_btRaceServer >= RC_ANIMAL then nPower := Round(nPower / 1.2)`）。
//
// 折扣发生在 RM_DELAYMAGIC 里、`GetMagStruckDamage > 0` 那个**门禁之后**，
// 所以顺序是：先用未打折的值判"能不能打中"，打折后再进 RM_MAGSTRUCK 扣血。
func MagAnimalDiscount(damage int) int { return delphi.Round(float64(damage) / 1.2) }

// MagUndeadBonus 是雷电术(11) 打不死系时 ×1.5 的加成（Magic.pas:401）。
func MagUndeadBonus(damage int) int { return delphi.Round(float64(damage) * 1.5) }

// ---------- 施法者属性 ----------

// PowerAttr 是每个技能用哪条攻击属性（MinMax 打包的 uint16）。
//
// 原版每个 case 各写各的，**不是**按职业统一：法师系用 MC（魔法攻击），
// 灵魂火符(13) 用 SC（道术）。用错属性会让高道击的法师打不出伤害，反之亦然。
type PowerAttr int

const (
	PowerAttrNone PowerAttr = iota
	PowerAttrMC             // 魔法攻击（法师系全部伤害技能）
	PowerAttrSC             // 道术（灵魂火符、治愈、群体治愈、幽灵盾/神圣战甲）
)

// RCAnimal 是原版 RC_ANIMAL 的种族下界：`m_btRaceServer >= RC_ANIMAL` 判"是动物"。
// 低于它的（RC_NPC(10)..49）是 NPC/守卫等非战斗单位，不吃魔法动物折扣。
const RCAnimal = 50

// IsAnimalTarget 判断目标是否走"魔法伤害对动物打 0.83 折"那条分支。
//
// 原版判的是 m_btRaceServer（对象**类型**），不是怪物模板的 Race 字段。
// 我们没有那个字段映射，用模板 Race 近似：玩家恒为 false，怪物按 Race>=50 判。
// NPC（IsNPC）不参与——它们不在这条伤害链路里。
func IsAnimalTarget(m *entity.Monster) bool {
	if m == nil || m.IsNPC || m.Info == nil {
		return false
	}
	return int(m.Info.Race) >= RCAnimal
}

// AttrOf 返回技能用的攻击属性（Magic.pas:280-600 逐个 case 核对）。
func AttrOf(magicID uint16) PowerAttr {
	switch magicID {
	case 13: // 灵魂火符（道士）
		return PowerAttrSC
	case 1, 5, 9, 10, 11, 22, 23, 24, 33: // 火球 大火球 地狱火 疾光 雷电 火墙 爆裂 雷光 冰咆哮
		return PowerAttrMC
	default:
		return PowerAttrNone
	}
}

// SpellRawPower 复刻原版每个伤害 case 的 nPower 计算：
//
//	GetAttackPower(GetPower(MPow(UserMagic)) + LoWord(attr) * mult,
//	               (HiWord(attr) - LoWord(attr)) * mult + 1)
//
// mult 是原版写死的属性系数（治愈/群体治疗是 SC*2，其余是 1）。
// 返回值就是原版 `nPower` 变量在 RM_DELAYMAGIC 里的初值。
func SpellRawPower(info *data.MagicInfo, level uint32, attr uint32, mult int) int {
	base := GetPower(MPow(info), info, level)
	lo := int(proto.UnpackLo(attr)) * mult
	spread := int(proto.UnpackHi(attr)) - int(proto.UnpackLo(attr))
	return AttackPower(base+lo, spread*mult+1)
}

// HealRawPower 是治愈术(2)/群体治愈术(29) 的 nPower（Magic.pas:308 / 533）。
//
// ⚠️ spread 的乘法位置与伤害技能**不等价**，必须分开写：
// 伤害技能 `SmallInt(Hi-Lo) + 1`（先 +1 再进 GetAttackPower），
// 治愈类 `SmallInt(Hi-Lo) * 2 + 1`（先 ×2 再 +1）。合成一个函数会算错。
func HealRawPower(info *data.MagicInfo, level uint32, sc uint32) int {
	base := GetPower(MPow(info), info, level)
	lo := int(proto.UnpackLo(sc)) * 2
	spread := int(proto.UnpackHi(sc)) - int(proto.UnpackLo(sc))
	return AttackPower(base+lo, spread*2+1)
}
