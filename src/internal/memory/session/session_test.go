package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MeiCorl/CodePilot/src/llm"
)

// createTestSession 辅助函数：创建带消息的测试会话并保存。
func createTestSession(t *testing.T, sm *SessionManager, id string, messages []llm.Message) *Session {
	t.Helper()
	s := &Session{
		ID:        id,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Messages:  messages,
	}
	if err := sm.Save(s); err != nil {
		t.Fatalf("保存测试会话失败: %v", err)
	}
	return s
}

// TestSessionCreateNew 验证创建新会话的基本属性。
func TestSessionCreateNew(t *testing.T) {
	sm, err := NewSessionManager()
	if err != nil {
		t.Fatalf("创建 SessionManager 失败: %v", err)
	}

	s := sm.CreateNew()
	if s.ID == "" {
		t.Fatal("新会话 ID 不应为空")
	}
	if s.CreatedAt.IsZero() {
		t.Fatal("新会话 CreatedAt 不应为零值")
	}
	if s.UpdatedAt.IsZero() {
		t.Fatal("新会话 UpdatedAt 不应为零值")
	}
	if len(s.Messages) != 0 {
		t.Fatalf("新会话消息数应为 0，实际为 %d", len(s.Messages))
	}
}

// TestSessionSaveAndLoad 验证会话的保存和加载。
func TestSessionSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	sm := &SessionManager{sessionsDir: dir}

	s := &Session{
		ID:        "test-session-001",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("你好")}},
			{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.NewTextBlock("你好！有什么可以帮你的？")}},
		},
	}

	// 保存
	if err := sm.Save(s); err != nil {
		t.Fatalf("保存会话失败: %v", err)
	}

	// 验证文件存在
	filePath := filepath.Join(dir, "test-session-001.json")
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Fatal("会话文件未创建")
	}

	// 加载
	loaded, err := sm.Load("test-session-001")
	if err != nil {
		t.Fatalf("加载会话失败: %v", err)
	}
	if loaded.ID != s.ID {
		t.Fatalf("会话 ID 不匹配: 期望 %s，实际 %s", s.ID, loaded.ID)
	}
	if len(loaded.Messages) != 2 {
		t.Fatalf("消息数不匹配: 期望 2，实际 %d", len(loaded.Messages))
	}
}

// TestContentBlockSerialization 验证 ContentBlock 的 JSON 序列化/反序列化正确性。
func TestContentBlockSerialization(t *testing.T) {
	msg := llm.Message{
		Role:    llm.RoleUser,
		Content: []llm.ContentBlock{llm.NewTextBlock("hello world")},
	}

	// 序列化
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("序列化消息失败: %v", err)
	}

	// 反序列化
	var loaded llm.Message
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("反序列化消息失败: %v", err)
	}

	if loaded.Role != llm.RoleUser {
		t.Fatalf("角色不匹配: 期望 %s，实际 %s", llm.RoleUser, loaded.Role)
	}
	if len(loaded.Content) != 1 {
		t.Fatalf("ContentBlock 数量不匹配: 期望 1，实际 %d", len(loaded.Content))
	}
	if loaded.Content[0].ToText() != "hello world" {
		t.Fatalf("文本内容不匹配: 期望 'hello world'，实际 '%s'", loaded.Content[0].ToText())
	}
	if loaded.Content[0].Type() != llm.ContentBlockTypeText {
		t.Fatalf("ContentBlock 类型不匹配: 期望 %s，实际 %s", llm.ContentBlockTypeText, loaded.Content[0].Type())
	}
}

// TestContentBlockRoundTrip 验证会话保存后重新加载，ContentBlock 内容保持一致。
func TestContentBlockRoundTrip(t *testing.T) {
	dir := t.TempDir()
	sm := &SessionManager{sessionsDir: dir}

	s := &Session{
		ID:        "round-trip-test",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("测试消息内容")}},
			{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.NewTextBlock("这是一条助手回复")}},
		},
	}

	sm.Save(s)
	loaded, _ := sm.Load("round-trip-test")

	// 验证 ContentBlock[0].ToText() 与原始文本一致
	if loaded.Messages[0].Content[0].ToText() != "测试消息内容" {
		t.Fatalf("用户消息内容不一致: '%s'", loaded.Messages[0].Content[0].ToText())
	}
	if loaded.Messages[1].Content[0].ToText() != "这是一条助手回复" {
		t.Fatalf("助手消息内容不一致: '%s'", loaded.Messages[1].Content[0].ToText())
	}
}

// TestLoadLatest 验证加载最新会话。
func TestLoadLatest(t *testing.T) {
	dir := t.TempDir()
	sm := &SessionManager{sessionsDir: dir}

	// 创建并保存第一个会话
	old := &Session{
		ID:        "old-session",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Messages:  []llm.Message{},
	}
	sm.Save(old)

	// 等待一小段时间确保时间戳不同
	time.Sleep(10 * time.Millisecond)

	// 创建并保存更新的会话
	recent := &Session{
		ID:        "recent-session",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Messages:  []llm.Message{},
	}
	sm.Save(recent)

	latest, err := sm.LoadLatest()
	if err != nil {
		t.Fatalf("加载最新会话失败: %v", err)
	}
	if latest.ID != "recent-session" {
		t.Fatalf("应返回最新会话，实际返回: %s", latest.ID)
	}
}

// TestLoadLatestEmpty 验证目录为空时返回 nil。
func TestLoadLatestEmpty(t *testing.T) {
	dir := t.TempDir()
	sm := &SessionManager{sessionsDir: dir}

	latest, err := sm.LoadLatest()
	if err != nil {
		t.Fatalf("空目录应无错误: %v", err)
	}
	if latest != nil {
		t.Fatal("空目录应返回 nil")
	}
}

// TestCorruptedSessionFile 验证损坏的会话文件处理。
func TestCorruptedSessionFile(t *testing.T) {
	dir := t.TempDir()
	sm := &SessionManager{sessionsDir: dir}

	// 写入损坏的 JSON
	corruptPath := filepath.Join(dir, "corrupted.json")
	os.WriteFile(corruptPath, []byte("{invalid json}"), 0644)

	_, err := sm.Load("corrupted")
	if err == nil {
		t.Fatal("损坏文件应返回错误")
	}
}

// TestCorruptedFileSkippedInLoadLatest 验证 LoadLatest 跳过损坏文件。
func TestCorruptedFileSkippedInLoadLatest(t *testing.T) {
	dir := t.TempDir()
	sm := &SessionManager{sessionsDir: dir}

	// 写入损坏的 JSON
	corruptPath := filepath.Join(dir, "corrupted.json")
	os.WriteFile(corruptPath, []byte("{bad}"), 0644)

	// 写入一个有效的会话
	valid := &Session{
		ID:        "valid-session",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Messages:  []llm.Message{},
	}
	sm.Save(valid)

	latest, err := sm.LoadLatest()
	if err != nil {
		t.Fatalf("应跳过损坏文件加载有效的: %v", err)
	}
	if latest.ID != "valid-session" {
		t.Fatalf("应返回有效会话，实际: %s", latest.ID)
	}
}

// TestDirectoryAutoCreate 验证会话目录自动创建。
func TestDirectoryAutoCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "sessions")
	sm := &SessionManager{sessionsDir: dir}

	// 目录不存在时 Save 应自动创建
	s := &Session{
		ID:        "auto-dir-test",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Messages:  []llm.Message{},
	}

	// 需要先确保目录存在（Save 不创建目录，NewSessionManager 创建）
	os.MkdirAll(dir, 0755)
	if err := sm.Save(s); err != nil {
		t.Fatalf("保存到嵌套目录失败: %v", err)
	}
}

// TestListSessionsOrderByUpdated 验证 ListSessions 按 UpdatedAt 降序排列。
func TestListSessionsOrderByUpdated(t *testing.T) {
	dir := t.TempDir()
	sm := &SessionManager{sessionsDir: dir}

	// 创建 3 个会话，间隔保存以确保 UpdatedAt 不同
	createTestSession(t, sm, "session-aaa", []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("第一条消息aaa")}},
	})
	time.Sleep(20 * time.Millisecond)

	createTestSession(t, sm, "session-bbb", []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("第一条消息bbb")}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.NewTextBlock("回复bbb")}},
	})
	time.Sleep(20 * time.Millisecond)

	createTestSession(t, sm, "session-ccc", []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("ccc")}},
	})

	summaries, err := sm.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions 失败: %v", err)
	}

	if len(summaries) != 3 {
		t.Fatalf("期望 3 条摘要，实际 %d", len(summaries))
	}

	// 按 UpdatedAt 降序：ccc 最新 → bbb → aaa
	if summaries[0].ID != "session-ccc" {
		t.Fatalf("第 1 条应为 session-ccc，实际 %s", summaries[0].ID)
	}
	if summaries[1].ID != "session-bbb" {
		t.Fatalf("第 2 条应为 session-bbb，实际 %s", summaries[1].ID)
	}
	if summaries[2].ID != "session-aaa" {
		t.Fatalf("第 3 条应为 session-aaa，实际 %s", summaries[2].ID)
	}

	// 验证摘要字段
	if summaries[1].MessageCount != 2 {
		t.Fatalf("session-bbb 消息数应为 2，实际 %d", summaries[1].MessageCount)
	}
	if summaries[1].Preview != "第一条消息bbb" {
		t.Fatalf("session-bbb 预览应为 '第一条消息bbb'，实际 '%s'", summaries[1].Preview)
	}
}

// TestListSessionsEmpty 验证空目录返回空列表。
func TestListSessionsEmpty(t *testing.T) {
	dir := t.TempDir()
	sm := &SessionManager{sessionsDir: dir}

	summaries, err := sm.ListSessions()
	if err != nil {
		t.Fatalf("空目录应无错误: %v", err)
	}
	if len(summaries) != 0 {
		t.Fatalf("空目录应返回空切片，实际长度 %d", len(summaries))
	}
}

// TestListSessionsSkipsCorrupted 验证损坏文件被跳过。
func TestListSessionsSkipsCorrupted(t *testing.T) {
	dir := t.TempDir()
	sm := &SessionManager{sessionsDir: dir}

	// 写入损坏文件
	os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{invalid}"), 0644)

	// 写入有效文件
	createTestSession(t, sm, "good-session", []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("hello")}},
	})

	summaries, err := sm.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions 失败: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("应跳过损坏文件返回 1 条，实际 %d", len(summaries))
	}
	if summaries[0].ID != "good-session" {
		t.Fatalf("应返回有效会话，实际 %s", summaries[0].ID)
	}
}

// TestListSessionsPreviewEmpty 验证无用户消息时 Preview 为 "(空会话)"。
func TestListSessionsPreviewEmpty(t *testing.T) {
	dir := t.TempDir()
	sm := &SessionManager{sessionsDir: dir}

	// 仅助手消息，无用户消息
	createTestSession(t, sm, "no-user-msg", []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.NewTextBlock("系统回复")}},
	})

	summaries, _ := sm.ListSessions()
	if len(summaries) != 1 {
		t.Fatalf("期望 1 条摘要，实际 %d", len(summaries))
	}
	if summaries[0].Preview != "(空会话)" {
		t.Fatalf("无用户消息时 Preview 应为 '(空会话)'，实际 '%s'", summaries[0].Preview)
	}
}

// TestDelete 验证删除会话文件。
func TestDelete(t *testing.T) {
	dir := t.TempDir()
	sm := &SessionManager{sessionsDir: dir}

	createTestSession(t, sm, "to-delete", []llm.Message{})

	// 删除前文件存在
	if _, err := os.Stat(filepath.Join(dir, "to-delete.json")); os.IsNotExist(err) {
		t.Fatal("删除前文件应存在")
	}

	// 执行删除
	if err := sm.Delete("to-delete"); err != nil {
		t.Fatalf("Delete 失败: %v", err)
	}

	// 删除后文件不存在
	if _, err := os.Stat(filepath.Join(dir, "to-delete.json")); !os.IsNotExist(err) {
		t.Fatal("删除后文件不应存在")
	}
}

// TestDeleteNotFound 验证删除不存在的 ID 返回错误。
func TestDeleteNotFound(t *testing.T) {
	dir := t.TempDir()
	sm := &SessionManager{sessionsDir: dir}

	err := sm.Delete("nonexistent-id")
	if err == nil {
		t.Fatal("删除不存在的 ID 应返回错误")
	}
}

// TestTruncateText 验证文本截断函数。
func TestTruncateText(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"hello world", 8, "hello..."},
		{"中文测试文本截断功能", 6, "中文测..."},
		{"abc", 3, "abc"},
	}

	for _, tt := range tests {
		got := truncateText(tt.input, tt.maxLen)
		if got != tt.want {
			t.Errorf("truncateText(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
		}
	}
}
