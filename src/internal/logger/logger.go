// Package logger 提供基于 zap 的文件日志系统。
// 日志以 JSON 结构化格式写入 ~/.codepilot/logs/codepilot.log，
// 不输出到 stdout，避免干扰 TUI 界面。
// 支持日志轮转（单文件最大 10MB，最多保留 5 个备份），
// 其他包通过 logger.Info() 等包级函数直接调用。
package logger

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	// logFilename 日志文件名
	logFilename = "codepilot.log"
	// maxSizeMB 单个日志文件最大大小（MB）
	maxSizeMB = 10
	// maxBackups 最大备份数量
	maxBackups = 5
)

// globalLogger 全局 zap.Logger 实例，通过包级函数暴露。
var globalLogger *zap.Logger

// globalWriter 全局 lumberjack 写入器，需要在关闭时释放文件句柄。
var globalWriter *lumberjack.Logger

// Init 初始化文件日志系统。
// 日志文件路径为 ~/.codepilot/logs/codepilot.log，
// 目录不存在时自动创建。初始化失败不阻塞主流程。
func Init() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("获取用户主目录失败: %w", err)
	}
	logDir := filepath.Join(homeDir, ".codepilot", "logs")
	return InitFromDir(logDir)
}

// InitFromDir 从指定目录初始化文件日志系统，供测试使用。
func InitFromDir(logDir string) error {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return fmt.Errorf("创建日志目录失败: %w", err)
	}

	logPath := filepath.Join(logDir, logFilename)
	writer := &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    maxSizeMB,
		MaxBackups: maxBackups,
		LocalTime:  true,
	}
	globalWriter = writer
	// 全局 logger 与会话 logger 共用 buildLogger 构造，保证编码格式/级别/caller 配置一致。
	// skip=1：包级 Info/Warn 等透传函数只隔一层到真实调用方。
	globalLogger = buildLogger(writer, 1)
	return nil
}

// buildLogger 用给定 lumberjack writer 构造一个带 caller 信息的 zap.Logger。
//
// 抽出此 helper 供全局 logger（InitFromDir）与会话 logger（OpenSession）共用，
// 保证二者编码格式（ISO8601 时间、Lowercase 级别、JSON）、日志级别（Info）、
// caller 配置完全同源，避免双份配置漂移导致格式不一致。
//
// skip 为 caller 跳层数：包级透传函数（Info/Warn/InfoCtx 等）都只隔一层到真实调用方，
// 故全局与会话统一传 1。
func buildLogger(writer *lumberjack.Logger, skip int) *zap.Logger {
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.TimeKey = "ts"
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	encoderConfig.EncodeLevel = zapcore.LowercaseLevelEncoder
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderConfig),
		zapcore.AddSync(writer),
		zapcore.InfoLevel,
	)
	return zap.New(core, zap.AddCaller(), zap.AddCallerSkip(skip))
}

// Sync 刷新日志缓冲区，在程序退出前调用。
func Sync() {
	if globalLogger != nil {
		_ = globalLogger.Sync()
	}
}

// Close 关闭日志文件句柄，释放资源。
func Close() {
	if globalWriter != nil {
		_ = globalWriter.Close()
	}
}

// L 返回底层 *zap.Logger 实例，便于需要直接传递 logger 的场景
// （如 mcp/adapter.RegisterAll、mcp/config.BuildTransports 等）。
// Init 之前调用 L 会得到 zap.NewNop() 占位（不影响业务行为）。
func L() *zap.Logger {
	if globalLogger == nil {
		return zap.NewNop()
	}
	return globalLogger
}

// Info 记录 Info 级别日志。
func Info(msg string, fields ...zap.Field) {
	if globalLogger != nil {
		globalLogger.Info(msg, fields...)
	}
}

// Debug 记录 Debug 级别日志。
func Debug(msg string, fields ...zap.Field) {
	if globalLogger != nil {
		globalLogger.Debug(msg, fields...)
	}
}

// Warn 记录 Warn 级别日志。
func Warn(msg string, fields ...zap.Field) {
	if globalLogger != nil {
		globalLogger.Warn(msg, fields...)
	}
}

// Error 记录 Error 级别日志。
func Error(msg string, fields ...zap.Field) {
	if globalLogger != nil {
		globalLogger.Error(msg, fields...)
	}
}

// ============================================================================
// 会话级日志（Session-scoped logging）
//
// 全局 logger 把所有项目、所有会话的日志写进同一个文件，混合难读。会话级日志把
// 「核心会话链路」（对话循环 / 工具执行 / 权限拦截 / 上下文压缩 / LLM 请求）产生的
// 日志按 sessionID 路由到各自会话目录下的 codepilot.log，便于按会话排查。
//
// 路由方式：通过 context.Context 携带 sessionID（WithSession），核心链路调用
// InfoCtx/WarnCtx 等便捷函数时，LCtx 从 ctx 取 sessionID 并查会话 logger 缓存；
// ctx 无 sessionID 或对应会话未 OpenSession 时一律回退 globalLogger，绝不丢日志。
// 系统级日志（启动、MCP 初始化、panic 等）继续用包级 Info/Warn/Error 走全局。
//
// ctx key 放在 logger 包而非 session 包：session 包已 import logger（持久化日志），
// 若 ctx helper 放 session 包会让 logger 反向依赖 session，形成循环导入。
// ============================================================================

// sessionLoggerKey 是 context 中携带 sessionID 的私有 key。
// 用空结构体类型作 key 避免与其他包的 ctx 值冲突。
type sessionLoggerKey struct{}

// sessionLogger 是单个会话的独立日志器：持有独立的 zap.Logger 与 lumberjack writer，
// 写入该会话目录下的 codepilot.log。生命周期由 OpenSession/CloseSession 管理。
type sessionLogger struct {
	logger *zap.Logger
	writer *lumberjack.Logger
}

// sessionLoggers 缓存所有已打开的会话 logger：sessionID -> *sessionLogger。
// 用 sync.Map 而非 mutex+map：OpenSession（写）低频，LCtx（读）在每条会话日志时
// 高频触发，sync.Map 对「读多写少」提供无锁读路径，性能更优。
var sessionLoggers sync.Map

// OpenSession 打开（或复用）指定会话的专属日志器，日志写入 <sessionDir>/codepilot.log。
//
// 幂等：同一 sessionID 重复调用直接复用已缓存的 *sessionLogger，不重复创建 writer、
// 不重复打开文件句柄——保证「会话切换时重复 OpenSession」无副作用（便于 resume 复用）。
// 并发安全：用 sync.Map.LoadOrStore 处理并发打开同一 sessionID 的竞态；竞态失败方
// 关闭自己刚创建的 writer，避免句柄泄漏。
//
// sessionDir 由调用方（SessionManager.SessionDir）提供，确保与会话消息目录同址。
// 返回的 error 调用方可忽略——LCtx 会回退全局 logger，不会因日志初始化失败影响业务。
func OpenSession(sessionID, sessionDir string) error {
	if sessionID == "" || sessionDir == "" {
		return nil
	}
	// 已缓存：直接复用，幂等。
	if _, ok := sessionLoggers.Load(sessionID); ok {
		return nil
	}
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return fmt.Errorf("创建会话日志目录失败: %w", err)
	}
	writer := &lumberjack.Logger{
		Filename:   filepath.Join(sessionDir, logFilename),
		MaxSize:    maxSizeMB,
		MaxBackups: maxBackups,
		LocalTime:  true,
	}
	sl := &sessionLogger{
		logger: buildLogger(writer, 1),
		writer: writer,
	}
	// LoadOrStore 处理并发竞态：若别的 goroutine 抢先存入，则丢弃自己新建的 writer。
	if _, loaded := sessionLoggers.LoadOrStore(sessionID, sl); loaded {
		_ = writer.Close()
	}
	return nil
}

// CloseSession 关闭并移除指定会话的日志器，释放其文件句柄。sessionID 不存在时 no-op。
// 通常无需在会话切换时调用（OpenSession 幂等复用，便于 resume 历史会话）；
// 主要供进程退出（CloseAllSessions）或显式资源回收使用。
func CloseSession(sessionID string) {
	v, ok := sessionLoggers.LoadAndDelete(sessionID)
	if !ok {
		return
	}
	if sl, ok := v.(*sessionLogger); ok {
		_ = sl.logger.Sync()
		_ = sl.writer.Close()
	}
}

// CloseAllSessions 关闭所有已打开的会话日志器，在进程退出时调用（main.go defer）。
// 每个会话 logger 先 Sync 刷盘再 Close 释放句柄。
func CloseAllSessions() {
	sessionLoggers.Range(func(k, v any) bool {
		sessionLoggers.Delete(k)
		if sl, ok := v.(*sessionLogger); ok {
			_ = sl.logger.Sync()
			_ = sl.writer.Close()
		}
		return true
	})
}

// WithSession 返回携带 sessionID 的派生 context，供下游 LCtx 据此路由到会话日志器。
// 范式与 tool.WithToolUseID 一致。sessionID 为空时原样返回 ctx（防御）。
func WithSession(ctx context.Context, sessionID string) context.Context {
	if sessionID == "" {
		return ctx
	}
	return context.WithValue(ctx, sessionLoggerKey{}, sessionID)
}

// SessionIDFromContext 从 ctx 中取出 WithSession 注入的 sessionID。
// 返回 (id, ok)：ctx 为 nil、无值、类型不符或空字符串时 ok=false。
func SessionIDFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	v := ctx.Value(sessionLoggerKey{})
	id, ok := v.(string)
	if !ok || id == "" {
		return "", false
	}
	return id, true
}

// LCtx 按 ctx 路由返回应使用的 zap.Logger：ctx 携带 sessionID 且该会话已 OpenSession
// 时返回会话日志器；否则回退 globalLogger（未初始化时返回 no-op logger）。
// 回退语义保证任何情况下都不返回 nil、不 panic、不丢日志。
func LCtx(ctx context.Context) *zap.Logger {
	if id, ok := SessionIDFromContext(ctx); ok {
		if v, ok := sessionLoggers.Load(id); ok {
			if sl, ok := v.(*sessionLogger); ok && sl.logger != nil {
				return sl.logger
			}
		}
	}
	if globalLogger != nil {
		return globalLogger
	}
	return zap.NewNop()
}

// InfoCtx 记录 Info 级别日志，按 ctx 路由到会话或全局日志器。
func InfoCtx(ctx context.Context, msg string, fields ...zap.Field) {
	LCtx(ctx).Info(msg, fields...)
}

// DebugCtx 记录 Debug 级别日志，按 ctx 路由到会话或全局日志器。
func DebugCtx(ctx context.Context, msg string, fields ...zap.Field) {
	LCtx(ctx).Debug(msg, fields...)
}

// WarnCtx 记录 Warn 级别日志，按 ctx 路由到会话或全局日志器。
func WarnCtx(ctx context.Context, msg string, fields ...zap.Field) {
	LCtx(ctx).Warn(msg, fields...)
}

// ErrorCtx 记录 Error 级别日志，按 ctx 路由到会话或全局日志器。
func ErrorCtx(ctx context.Context, msg string, fields ...zap.Field) {
	LCtx(ctx).Error(msg, fields...)
}
