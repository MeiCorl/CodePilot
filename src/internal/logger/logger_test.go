package logger

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
