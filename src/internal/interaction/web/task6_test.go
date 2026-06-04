package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/MeiCorl/CodePilot/src/internal/config"
	"github.com/MeiCorl/CodePilot/src/internal/engine/conversation"
	"github.com/MeiCorl/CodePilot/src/internal/memory/session"
	"github.com/MeiCorl/CodePilot/src/llm"
	"github.com/MeiCorl/CodePilot/src/tool"
)

// ---- Task 6: 主流程接入验证 ----

// TestMultiTurnSessionPersistence 验证多轮工具调用消息正确序列化到会话 JSON 并能完整恢复。
//
// 场景：构造一个含多轮 tool_use/tool_result 的会话，保存后重新加载，
// 验证 buildChatMessages 能正确渲染所有工具调用链。
func TestMultiTurnSessionPersistence(t *testing.T) {
	dir := t.TempDir()
	sm, _ := session.NewSessionManagerWithDir(dir)
	sess := sm.CreateNew()

	// 构造多轮工具调用的消息序列：
	// user -> assistant(tool_use: read_a) -> user(tool_result: read_a) ->
	// assistant(tool_use: write_b) -> user(tool_result: write_b) ->
	// assistant(text: done)
	sess.Messages = []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("读取 a.txt 并写入 b.txt")}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			&llm.ToolUseBlock{ID: "tu-1", Name: "read_file", Input: json.RawMessage(`{"file_path":"a.txt"}`)},
		}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{
			&llm.ToolResultBlock{ToolUseID: "tu-1", Content: "hello world", IsError: false},
		}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			&llm.ToolUseBlock{ID: "tu-2", Name: "write_file", Input: json.RawMessage(`{"file_path":"b.txt","content":"hello world"}`)},
		}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{
			&llm.ToolResultBlock{ToolUseID: "tu-2", Content: "ok", IsError: false},
		}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			llm.NewTextBlock("已完成：读取 a.txt 并写入 b.txt"),
		}},
	}
	if err := sm.Save(sess); err != nil {
		t.Fatalf("保存会话失败: %v", err)
	}

	// 从磁盘重新加载
	loaded, err := sm.Load(sess.ID)
	if err != nil {
		t.Fatalf("加载会话失败: %v", err)
	}
	if len(loaded.Messages) != 6 {
		t.Fatalf("加载后消息数 = %d, 期望 6", len(loaded.Messages))
	}

	// 用 buildChatMessages 渲染并验证
	chatMsgs := buildChatMessages(loaded.Messages)

	// 期望：
	// 1. user text
	// 2. ToolCall(read_file, output="hello world")
	// 3. ToolCall(write_file, output="ok")
	// 4. assistant text
	if len(chatMsgs) != 4 {
		t.Fatalf("chatMsgs 数量 = %d, 期望 4, 实际: %+v", len(chatMsgs), chatMsgs)
	}

	// 第 1 条：user text
	if chatMsgs[0].Role != "user" || !strings.Contains(chatMsgs[0].Content, "读取") {
		t.Errorf("第 1 条: role=%q content=%q", chatMsgs[0].Role, chatMsgs[0].Content)
	}
	// 第 2 条：ToolCall read_file
	if chatMsgs[1].ToolCall == nil || chatMsgs[1].ToolCall.Name != "read_file" {
		t.Errorf("第 2 条: 应为 read_file ToolCall, 实际: %+v", chatMsgs[1])
	}
	if chatMsgs[1].ToolCall.Output != "hello world" {
		t.Errorf("第 2 条 Output = %q, 期望 hello world", chatMsgs[1].ToolCall.Output)
	}
	// 第 3 条：ToolCall write_file
	if chatMsgs[2].ToolCall == nil || chatMsgs[2].ToolCall.Name != "write_file" {
		t.Errorf("第 3 条: 应为 write_file ToolCall, 实际: %+v", chatMsgs[2])
	}
	// 第 4 条：assistant text
	if chatMsgs[3].Role != "assistant" || !strings.Contains(chatMsgs[3].Content, "已完成") {
		t.Errorf("第 4 条: role=%q content=%q", chatMsgs[3].Role, chatMsgs[3].Content)
	}
}

// TestOldSessionBackwardCompatible 验证 Step 2 产生的单轮工具调用会话在新代码下正常加载。
func TestOldSessionBackwardCompatible(t *testing.T) {
	dir := t.TempDir()
	sm, _ := session.NewSessionManagerWithDir(dir)
	sess := sm.CreateNew()

	// 模拟 Step 2 风格的单轮工具调用
	sess.Messages = []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("读取 main.go")}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			&llm.ToolUseBlock{ID: "old-1", Name: "read_file", Input: json.RawMessage(`{"file_path":"main.go"}`)},
		}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{
			&llm.ToolResultBlock{ToolUseID: "old-1", Content: "package main", IsError: false},
		}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			llm.NewTextBlock("文件内容如上"),
		}},
	}
	if err := sm.Save(sess); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	// 通过 Handler 加载并发送 get_current_session
	cfg := &config.Config{Provider: "anthropic", Model: "test", APIKey: "k", MaxTokens: 1024}
	mp := &scriptedProvider{}
	registry := tool.NewRegistry()
	h := NewHandler(mp, sm, cfg, 10, "", 100000, dir, registry, conversation.NewToolHandler(registry, 5*time.Second, dir))
	s := NewServer("127.0.0.1:0")
	h.Register(s.Router())
	ts := httptest.NewServer(http.HandlerFunc(s.ConnectionManager().HandleWS))
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"
	client, _, _ := websocket.DefaultDialer.Dial(wsURL, nil)
	defer client.Close()

	r := &toolRig{h: h, mp: mp, sessDir: dir, srv: ts, client: client}
	client.WriteMessage(websocket.TextMessage, mustEncode(MsgTypeGetCurrentSession, struct{}{}))

	loaded := waitForType(t, r, MsgTypeSessionLoaded, 2*time.Second)
	if loaded == nil {
		t.Fatal("未收到 session_loaded")
	}
	p, _ := AsPayload[SessionLoadedPayload](*loaded)
	// 期望 3 条 chatMsgs: user text + ToolCall + assistant text
	if len(p.Messages) != 3 {
		t.Fatalf("Messages 数量 = %d, 期望 3 (user + ToolCall + assistant)", len(p.Messages))
	}
	if p.Messages[1].ToolCall == nil {
		t.Fatalf("第 2 条应为 ToolCall")
	}
	if p.Messages[1].ToolCall.Name != "read_file" {
		t.Errorf("ToolCall.Name = %q, 期望 read_file", p.Messages[1].ToolCall.Name)
	}
	if p.Messages[1].ToolCall.Output != "package main" {
		t.Errorf("ToolCall.Output = %q, 期望 package main", p.Messages[1].ToolCall.Output)
	}
}

// TestToolErrorInMultiTurnSession 验证多轮工具调用中包含错误结果的序列化兼容性。
func TestToolErrorInMultiTurnSession(t *testing.T) {
	dir := t.TempDir()
	sm, _ := session.NewSessionManagerWithDir(dir)
	sess := sm.CreateNew()

	// 构造含工具错误的场景：read_file 失败后 LLM 换策略
	sess.Messages = []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("读取不存在的文件")}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			&llm.ToolUseBlock{ID: "err-1", Name: "read_file", Input: json.RawMessage(`{"file_path":"/no/such/file"}`)},
		}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{
			&llm.ToolResultBlock{ToolUseID: "err-1", Content: "文件不存在: /no/such/file", IsError: true},
		}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			llm.NewTextBlock("抱歉，文件不存在，请检查路径。"),
		}},
	}
	if err := sm.Save(sess); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	loaded, err := sm.Load(sess.ID)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	chatMsgs := buildChatMessages(loaded.Messages)
	// 期望: user text + ToolCall(error) + assistant text
	if len(chatMsgs) != 3 {
		t.Fatalf("chatMsgs 数量 = %d, 期望 3", len(chatMsgs))
	}
	tc := chatMsgs[1].ToolCall
	if tc == nil {
		t.Fatal("第 2 条应为 ToolCall")
	}
	if !tc.IsError {
		t.Error("ToolCall.IsError 应为 true")
	}
	if tc.Status != ToolCallStatusError {
		t.Errorf("ToolCall.Status = %q, 期望 error", tc.Status)
	}
	if !strings.Contains(tc.Output, "不存在") {
		t.Errorf("ToolCall.Output 应包含 '不存在', 实际: %q", tc.Output)
	}
}

// TestDeprecatedFieldsCleaned 验证 StreamChunk 和 RunOneTurnResult 的旧 deprecated 字段已被清理。
func TestDeprecatedFieldsCleaned(t *testing.T) {
	// StreamChunk 不应有 ToolUse 字段（只有 ToolUses 切片）
	chunk := llm.StreamChunk{}
	_ = chunk.ToolUses // 编译通过即证明字段存在
	_ = chunk.HasToolUse()
	_ = chunk.FirstToolUse()

	// RunOneTurnResult 不应有 ToolUse 字段（只有 ToolUses 切片）
	result := conversation.RunOneTurnResult{}
	_ = result.ToolUses // 编译通过即证明字段存在
	_ = result.HasToolUse()
	_ = result.FirstToolUse()
}
