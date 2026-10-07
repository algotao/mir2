// 游戏模型常量（职业、属性基数、容量上限、技能等级上限）。
//
// 为什么放 entity：这些值描述的是**实体自身的属性与容量**，是服务端与客户端都要用的
// 模型常识，不是某个服务的调优参数。搬出来之前它们散在 cmd/gamesvr 的
// hitpoint.go / main.go / spell.go / pkpenalty.go 里，客户端只能靠魔数或注释对齐。
package entity

// 职业号（原版 `Grobal2.pas` 的 jWarr / jWizard / jTaos）。
//
// 存档里就是 `pb.Ability.Job`，建角与选人界面也按这三个值走。
const (
	JobWarr   = 0
	JobWizard = 1
	JobTaos   = 2
)

// DefHit 是 m_btHitPoint 的基数（M2Share.pas:128 `DEFHIT = 5`）。
//
// 攻击方"打空的加成"（`m_nHitPlus`）也用它 —— 见 cmd/gamesvr/warrskill.go。
const DefHit = 5

// DefSpeed 是 m_btSpeedPoint 的基数（M2Share.pas:129 `DEFSPEED = 15`）。
const DefSpeed = 15

// MaxBagSize 是背包容量（原版 `MAXBAGITEM = 46`）。
const MaxBagSize = 46

// ItemValueLen 是物品实例 `Value` 的字节数（原版 `THumItem.btValue[0..13]` = 14）。
//
// 原名 `btValueMaxLen`（在 cmd/gamesvr/pkpenalty.go）；导出后改名，让人一眼看出
// 它说的是**物品实例的 Value 长度**，而不是某个具体用途（幸运/诅咒位）的长度。
const ItemValueLen = 14

// MagicMaxLevel 是技能等级上限（0..3，原版按 3 档设计）。
const MagicMaxLevel = 3
