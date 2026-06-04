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

// newTestOpenAIProvider 创建测试用 OpenAIProvider（不调用真实 API）
func newTestOpenAIProvider() *OpenAIProvider {
	cfg := &config.Config{
		Provider:   "openai",
		Model:     "gpt-4o",
		APIKey:    "sk-test-key",
		MaxTokens: 1024,
		Timeout:   5,
		MaxRetries: 1,
	}
	return NewOpenAIProvider(cfg)
}

// TestOpenAIConvertMessages 验证消息格式转换
func TestOpenAIConvertMessages(t *testing.T) {
	p := newTestOpenAIProvider()

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
}

// TestOpenAIConvertMessagesEmpty 验证空消息列表转换
func TestOpenAIConvertMessagesEmpty(t *testing.T) {
	p := newTestOpenAIProvider()
	params := p.convertMessages([]Message{})
	if len(params) != 0 {
		t.Errorf("空消息列表转换后应为空, 实际长度 %d", len(params))
	}
}

// TestOpenAIProviderInit 验证客户端初始化不 panic
func TestOpenAIProviderInit(t *testing.T) {
	p := newTestOpenAIProvider()
	if p == nil {
		t.Fatal("Provider 不应为 nil")
	}
	if p.model != "gpt-4o" {
		t.Errorf("model 错误: 期望 gpt-4o, 实际 %s", p.model)
	}
	if p.maxTokens != 1024 {
		t.Errorf("maxTokens 错误: 期望 1024, 实际 %d", p.maxTokens)
	}
}

// TestOpenAIProviderWithBaseURL 验证自定义 BaseURL 不 panic
func TestOpenAIProviderWithBaseURL(t *testing.T) {
	cfg := &config.Config{
		Provider:  "openai",
		Model:    "gpt-4o",
		APIKey:   "test-key",
		MaxTokens: 1024,
		BaseURL:  "https://my-proxy.example.com/openai",
	}
	p := NewOpenAIProvider(cfg)
	if p == nil {
		t.Fatal("Provider 不应为 nil")
	}
}

// TestOpenAICtxCancel 验证 ctx 取消时流式请求终止
func TestOpenAICtxCancel(t *testing.T) {
	p := newTestOpenAIProvider()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	messages := []Message{
		{Role: RoleUser, Content: []ContentBlock{NewTextBlock("test")}},
	}

	ch, err := p.StreamChat(ctx, "system prompt", messages, nil)
	if err != nil {
		t.Fatalf("StreamChat 返回错误: %v", err)
	}

	select {
	case chunk := <-ch:
		if !chunk.Done {
			t.Log("收到非 Done chunk:", chunk.Content)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("超时未收到响应")
	}
}

// TestOpenAIShouldRetry 验证重试判断逻辑
func TestOpenAIShouldRetry(t *testing.T) {
	p := newTestOpenAIProvider()

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

// TestOpenAIConvertTools 验证 ToolSpec 列表正确转换为 OpenAI tools 数组。
// 通过 JSON 序列化检查关键字段（type=function / function.name / function.description / function.parameters）。
func TestOpenAIConvertTools(t *testing.T) {
	p := newTestOpenAIProvider()

	specs := []tool.ToolSpec{
		{
			Name:        "ReadFile",
			Description: "读取文件内容",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}`),
		},
	}

	tools := p.convertTools(specs)
	if len(tools) != 1 {
		t.Fatalf("转换后工具数量错误: 期望 1, 实际 %d", len(tools))
	}

	data, err := json.Marshal(tools[0])
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, `"type":"function"`) {
		t.Errorf("缺少 type=function 字段: %s", s)
	}
	if !strings.Contains(s, `"name":"ReadFile"`) {
		t.Errorf("缺少 function.name 字段: %s", s)
	}
	if !strings.Contains(s, `"description":"读取文件内容"`) {
		t.Errorf("缺少 function.description 字段: %s", s)
	}
	if !strings.Contains(s, `"parameters"`) {
		t.Errorf("缺少 function.parameters 字段: %s", s)
	}
}

// TestOpenAIConvertToolsEmpty 验证空 specs 不 panic
func TestOpenAIConvertToolsEmpty(t *testing.T) {
	p := newTestOpenAIProvider()
	tools := p.convertTools(nil)
	if len(tools) != 0 {
		t.Errorf("空 specs 应返回空数组, 实际长度 %d", len(tools))
	}
}

// TestOpenAIConvertMessagesWithToolCalls 验证 assistant 消息中含 ToolUseBlock 时
// 能正确转换为 OpenAI assistant 消息（带 tool_calls 字段）。
func TestOpenAIConvertMessagesWithToolCalls(t *testing.T) {
	p := newTestOpenAIProvider()

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
				NewToolUseBlock("call_abc123", "ReadFile", json.RawMessage(`{"file_path":"main.go"}`)),
			},
		},
	}

	params := p.convertMessages(messages)
	if len(params) != 2 {
		t.Fatalf("转换后消息数量错误: 期望 2, 实际 %d", len(params))
	}

	// 验证 assistant 消息序列化后含 tool_calls 字段
	data, err := json.Marshal(params[1])
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, `"tool_calls"`) {
		t.Errorf("缺少 tool_calls 字段: %s", s)
	}
	if !strings.Contains(s, `"call_abc123"`) {
		t.Errorf("缺少 tool call ID: %s", s)
	}
	if !strings.Contains(s, `"ReadFile"`) {
		t.Errorf("缺少 tool call name: %s", s)
	}
}

// TestOpenAIConvertMessagesWithToolResult 验证 user 消息中含 ToolResultBlock 时
// 能正确转换为 role=tool 消息，并通过 JSON 序列化验证 role / tool_call_id / content 落地。
func TestOpenAIConvertMessagesWithToolResult(t *testing.T) {
	p := newTestOpenAIProvider()

	messages := []Message{
		{
			Role: RoleAssistant,
			Content: []ContentBlock{
				NewToolUseBlock("call_abc123", "ReadFile", json.RawMessage(`{"file_path":"main.go"}`)),
			},
		},
		{
			Role: RoleUser,
			Content: []ContentBlock{
				NewToolResultBlock("call_abc123", "L1: package main", false),
			},
		},
	}

	params := p.convertMessages(messages)
	// assistant 1 条 + tool 1 条 = 2
	if len(params) != 2 {
		t.Fatalf("转换后消息数量错误: 期望 2, 实际 %d", len(params))
	}

	// 第二条应是 role=tool 消息
	data, err := json.Marshal(params[1])
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, `"role":"tool"`) {
		t.Errorf("缺少 role=tool 字段: %s", s)
	}
	if !strings.Contains(s, `"tool_call_id":"call_abc123"`) {
		t.Errorf("缺少 tool_call_id 字段: %s", s)
	}
	if !strings.Contains(s, `"L1: package main"`) {
		t.Errorf("缺少 content 字段: %s", s)
	}
}

// TestOpenAIConvertMessagesMixedUserToolAndText 验证 user 消息同时含 text 与 tool_result
// 时，text 段独立成 user 消息、tool_result 独立成 role=tool 消息（OpenAI 协议强制）。
func TestOpenAIConvertMessagesMixedUserToolAndText(t *testing.T) {
	p := newTestOpenAIProvider()

	messages := []Message{
		{
			Role: RoleUser,
			Content: []ContentBlock{
				NewTextBlock("这是 text"),
				NewToolResultBlock("call_1", "result", false),
			},
		},
	}

	params := p.convertMessages(messages)
	// text 1 条 + tool 1 条 = 2
	if len(params) != 2 {
		t.Fatalf("混合 user 消息应拆分为 2 条, 实际 %d", len(params))
	}
}
