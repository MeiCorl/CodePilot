package llm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MeiCorl/CodePilot/src/internal/config"
	"github.com/MeiCorl/CodePilot/src/internal/tool"
)

// newTestAnthropicProvider 创建测试用 AnthropicProvider（不调用真实 API）
func newTestAnthropicProvider() *AnthropicProvider {
	cfg := &config.Config{
		Provider:   "anthropic",
		Model:     "claude-sonnet-4-20250514",
		APIKey:    "sk-ant-test-key",
		MaxTokens: 1024,
		Timeout:   5,
		MaxRetries: 1,
	}
	return NewAnthropicProvider(cfg)
}

// TestAnthropicConvertMessages 验证消息格式转换
func TestAnthropicConvertMessages(t *testing.T) {
	p := newTestAnthropicProvider()

	messages := []Message{
		{
			Role:    RoleUser,
			Content: []ContentBlock{NewTextBlock("你好")},
		},
		{
			Role:    RoleAssistant,
			Content: []ContentBlock{NewTextBlock("你好！有什么可以帮你的？")},
		},
		{
			Role:    RoleUser,
			Content: []ContentBlock{NewTextBlock("写一个 hello world")},
		},
	}

	params := p.convertMessages(messages)

	if len(params) != 3 {
		t.Fatalf("转换后消息数量错误: 期望 3, 实际 %d", len(params))
	}

	// 验证第一条是 UserMessage，内容为 "你好"
	// SDK 的 MessageParam 是 union 类型，无法直接检查内容
	// 但可以验证不 panic 且数量正确
}

// TestAnthropicConvertMessagesEmpty 验证空消息列表转换
func TestAnthropicConvertMessagesEmpty(t *testing.T) {
	p := newTestAnthropicProvider()
	params := p.convertMessages([]Message{})
	if len(params) != 0 {
		t.Errorf("空消息列表转换后应为空, 实际长度 %d", len(params))
	}
}

// TestAnthropicProviderInit 验证客户端初始化不 panic
func TestAnthropicProviderInit(t *testing.T) {
	p := newTestAnthropicProvider()
	if p == nil {
		t.Fatal("Provider 不应为 nil")
	}
	if p.model != "claude-sonnet-4-20250514" {
		t.Errorf("model 错误: 期望 claude-sonnet-4-20250514, 实际 %s", p.model)
	}
	if p.maxTokens != 1024 {
		t.Errorf("maxTokens 错误: 期望 1024, 实际 %d", p.maxTokens)
	}
}

// TestAnthropicProviderWithBaseURL 验证自定义 BaseURL 不 panic
func TestAnthropicProviderWithBaseURL(t *testing.T) {
	cfg := &config.Config{
		Provider:  "anthropic",
		Model:    "claude-sonnet-4-20250514",
		APIKey:   "test-key",
		MaxTokens: 1024,
		BaseURL:  "https://my-proxy.example.com/anthropic",
	}
	p := NewAnthropicProvider(cfg)
	if p == nil {
		t.Fatal("Provider 不应为 nil")
	}
}

// TestAnthropicCtxCancel 验证 ctx 取消时流式请求终止
func TestAnthropicCtxCancel(t *testing.T) {
	p := newTestAnthropicProvider()
	ctx, cancel := context.WithCancel(context.Background())

	// 立即取消
	cancel()

	messages := []Message{
		{Role: RoleUser, Content: []ContentBlock{NewTextBlock("test")}},
	}

	ch, err := p.StreamChat(ctx, NewSystemPromptFromText("system prompt"), messages, nil)
	if err != nil {
		t.Fatalf("StreamChat 返回错误: %v", err)
	}

	// 应该能收到 Done chunk（可能带错误）
	select {
	case chunk := <-ch:
		// ctx 取消后应该正常结束
		if !chunk.Done {
			t.Log("收到非 Done chunk:", chunk.Content)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("超时未收到响应")
	}
}

// TestAnthropicShouldRetry 验证重试判断逻辑
func TestAnthropicShouldRetry(t *testing.T) {
	p := newTestAnthropicProvider()

	tests := []struct {
		name      string
		err       error
		wantRetry bool
	}{
		{"context取消", context.Canceled, false},
		{"context超时", context.DeadlineExceeded, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := p.shouldRetry(tt.err)
			if got != tt.wantRetry {
				t.Errorf("shouldRetry(%v) = %v, want %v", tt.err, got, tt.wantRetry)
			}
		})
	}
}

// TestAnthropicConvertTools 验证 ToolSpec 列表正确转换为 Anthropic tools 数组。
// 通过 JSON 序列化检查关键字段（name / description / input_schema）落地。
func TestAnthropicConvertTools(t *testing.T) {
	p := newTestAnthropicProvider()

	specs := []tool.ToolSpec{
		{
			Name:        "ReadFile",
			Description: "读取文件内容",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}`),
		},
		{
			Name:        "Bash",
			Description: "执行 Shell 命令",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`),
		},
	}

	tools := p.convertTools(specs)
	if len(tools) != 2 {
		t.Fatalf("转换后工具数量错误: 期望 2, 实际 %d", len(tools))
	}

	// 验证第一个工具的 JSON 序列化形态
	data, err := json.Marshal(tools[0].OfTool)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, `"name":"ReadFile"`) {
		t.Errorf("缺少 name 字段: %s", s)
	}
	if !strings.Contains(s, `"description":"读取文件内容"`) {
		t.Errorf("缺少 description 字段: %s", s)
	}
	if !strings.Contains(s, `"input_schema"`) {
		t.Errorf("缺少 input_schema 字段: %s", s)
	}
	if !strings.Contains(s, `"required":["file_path"]`) {
		t.Errorf("缺少 required 字段: %s", s)
	}
}

// TestAnthropicConvertToolsEmpty 验证空 specs 不 panic
func TestAnthropicConvertToolsEmpty(t *testing.T) {
	p := newTestAnthropicProvider()
	tools := p.convertTools(nil)
	if len(tools) != 0 {
		t.Errorf("空 specs 应返回空数组, 实际长度 %d", len(tools))
	}
}

// TestAnthropicConvertMessagesWithToolUse 验证 assistant 消息中含 ToolUseBlock 时
// 能正确转换为 Anthropic assistant 消息（带 tool_use 块）。
func TestAnthropicConvertMessagesWithToolUse(t *testing.T) {
	p := newTestAnthropicProvider()

	messages := []Message{
		{
			Role: RoleUser,
			Content: []ContentBlock{
				NewTextBlock("读一下 main.go"),
			},
		},
		{
			Role: RoleAssistant,
			Content: []ContentBlock{
				NewToolUseBlock("tool_use_1", "ReadFile", json.RawMessage(`{"file_path":"main.go"}`)),
			},
		},
	}

	params := p.convertMessages(messages)
	if len(params) != 2 {
		t.Fatalf("转换后消息数量错误: 期望 2, 实际 %d", len(params))
	}
	// 不 panic 即视为通过；具体 union 内容由 Anthropic SDK 自身保证
}

// TestAnthropicConvertMessagesWithToolResult 验证 user 消息中含 ToolResultBlock 时
// 能正确转换为 Anthropic tool_result 块（无 panic）。
func TestAnthropicConvertMessagesWithToolResult(t *testing.T) {
	p := newTestAnthropicProvider()

	messages := []Message{
		{
			Role: RoleAssistant,
			Content: []ContentBlock{
				NewToolUseBlock("tool_use_1", "ReadFile", json.RawMessage(`{"file_path":"main.go"}`)),
			},
		},
		{
			Role: RoleUser,
			Content: []ContentBlock{
				NewToolResultBlock("tool_use_1", "L1: package main", false),
			},
		},
	}

	params := p.convertMessages(messages)
	if len(params) != 2 {
		t.Fatalf("转换后消息数量错误: 期望 2, 实际 %d", len(params))
	}
}

// TestStreamChunkToolUsesField 验证 StreamChunk 新增 ToolUses 切片字段可被正常赋值
func TestStreamChunkToolUsesField(t *testing.T) {
	toolUse := ToolUseBlock{ID: "abc", Name: "ReadFile", Input: json.RawMessage(`{"file_path":"x"}`)}
	chunk := StreamChunk{Done: true, ToolUses: []ToolUseBlock{toolUse}}
	if !chunk.HasToolUse() {
		t.Fatal("HasToolUse() 应返回 true")
	}
	if chunk.FirstToolUse() == nil {
		t.Fatal("FirstToolUse() 不应为 nil")
	}
	if chunk.FirstToolUse().ID != "abc" {
		t.Errorf("FirstToolUse().ID 错误: %s", chunk.FirstToolUse().ID)
	}
}
