package data

// MagicInfo 是技能模板。
//
// 字段来自 MySQL `magics` 表（Idx, MagID, MagName, EffectType, Effect, Spell,
// Power, MaxPower, DefSpell, DefPower, DefMaxPower, Job, NeedL1, L1Train,
// NeedL2, L2Train, NeedL3, L3Train, Delay, Descr）。
//
// ⚠️ 字段集合与 Delphi `SELECT * FROM Magic` 相同，但**顺序不同**：
// Delphi 是 ... MaxPower, Job, NeedL1..3, L1Train..L3Train, Delay, DefSpell ...
// SQL 里 DefSpell/DefPower/DefMaxPower 排在 Job 之前。转换时按名字映射，勿按位置。
//
// 对应 Delphi TMagic（Common/Grobal2.pas:633-651）。
type MagicInfo struct {
	Index       int32  `json:"index"`
	MagicID     uint16 `json:"magic_id"` // 技能号，见 internal/proto 的 SKILL_*
	Name        string `json:"name"`
	EffectType  uint8  `json:"effect_type"`
	Effect      uint8  `json:"effect"`
	Spell       uint16 `json:"spell"` // 基础 MP 消耗
	Power       uint16 `json:"power"`
	MaxPower    uint16 `json:"max_power"`
	DefSpell    uint16 `json:"def_spell"` // 附加 MP 消耗
	DefPower    uint16 `json:"def_power"`
	DefMaxPower uint16 `json:"def_max_power"`
	Job         uint8  `json:"job"` // 0=战 1=法 2=道

	// 修炼四档。Delphi TMagic 的 TrainLevel[0..3] / MaxTrain[0..3]
	// （Grobal2.pas:641-642，注释分别是"升级需要的等级"与"锻炼"）。
	//
	// 装载规则已按 LocalDB.pas:374-382 核对（TODO(P4) 关闭）：
	//	TrainLevel[0..2] := NeedL1/NeedL2/NeedL3；TrainLevel[3] := NeedL3
	//	MaxTrain[0..2]  := L1Train/L2Train/L3Train；MaxTrain[3]  := MaxTrain[2]
	//	btTrainLv := 3                                  ← 技能等级上限硬编码为 3
	// 数组第 4 项只是防御性填充（数组声明为 [0..3]），逻辑上只用 [0..2]。
	NeedL1  uint16 `json:"need_l1"`  // TrainLevel[0]：开始积攒该技能修炼点所需人物等级
	L1Train uint32 `json:"l1_train"` // MaxTrain[0]：0 级 → 1 级所需修炼点
	NeedL2  uint16 `json:"need_l2"`  // TrainLevel[1]：1 级 → 2 级所需人物等级
	L2Train uint32 `json:"l2_train"` // MaxTrain[1]
	NeedL3  uint16 `json:"need_l3"`  // TrainLevel[2]：2 级 → 3 级所需人物等级
	L3Train uint32 `json:"l3_train"` // MaxTrain[2]

	Delay int32  `json:"delay"` // 施法间隔（ms）
	Descr string `json:"descr"`
}

// MagicSet 是技能表。按 MagicID 索引（技能号不连续，且 Index≠MagicID）。
//
// ⚠️ 与物品/怪物不同，技能表的 Index **不保证连续**：OpenMir2 的 magics 表里
// index 1..108 是经典技能，158..206 是 1.8+ 的四级/英雄技能，中间有空洞。
// 因此这里不做连续性校验，一律用 map 索引。
type MagicSet struct {
	magics []*MagicInfo
	byID   map[uint16]*MagicInfo
	byName map[string]*MagicInfo
}

// NewMagicSet 建立技能表。
//
// MagicID 重复时保留 Index 较小者——扩展包里"四级召唤神兽"(idx 162) 与
// "四级英雄神兽"(idx 163) 共用 MagicID=71，经典版优先。
func NewMagicSet(ms []*MagicInfo) (*MagicSet, error) {
	s := &MagicSet{
		magics: ms,
		byID:   make(map[uint16]*MagicInfo, len(ms)),
		byName: make(map[string]*MagicInfo, len(ms)),
	}
	for _, m := range ms {
		if old, dup := s.byID[m.MagicID]; dup && old.Index <= m.Index {
			continue
		}
		s.byID[m.MagicID] = m
		s.byName[m.Name] = m
	}
	return s, nil
}

// GetByID 按技能号取（SKILL_FIREBALL=1 等）。
func (s *MagicSet) GetByID(id uint16) *MagicInfo { return s.byID[id] }

// GetByName 按技能名取。
func (s *MagicSet) GetByName(name string) *MagicInfo { return s.byName[name] }

// Get 按下标取（0-based）。
func (s *MagicSet) Get(idx int) *MagicInfo {
	if idx < 0 || idx >= len(s.magics) {
		return nil
	}
	return s.magics[idx]
}

// Len 返回总数。
func (s *MagicSet) Len() int { return len(s.magics) }

// All 返回全部。
func (s *MagicSet) All() []*MagicInfo { return s.magics }
