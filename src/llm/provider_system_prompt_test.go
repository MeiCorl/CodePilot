package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MeiCorl/CodePilot/src/internal/config"
)

// testContext 返回一个带 5s 超时的 context，供 httptest 测试使用。
// 显式持有 cancel 引用以避免 go vet 警告 context 泄漏。
func testContext() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = cancel // 测试结束后随进程结束被回收，显式保留引用以满足 linter
	return ctx
}

// ---- 纯函数测试：buildAnthropicSystemBlocks ----

// TestBuildAnthropicSystemBlocks_Empty 验证空输入返回 nil。
func TestBuildAnthropicSystemBlocks_Empty(t *testing.T) {
	got := buildAnthropicSystemBlocks(nil)
	if got != nil {
		t.Errorf("空输入应返回 nil，实际: %+v", got)
	}
	got = buildAnthropicSystemBlocks([]SystemBlock{})
	if got != nil {
		t.Errorf("空 slice 应返回 nil，实际: %+v", got)
	}
}

// TestBuildAnthropicSystemBlocks_SingleBlock 验证单段时不打 cache_control
// 标记（最后一段永远不标记，作为"边界"）。
func TestBuildAnthropicSystemBlocks_SingleBlock(t *testing.T) {
	blocks := []SystemBlock{{Text: "single", Cacheable: true}}
	got := buildAnthropicSystemBlocks(blocks)
	if len(got) != 1 {
		t.Fatalf("应返回 1 段，实际 %d 段", len(got))
	}
	if got[0].Text != "single" {
		t.Errorf("Text = %q, 期望 %q", got[0].Text, "single")
	}
	// 序列化检查：单段不出现 cache_control 字段
	data, _ := json.Marshal(got[0])
	if strings.Contains(string(data), "cache_control") {
		t.Errorf("单段不应有 cache_control 标记，实际序列化: %s", data)
	}
}

// TestBuildAnthropicSystemBlocks_MultiBlocks 验证多段时前 N-1 段
// 都带 cache_control 标记，最后一段不带。
func TestBuildAnthropicSystemBlocks_MultiBlocks(t *testing.T) {
	blocks := []SystemBlock{
		{Text: "static SP", Cacheable: true},
		{Text: "environment", Cacheable: true},
		{Text: "trailing", Cacheable: true},
	}
	got := buildAnthropicSystemBlocks(blocks)
	if len(got) != 3 {
		t.Fatalf("应返回 3 段，实际 %d 段", len(got))
	}

	// 前两段应有 cache_control 标记
	for i := 0; i < 2; i++ {
		data, _ := json.Marshal(got[i])
		if !strings.Contains(string(data), `"cache_control"`) {
			t.Errorf("第 %d 段应有 cache_control 字段，实际序列化: %s", i, data)
		}
		if !strings.Contains(string(data), `"type":"ephemeral"`) {
			t.Errorf("第 %d 段 cache_control type 应为 ephemeral，实际: %s", i, data)
		}
		if !strings.Contains(string(data), `"ttl":"5m"`) {
			t.Errorf("第 %d 段 cache_control TTL 应为 5m，实际: %s", i, data)
		}
	}

	// 最后一段不应有 cache_control 标记
	lastData, _ := json.Marshal(got[2])
	if strings.Contains(string(lastData), "cache_control") {
		t.Errorf("最后一段不应有 cache_control 标记，实际序列化: %s", lastData)
	}
}

// TestBuildAnthropicSystemBlocks_NonCacheable 验证 Cacheable=false 的段
// 不打 cache_control 标记（即使不是最后一段）。
func TestBuildAnthropicSystemBlocks_NonCacheable(t *testing.T) {
	blocks := []SystemBlock{
		{Text: "static", Cacheable: true},
		{Text: "dynamic-no-cache", Cacheable: false}, // 不应标记
		{Text: "trailing", Cacheable: true},
	}
	got := buildAnthropicSystemBlocks(blocks)
	// 第 0 段（可缓存）应有 cache_control
	data0, _ := json.Marshal(got[0])
	if !strings.Contains(string(data0), "cache_control") {
		t.Errorf("第 0 段（Cacheable=true）应有 cache_control，实际: %s", data0)
	}
	// 第 1 段（Cacheable=false）不应有 cache_control
	data1, _ := json.Marshal(got[1])
	if strings.Contains(string(data1), "cache_control") {
		t.Errorf("第 1 段（Cacheable=false）不应有 cache_control，实际: %s", data1)
	}
}

// ---- 纯函数测试：buildOpenAISystemText ----

// TestBuildOpenAISystemText_Empty 验证空输入返回空字符串。
func TestBuildOpenAISystemText_Empty(t *testing.T) {
	if got := buildOpenAISystemText(nil); got != "" {
		t.Errorf("空输入应返回空字符串，实际: %q", got)
	}
	if got := buildOpenAISystemText([]SystemBlock{}); got != "" {
		t.Errorf("空 slice 应返回空字符串，实际: %q", got)
	}
}

// TestBuildOpenAISystemText_Single 验证单段直接返回其文本。
func TestBuildOpenAISystemText_Single(t *testing.T) {
	got := buildOpenAISystemText([]SystemBlock{
		{Text: "hello"},
	})
	if got != "hello" {
		t.Errorf("单段 = %q, 期望 %q", got, "hello")
	}
}

// TestBuildOpenAISystemText_Multi 验证多段按顺序用 \n\n 连接。
func TestBuildOpenAISystemText_Multi(t *testing.T) {
	got := buildOpenAISystemText([]SystemBlock{
		{Text: "role"},
		{Text: "principles"},
		{Text: "tools"},
	})
	want := "role\n\nprinciples\n\ntools"
	if got != want {
		t.Errorf("多段拼接 = %q, 期望 %q", got, want)
	}
}

// TestBuildOpenAISystemText_SkipEmpty 验证空段被跳过（避免出现孤立分隔符）。
func TestBuildOpenAISystemText_SkipEmpty(t *testing.T) {
	got := buildOpenAISystemText([]SystemBlock{
		{Text: "a"},
		{Text: ""},
		{Text: "b"},
	})
	if got != "a\n\nb" {
		t.Errorf("应跳过空段，得到 %q, 期望 %q", got, "a\n\nb")
	}
}

// ---- 纯函数测试：prependLeadUserMessage ----

// TestPrependLeadUserMessage_Empty 验证空 lead 返回原 messages（不构造空消息）。
func TestPrependLeadUserMessage_Empty(t *testing.T) {
	messages := []Message{
		{Role: RoleUser, Content: []ContentBlock{NewTextBlock("hi")}},
	}
	got := prependLeadUserMessage(messages, "")
	// 同一 slice（无 prepend）
	if len(got) != 1 || got[0].Content[0].ToText() != "hi" {
		t.Errorf("空 lead 应保持原 messages，实际: %+v", got)
	}
}

// TestPrependLeadUserMessage_NonEmpty 验证 lead 拼接到最前。
func TestPrependLeadUserMessage_NonEmpty(t *testing.T) {
	messages := []Message{
		{Role: RoleUser, Content: []ContentBlock{NewTextBlock("user1")}},
		{Role: RoleAssistant, Content: []ContentBlock{NewTextBlock("asst1")}},
	}
	got := prependLeadUserMessage(messages, "<project>AGENTS.md</project>")
	if len(got) != 3 {
		t.Fatalf("应得 3 条，实际 %d 条", len(got))
	}
	if got[0].Role != RoleUser {
		t.Errorf("第 0 条 Role = %q, 期望 user", got[0].Role)
	}
	if got[0].Content[0].ToText() != "<project>AGENTS.md</project>" {
		t.Errorf("第 0 条内容 = %q, 期望 lead", got[0].Content[0].ToText())
	}
	// 后两条保持原序
	if got[1].Content[0].ToText() != "user1" {
		t.Errorf("第 1 条 = %q, 期望 user1", got[1].Content[0].ToText())
	}
	if got[2].Content[0].ToText() != "asst1" {
		t.Errorf("第 2 条 = %q, 期望 asst1", got[2].Content[0].ToText())
	}
}

// ---- 端到端：Anthropic httptest 验证 system 字段是数组 + cache_control ----

// TestAnthropicStreamChat_SystemBlocksAndLeadUserMessage 端到端验证：
//  1. system 字段被序列化为数组（而非字符串）
//  2. 前 N-1 段带 cache_control 标记
//  3. LeadUserMessage 出现在 messages 最前部
func TestAnthropicStreamChat_SystemBlocksAndLeadUserMessage(t *testing.T) {
	var capturedRequest map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 读取请求体
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedRequest)
		// 返回最小的合法 SSE 响应
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		// 最小可用响应：仅 message_start + message_stop，让 SDK 立即结束流
		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_start","message":{"id":"msg_x","type":"message","role":"assistant","content":[],"model":"claude-sonnet","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":50,"output_tokens":1}}}` + "\n\n"))
		_, _ = w.Write([]byte("event: message_stop\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_stop"}` + "\n\n"))
	}))
	defer ts.Close()

	cfg := &config.Config{
		Provider:  "anthropic",
		Model:     "claude-test",
		APIKey:    "test-key",
		BaseURL:   ts.URL,
		MaxTokens: 1024,
		Timeout:   3,
		MaxRetries: 0,
	}
	p := NewAnthropicProvider(cfg)

	sp := SystemPrompt{
		SystemBlocks: []SystemBlock{
			{Text: "STATIC PART", Cacheable: true},
			{Text: "ENV PART", Cacheable: true},
		},
		LeadUserMessage: "<lead>AGENTS.md content</lead>",
	}
	messages := []Message{
		{Role: RoleUser, Content: []ContentBlock{NewTextBlock("user question")}},
	}

	ch, err := p.StreamChat(testContext(), sp, messages, nil)
	if err != nil {
		t.Fatalf("StreamChat 返回错误: %v", err)
	}
	// 消费流以确保请求真正发出
	for range ch {
	}

	if capturedRequest == nil {
		t.Fatal("未捕获到请求体")
	}

	// 1. system 字段应为数组
	sysRaw, ok := capturedRequest["system"]
	if !ok {
		t.Fatal("请求体中无 system 字段")
	}
	sysArr, ok := sysRaw.([]any)
	if !ok {
		t.Fatalf("system 字段应为数组，实际类型: %T", sysRaw)
	}
	if len(sysArr) != 2 {
		t.Fatalf("system 应有 2 段，实际 %d 段", len(sysArr))
	}

	// 2. 前 1 段（index 0）应有 cache_control；最后 1 段不应有
	seg0 := sysArr[0].(map[string]any)
	if _, has := seg0["cache_control"]; !has {
		t.Errorf("system[0] 应有 cache_control 字段，实际: %+v", seg0)
	}
	if seg0["text"] != "STATIC PART" {
		t.Errorf("system[0].text = %v, 期望 STATIC PART", seg0["text"])
	}
	seg1 := sysArr[1].(map[string]any)
	if _, has := seg1["cache_control"]; has {
		t.Errorf("system[1]（最后一段）不应有 cache_control 字段，实际: %+v", seg1)
	}
	if seg1["text"] != "ENV PART" {
		t.Errorf("system[1].text = %v, 期望 ENV PART", seg1["text"])
	}

	// 3. messages[0] 应为 LeadUserMessage
	msgsRaw, ok := capturedRequest["messages"].([]any)
	if !ok {
		t.Fatalf("messages 字段应为数组，实际类型: %T", capturedRequest["messages"])
	}
	if len(msgsRaw) != 2 {
		t.Fatalf("messages 应有 2 条（lead + 1），实际 %d 条", len(msgsRaw))
	}
	firstMsg := msgsRaw[0].(map[string]any)
	if firstMsg["role"] != "user" {
		t.Errorf("messages[0].role = %v, 期望 user", firstMsg["role"])
	}
	firstContent := firstMsg["content"].([]any)
	if len(firstContent) == 0 {
		t.Fatal("messages[0].content 为空")
	}
	firstTextBlock := firstContent[0].(map[string]any)
	if firstTextBlock["text"] != "<lead>AGENTS.md content</lead>" {
		t.Errorf("messages[0].content[0].text = %v, 期望 lead 文本", firstTextBlock["text"])
	}
}

// TestAnthropicStreamChat_NoSystemNoLead 验证无 system 与 lead 时请求体不出现 system 字段。
func TestAnthropicStreamChat_NoSystemNoLead(t *testing.T) {
	var capturedRequest map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedRequest)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","content":[],"model":"claude","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n"))
		_, _ = w.Write([]byte("event: message_stop\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_stop"}` + "\n\n"))
	}))
	defer ts.Close()

	cfg := &config.Config{
		Provider: "anthropic", Model: "claude-test", APIKey: "test-key",
		BaseURL: ts.URL, MaxTokens: 1024, Timeout: 3, MaxRetries: 0,
	}
	p := NewAnthropicProvider(cfg)
	sp := SystemPrompt{} // 完全空
	ch, err := p.StreamChat(testContext(), sp, []Message{
		{Role: RoleUser, Content: []ContentBlock{NewTextBlock("hi")}},
	}, nil)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	for range ch {
	}

	if capturedRequest == nil {
		t.Fatal("未捕获到请求体")
	}
	if _, has := capturedRequest["system"]; has {
		t.Errorf("空 SP 时请求体不应有 system 字段，实际: %+v", capturedRequest["system"])
	}
}

// ---- 端到端：OpenAI httptest 验证 system 字符串拼接 + LeadUserMessage ----

// TestOpenAIStreamChat_SystemBlocksAndLeadUserMessage 端到端验证：
//  1. messages[0] 是 role=system，内容为多段拼接
//  2. messages[1] 是 LeadUserMessage（role=user）
func TestOpenAIStreamChat_SystemBlocksAndLeadUserMessage(t *testing.T) {
	var capturedRequest map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedRequest)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1,\"total_tokens\":11}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer ts.Close()

	cfg := &config.Config{
		Provider: "openai", Model: "gpt-4o", APIKey: "test-key",
		BaseURL: ts.URL, MaxTokens: 1024, Timeout: 3, MaxRetries: 0,
	}
	p := NewOpenAIProvider(cfg)

	sp := SystemPrompt{
		SystemBlocks: []SystemBlock{
			{Text: "ROLE"},
			{Text: "PRINCIPLES"},
		},
		LeadUserMessage: "<lead>AGENTS.md</lead>",
	}
	messages := []Message{
		{Role: RoleUser, Content: []ContentBlock{NewTextBlock("user q")}},
	}

	ch, err := p.StreamChat(testContext(), sp, messages, nil)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	for range ch {
	}

	if capturedRequest == nil {
		t.Fatal("未捕获到请求体")
	}
	msgsRaw, ok := capturedRequest["messages"].([]any)
	if !ok {
		t.Fatalf("messages 应为数组，实际 %T", capturedRequest["messages"])
	}
	if len(msgsRaw) != 3 {
		t.Fatalf("应得 3 条（system + lead + user），实际 %d 条", len(msgsRaw))
	}

	// 1. messages[0] 应为 system
	firstMsg := msgsRaw[0].(map[string]any)
	if firstMsg["role"] != "system" {
		t.Errorf("messages[0].role = %v, 期望 system", firstMsg["role"])
	}
	content, _ := firstMsg["content"].(string)
	if !strings.Contains(content, "ROLE") || !strings.Contains(content, "PRINCIPLES") {
		t.Errorf("messages[0].content 应同时含 ROLE 与 PRINCIPLES，实际: %q", content)
	}
	// 两段之间应有 \n\n 分隔
	if !strings.Contains(content, "ROLE\n\nPRINCIPLES") {
		t.Errorf("两段应以 \\n\\n 分隔，实际: %q", content)
	}

	// 2. messages[1] 应为 LeadUserMessage
	secondMsg := msgsRaw[1].(map[string]any)
	if secondMsg["role"] != "user" {
		t.Errorf("messages[1].role = %v, 期望 user（lead）", secondMsg["role"])
	}
	if !strings.Contains(secondMsg["content"].(string), "<lead>AGENTS.md</lead>") {
		t.Errorf("messages[1].content 应为 lead 文本，实际: %v", secondMsg["content"])
	}
}

// TestOpenAIStreamChat_NoSystemNoLead 验证空 SP 时 messages 不含 system 也不含 lead。
func TestOpenAIStreamChat_NoSystemNoLead(t *testing.T) {
	var capturedRequest map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedRequest)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer ts.Close()

	cfg := &config.Config{
		Provider: "openai", Model: "gpt-4o", APIKey: "test-key",
		BaseURL: ts.URL, MaxTokens: 1024, Timeout: 3, MaxRetries: 0,
	}
	p := NewOpenAIProvider(cfg)
	sp := SystemPrompt{}
	ch, err := p.StreamChat(testContext(), sp, []Message{
		{Role: RoleUser, Content: []ContentBlock{NewTextBlock("hi")}},
	}, nil)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	for range ch {
	}

	msgsRaw := capturedRequest["messages"].([]any)
	if len(msgsRaw) != 1 {
		t.Fatalf("空 SP 时应只有 1 条 user 消息，实际 %d 条", len(msgsRaw))
	}
	if msgsRaw[0].(map[string]any)["role"] != "user" {
		t.Errorf("messages[0].role 应为 user，实际: %v", msgsRaw[0].(map[string]any)["role"])
	}
}
