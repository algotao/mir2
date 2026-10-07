// Package obs 是服务端的**事件日志**（结构化），与人类可读的 log 分开。
//
// 为什么单独一套：
//   - 端到端用例出问题时，最缺的是"服务端到底发生了什么"——
//     走路有没有被限流、攻击落在哪一格、经验差多少才升级、
//     掉落有没有生成、捡取有没有被坐标校验拒绝。
//     这些在 log 的自然语言里要靠猜，在这里是可直接 grep 的字段。
//   - 事件日志是**常开**的（写文件、不掺 stdout），所以可以放心长期跑，
//     出事后整份文件就是时间线。
//
// 用法：
//
//	obs.Init("logs/events.log", obs.LevelInfo, false) // 传空路径则关闭
//	defer obs.Close()
//	obs.Event("attack_hit", "player", p.Char.Name, "monster", id, "dmg", dmg)
//
// 输出为 JSON Lines（每行一个事件），便于 grep / jq / 时间线对齐。
package obs

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// Level 是事件日志级别（与 slog 对齐，取值用 slog 的常量）。
type Level = slog.Level

// 级别别名，避免调用方直接 import log/slog。
const (
	LevelDebug = slog.LevelDebug
	LevelInfo  = slog.LevelInfo
	LevelWarn  = slog.LevelWarn
	LevelError = slog.LevelError
)

var (
	mu      sync.Mutex
	logger  *slog.Logger
	file    *os.File
	enabled bool
)

// Init 打开事件日志文件。path 为空时不启用（Event 会变成空操作）。
//
// append=true 时追加到既有文件，便于重启后仍能看到上一次的时间线。
func Init(path string, level Level, appendTo bool) error {
	mu.Lock()
	defer mu.Unlock()

	if file != nil {
		_ = file.Close()
		file = nil
	}
	logger, enabled = nil, false
	if path == "" {
		return nil
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	flags := os.O_CREATE | os.O_WRONLY
	if appendTo {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return err
	}
	file = f
	// JSON Lines：每行一个事件，便于 grep 与机器解析。
	logger = slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{Level: level}))
	enabled = true
	slog.Info("事件日志已启用", "path", path)
	return nil
}

// Close 关闭事件日志文件。
func Close() error {
	mu.Lock()
	defer mu.Unlock()
	if file == nil {
		return nil
	}
	err := file.Close()
	file, logger, enabled = nil, nil, false
	return err
}

// Enabled 报告事件日志是否已启用。
func Enabled() bool {
	mu.Lock()
	defer mu.Unlock()
	return enabled
}

// Event 记一条事件。attrs 是 key, value 成对的列表（与 slog 相同）。
//
// 未启用时是空操作（调用点可以无条件调用，不必到处判断）。
func Event(name string, attrs ...any) {
	mu.Lock()
	defer mu.Unlock()
	if !enabled || logger == nil {
		return
	}
	if len(attrs)%2 != 0 {
		// 属性必须成对；宁可补一个占位值也不要丢掉整条事件。
		attrs = append(attrs, "malformed_attrs", len(attrs))
	}
	args := make([]any, 0, len(attrs)+1)
	args = append(args, "event", name)
	args = append(args, attrs...)
	logger.Info("", args...)
}

// Writer 返回底层文件（用于测试或特殊场景），未启用时返回 nil。
func Writer() io.Writer {
	mu.Lock()
	defer mu.Unlock()
	if file == nil {
		return nil
	}
	return file
}
