package gamesvr

import (
	"testing"
	"time"
)

// nowRef 是测试用的基准时刻。
var nowRef = time.Date(2026, 10, 3, 12, 0, 0, 0, time.Local)

func TestScriptTimerQueue(t *testing.T) {
	var q scriptTimerQueue

	// 到点的先返回，未到点的留下
	q.add(&scriptTimer{charName: "甲", label: "@a", at: nowRef.Add(-1 * time.Second)})
	q.add(&scriptTimer{charName: "甲", label: "@b", at: nowRef.Add(30 * time.Second)})
	q.add(&scriptTimer{charName: "乙", label: "@c", at: nowRef.Add(-5 * time.Second)})

	due := q.due(nowRef)
	if len(due) != 2 {
		t.Fatalf("到点条目 = %d, 期望 2", len(due))
	}
	if due[0].label != "@a" || due[1].label != "@c" {
		t.Errorf("到点条目 = %q,%q", due[0].label, due[1].label)
	}
	// 到点的被摘走，未到点的留下
	if q.len() != 1 {
		t.Errorf("剩余 = %d, 期望 1", q.len())
	}
	// 未到点的 @b 不该被取出
	if got := q.due(nowRef); len(got) != 0 {
		t.Errorf("未到点却被取出 %d 条", len(got))
	}
	// 仍在队列里，且时间到了才取
	if q.len() != 1 {
		t.Errorf("剩余 = %d, 期望 1", q.len())
	}
	if got := q.due(nowRef.Add(time.Minute)); len(got) != 1 || got[0].label != "@b" {
		t.Errorf("到点后应取出 @b，实际 %+v", got)
	}
}

func TestScriptTimerRemoveByChar(t *testing.T) {
	var q scriptTimerQueue
	q.add(&scriptTimer{charName: "甲", label: "@1", at: nowRef.Add(time.Hour)})
	q.add(&scriptTimer{charName: "甲", label: "@2", at: nowRef.Add(time.Hour)})
	q.add(&scriptTimer{charName: "乙", label: "@3", at: nowRef.Add(time.Hour)})

	if n := q.removeByChar("甲"); n != 2 {
		t.Errorf("清除甲的条目 = %d, 期望 2", n)
	}
	if q.len() != 1 {
		t.Errorf("剩余 = %d, 期望 1（乙的）", q.len())
	}
	if q.due(nowRef.Add(2 * time.Hour))[0].charName != "乙" {
		t.Error("剩下应是乙的条目")
	}
	// 清不存在的角色不报错
	if n := q.removeByChar("丙"); n != 0 {
		t.Errorf("清不存在的角色 = %d, 期望 0", n)
	}
}

func TestScriptTimerDueEmpty(t *testing.T) {
	var q scriptTimerQueue
	if got := q.due(nowRef); len(got) != 0 {
		t.Errorf("空队列到点 = %d, 期望 0", len(got))
	}
	if n := q.removeByChar("甲"); n != 0 {
		t.Errorf("空队列清除 = %d, 期望 0", n)
	}
}
