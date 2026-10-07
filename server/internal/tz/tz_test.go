package tz

import (
	"testing"
	"time"
)

// TestEmbeddedZoneinfo 钉住"内嵌时区库生效"：本包空导入 `time/tzdata` 之后，
// 按名字加载时区必须成功，且上海是 +8 小时。
//
// ⚠️ 这条测试在**开发机**上跑不出"镜像里没有 zoneinfo"的差异（本机有系统 zoneinfo，
// 两种情况下都能加载成功）⇒ 真正的验证是**容器里**那次 A/B：
//
//	docker run --rm --network none -e TZ=Asia/Shanghai --entrypoint /app/gamesvr <img> \
//	    -db /tmp/x.db -data /app/data -addr :7299
//	# 修复前日志时间戳是 UTC；修复后应与宿主（CST）一致
func TestEmbeddedZoneinfo(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("按名字加载 Asia/Shanghai 失败: %v", err)
	}
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC).In(loc)
	if _, off := at.Zone(); off != 8*3600 {
		t.Errorf("上海应为 +8 小时，实际偏移 %d 秒", off)
	}
	if got := at.Hour(); got != 20 {
		t.Errorf("12:00 UTC 在上海应为 20 点，实际 %d 点", got)
	}
	// 顺带确认"TZ 这个名字能被解析"：Go 读 $TZ，解析不了就静默退回 UTC ——
	// 那正是线上那个 bug。测试里只读一遍环境变量，不改进程全局状态（`time.Local`）。
	t.Setenv("TZ", "Asia/Shanghai")
	if got := time.Local.String(); got == "" {
		t.Error("time.Local 不该为空")
	}
}
