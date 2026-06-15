package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MeiCorl/CodePilot/src/llm"
)

// testWorkdir 为测试用的稳定项目工作目录，basename = CodePilot。
const testWorkdir = "/test/workspace/CodePilot"

// newTestSM 构造一个使用临时会话根目录 + 固定 workdir 的 SessionManager。
func newTestSM(t *testing.T) *SessionManager {
	t.Helper()
	dir := t.TempDir()
	sm, err := NewSessionManagerWithDir(dir, testWorkdir)
	if err != nil {
		t.Fatalf("创建 SessionManager 失败: %v", err)
	}
	return sm
}

// createTestSession 辅助函数：创建带消息的测试会话并落盘（CreateSession + AppendMessages）。
func createTestSession(t *testing.T, sm *SessionManager, id string, messages []llm.Message) *Session {
	t.Helper()
	s := &Session{
		ID:        id,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Messages:  messages,
	}
	if err := sm.CreateSession(s); err != nil {
		t.Fatalf("创建会话目录失败: %v", err)
	}
	if len(messages) > 0 {
		if err := sm.AppendMessages(id, messages); err != nil {
			t.Fatalf("追加消息失败: %v", err)
		}
	}
	return s
}

// TestSessionCreateNew 验证创建新会话的基本属性。
func TestSessionCreateNew(t *testing.T) {
	sm := newTestSM(t)

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

// TestSessionCreateAndLoad 验证会话的创建落盘与加载。
func TestSessionCreateAndLoad(t *testing.T) {
	sm := newTestSM(t)

	s := &Session{
		ID:        "test-session-001",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("你好")}},
			{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.NewTextBlock("你好！有什么可以帮你的？")}},
		},
	}

	if err := sm.CreateSession(s); err != nil {
		t.Fatalf("创建会话目录失败: %v", err)
	}
	if err := sm.AppendMessages("test-session-001", s.Messages); err != nil {
		t.Fatalf("追加消息失败: %v", err)
	}

	// 验证消息文件存在
	if _, err := os.Stat(sm.messagesFilePath("test-session-001")); os.IsNotExist(err) {
		t.Fatal("messages.jsonl 未创建")
	}

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

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("序列化消息失败: %v", err)
	}

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
	sm := newTestSM(t)

	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("测试消息内容")}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.NewTextBlock("这是一条助手回复")}},
	}
	if err := sm.AppendMessages("round-trip-test", msgs); err != nil {
		t.Fatalf("AppendMessages 失败: %v", err)
	}
	loaded, err := sm.Load("round-trip-test")
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}

	if loaded.Messages[0].Content[0].ToText() != "测试消息内容" {
		t.Fatalf("用户消息内容不一致: '%s'", loaded.Messages[0].Content[0].ToText())
	}
	if loaded.Messages[1].Content[0].ToText() != "这是一条助手回复" {
		t.Fatalf("助手消息内容不一致: '%s'", loaded.Messages[1].Content[0].ToText())
	}
}

// TestLoadLatest 验证加载最新会话。
func TestLoadLatest(t *testing.T) {
	sm := newTestSM(t)

	createTestSession(t, sm, "old-session", []llm.Message{})
	time.Sleep(10 * time.Millisecond)
	createTestSession(t, sm, "recent-session", []llm.Message{})

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
	sm := newTestSM(t)

	latest, err := sm.LoadLatest()
	if err != nil {
		t.Fatalf("空目录应无错误: %v", err)
	}
	if latest != nil {
		t.Fatal("空目录应返回 nil")
	}
}

// TestLoadNonexistentSession 验证加载不存在的会话返回错误。
func TestLoadNonexistentSession(t *testing.T) {
	sm := newTestSM(t)

	_, err := sm.Load("nonexistent-id")
	if err == nil {
		t.Fatal("加载不存在的会话应返回错误")
	}
}

// TestCorruptedSubdirSkippedInLoadLatest 验证 LoadLatest 跳过无 meta 的损坏目录。
func TestCorruptedSubdirSkippedInLoadLatest(t *testing.T) {
	sm := newTestSM(t)

	// 构造一个"损坏"的 session 子目录：仅建目录、不写 meta
	os.MkdirAll(sm.sessionDirPath("corrupted"), 0755)

	// 写入一个有效的会话
	createTestSession(t, sm, "valid-session", []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("hi")}},
	})

	latest, err := sm.LoadLatest()
	if err != nil {
		t.Fatalf("应跳过损坏目录加载有效的: %v", err)
	}
	if latest == nil || latest.ID != "valid-session" {
		t.Fatalf("应返回有效会话，实际: %v", latest)
	}
}

// TestDirectoryAutoCreate 验证嵌套会话根目录与 session 目录的惰性自动创建。
func TestDirectoryAutoCreate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nested", "sessions")
	sm, err := NewSessionManagerWithDir(root, "/auto/CodePilot")
	if err != nil {
		t.Fatalf("NewSessionManagerWithDir 失败: %v", err)
	}
	// 首次 Append 应惰性创建 session 目录与 messages.jsonl
	if err := sm.AppendMessages("auto-session", []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("x")}},
	}); err != nil {
		t.Fatalf("Append 到嵌套目录失败: %v", err)
	}
	if _, err := os.Stat(sm.messagesFilePath("auto-session")); os.IsNotExist(err) {
		t.Fatal("messages.jsonl 未创建")
	}
}

// TestListSessionsOrderByUpdated 验证 ListSessions 按 UpdatedAt 降序排列。
func TestListSessionsOrderByUpdated(t *testing.T) {
	sm := newTestSM(t)

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

	if summaries[1].MessageCount != 2 {
		t.Fatalf("session-bbb 消息数应为 2，实际 %d", summaries[1].MessageCount)
	}
	if summaries[1].Preview != "第一条消息bbb" {
		t.Fatalf("session-bbb 预览应为 '第一条消息bbb'，实际 '%s'", summaries[1].Preview)
	}
}

// TestListSessionsEmpty 验证空目录返回空列表。
func TestListSessionsEmpty(t *testing.T) {
	sm := newTestSM(t)

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
	sm := newTestSM(t)

	createTestSession(t, sm, "sess-001", []llm.Message{})
	time.Sleep(20 * time.Millisecond)
	createTestSession(t, sm, "sess-002", []llm.Message{})
	time.Sleep(20 * time.Millisecond)
	createTestSession(t, sm, "sess-003", []llm.Message{})

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
	sm := newTestSM(t)

	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("sess-%03d", i)
		createTestSession(t, sm, id, []llm.Message{})
		time.Sleep(2 * time.Millisecond)
	}

	summaries, err := sm.ListRecentSessions(0)
	if err != nil {
		t.Fatalf("ListRecentSessions 失败: %v", err)
	}
	if len(summaries) != 10 {
		t.Fatalf("默认 limit 应为 10，实际 %d", len(summaries))
	}
	if summaries[0].ID != "sess-011" {
		t.Fatalf("第 1 条应为 sess-011，实际 %s", summaries[0].ID)
	}
}

// TestListSessionsSkipsCorrupted 验证无 meta 的损坏子目录被跳过。
func TestListSessionsSkipsCorrupted(t *testing.T) {
	sm := newTestSM(t)

	// 损坏子目录：仅建目录、不写 meta
	os.MkdirAll(sm.sessionDirPath("bad"), 0755)
	// 有效会话
	createTestSession(t, sm, "good-session", []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("hello")}},
	})

	summaries, err := sm.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions 失败: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("应跳过损坏目录返回 1 条，实际 %d", len(summaries))
	}
	if summaries[0].ID != "good-session" {
		t.Fatalf("应返回 good-session，实际 %s", summaries[0].ID)
	}
}

// TestListSessionsPreviewEmpty 验证无用户消息时 Preview 为 "(空会话)"。
func TestListSessionsPreviewEmpty(t *testing.T) {
	sm := newTestSM(t)

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

// TestDelete 验证删除会话目录。
func TestDelete(t *testing.T) {
	sm := newTestSM(t)

	createTestSession(t, sm, "to-delete", []llm.Message{})

	if _, err := os.Stat(sm.sessionDirPath("to-delete")); os.IsNotExist(err) {
		t.Fatal("删除前目录应存在")
	}
	if err := sm.Delete("to-delete"); err != nil {
		t.Fatalf("Delete 失败: %v", err)
	}
	if _, err := os.Stat(sm.sessionDirPath("to-delete")); !os.IsNotExist(err) {
		t.Fatal("删除后目录不应存在")
	}
}

// TestDeleteNotFound 验证删除不存在的 ID 返回错误。
func TestDeleteNotFound(t *testing.T) {
	sm := newTestSM(t)

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
// 在 append+load 后能完整还原（会话持久化兼容）。
func TestSessionRoundTripWithToolUseAndToolResult(t *testing.T) {
	sm := newTestSM(t)

	inputJSON := json.RawMessage(`{"file_path":"src/main.go","offset":0,"limit":20}`)
	original := []llm.Message{
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
	}
	if err := sm.AppendMessages("tool-roundtrip", original); err != nil {
		t.Fatalf("AppendMessages 失败: %v", err)
	}

	loaded, err := sm.Load("tool-roundtrip")
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if len(loaded.Messages) != len(original) {
		t.Fatalf("消息数不匹配: 期望 %d, 实际 %d", len(original), len(loaded.Messages))
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
	// 逐字节比对失败时降级为语义比对
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

// TestSessionRawJSONLContainsToolUseType 验证 messages.jsonl 每行含
// `type: "tool_use"` 与 `type: "tool_result"` 字段。
func TestSessionRawJSONLContainsToolUseType(t *testing.T) {
	sm := newTestSM(t)

	msgs := []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			&llm.ToolUseBlock{ID: "u1", Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)},
		}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{
			&llm.ToolResultBlock{ToolUseID: "u1", Content: "ok", IsError: false},
		}},
	}
	if err := sm.AppendMessages("raw-tool", msgs); err != nil {
		t.Fatalf("AppendMessages 失败: %v", err)
	}

	raw, err := os.ReadFile(sm.messagesFilePath("raw-tool"))
	if err != nil {
		t.Fatalf("读 messages.jsonl 失败: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("期望 2 行, 实际 %d", len(lines))
	}

	var first struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("解析第 1 行失败: %v", err)
	}
	if got := first.Content[0].Type; got != "tool_use" {
		t.Errorf("第 1 行 type: 期望 tool_use, 实际 %s", got)
	}

	var second struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("解析第 2 行失败: %v", err)
	}
	if got := second.Content[0].Type; got != "tool_result" {
		t.Errorf("第 2 行 type: 期望 tool_result, 实际 %s", got)
	}
}

// ---- 新增：append-only 增量 / 容错 / 项目目录 测试 ----

// TestAppendMessagesIncremental 验证多次 Append 只追加、不重写历史。
func TestAppendMessagesIncremental(t *testing.T) {
	sm := newTestSM(t)
	sid := "incr-session"

	if err := sm.AppendMessages(sid, []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("hello")}},
	}); err != nil {
		t.Fatalf("第一次 Append 失败: %v", err)
	}
	if err := sm.AppendMessages(sid, []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.NewTextBlock("hi")}},
	}); err != nil {
		t.Fatalf("第二次 Append 失败: %v", err)
	}

	data, _ := os.ReadFile(sm.messagesFilePath(sid))
	lineCount := strings.Count(string(data), "\n")
	if lineCount != 2 {
		t.Fatalf("期望 2 行（仅追加不重复），实际 %d", lineCount)
	}

	loaded, err := sm.Load(sid)
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if len(loaded.Messages) != 2 {
		t.Fatalf("期望 2 条消息，实际 %d", len(loaded.Messages))
	}
}

// TestAppendMessagesEmptyNoop 验证空切片不改变文件（幂等）。
func TestAppendMessagesEmptyNoop(t *testing.T) {
	sm := newTestSM(t)
	sid := "empty-noop"

	if err := sm.AppendMessages(sid, []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("x")}},
	}); err != nil {
		t.Fatalf("首次 Append 失败: %v", err)
	}
	before, _ := os.ReadFile(sm.messagesFilePath(sid))

	if err := sm.AppendMessages(sid, nil); err != nil {
		t.Fatalf("空 Append 不应失败: %v", err)
	}
	after, _ := os.ReadFile(sm.messagesFilePath(sid))

	if string(before) != string(after) {
		t.Fatal("空 Append 不应改变文件内容")
	}
}

// TestTruncateMessagesClearsFile 验证清空会话消息。
func TestTruncateMessagesClearsFile(t *testing.T) {
	sm := newTestSM(t)
	sid := "trunc-session"

	if err := sm.AppendMessages(sid, []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("a")}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.NewTextBlock("b")}},
	}); err != nil {
		t.Fatalf("Append 失败: %v", err)
	}
	if err := sm.TruncateMessages(sid); err != nil {
		t.Fatalf("Truncate 失败: %v", err)
	}

	info, err := os.Stat(sm.messagesFilePath(sid))
	if err != nil {
		t.Fatalf("Stat 失败: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("Truncate 后文件应为 0 字节, 实际 %d", info.Size())
	}

	loaded, _ := sm.Load(sid)
	if len(loaded.Messages) != 0 {
		t.Fatalf("Truncate 后应无消息, 实际 %d", len(loaded.Messages))
	}
	meta, ok, _ := sm.readSessionMeta(sid)
	if !ok || meta.MessageCount != 0 {
		t.Fatal("Truncate 后 meta message_count 应为 0")
	}

	// 清空后再追加应从 0 重新增长
	if err := sm.AppendMessages(sid, []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("c")}},
	}); err != nil {
		t.Fatalf("清空后再 Append 失败: %v", err)
	}
	data, _ := os.ReadFile(sm.messagesFilePath(sid))
	if strings.Count(string(data), "\n") != 1 {
		t.Fatalf("清空后再追加应为 1 行, 实际 %d", strings.Count(string(data), "\n"))
	}
}

// TestLoadSkipsCorruptedLine 验证 Load 跳过损坏的 JSON 行，保留好行。
func TestLoadSkipsCorruptedLine(t *testing.T) {
	sm := newTestSM(t)
	sid := "corrupt-line"

	// 手工构造 messages.jsonl：好行 + 坏行 + 好行
	dir := sm.sessionDirPath(sid)
	os.MkdirAll(dir, 0755)
	sm.writeSessionMeta(dir, &sessionMeta{ID: sid, CreatedAt: time.Now(), UpdatedAt: time.Now()})
	content := `{"role":"user","content":[{"type":"text","text":"first"}]}
{bad json line}
{"role":"assistant","content":[{"type":"text","text":"third"}]}
`
	if err := os.WriteFile(sm.messagesFilePath(sid), []byte(content), 0644); err != nil {
		t.Fatalf("写测试文件失败: %v", err)
	}

	loaded, err := sm.Load(sid)
	if err != nil {
		t.Fatalf("Load 损坏行会话失败: %v", err)
	}
	if len(loaded.Messages) != 2 {
		t.Fatalf("应跳过坏行返回 2 条消息, 实际 %d", len(loaded.Messages))
	}
	if loaded.Messages[0].Content[0].ToText() != "first" {
		t.Fatalf("第 1 条应为 first, 实际 '%s'", loaded.Messages[0].Content[0].ToText())
	}
	if loaded.Messages[1].Content[0].ToText() != "third" {
		t.Fatalf("第 2 条应为 third, 实际 '%s'", loaded.Messages[1].Content[0].ToText())
	}
}

// TestProjectDirConflictHashSuffix 验证同名 basename 不同路径的项目隔离到不同目录。
func TestProjectDirConflictHashSuffix(t *testing.T) {
	root := t.TempDir()
	sm1, err := NewSessionManagerWithDir(root, "/path/one/demo")
	if err != nil {
		t.Fatalf("sm1 失败: %v", err)
	}
	sm2, err := NewSessionManagerWithDir(root, "/path/two/demo")
	if err != nil {
		t.Fatalf("sm2 失败: %v", err)
	}

	// 两者 basename 均为 demo，但 path 不同 → 必须隔离到不同项目目录
	if sm1.projectDir == sm2.projectDir {
		t.Fatalf("同名不同路径的项目应隔离到不同目录: %s", sm1.projectDir)
	}
	// 首个项目用纯 basename，冲突项目用 basename-<hash>
	if filepath.Base(sm1.projectDir) != "demo" {
		t.Fatalf("首个项目目录应为 'demo', 实际 %s", filepath.Base(sm1.projectDir))
	}
	if !strings.HasPrefix(filepath.Base(sm2.projectDir), "demo-") {
		t.Fatalf("冲突项目目录应以 'demo-' 开头, 实际 %s", filepath.Base(sm2.projectDir))
	}
}

// TestProjectDirReuseSamePath 验证相同路径复用同一项目目录。
func TestProjectDirReuseSamePath(t *testing.T) {
	root := t.TempDir()
	sm1, err := NewSessionManagerWithDir(root, "/work/CodePilot")
	if err != nil {
		t.Fatalf("sm1 失败: %v", err)
	}
	sm2, err := NewSessionManagerWithDir(root, "/work/CodePilot")
	if err != nil {
		t.Fatalf("sm2 失败: %v", err)
	}
	if sm1.projectDir != sm2.projectDir {
		t.Fatalf("同路径应复用同一项目目录: %s vs %s", sm1.projectDir, sm2.projectDir)
	}
}

// TestListSessionsScansSubdirsIgnoreFiles 验证列表只扫子目录，
// 项目目录内的非目录文件（旧 .json / .project.json）被忽略。
func TestListSessionsScansSubdirsIgnoreFiles(t *testing.T) {
	root := t.TempDir()
	sm, err := NewSessionManagerWithDir(root, "/proj/CodePilot")
	if err != nil {
		t.Fatalf("创建 SM 失败: %v", err)
	}
	// 在项目目录内放一个旧 .json 文件（非目录），应被 IsDir 过滤忽略
	if err := os.WriteFile(filepath.Join(sm.projectDir, "legacy.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("写 legacy.json 失败: %v", err)
	}
	// 创建一个有效会话
	createTestSession(t, sm, "real-session", []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("hi")}},
	})

	summaries, err := sm.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions 失败: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("应只扫到 1 个会话(忽略非目录文件), 实际 %d", len(summaries))
	}
	if summaries[0].ID != "real-session" {
		t.Fatalf("应为 real-session, 实际 %s", summaries[0].ID)
	}
}

// TestCrossProjectIsolation 验证不同项目目录互不可见。
func TestCrossProjectIsolation(t *testing.T) {
	root := t.TempDir()
	smA, _ := NewSessionManagerWithDir(root, "/work/Alpha")
	smB, _ := NewSessionManagerWithDir(root, "/work/Beta")

	createTestSession(t, smA, "alpha-session", []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("from alpha")}},
	})
	createTestSession(t, smB, "beta-session", []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("from beta")}},
	})

	// Alpha 项目不应看到 Beta 的会话
	aSummaries, _ := smA.ListSessions()
	if len(aSummaries) != 1 || aSummaries[0].ID != "alpha-session" {
		t.Fatalf("Alpha 项目应只见 alpha-session, 实际 %+v", aSummaries)
	}
	bSummaries, _ := smB.ListSessions()
	if len(bSummaries) != 1 || bSummaries[0].ID != "beta-session" {
		t.Fatalf("Beta 项目应只见 beta-session, 实际 %+v", bSummaries)
	}
}
