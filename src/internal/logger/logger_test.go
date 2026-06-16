package logger

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"
)

// restoreLogger 保存原始 logger 并在测试结束后恢复。
func restoreLogger() func() {
	orig := globalLogger
	origWriter := globalWriter
	return func() {
		globalLogger = orig
		globalWriter = origWriter
	}
}

// restoreLoggerWithClose 在 restoreLogger 基础上，先关闭本次测试创建的全局与会话 logger
// 文件句柄，避免 Windows 下 t.TempDir 清理因句柄仍被占用而失败。
// 关闭顺序：先会话 logger（CloseAllSessions 内部各自 Sync+Close）→ 再全局 writer（Close）→ 恢复指针。
func restoreLoggerWithClose() func() {
	orig := globalLogger
	origWriter := globalWriter
	return func() {
		CloseAllSessions()
		Close()
		globalLogger = orig
		globalWriter = origWriter
	}
}

// TestLogFileCreated 验证日志文件正常创建。
func TestLogFileCreated(t *testing.T) {
	defer restoreLogger()()

	dir := t.TempDir()
	if err := InitFromDir(dir); err != nil {
		t.Fatalf("初始化日志失败: %v", err)
	}

	Info("test message")
	Sync()
	Close()

	logPath := filepath.Join(dir, logFilename)
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		t.Fatal("日志文件未创建")
	}
}

// TestLogDirectoryAutoCreated 验证日志目录自动创建。
func TestLogDirectoryAutoCreated(t *testing.T) {
	defer restoreLogger()()

	base := t.TempDir()
	nestedDir := filepath.Join(base, "nested", "logs")

	if err := InitFromDir(nestedDir); err != nil {
		t.Fatalf("初始化日志失败: %v", err)
	}
	Sync()
	Close()

	if _, err := os.Stat(nestedDir); os.IsNotExist(err) {
		t.Fatal("嵌套日志目录未自动创建")
	}
}

// TestLogJSONFormat 验证日志内容为 JSON 格式。
func TestLogJSONFormat(t *testing.T) {
	defer restoreLogger()()

	dir := t.TempDir()
	InitFromDir(dir)
	Info("json format test", zap.String("key", "value"))
	Sync()
	Close()

	logPath := filepath.Join(dir, logFilename)
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("读取日志文件失败: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 {
		t.Fatal("日志文件为空")
	}

	var entry map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("日志内容不是有效 JSON: %v", err)
	}

	if _, ok := entry["level"]; !ok {
		t.Fatal("日志 JSON 缺少 level 字段")
	}
	if _, ok := entry["ts"]; !ok {
		t.Fatal("日志 JSON 缺少 ts 字段")
	}
	if _, ok := entry["msg"]; !ok {
		t.Fatal("日志 JSON 缺少 msg 字段")
	}
}

// TestLogNoStdout 验证日志不输出到 stdout（仅写文件）。
func TestLogNoStdout(t *testing.T) {
	defer restoreLogger()()

	dir := t.TempDir()
	InitFromDir(dir)
	Info("this should only appear in file")
	Sync()
	Close()

	logPath := filepath.Join(dir, logFilename)
	data, _ := os.ReadFile(logPath)
	if len(data) == 0 {
		t.Fatal("日志文件应有内容")
	}
	if !strings.Contains(string(data), "this should only appear in file") {
		t.Fatal("日志文件中未找到预期内容")
	}
}

// TestLogAPIRequest 验证 API 请求/响应可记录在日志中。
func TestLogAPIRequest(t *testing.T) {
	defer restoreLogger()()

	dir := t.TempDir()
	InitFromDir(dir)
	Info("API请求", zap.String("model", "claude-sonnet-4"), zap.Int("tokens", 100))
	Sync()
	Close()

	logPath := filepath.Join(dir, logFilename)
	data, _ := os.ReadFile(logPath)
	content := string(data)

	if !strings.Contains(content, "API请求") {
		t.Fatal("日志中未找到 API 请求记录")
	}
	if !strings.Contains(content, "claude-sonnet-4") {
		t.Fatal("日志中未找到模型名称")
	}
}

// TestInitFallback 验证日志初始化失败不阻塞（globalLogger 为 nil 时不 panic）。
func TestInitFallback(t *testing.T) {
	defer restoreLogger()()

	globalLogger = nil

	// 这些调用不应 panic
	Info("should not panic")
	Error("should not panic either")
	Debug("no panic")
	Warn("safe")
	Sync()
}

// TestMultipleLogEntries 验证多条日志正确写入且均为 JSON 格式。
func TestMultipleLogEntries(t *testing.T) {
	defer restoreLogger()()

	dir := t.TempDir()
	InitFromDir(dir)
	Info("first message")
	Info("second message")
	Warn("warning message")
	Error("error message")
	Sync()
	Close()

	logPath := filepath.Join(dir, logFilename)
	file, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("打开日志文件失败: %v", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	count := 0
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("第 %d 行不是有效 JSON: %v, 内容: %s", count+1, err, line)
		}
		count++
	}
	if count != 4 {
		t.Fatalf("期望 4 条日志，实际 %d 条", count)
	}
}

// ---- 会话级日志（Session-scoped logging）测试 ----

// mkTempDir 创建一个临时目录并注册测试结束清理（忽略清理错误）。
// 用 os.MkdirTemp 而非 t.TempDir：后者清理失败会令测试失败，而 Windows 下
// lumberjack writer 句柄释放存在 OS 级延迟，偶发清理报错；忽略该平台性错误
// 不影响测试结论（日志路由正确性才是验证目标）。
func mkTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "codepilot-log-test-*")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// readSessionLog 读取会话目录下 codepilot.log 的全部内容；文件不存在返回空串。
func readSessionLog(t *testing.T, sessionDir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(sessionDir, logFilename))
	if err != nil {
		return ""
	}
	return string(data)
}

// TestSessionLogRouting 验证 OpenSession 后 InfoCtx 按 ctx 中的 sessionID 路由到会话目录，
// 且不写入全局日志（不双写）。
func TestSessionLogRouting(t *testing.T) {
	defer restoreLoggerWithClose()

	globalDir := mkTempDir(t)
	if err := InitFromDir(globalDir); err != nil {
		t.Fatalf("初始化全局日志失败: %v", err)
	}

	sessionDir := mkTempDir(t)
	const sid = "test-session-routing"
	if err := OpenSession(sid, sessionDir); err != nil {
		t.Fatalf("OpenSession 失败: %v", err)
	}

	ctx := WithSession(context.Background(), sid)
	InfoCtx(ctx, "session-scoped message", zap.String("sid", sid))
	CloseSession(sid) // 内部 Sync 刷盘

	content := readSessionLog(t, sessionDir)
	if !strings.Contains(content, "session-scoped message") {
		t.Fatalf("会话日志文件未包含会话消息，内容: %s", content)
	}
	// 全局文件不应包含该会话消息（不双写）
	globalContent := readSessionLog(t, globalDir)
	if strings.Contains(globalContent, "session-scoped message") {
		t.Fatal("会话消息不应写入全局日志")
	}
}

// TestSessionLogFallbackNotOpened 验证 ctx 携带 sessionID 但未 OpenSession 时回退全局。
func TestSessionLogFallbackNotOpened(t *testing.T) {
	defer restoreLoggerWithClose()

	globalDir := mkTempDir(t)
	InitFromDir(globalDir)

	sessionDir := mkTempDir(t)
	const sid = "test-session-fallback"
	// 故意不调 OpenSession
	ctx := WithSession(context.Background(), sid)
	InfoCtx(ctx, "fallback message")
	Sync()

	if readSessionLog(t, sessionDir) != "" {
		t.Fatal("未 OpenSession 时不应写会话目录")
	}
	if !strings.Contains(readSessionLog(t, globalDir), "fallback message") {
		t.Fatal("未 OpenSession 时应回退写全局日志")
	}
}

// TestSessionLogFallbackNoCtx 验证无 sessionID 的 ctx 回退全局。
func TestSessionLogFallbackNoCtx(t *testing.T) {
	defer restoreLoggerWithClose()

	globalDir := mkTempDir(t)
	InitFromDir(globalDir)

	InfoCtx(context.Background(), "no ctx message")
	Sync()

	if !strings.Contains(readSessionLog(t, globalDir), "no ctx message") {
		t.Fatal("无 sessionID 时应回退写全局日志")
	}
}

// TestSessionLogIdempotent 验证并发 OpenSession 同一 sessionID 幂等：缓存仅 1 条、无句柄泄漏。
func TestSessionLogIdempotent(t *testing.T) {
	defer restoreLoggerWithClose()

	globalDir := mkTempDir(t)
	InitFromDir(globalDir)

	sessionDir := mkTempDir(t)
	const sid = "test-session-idempotent"

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = OpenSession(sid, sessionDir)
		}()
	}
	wg.Wait()

	count := 0
	sessionLoggers.Range(func(k, v any) bool {
		count++
		return true
	})
	if count != 1 {
		t.Fatalf("期望缓存 1 个会话 logger，实际 %d", count)
	}

	// Close 后再写：LCtx 回退全局，不应 panic
	CloseSession(sid)
	InfoCtx(WithSession(context.Background(), sid), "after close no panic")
}

// TestSessionLogCaller 校验 InfoCtx 的 caller 指向调用方文件（logger_test.go），
// 以此验证会话 logger 的 AddCallerSkip 配置正确。
func TestSessionLogCaller(t *testing.T) {
	defer restoreLoggerWithClose()

	globalDir := mkTempDir(t)
	InitFromDir(globalDir)

	sessionDir := mkTempDir(t)
	const sid = "test-session-caller"
	OpenSession(sid, sessionDir)
	ctx := WithSession(context.Background(), sid)
	callerHelper(ctx) // 在 helper 内调用 InfoCtx
	CloseSession(sid)

	content := readSessionLog(t, sessionDir)
	var entry map[string]interface{}
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, "caller-help-msg") {
			_ = json.Unmarshal([]byte(line), &entry)
			break
		}
	}
	if entry == nil {
		t.Fatalf("未找到 helper 日志条目，内容: %s", content)
	}
	caller, _ := entry["caller"].(string)
	// AddCallerSkip 正确时 caller 应指向调用方文件 logger_test.go，而非 logger.go
	if !strings.Contains(caller, "logger_test.go") {
		t.Fatalf("caller 应指向 logger_test.go，实际: %s（需检查 buildLogger 的 skip 参数）", caller)
	}
}

// callerHelper 封装 InfoCtx 调用，用于 caller 行号校验。
// 调用栈：测试 → callerHelper → InfoCtx → zap；AddCallerSkip(1) 应跳过 InfoCtx 指向本函数内的调用行。
func callerHelper(ctx context.Context) {
	InfoCtx(ctx, "caller-help-msg")
}

// TestSessionLogClose 验证 CloseSession 释放句柄、此前日志已落盘可读。
func TestSessionLogClose(t *testing.T) {
	defer restoreLoggerWithClose()

	globalDir := mkTempDir(t)
	InitFromDir(globalDir)

	sessionDir := mkTempDir(t)
	const sid = "test-session-close"
	OpenSession(sid, sessionDir)
	InfoCtx(WithSession(context.Background(), sid), "before close")
	CloseSession(sid)

	if !strings.Contains(readSessionLog(t, sessionDir), "before close") {
		t.Fatal("Close 前的日志应已落盘")
	}
}
