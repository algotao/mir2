package data

// MonsterInfo 是怪物模板。
//
// 字段顺序与 MySQL `monsters` 表前 24 列一致，也对应 Delphi 的
// `SELECT * FROM Monster` 字段（LocalDB.pas:1311-1364）：
// NAME Race RaceImg Appr Lvl Undead CoolEye Exp HP MP AC MAC DC DCMAX MC SC
// SPEED HIT WALK_SPD WalkStep WalkWait ATTACK_SPD
type MonsterInfo struct {
	Index       int32  `json:"index"`
	Name        string `json:"name"`
	Race        uint16 `json:"race"` // 决定 AI 类（见 internal/entity/mon）
	RaceImg     uint16 `json:"race_img"`
	Appr        uint16 `json:"appr"` // 外观
	Level       uint16 `json:"level"`
	Undead      uint8  `json:"undead"`   // LA_UNDEAD=1，圣言术/不死系加成判定
	CoolEye     uint8  `json:"cool_eye"` // 是否识破隐身
	Exp         uint32 `json:"exp"`
	HP          uint32 `json:"hp"`
	MP          uint32 `json:"mp"`
	AC          uint16 `json:"ac"`
	MAC         uint16 `json:"mac"`
	DC          uint16 `json:"dc"`
	DCMax       uint16 `json:"dc_max"`
	MC          uint16 `json:"mc"`
	SC          uint16 `json:"sc"`
	Speed       uint16 `json:"speed"`
	Hit         uint16 `json:"hit"`        // 命中
	WalkSpeed   uint16 `json:"walk_speed"` // LocalDB 下限 200
	WalkStep    uint16 `json:"walk_step"`  // LocalDB 下限 1
	WalkWait    uint16 `json:"walk_wait"`
	AttackSpeed uint16 `json:"attack_speed"` // LocalDB 下限 200
}

// MonsterSet 是怪物模板表，下标 = Index-1。
type MonsterSet struct {
	monsters []*MonsterInfo
	byName   map[string]*MonsterInfo
}

// NewMonsterSet 建立怪物表。
func NewMonsterSet(ms []*MonsterInfo) (*MonsterSet, error) {
	s := &MonsterSet{monsters: ms, byName: make(map[string]*MonsterInfo, len(ms))}
	for i, m := range ms {
		if m.Index != int32(i)+1 {
			return nil, &IndexError{Kind: "monsters", Got: m.Index, Want: int32(i) + 1}
		}
		s.byName[m.Name] = m
	}
	return s, nil
}

// Get 按下标取（0-based）。
func (s *MonsterSet) Get(idx int) *MonsterInfo {
	if idx < 0 || idx >= len(s.monsters) {
		return nil
	}
	return s.monsters[idx]
}

// GetByName 按名称取；刷怪配置 MonGen.txt 里写的是怪物名。
func (s *MonsterSet) GetByName(name string) *MonsterInfo { return s.byName[name] }

// Len 返回总数。
func (s *MonsterSet) Len() int { return len(s.monsters) }

// All 返回全部。
func (s *MonsterSet) All() []*MonsterInfo { return s.monsters }
