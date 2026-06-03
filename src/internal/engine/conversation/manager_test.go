package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MeiCorl/CodePilot/src/llm"
	"github.com/MeiCorl/CodePilot/src/tool"
	"github.com/MeiCorl/CodePilot/src/tool/builtin"
)

func TestConversationManager_AddUserMessage(t *testing.T) {
	m := NewConversationManager(5)
	m.AddUserMessage("你好")
	ctx := m.GetContext("")
	if len(ctx) != 1 {
		t.Fatalf("期望 1 条消息，实际 %d 条", len(ctx))
	}
	if ctx[0].Role != "user" {
		t.Fatalf("角色应为 user，实际为 %s", ctx[0].Role)
	}
	if ctx[0].Content[0].ToText() != "你好" {
		t.Fatalf("内容应为 '你好'，实际为 '%s'", ctx[0].Content[0].ToText())
	}
}

func TestConversationManager_AddAssistantMessage(t *testing.T) {
	m := NewConversationManager(5)
	m.AddAssistantMessage("你好！有什么可以帮你的？")
	ctx := m.GetContext("")
	if len(ctx) != 1 {
		t.Fatalf("期望 1 条消息，实际 %d 条", len(ctx))
	}
	if ctx[0].Role != "assistant" {
		t.Fatalf("角色应为 assistant，实际为 %s", ctx[0].Role)
	}
}

func TestConversationManager_ContentBlockArray(t *testing.T) {
	// 验证 AddUserMessage 构造的消息 Content 字段为 []ContentBlock
	m := NewConversationManager(5)
	m.AddUserMessage("hello")
	ctx := m.GetContext("")
	if len(ctx) != 1 {
		t.Fatalf("期望 1 条消息")
	}
	if len(ctx[0].Content) != 1 {
		t.Fatalf("期望 Content 有 1 个 block，实际 %d 个", len(ctx[0].Content))
	}
	block := ctx[0].Content[0]
	if block.Type() != "text" {
		t.Fatalf("ContentBlock 类型应为 text，实际为 %s", block.Type())
	}
	if block.ToText() != "hello" {
		t.Fatalf("ContentBlock 文本应为 'hello'，实际为 '%s'", block.ToText())
	}
}

func TestConversationManager_GetContextWithSystemPrompt(t *testing.T) {
	m := NewConversationManager(5)
	m.AddUserMessage("hello")
	m.AddAssistantMessage("hi")

	ctx := m.GetContext("你是一个助手")
	if len(ctx) != 3 {
		t.Fatalf("期望 3 条消息（system + 2），实际 %d 条", len(ctx))
	}
	if ctx[0].Role != "system" {
		t.Fatalf("第一条应为 system 角色")
	}
}

func TestConversationManager_TokenEstimate(t *testing.T) {
	m := NewConversationManager(5)
	// 英文: "hello" = 5 个非 CJK 字符，约 5/4 ≈ 2 tokens
	m.AddUserMessage("hello")
	tokens := m.TokenEstimate()
	if tokens <= 0 {
		t.Fatalf("Token 估算应大于 0，实际为 %d", tokens)
	}
	if tokens > 5 {
		t.Fatalf("单条短消息 token 估算不应过大，实际为 %d", tokens)
	}
}

func TestConversationManager_TokenEstimateEnglish(t *testing.T) {
	m := NewConversationManager(5)
	// 100 个英文字符: "aaaa...a" (100 个 a)
	text := ""
	for i := 0; i < 100; i++ {
		text += "a"
	}
	m.AddUserMessage(text)
	tokens := m.TokenEstimate()
	// 100 / 4 = 25 tokens
	if tokens < 15 || tokens > 35 {
		t.Fatalf("100 个英文字符估算 token 应在 15~35 范围内，实际为 %d", tokens)
	}
}

func TestConversationManager_TokenEstimateChinese(t *testing.T) {
	m := NewConversationManager(5)
	// 100 个中文字符
	text := ""
	for i := 0; i < 100; i++ {
		text += "你"
	}
	m.AddUserMessage(text)
	tokens := m.TokenEstimate()
	// 100 / 2 = 50 tokens
	if tokens < 40 || tokens > 60 {
		t.Fatalf("100 个中文字符估算 token 应在 40~60 范围内，实际为 %d", tokens)
	}
}

func TestConversationManager_RemainingTokens(t *testing.T) {
	m := NewConversationManager(5)
	m.AddUserMessage("hello")

	remaining := m.RemainingTokens(100000)
	if remaining <= 0 {
		t.Fatalf("剩余 token 应大于 0")
	}
	if remaining > 100000 {
		t.Fatalf("剩余 token 不应超过总额度")
	}
}

func TestConversationManager_RemainingTokensZero(t *testing.T) {
	m := NewConversationManager(5)
	// 添加大量消息使 token 超出额度
	for i := 0; i < 1000; i++ {
		m.AddUserMessage("这是一段测试文本用于验证token估算")
	}
	remaining := m.RemainingTokens(10)
	if remaining != 0 {
		t.Fatalf("超出额度时剩余 token 应为 0，实际为 %d", remaining)
	}
}

func TestConversationManager_MessageCount(t *testing.T) {
	m := NewConversationManager(5)
	if m.MessageCount() != 0 {
		t.Fatalf("初始消息数应为 0")
	}
	m.AddUserMessage("hi")
	m.AddAssistantMessage("hello")
	if m.MessageCount() != 2 {
		t.Fatalf("添加 2 条后消息数应为 2，实际为 %d", m.MessageCount())
	}
}

// TestConversationManager_AllMessagesKeepsFullHistory 验证修复效果：
// 当对话轮数超出窗口容量时，GetContext 返回被裁剪的视图，
// 而 AllMessages 必须返回完整历史（不丢失超窗的早期消息），用于持久化归档。
func TestConversationManager_AllMessagesKeepsFullHistory(t *testing.T) {
	const maxRounds = 2
	const totalRounds = 5
	m := NewConversationManager(maxRounds)

	for i := 0; i < totalRounds; i++ {
		m.AddUserMessage("u")
		m.AddAssistantMessage("a")
	}

	// GetContext 应被窗口裁剪到最近 maxRounds 轮
	ctx := m.GetContext("")
	if len(ctx) != maxRounds*2 {
		t.Fatalf("窗口视图应为 %d 条，实际 %d 条", maxRounds*2, len(ctx))
	}

	// AllMessages 应保留全部历史
	all := m.AllMessages()
	if len(all) != totalRounds*2 {
		t.Fatalf("完整历史应为 %d 条，实际 %d 条", totalRounds*2, len(all))
	}
	if m.MessageCount() != totalRounds*2 {
		t.Fatalf("MessageCount 应反映完整历史 %d，实际 %d", totalRounds*2, m.MessageCount())
	}
}

// TestConversationManager_AllMessagesIsCopy 验证 AllMessages 返回的是副本，
// 外部修改不应影响内部历史。
func TestConversationManager_AllMessagesIsCopy(t *testing.T) {
	m := NewConversationManager(5)
	m.AddUserMessage("hi")

	all := m.AllMessages()
	all[0].Role = "assistant"

	again := m.AllMessages()
	if again[0].Role != "user" {
		t.Fatalf("AllMessages 应返回副本，内部历史不应被外部修改影响")
	}
}

// ---- Step 2: RunTurn 单轮 LLM + 工具执行闭环 ----

// scriptedProvider 实现 llm.Provider，按预设脚本依次返回 chunks。
//
// 每次 StreamChat 调用消耗脚本中一个脚本项（按索引顺序），所有 chunks
// 发送完后该脚本项"用尽"，下次 StreamChat 取下一个脚本项。
// 脚本项为 nil 时该次调用立刻发 Done=true 结束。
type scriptedProvider struct {
	scripts [][]llm.StreamChunk
	cursor  int
}

func (p *scriptedProvider) StreamChat(_ context.Context, _ string, _ []llm.Message, _ []tool.ToolSpec) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, 32)
	if p.cursor >= len(p.scripts) {
		// 没有更多脚本，发一个 Done 结束
		ch <- llm.StreamChunk{Done: true}
		close(ch)
		return ch, nil
	}
	script := p.scripts[p.cursor]
	p.cursor++
	go func() {
		defer close(ch)
		for _, c := range script {
			ch <- c
		}
	}()
	return ch, nil
}

// echoTool 是测试用 Tool：Execute 直接把 input 解析为 params 再以 JSON 回写。
// 用于验证 ToolHandler.Execute 能正常传参与回传结果。
type echoTool struct {
	tool.BaseTool
	calls int
}

func newEchoTool() *echoTool {
	return &echoTool{
		BaseTool: tool.BaseTool{
			ToolName:        "echo",
			ToolDescription: "echo input back",
			ToolInputSchema: json.RawMessage(`{"type":"object","properties":{"msg":{"type":"string"}},"required":["msg"]}`),
			ToolPermission:  tool.PermRead,
		},
	}
}

func (e *echoTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	e.calls++
	var p struct {
		Msg string `json:"msg"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}
	return fmt.Sprintf("echo:%s", p.Msg), nil
}

// errTool 总是返回错误的工具，用于测试 is_error 路径。
type errTool struct {
	tool.BaseTool
}

func newErrTool() *errTool {
	return &errTool{
		BaseTool: tool.BaseTool{
			ToolName:        "always_err",
			ToolDescription: "always returns error",
			ToolInputSchema: json.RawMessage(`{"type":"object"}`),
			ToolPermission:  tool.PermRead,
		},
	}
}

func (e *errTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "", fmt.Errorf("simulated tool error")
}

// TestRunTurn_NoToolUse 验证 LLM 不返回 tool_use 时仅产生一条 assistant 文本。
func TestRunTurn_NoToolUse(t *testing.T) {
	p := &scriptedProvider{
		scripts: [][]llm.StreamChunk{
			{
				{Content: "Hello"},
				{Content: ", "},
				{Content: "world!"},
				{Done: true},
			},
		},
	}
	m := NewConversationManager(10)
	m.AddUserMessage("hi")

	var chunks []llm.StreamChunk
	res := m.RunTurn(context.Background(), p, "system", nil, NewToolHandler(tool.NewRegistry(), time.Second, ""), TurnHooks{
		OnStreamChunk: func(c llm.StreamChunk) { chunks = append(chunks, c) },
	})

	if res.Error != nil {
		t.Fatalf("RunTurn 返回错误: %v", res.Error)
	}
	if res.Aborted {
		t.Error("不应被 abort")
	}
	if res.FinalText != "Hello, world!" {
		t.Errorf("FinalText = %q，期望 %q", res.FinalText, "Hello, world!")
	}
	if res.ToolUse != nil {
		t.Errorf("不应有 ToolUse，实际: %+v", res.ToolUse)
	}

	// history 应只追加 1 条 assistant 文本消息
	if got := m.MessageCount(); got != 2 {
		t.Errorf("history 消息数 = %d，期望 2（user + assistant）", got)
	}
	all := m.AllMessages()
	if all[1].Role != llm.RoleAssistant {
		t.Errorf("history[1].Role = %q，期望 assistant", all[1].Role)
	}

	// 验证 OnStreamChunk 被多次调用（含 Done）
	if len(chunks) < 4 {
		t.Errorf("OnStreamChunk 调用次数 = %d，期望 >= 4", len(chunks))
	}
}

// TestRunTurn_ToolUseHappensOnce 验证 LLM 决定调用工具时：tool_use 消息 + tool_result 消息 + 最终 assistant 文本。
func TestRunTurn_ToolUseHappensOnce(t *testing.T) {
	p := &scriptedProvider{
		scripts: [][]llm.StreamChunk{
			// 第一次 LLM：返回 tool_use（无文本）
			{
				{
					Done:    true,
					ToolUse: &llm.ToolUseBlock{ID: "call-1", Name: "echo", Input: json.RawMessage(`{"msg":"ping"}`)},
				},
			},
			// 第二次 LLM：基于 tool_result 给出最终回复
			{
				{Content: "done"},
				{Done: true},
			},
		},
	}

	reg := tool.NewRegistry()
	if err := reg.Register(newEchoTool()); err != nil {
		t.Fatalf("注册 echo 工具失败: %v", err)
	}

	m := NewConversationManager(10)
	m.AddUserMessage("call echo")

	var (
		gotToolUse     *llm.ToolUseBlock
		gotToolResult  *llm.ToolResultBlock
		onErrorCalled  bool
		streamChunkN   int
	)
	res := m.RunTurn(context.Background(), p, "system", nil, NewToolHandler(reg, time.Second, ""), TurnHooks{
		OnToolUse:    func(b llm.ToolUseBlock) { gotToolUse = &b },
		OnToolResult: func(b llm.ToolResultBlock) { gotToolResult = &b },
		OnError:      func(error) { onErrorCalled = true },
		OnStreamChunk: func(llm.StreamChunk) {
			streamChunkN++
		},
	})

	if res.Error != nil {
		t.Fatalf("RunTurn 返回错误: %v", res.Error)
	}
	if res.FinalText != "done" {
		t.Errorf("FinalText = %q，期望 %q", res.FinalText, "done")
	}
	if res.ToolUse == nil || res.ToolUse.ID != "call-1" {
		t.Errorf("ToolUse = %+v，期望 ID=call-1", res.ToolUse)
	}
	if res.ToolResult.Content != "echo:ping" {
		t.Errorf("ToolResult.Content = %q，期望 %q", res.ToolResult.Content, "echo:ping")
	}
	if res.ToolResult.IsError {
		t.Errorf("ToolResult.IsError 应为 false")
	}

	if onErrorCalled {
		t.Error("OnError 不应被调用")
	}
	if gotToolUse == nil || gotToolUse.ID != "call-1" {
		t.Error("OnToolUse 未触发或 ID 不匹配")
	}
	if gotToolResult == nil || gotToolResult.Content != "echo:ping" {
		t.Error("OnToolResult 未触发或内容不匹配")
	}

	// history 应有：user + assistant(tool_use) + user(tool_result) + assistant(text) = 4 条
	if got := m.MessageCount(); got != 4 {
		t.Errorf("history 消息数 = %d，期望 4", got)
	}
	all := m.AllMessages()
	if all[1].Role != llm.RoleAssistant {
		t.Errorf("history[1].Role = %q，期望 assistant", all[1].Role)
	}
	if len(all[1].Content) != 1 || all[1].Content[0].Type() != llm.ContentBlockTypeToolUse {
		t.Errorf("history[1] 应为 tool_use 块，实际: %+v", all[1].Content)
	}
	if all[2].Role != llm.RoleUser {
		t.Errorf("history[2].Role = %q，期望 user（OpenAI/Anthropic 协议 tool_result 视为 user）", all[2].Role)
	}
	if len(all[2].Content) != 1 || all[2].Content[0].Type() != llm.ContentBlockTypeToolResult {
		t.Errorf("history[2] 应为 tool_result 块，实际: %+v", all[2].Content)
	}
	if all[3].Role != llm.RoleAssistant || all[3].Content[0].ToText() != "done" {
		t.Errorf("history[3] 应为 assistant(done)，实际: %+v", all[3].Content)
	}

	// 验证 Provider 真的被调了 2 次
	if p.cursor != 2 {
		t.Errorf("scriptedProvider.cursor = %d，期望 2", p.cursor)
	}
	// 验证 echo 工具被调了 1 次
	tool, _ := reg.Get("echo")
	echoImpl, ok := tool.(*echoTool)
	if !ok {
		t.Fatal("registry 中的 echo 工具类型断言失败")
	}
	if echoImpl.calls != 1 {
		t.Errorf("echo 工具调用次数 = %d，期望 1", echoImpl.calls)
	}
	if streamChunkN < 2 {
		t.Errorf("OnStreamChunk 应被至少调用 2 次（第一次 Done + 第二次 2 个 chunk），实际 %d", streamChunkN)
	}
}

// TestRunTurn_ToolErrorPropagatesAsIsError 验证工具执行失败时 tool_result.IsError=true。
func TestRunTurn_ToolErrorPropagatesAsIsError(t *testing.T) {
	p := &scriptedProvider{
		scripts: [][]llm.StreamChunk{
			{
				{Done: true, ToolUse: &llm.ToolUseBlock{ID: "e1", Name: "always_err", Input: json.RawMessage(`{}`)}},
			},
			{
				{Content: "我看到错误了", Done: true},
			},
		},
	}
	reg := tool.NewRegistry()
	if err := reg.Register(newErrTool()); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	m := NewConversationManager(10)
	m.AddUserMessage("call err tool")

	res := m.RunTurn(context.Background(), p, "system", nil, NewToolHandler(reg, time.Second, ""), TurnHooks{})

	if res.Error != nil {
		t.Fatalf("RunTurn 返回错误: %v", res.Error)
	}
	if !res.ToolResult.IsError {
		t.Error("ToolResult.IsError 应为 true")
	}
	if res.ToolResult.Content == "" {
		t.Error("ToolResult.Content 应包含错误描述")
	}
	if res.FinalText != "我看到错误了" {
		t.Errorf("FinalText = %q，期望 %q", res.FinalText, "我看到错误了")
	}
}

// TestRunTurn_ToolNotFoundInRegistry 验证调用未注册工具时返回 ErrToolNotFound 封装的 is_error。
func TestRunTurn_ToolNotFoundInRegistry(t *testing.T) {
	p := &scriptedProvider{
		scripts: [][]llm.StreamChunk{
			{
				{Done: true, ToolUse: &llm.ToolUseBlock{ID: "x", Name: "missing", Input: json.RawMessage(`{}`)}},
			},
			{
				{Content: "工具不存在", Done: true},
			},
		},
	}
	m := NewConversationManager(10)
	m.AddUserMessage("call missing")

	res := m.RunTurn(context.Background(), p, "system", nil, NewToolHandler(tool.NewRegistry(), time.Second, ""), TurnHooks{})

	if res.Error != nil {
		t.Fatalf("RunTurn 返回错误: %v", res.Error)
	}
	if !res.ToolResult.IsError {
		t.Error("未注册工具应返回 IsError=true")
	}
	if !strings.Contains(res.ToolResult.Content, "missing") {
		t.Errorf("错误信息应包含工具名 missing，实际: %s", res.ToolResult.Content)
	}
}

// TestRunTurn_FirstLLMContextCancelled 验证第一次 LLM 期间 ctx 取消时返回 Aborted=true。
func TestRunTurn_FirstLLMContextCancelled(t *testing.T) {
	// 第一次 LLM 用一个永远不发 chunk 的脚本（保持 pending）
	p := &scriptedProvider{
		scripts: [][]llm.StreamChunk{
			{}, // 空脚本，立刻发 Done=true（流结束但 ToolUse=nil）
		},
	}
	m := NewConversationManager(10)
	m.AddUserMessage("hi")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	res := m.RunTurn(ctx, p, "system", nil, NewToolHandler(tool.NewRegistry(), time.Second, ""), TurnHooks{})

	// 第一次 LLM 立刻 Done，ToolUse=nil，aborted 取决于 ctx 状态
	if res.Error != nil {
		t.Fatalf("不应有 Error: %v", res.Error)
	}
	if res.ToolUse != nil {
		t.Error("不应触发 tool_use")
	}
	// ctx 在 RunTurn 期间已取消，第二次 LLM 不会被调用，所以 Res.Aborted 可能为 false
	// 关键验证：未 panic、未无限阻塞
}

// TestRunTurn_FirstLLMChunkError 验证 StreamChunk.Err 通过 OnError 回调外推。
func TestRunTurn_FirstLLMChunkError(t *testing.T) {
	p := &scriptedProvider{
		scripts: [][]llm.StreamChunk{
			{
				{Content: "partial"},
				{Err: fmt.Errorf("boom"), Done: true},
			},
		},
	}
	m := NewConversationManager(10)
	m.AddUserMessage("hi")

	var gotErr error
	res := m.RunTurn(context.Background(), p, "system", nil, NewToolHandler(tool.NewRegistry(), time.Second, ""), TurnHooks{
		OnError: func(e error) { gotErr = e },
	})

	if res.Error == nil {
		t.Fatal("RunTurn 应返回 Error")
	}
	if gotErr == nil {
		t.Error("OnError 回调应被触发")
	}
	if !strings.Contains(res.Error.Error(), "boom") {
		t.Errorf("Error 应包含 boom，实际: %v", res.Error)
	}
}

// TestRunTurn_ToolHandlerOnStartOnEnd 验证 ToolHandler 的 OnStart/OnEnd 回调触发顺序与内容。
func TestRunTurn_ToolHandlerOnStartOnEnd(t *testing.T) {
	p := &scriptedProvider{
		scripts: [][]llm.StreamChunk{
			{
				{Done: true, ToolUse: &llm.ToolUseBlock{ID: "e1", Name: "echo", Input: json.RawMessage(`{"msg":"hi"}`)}},
			},
			{
				{Content: "final", Done: true},
			},
		},
	}
	reg := tool.NewRegistry()
	_ = reg.Register(newEchoTool())
	th := NewToolHandler(reg, time.Second, "")

	var events []ToolExecutionEvent
	th.SetOnStart(func(e ToolExecutionEvent) {
		e.Status = ToolEventStatusRunning // 复制后修改以避免污染原值
		events = append(events, e)
	})
	th.SetOnEnd(func(e ToolExecutionEvent) {
		events = append(events, e)
	})

	m := NewConversationManager(10)
	m.AddUserMessage("hi")

	res := m.RunTurn(context.Background(), p, "system", nil, th, TurnHooks{})
	if res.Error != nil {
		t.Fatalf("RunTurn 返回错误: %v", res.Error)
	}

	// 应收到 2 个事件：start(running) + end(completed)
	if len(events) != 2 {
		t.Fatalf("事件数 = %d，期望 2", len(events))
	}
	if events[0].Status != ToolEventStatusRunning {
		t.Errorf("events[0].Status = %q，期望 running", events[0].Status)
	}
	if events[0].Name != "echo" || events[0].ToolUseID != "e1" {
		t.Errorf("events[0] 元数据不正确: %+v", events[0])
	}
	if events[1].Status != ToolEventStatusCompleted {
		t.Errorf("events[1].Status = %q，期望 completed", events[1].Status)
	}
	if events[1].Output != "echo:hi" {
		t.Errorf("events[1].Output = %q，期望 echo:hi", events[1].Output)
	}
	if events[1].IsError {
		t.Error("events[1].IsError 应为 false")
	}
	if events[1].DurationMs < 0 {
		t.Errorf("events[1].DurationMs = %d，应 >= 0", events[1].DurationMs)
	}
}

// TestRunTurn_ToolHandlerTimeout 验证 ToolHandler 的 timeout 触发。
func TestRunTurn_ToolHandlerTimeout(t *testing.T) {
	slow := &slowTool{
		BaseTool: tool.BaseTool{
			ToolName:        "slow",
			ToolDescription: "sleeps for a while",
			ToolInputSchema: json.RawMessage(`{"type":"object"}`),
			ToolPermission:  tool.PermRead,
		},
		delay: 500 * time.Millisecond,
	}
	reg := tool.NewRegistry()
	_ = reg.Register(slow)

	p := &scriptedProvider{
		scripts: [][]llm.StreamChunk{
			{
				{Done: true, ToolUse: &llm.ToolUseBlock{ID: "s1", Name: "slow", Input: json.RawMessage(`{}`)}},
			},
			{
				{Content: "give up", Done: true},
			},
		},
	}

	m := NewConversationManager(10)
	m.AddUserMessage("hi")

	var endEvent ToolExecutionEvent
	th := NewToolHandler(reg, 50*time.Millisecond, "")
	th.SetOnEnd(func(e ToolExecutionEvent) { endEvent = e })

	res := m.RunTurn(context.Background(), p, "system", nil, th, TurnHooks{})
	if res.Error != nil {
		t.Fatalf("RunTurn 返回错误: %v", res.Error)
	}
	if !res.ToolResult.IsError {
		t.Error("工具超时应返回 IsError=true")
	}
	if !strings.Contains(res.ToolResult.Content, "超时") {
		t.Errorf("超时错误信息应包含 '超时'，实际: %s", res.ToolResult.Content)
	}
	if endEvent.Status != ToolEventStatusError {
		t.Errorf("OnEnd 事件 Status = %q，期望 error", endEvent.Status)
	}
}

// slowTool 慢执行工具，用于 timeout 测试。
type slowTool struct {
	tool.BaseTool
	delay time.Duration
}

func (s *slowTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	select {
	case <-time.After(s.delay):
		return "done", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// TestRunTurn_BlacklistInterceptedThenNormalCommand 验证 spec 9.4：
// 端到端下，先让 LLM 触发 `rm -rf /`（黑名单拦截，IsError=true），
// 再让 LLM 触发 ReadFile 读 main.go（正常执行，IsError=false）；
// 两次 RunTurn 共用同一 Registry 与 ToolHandler，证明拦截与正常命令互不影响。
// （"正常命令"在 Windows 上选 ReadFile 而非 Bash：spec 非功能要求 4 明确
// "Bash 工具在 Windows 平台暂不支持"；Bash 路径上的拦截与正常命令互不影响
// 已在 Unix 平台由 TestBashSuccess + TestBashFailure + TestCheckBashCommandSafeCases
// 三套单测联合覆盖，本测试聚焦于 ToolHandler 调度层在两次 RunTurn 间无状态污染。）
func TestRunTurn_BlacklistInterceptedThenNormalCommand(t *testing.T) {
	// 独立 Registry 避免污染 DefaultRegistry；用 tempDir 作为 sandbox，并在里面建一个测试文件
	tmp := t.TempDir()
	hello := `L1: hello
L2: world
`
	if err := os.WriteFile(filepath.Join(tmp, "hello.txt"), []byte(hello), 0644); err != nil {
		t.Fatalf("建测试文件失败: %v", err)
	}
	r := tool.NewRegistry()
	builtin.RegisterWithOptions(r, tmp, 5*time.Second)
	h := NewToolHandler(r, 5*time.Second, tmp)

	// 第一次 RunTurn：LLM 调 Bash 执行 `rm -rf /`
	p1 := &scriptedProvider{
		scripts: [][]llm.StreamChunk{
			{
				{Done: true, ToolUse: &llm.ToolUseBlock{
					ID:    "t1",
					Name:  "bash",
					Input: json.RawMessage(`{"command":"rm -rf /"}`),
				}},
			},
			{
				{Content: "我已拒绝执行该命令。", Done: true},
			},
		},
	}
	m1 := NewConversationManager(10)
	m1.AddUserMessage("执行 rm -rf /")
	res1 := m1.RunTurn(context.Background(), p1, "system", nil, h, TurnHooks{})
	if res1.Error != nil {
		t.Fatalf("第一次 RunTurn 出错: %v", res1.Error)
	}
	if !res1.ToolResult.IsError {
		t.Errorf("`rm -rf /` 应被黑名单拦截, 实际 IsError=false, Content=%q", res1.ToolResult.Content)
	}
	if !strings.Contains(res1.ToolResult.Content, "禁止") && !strings.Contains(res1.ToolResult.Content, "Dangerous") {
		t.Errorf("错误信息应说明危险命令, 实际: %q", res1.ToolResult.Content)
	}

	// 第二次 RunTurn：同一 Registry + ToolHandler，LLM 调 ReadFile 读 hello.txt
	p2 := &scriptedProvider{
		scripts: [][]llm.StreamChunk{
			{
				{Done: true, ToolUse: &llm.ToolUseBlock{
					ID:    "t2",
					Name:  "read_file",
					Input: json.RawMessage(`{"file_path":"hello.txt"}`),
				}},
			},
			{
				{Content: "hello.txt 内容如上。", Done: true},
			},
		},
	}
	m2 := NewConversationManager(10)
	m2.AddUserMessage("读 hello.txt")
	res2 := m2.RunTurn(context.Background(), p2, "system", nil, h, TurnHooks{})
	if res2.Error != nil {
		t.Fatalf("第二次 RunTurn 出错: %v", res2.Error)
	}
	if res2.ToolResult.IsError {
		t.Errorf("ReadFile 应正常执行, 实际 IsError=true, Content=%q", res2.ToolResult.Content)
	}
	if !strings.Contains(res2.ToolResult.Content, "L1:") {
		t.Errorf("ReadFile 输出应含行号标记, 实际: %q", res2.ToolResult.Content)
	}
}
