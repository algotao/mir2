// Package data 定义静态游戏数据（物品 / 怪物 / 魔法）的内存模型与加载。
//
// 设计决策：静态数据**不进数据库**。总量约 1000 物品 / 705 怪物 / 206 魔法，
// 启动时全量加载进内存，运行期零查询。用版本化的 JSON 放在仓库里，
// 既能 diff/review，又跨平台无差异，还省掉一个外部依赖。
//
// 数据来源：/data/git/OpenMir2/sql/mir2_data.sql（MySQL dump）。
// 字段对照：/data/git/MIR2/GameOfMir/M2Server/LocalDB.pas:250-335（LoadItemsDB）。
package data

// ItemType 是物品种类，由 StdMode 映射而来（LocalDB.pas:313-322）。
type ItemType uint8

const (
	ItemTypeOther     ItemType = 0 // 其它
	ItemTypeWeapon    ItemType = 1 // 武器（StdMode 5,6）
	ItemTypeDress     ItemType = 2 // 衣服（StdMode 10,11）
	ItemTypeAccessory ItemType = 3 // 首饰（StdMode 15,19-24,26,51-54,62-64）
)

// MaxStack 是堆叠上限。
const MaxStack = 100

// stackableModes 是可堆叠物品的 StdMode。
//
// 0=药品 3=卷轴 25=药粉 31=药包 40/41/42=肉/食品/材料 43=矿石。
// 装备（武器/衣服/首饰）不可堆叠。
var stackableModes = map[uint8]bool{
	0: true, 3: true, 25: true, 31: true,
	40: true, 41: true, 42: true, 43: true,
}

// StackLimit 返回物品的堆叠上限，0 表示不可堆叠。
//
// ⚠️ 不用 GeeM2 的 OverLap 列：该列在官方配置里大多是 0/空，
// 按 StdMode 判定更稳，也符合传奇的实际规则。
//
// 对可堆叠物品，UserItem.Dura 表示**数量**而非耐久
// （原版就是这样复用同一个字段）。
func (s *StdItem) StackLimit() uint32 {
	if !stackableModes[s.StdMode] {
		return 0
	}
	return MaxStack
}

// ItemTypeOf 按 StdMode 判定物品种类，规则来自 LocalDB.pas:313-322。
func ItemTypeOf(stdMode uint8) ItemType {
	switch {
	case stdMode == 5 || stdMode == 6:
		return ItemTypeWeapon
	case stdMode == 10 || stdMode == 11:
		return ItemTypeDress
	case stdMode == 15 || stdMode == 19 || stdMode == 20 || stdMode == 21 ||
		stdMode == 22 || stdMode == 23 || stdMode == 24 || stdMode == 26 ||
		(stdMode >= 51 && stdMode <= 54) || (stdMode >= 62 && stdMode <= 64):
		return ItemTypeAccessory
	default:
		return ItemTypeOther
	}
}

// MinMax 表示属性的下限/上限对。
//
// 原版把它们打包进一个 DWord（LoWord=下限，HiWord=上限，见 Common/Grobal2.pas:734-753）。
// Go 侧拆成两个字段——战斗公式里大量 LoWord/HiWord 拆分，拆开后可读性大幅提升。
type MinMax struct {
	Min uint16 `json:"min"`
	Max uint16 `json:"max"`
}

// Pack 还原为原版的 DWord 打包形式（LoWord=Min，HiWord=Max）。
func (m MinMax) Pack() uint32 { return uint32(m.Min) | uint32(m.Max)<<16 }

// UnpackMinMax 从原版 DWord 还原。
func UnpackMinMax(v uint32) MinMax { return MinMax{Min: uint16(v & 0xFFFF), Max: uint16(v >> 16)} }

// StdItem 是物品模板（对应 Delphi TStdItem / TItem）。
//
// 字段顺序与 MySQL `stditems` 表前 24 列一致（Id, Name, StdMode, Shape, Weight,
// AniCount, Source, Reserved, ImgIndex, DuraMax, Ac, AcMax, Mac, MacMax, Dc, DcMax,
// Mc, McMax, Sc, ScMax, Need, NeedLevel, Price, Stock）。
type StdItem struct {
	Index    int32  `json:"index"` // 1-based；GetStdItem 时需先 -1
	Name     string `json:"name"`
	StdMode  uint8  `json:"std_mode"`
	Shape    uint8  `json:"shape"` // 书的类别
	Weight   uint8  `json:"weight"`
	AniCount uint8  `json:"ani_count"`
	Source   int8   `json:"source"` // 武器神圣值（sbyte）
	Reserved uint8  `json:"reserved"`
	Looks    uint16 `json:"looks"` // Items.WIL 图片索引（SQL 列名 ImgIndex）
	DuraMax  uint32 `json:"dura_max"`

	// 原版 AC/MAC/DC/MC/SC 打包在 DWord 的高低 16 位，此处拆开存储。
	// ⚠️ 武器特例（Grobal2.pas:544-547 注释）：AC 高位=准确、低位=幸运；
	//    MAC 高位=速度、低位=诅咒。该语义待 P4 战斗系统确认后单独处理。
	AC  MinMax `json:"ac"`
	MAC MinMax `json:"mac"`
	DC  MinMax `json:"dc"`
	MC  MinMax `json:"mc"`
	SC  MinMax `json:"sc"`

	Need      uint32 `json:"need"` // 0=等级 1=攻击力 2=魔法力 3=精神力
	NeedLevel uint32 `json:"need_level"`
	Price     uint32 `json:"price"`
	Stock     uint32 `json:"stock"`
}

// Type 返回物品种类（由 StdMode 推导）。
func (s *StdItem) Type() ItemType { return ItemTypeOf(s.StdMode) }

// StdItemSet 是物品表，下标 = Index-1。
type StdItemSet struct {
	items  []*StdItem
	byName map[string]*StdItem
}

// NewStdItemSet 建立物品表；Index 必须连续且从 1 开始（LocalDB.pas:313-322 的约束）。
func NewStdItemSet(items []*StdItem) (*StdItemSet, error) {
	s := &StdItemSet{items: items, byName: make(map[string]*StdItem, len(items))}
	for i, it := range items {
		if it.Index != int32(i)+1 {
			return nil, &IndexError{Kind: "stditems", Got: it.Index, Want: int32(i) + 1}
		}
		s.byName[it.Name] = it
	}
	return s, nil
}

// Get 按下标取物品（0-based）。
func (s *StdItemSet) Get(idx int) *StdItem {
	if idx < 0 || idx >= len(s.items) {
		return nil
	}
	return s.items[idx]
}

// GetByName 按名称取物品。
func (s *StdItemSet) GetByName(name string) *StdItem { return s.byName[name] }

// Len 返回物品总数。
func (s *StdItemSet) Len() int { return len(s.items) }

// All 返回全部物品。
func (s *StdItemSet) All() []*StdItem { return s.items }

// IndexError 表示物品/怪物索引不连续。
type IndexError struct {
	Kind      string
	Got, Want int32
}

func (e *IndexError) Error() string {
	return "data: " + e.Kind + " 索引不连续"
}
