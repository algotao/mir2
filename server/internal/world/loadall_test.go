package world

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestLoadAllMapsCost 测量"全部地图常驻内存"的真实成本。
//
// 用于回答"能不能全加载"这个问题——答案取决于实测数字而非直觉。
// 数据目录不存在时自动跳过。
func TestLoadAllMapsCost(t *testing.T) {
	if _, err := os.Stat(realMapDir); err != nil {
		t.Skipf("跳过：真实地图数据不可用 (%v)", err)
	}

	entries, err := os.ReadDir(realMapDir)
	if err != nil {
		t.Fatalf("列目录: %v", err)
	}

	// 先统计磁盘规模
	var diskTotal, cells int64
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".map" {
			continue
		}
		if fi, err := e.Info(); err == nil {
			diskTotal += fi.Size()
		}
		raw, err := os.ReadFile(filepath.Join(realMapDir, e.Name()))
		if err != nil {
			continue
		}
		m, err := Parse(e.Name(), raw)
		if err != nil {
			continue
		}
		cells += int64(m.Width() * m.Height())
	}

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	start := time.Now()
	// 全量加载并**持有引用**，防止被 GC 回收后测不到
	mm := NewMapManager(realMapDir, 0) // 0 = 不淘汰
	held := make([]*Map, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".map" {
			continue
		}
		// ⚠️ Get 内部会再拼一次 ".map"，这里要先去掉扩展名
		id := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		mp, err := mm.Get(id)
		if err != nil {
			continue
		}
		held = append(held, mp)
	}
	elapsed := time.Since(start)

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	_ = held // 保持引用，防止被 GC 回收导致测不到

	// ⚠️ 用 Sys 增量而非 HeapAlloc 差值：HeapAlloc 可能因 GC 回收读文件的
	// 临时缓冲而变小，uint64 相减会下溢成一个天文数字。
	sys := after.Sys - before.Sys

	t.Logf("地图数=%d 磁盘=%.1fMB 总格数=%d", len(held), float64(diskTotal)/1024/1024, cells)
	t.Logf("全量加载耗时=%v", elapsed)
	t.Logf("常驻内存增量=%.1fMB（Sys 差值）", float64(sys)/1024/1024)
	t.Logf("加载后 HeapAlloc=%.1fMB", float64(after.HeapAlloc)/1024/1024)
	if cells > 0 {
		t.Logf("每格实际占用=%.1f 字节", float64(sys)/float64(cells))
	}
}
