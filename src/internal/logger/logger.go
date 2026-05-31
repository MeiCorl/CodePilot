// Package logger 提供基于 zap 的文件日志系统。
// 日志以 JSON 结构化格式写入 ~/.codepilot/logs/codepilot.log，
// 不输出到 stdout，避免干扰 TUI 界面。
// 支持日志轮转（单文件最大 10MB，最多保留 5 个备份），
// 其他包通过 logger.Info() 等包级函数直接调用。
package logger

import (
	"fmt"
	"os"
	"path/filepath"

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

	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.TimeKey = "ts"
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	encoderConfig.EncodeLevel = zapcore.LowercaseLevelEncoder

	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderConfig),
		zapcore.AddSync(writer),
		zapcore.InfoLevel,
	)

	globalLogger = zap.New(core, zap.AddCaller(), zap.AddCallerSkip(1))
	return nil
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
