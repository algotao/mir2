package netgate

import (
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/proto"
)

// TestGateDecide 守住服务端"门禁间隔"的判定（原版 ObjBase.pas:8760-8800）：
//
//	间隔够          → 立即放行，并**清零**连续超速计数
//	间隔不够        → 算出还差多少，延时投递
//	差得不多(<10ms) → 直接放行（原版 `else dwDelayTime := 0`），**不算超速**
//	连续超速 ≥4 次  → 置告警位（原版 m_dwAttackCount >= 4）
//
// 数值取官方 !setup.txt：HitIntervalTime=520 / MagicHitIntervalTime=450 /
// DropOverSpeed=10 / OverSpeedKickCount=4。
func TestGateDecide(t *testing.T) {
	g := NewGate()
	t0 := time.Now()

	// 第一条：没有历史 ⇒ 立即放行
	if gap, over := g.Decide(proto.CM_HIT, t0, hitIntervalTime); gap != 0 || over {
		t.Fatalf("首条攻击应立刻放行，得到 gap=%v over=%v", gap, over)
	}
	// 紧接着第二条：还差 ~520ms ⇒ 延时，且不算超速（计数 1）
	gap, over := g.Decide(proto.CM_HIT, t0.Add(10*time.Millisecond), hitIntervalTime)
	if gap <= 0 || over {
		t.Fatalf("10ms 后第二条攻击应延时投递，得到 gap=%v over=%v", gap, over)
	}
	if want := hitIntervalTime - 10*time.Millisecond; gap != want {
		t.Errorf("延时 = %v，期望 %v", gap, want)
	}

	// 差得不多：距上次已过 515ms（只差 5ms < DropOverSpeed）⇒ 立即放行
	if gap, over := g.Decide(proto.CM_HIT, t0.Add(515*time.Millisecond), hitIntervalTime); gap != 0 || over {
		t.Errorf("差 5ms 应直接放行，得到 gap=%v over=%v", gap, over)
	}

	// 间隔足够（>520ms）⇒ 放行并清零计数
	g2 := NewGate()
	g2.Decide(proto.CM_HIT, t0, hitIntervalTime)
	g2.Decide(proto.CM_HIT, t0.Add(20*time.Millisecond), hitIntervalTime) // 计数 1
	g2.Decide(proto.CM_HIT, t0.Add(600*time.Millisecond), hitIntervalTime)
	if g2.count[proto.CM_HIT] != 0 {
		t.Errorf("正常间隔后连续超速计数应清零，实际 %d", g2.count[proto.CM_HIT])
	}

	// 连续超速 4 次 ⇒ 第 4 次带告警
	g3 := NewGate()
	g3.Decide(proto.CM_HIT, t0, hitIntervalTime)
	var gotOver bool
	for i := 0; i < OverSpeedKickCount; i++ {
		_, over := g3.Decide(proto.CM_HIT, t0.Add(time.Duration(i+1)*time.Millisecond), hitIntervalTime)
		gotOver = gotOver || over
	}
	if !gotOver {
		t.Errorf("连续超速 %d 次应带告警位", OverSpeedKickCount)
	}

	// 不受门禁的消息号：恒立即放行，且不建状态
	g4 := NewGate()
	for i := 0; i < 5; i++ {
		if gap, over := g4.Decide(proto.CM_SAY, t0, 0); gap != 0 || over {
			t.Errorf("说话不受门禁，得到 gap=%v over=%v", gap, over)
		}
	}
	if len(g4.tick) != 0 {
		t.Errorf("不受门禁的消息不该留下门禁状态，实际 %d 条", len(g4.tick))
	}

	// 施法走 MagicHitIntervalTime=450
	g5 := NewGate()
	g5.Decide(proto.CM_SPELL, t0, magicHitInterval)
	gap, _ = g5.Decide(proto.CM_SPELL, t0.Add(100*time.Millisecond), magicHitInterval)
	if want := magicHitInterval - 100*time.Millisecond; gap != want {
		t.Errorf("施法延时 = %v，期望 %v（MagicHitIntervalTime=450）", gap, want)
	}
}

// TestGateIntervalOf 固定住门禁覆盖范围（原版只在攻击族与施法上有间隔判定）。
func TestGateIntervalOf(t *testing.T) {
	for _, id := range []uint16{
		proto.CM_HIT, proto.CM_HEAVYHIT, proto.CM_BIGHIT, proto.CM_POWERHIT,
		proto.CM_LONGHIT, proto.CM_WIDEHIT, proto.CM_FIREHIT,
	} {
		if IntervalOf(id) != hitIntervalTime {
			t.Errorf("消息 %d 应走攻击间隔", id)
		}
	}
	if IntervalOf(proto.CM_SPELL) != magicHitInterval {
		t.Error("施法应走 MagicHitIntervalTime")
	}
	// 走路/说话/交易等不加门禁（各自的实体层/chat.go 已有）
	for _, id := range []uint16{proto.CM_WALK, proto.CM_RUN, proto.CM_TURN, proto.CM_SAY} {
		if IntervalOf(id) != 0 {
			t.Errorf("消息 %d 不该有门禁（会与既有层互相压制）", id)
		}
	}
}
