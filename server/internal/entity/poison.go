package entity

import "time"

// 中毒状态（原版 TBaseObject 的 `m_wStatusTimeArr[]` 与 `m_btGreenPoisoningPoint`，
// 施加点是 ObjBase.pas:22730 `TBaseObject.MakePosion(nType, nTime, nPoint)`）。
//
// 原版把"中毒类型"直接当**状态数组下标**用，并顺带进状态位：
//
//	POISON_DECHEALTH   = 0  绿毒：每 dwPosionDecHealthTime(2500ms) 扣 (Point+1) 血
//	POISON_DAMAGEARMOR = 1  红毒：受伤与掉持久都 ×(nPosionDamagarmor/10)（默认 1.2）
//	POISON_LOCKSPELL   = 2  锁技能（1.76 的施毒术用不到，我们不做）
//	POISON_STONE       = 5  石化/麻痹（我们已有 StoneMode，另见 buff）
//
// `m_nCharStatus := GetCharStatus()` 会把"所有未到期的状态位"拼进
// `SM_CHARSTATUSCHANGED`（ObjBase.pas 的 GetCharStatus：`$80000000 shr i`），
// 客户端据此画中毒的颜色。状态位沿用 `Object.Status`，与隐身/护盾、绿毒/红毒
// **共用同一张位表**（见本包 buff.go 的 `State*`）——
// ⚠️ 服务端实际用的是 `StatePoisonGreen/StatePoisonRed`（第 2、3 位）。
//
// ⚠️ 参数语义容易看反（Delphi 与 OpenMir2 都是这样传的）：
//
//	SendDelayMsg(..., RM_POISON, 中毒类型, nPower, 施法者, ROUND(等级/3 * nPower/AmyOunsulPoint), ...)
//	          → MakePosion(nType, nTime = nPower, nPoint = ROUND(...))
//
// 也就是说 **nTime 是"秒数"（≈40~60），nPoint 才是"每跳伤害"**。
// OpenMir2 的怪物实现直接写成 `MakePosion(PoisonState.DECHEALTH, 60, 3)`
// （60 秒、每跳 3 点），可作旁证（src/M2Server/Monster/Monsters/*.cs）。
const (
	// PoisonDecHealth 绿毒（掉血）。
	PoisonDecHealth = 0
	// PoisonDamageArmor 红毒（受伤放大）。
	PoisonDamageArmor = 1
	// PoisonTypeCount 我们建模的中毒种类数（绿毒/红毒）。
	PoisonTypeCount = 2
)

// PoisonState 是一个对象身上的中毒状态。
type PoisonState struct {
	// Until[t] 是第 t 种中毒的到期时刻（原版 `m_wStatusTimeArr[t]` 按秒递减）。
	Until [PoisonTypeCount]time.Time
	// Point 是绿毒每跳的伤害基数（原版 `m_btGreenPoisoningPoint`，Byte）。
	//
	// ⚠️ 原版是**单个**字段：再中一次毒就覆盖它（跟着最新那次走），
	// 而时长是"取长的"（`if m_wStatusTimeArr[t] < nTime then := nTime`）。
	Point uint32
	// TickAt 是下一次绿毒结算时刻（原版 `m_dwPoisoningTick` 的节拍）。
	TickAt time.Time
	// SrcID 是施毒者的 ActorId（原版 RM_POISON 里 SetLastHiter；死亡时据此记功）。
	SrcID uint32
}

// ---------- 中毒状态（Object.poison 的唯一出口）----------
//
// ⚠️ 跨 goroutine：施毒者是**别人**的 goroutine（打怪/打人时施加），
// 而结算在 ticker（poisonTickLoop）、状态位计算在本人/别人的 goroutine 上。

// PoisonSnapshot 返回中毒状态的**拷贝**（要一次看多个字段时用它）。
func (o *Object) PoisonSnapshot() PoisonState {
	if o == nil {
		return PoisonState{}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.poison
}

// WithPoison 在持锁下读改写中毒状态。
//
// ⚠️ 回调里只能碰 `ps`（不得再调本类型的其它访问器 —— `mu` 不可重入）。
func (o *Object) WithPoison(fn func(ps *PoisonState)) {
	if o == nil || fn == nil {
		return
	}
	o.mu.Lock()
	fn(&o.poison)
	o.mu.Unlock()
}

// PoisonUntil 返回第 t 种中毒的到期时刻。
func (o *Object) PoisonUntil(t int) time.Time {
	if o == nil || t < 0 || t >= PoisonTypeCount {
		return time.Time{}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.poison.Until[t]
}

// PoisonPoint 返回绿毒每跳伤害基数。
func (o *Object) PoisonPoint() uint32 {
	if o == nil {
		return 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.poison.Point
}

// PoisonTickAt 返回下一次绿毒结算时刻。
func (o *Object) PoisonTickAt() time.Time {
	if o == nil {
		return time.Time{}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.poison.TickAt
}

// PoisonSrcID 返回施毒者的 ActorId。
func (o *Object) PoisonSrcID() uint32 {
	if o == nil {
		return 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.poison.SrcID
}

// PoisonActive 返回第 t 种中毒是否仍未到期。
func (o *Object) PoisonActive(t int, now time.Time) bool {
	if o == nil || t < 0 || t >= PoisonTypeCount {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return now.Before(o.poison.Until[t])
}

// AnyPoisonActive 返回是否处于任一中毒状态。
func (o *Object) AnyPoisonActive(now time.Time) bool {
	if o == nil {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for t := 0; t < PoisonTypeCount; t++ {
		if now.Before(o.poison.Until[t]) {
			return true
		}
	}
	return false
}

// PoisonMask 返回中毒在状态位里的掩码（原版 `$80000000 shr t`，只取低 32 位）。
//
// ⚠️ **当前没有调用点**（服务端走的是 cmd/gamesvr/poison.go 的 `poisonMask`）。
// 那一条用的是 `StatePoisonGreen/StatePoisonRed`（状态位的第 2、3 位，
// 见 buff.go），而这里还是"从第 0 位起"的写法 ⇒ 两者**不一致**。
// 本次只做常量搬家（2026-10-06），没有改这段逻辑：要么接线时把 `1 << t` 换成
// `StatePoisonGreen/StatePoisonRed`，要么把它删掉 —— 别照着它去理解线上状态位。
//
// 状态位的整体排法（为什么不用原版最高位）见 buff.go 的注释。
func (o *Object) PoisonMask(now time.Time) int32 {
	if o == nil {
		return 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	var m int32
	for t := 0; t < PoisonTypeCount; t++ {
		if now.Before(o.poison.Until[t]) {
			m |= 1 << t
		}
	}
	return m
}

// ApplyPoison 施加一次中毒（原版 MakePosion 的核心：时长取长、点数覆盖）。
//
// 返回是否"状态位发生了变化"（原版仅在这种情况下才 StatusChanged 广播）。
func (o *Object) ApplyPoison(t int, seconds int, point uint32, srcID uint32, now time.Time) bool {
	if o == nil || t < 0 || t >= PoisonTypeCount {
		return false
	}
	until := now.Add(time.Duration(seconds) * time.Second)
	o.mu.Lock()
	defer o.mu.Unlock()
	wasActive := now.Before(o.poison.Until[t])
	if wasActive {
		// `if m_wStatusTimeArr[t] < nTime then := nTime`：只延长、不缩短
		if o.poison.Until[t].Before(until) {
			o.poison.Until[t] = until
		}
	} else {
		o.poison.Until[t] = until
	}
	// 绿毒的点数跟着最新一次走（原版是无条件覆盖）
	if t == PoisonDecHealth {
		o.poison.Point = point
	}
	o.poison.TickAt = now
	o.poison.SrcID = srcID
	return !wasActive
}

// ClearPoison 清掉某种中毒（解毒术）。返回是否真的清掉了。
func (o *Object) ClearPoison(t int) bool {
	if o == nil || t < 0 || t >= PoisonTypeCount {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.poison.Until[t].IsZero() {
		return false
	}
	o.poison.Until[t] = time.Time{}
	return true
}
