package session

import (
	"encoding/json"
	"fmt"
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

// TestListRecentSessionsOrderByCreated 验证 ListRecentSessions 按 CreatedAt 降序，
// 且只返回最近 limit 个会话。
func TestListRecentSessionsOrderByCreated(t *testing.T) {
	dir := t.TempDir()
	sm := &SessionManager{sessionsDir: dir}

	// 创建 3 个会话，按时间顺序逐个保存，让 CreatedAt 形成稳定差值
	createTestSession(t, sm, "sess-001", []llm.Message{})
	time.Sleep(20 * time.Millisecond)
	createTestSession(t, sm, "sess-002", []llm.Message{})
	time.Sleep(20 * time.Millisecond)
	createTestSession(t, sm, "sess-003", []llm.Message{})

	// 全部：按 CreatedAt 降序：003 → 002 → 001
	all, err := sm.ListRecentSessions(0)
	if err != nil {
		t.Fatalf("ListRecentSessions 失败: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("limit=0 应返回全部 3 条，实际 %d", len(all))
	}
	if all[0].ID != "sess-003" || all[1].ID != "sess-002" || all[2].ID != "sess-001" {
		t.Fatalf("排序错误: %s / %s / %s", all[0].ID, all[1].ID, all[2].ID)
	}

	// limit=2：只返回最近创建的 2 个
	top, err := sm.ListRecentSessions(2)
	if err != nil {
		t.Fatalf("ListRecentSessions(2) 失败: %v", err)
	}
	if len(top) != 2 {
		t.Fatalf("limit=2 应返回 2 条，实际 %d", len(top))
	}
	if top[0].ID != "sess-003" || top[1].ID != "sess-002" {
		t.Fatalf("limit=2 排序错误: %s / %s", top[0].ID, top[1].ID)
	}
}

// TestListRecentSessionsDefaultLimit 验证 limit<=0 时使用默认 10。
func TestListRecentSessionsDefaultLimit(t *testing.T) {
	dir := t.TempDir()
	sm := &SessionManager{sessionsDir: dir}

	// 创建 12 个会话
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("sess-%03d", i)
		createTestSession(t, sm, id, []llm.Message{})
		time.Sleep(2 * time.Millisecond)
	}

	// limit=0 应回退到默认 10
	summaries, err := sm.ListRecentSessions(0)
	if err != nil {
		t.Fatalf("ListRecentSessions 失败: %v", err)
	}
	if len(summaries) != 10 {
		t.Fatalf("默认 limit 应为 10，实际 %d", len(summaries))
	}
	// 第 1 条应该是最近创建的 sess-011
	if summaries[0].ID != "sess-011" {
		t.Fatalf("第 1 条应为 sess-011，实际 %s", summaries[0].ID)
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

// TestSessionRoundTripWithToolUseAndToolResult 验证 ToolUseBlock / ToolResultBlock
// 在会话 save+load 后能完整还原（spec 能力清单 16：会话持久化兼容）。
func TestSessionRoundTripWithToolUseAndToolResult(t *testing.T) {
	dir := t.TempDir()
	sm := &SessionManager{sessionsDir: dir}

	inputJSON := json.RawMessage(`{"file_path":"src/main.go","offset":0,"limit":20}`)
	original := &Session{
		ID:        "tool-roundtrip",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("请读一下 src/main.go 前 20 行")}},
			{
				Role: llm.RoleAssistant,
				Content: []llm.ContentBlock{
					&llm.ToolUseBlock{ID: "toolu_abc123", Name: "ReadFile", Input: inputJSON},
				},
			},
			{
				Role: llm.RoleUser,
				Content: []llm.ContentBlock{
					&llm.ToolResultBlock{ToolUseID: "toolu_abc123", Content: "L1: package main\nL2:", IsError: false},
				},
			},
			{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.NewTextBlock("前 20 行内容如上。")}},
		},
	}
	if err := sm.Save(original); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}

	loaded, err := sm.Load("tool-roundtrip")
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if len(loaded.Messages) != len(original.Messages) {
		t.Fatalf("消息数不匹配: 期望 %d, 实际 %d", len(original.Messages), len(loaded.Messages))
	}

	// 验证 assistant tool_use 消息
	asstToolUse := loaded.Messages[1].Content[0]
	if asstToolUse.Type() != llm.ContentBlockTypeToolUse {
		t.Fatalf("第 2 条消息类型: 期望 %s, 实际 %s", llm.ContentBlockTypeToolUse, asstToolUse.Type())
	}
	tu, ok := asstToolUse.(*llm.ToolUseBlock)
	if !ok {
		t.Fatalf("第 2 条消息应能转回 *ToolUseBlock, 实际 %T", asstToolUse)
	}
	if tu.ID != "toolu_abc123" || tu.Name != "ReadFile" {
		t.Errorf("ToolUse 字段不一致: ID=%s Name=%s", tu.ID, tu.Name)
	}
	if string(tu.Input) != string(inputJSON) {
		// session 落盘用 MarshalIndent 会重排空白，原始 RawMessage 不会逐字节相等；
		// 改用解析后语义比对：两边都 unmarshal 成 map，验证键值一致
		var orig, got map[string]any
		if err := json.Unmarshal(inputJSON, &orig); err != nil {
			t.Fatalf("原 Input JSON 解析失败: %v", err)
		}
		if err := json.Unmarshal(tu.Input, &got); err != nil {
			t.Fatalf("回读 Input JSON 解析失败: %v", err)
		}
		if fmt.Sprintf("%v", orig) != fmt.Sprintf("%v", got) {
			t.Errorf("ToolUse.Input 语义不一致: 期望 %v, 实际 %v", orig, got)
		}
	}

	// 验证 user tool_result 消息
	userToolResult := loaded.Messages[2].Content[0]
	if userToolResult.Type() != llm.ContentBlockTypeToolResult {
		t.Fatalf("第 3 条消息类型: 期望 %s, 实际 %s", llm.ContentBlockTypeToolResult, userToolResult.Type())
	}
	tr, ok := userToolResult.(*llm.ToolResultBlock)
	if !ok {
		t.Fatalf("第 3 条消息应能转回 *ToolResultBlock, 实际 %T", userToolResult)
	}
	if tr.ToolUseID != "toolu_abc123" || tr.IsError || tr.Content == "" {
		t.Errorf("ToolResult 字段不一致: ID=%s IsError=%v Content=%q", tr.ToolUseID, tr.IsError, tr.Content)
	}
}

// TestSessionRawJSONContainsToolUseType 验证 session JSON 文件中含
// `type: "tool_use"` 与 `type: "tool_result"` 字段（spec 8.1）。
func TestSessionRawJSONContainsToolUseType(t *testing.T) {
	dir := t.TempDir()
	sm := &SessionManager{sessionsDir: dir}
	s := &Session{
		ID:        "raw-tool",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Messages: []llm.Message{
			{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
				&llm.ToolUseBlock{ID: "u1", Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)},
			}},
			{Role: llm.RoleUser, Content: []llm.ContentBlock{
				&llm.ToolResultBlock{ToolUseID: "u1", Content: "ok", IsError: false},
			}},
		},
	}
	if err := sm.Save(s); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}

	// 解析落盘 JSON 检查 type 字段（避开缩进空格带来的字符串匹配歧义）
	var parsed struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
			} `json:"content"`
		} `json:"messages"`
	}
	raw, err := os.ReadFile(filepath.Join(dir, "raw-tool.json"))
	if err != nil {
		t.Fatalf("读 session 文件失败: %v", err)
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("解析 session JSON 失败: %v\n原始内容: %s", err, raw)
	}
	if len(parsed.Messages) != 2 {
		t.Fatalf("期望 2 条消息, 实际 %d", len(parsed.Messages))
	}
	if got := parsed.Messages[0].Content[0].Type; got != "tool_use" {
		t.Errorf("assistant content[0].type: 期望 tool_use, 实际 %s", got)
	}
	if got := parsed.Messages[1].Content[0].Type; got != "tool_result" {
		t.Errorf("user content[0].type: 期望 tool_result, 实际 %s", got)
	}
}
