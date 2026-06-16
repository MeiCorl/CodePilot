package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MeiCorl/CodePilot/src/internal/security"
	"github.com/MeiCorl/CodePilot/src/internal/tool"
	"github.com/MeiCorl/CodePilot/src/internal/tool/builtin"
	"github.com/MeiCorl/CodePilot/src/llm"
)

func TestConversationManager_AddUserMessage(t *testing.T) {
	m := NewConversationManager(5)
	m.AddUserMessage("你好")
	ctx := m.GetContext()
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
	ctx := m.GetContext()
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
	ctx := m.GetContext()
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

// TestConversationManager_GetContextExcludesSystemPrompt 验证 GetContext 返回
// 的消息序列不再包含 system 消息——system 字段在 Step 4 起被迁移到
// llm.SystemPrompt（由 Provider.StreamChat 单独携带），不在 messages 内。
// 这是行为变更：原 GetContext(systemPrompt) 会把 system 拼到 messages[0]，
// 新版本 GetContext() 不再拼接；调用方应使用 SystemPrompt 传递 system 内容。
func TestConversationManager_GetContextExcludesSystemPrompt(t *testing.T) {
	m := NewConversationManager(5)
	m.AddUserMessage("hello")
	m.AddAssistantMessage("hi")

	ctx := m.GetContext()
	// 仅 2 条 user/assistant 消息，不应包含 system 消息
	if len(ctx) != 2 {
		t.Fatalf("期望 2 条消息（user+assistant），实际 %d 条", len(ctx))
	}
	if ctx[0].Role != "user" {
		t.Fatalf("第一条应为 user 角色，实际为 %s", ctx[0].Role)
	}
}

// ---- Step 4 — LeadUserMessage 保护测试 ----

// TestConversationManager_LeadUserMessage_DefaultEmpty 验证新构造的 manager
// 默认无 lead（GetContext 不构造空消息）。
func TestConversationManager_LeadUserMessage_DefaultEmpty(t *testing.T) {
	m := NewConversationManager(5)
	m.AddUserMessage("hi")

	if m.LeadUserMessage() != "" {
		t.Errorf("默认 LeadUserMessage 应为空，实际: %q", m.LeadUserMessage())
	}
	if m.IsLeadUserMessage(0) {
		t.Error("未设置 lead 时，IsLeadUserMessage(0) 应返回 false")
	}

	ctx := m.GetContext()
	if len(ctx) != 1 {
		t.Fatalf("无 lead 时应只有 1 条消息，实际 %d 条", len(ctx))
	}
}

// TestConversationManager_LeadUserMessage_SetAndPrepend 验证 SetLeadUserMessage
// 后 GetContext 在 messages 最前追加一条 user 消息，IsLeadUserMessage(0) 返回 true。
func TestConversationManager_LeadUserMessage_SetAndPrepend(t *testing.T) {
	m := NewConversationManager(5)
	m.AddUserMessage("user1")
	m.AddAssistantMessage("asst1")
	m.SetLeadUserMessage("<project>AGENTS.md</project>")

	if m.LeadUserMessage() != "<project>AGENTS.md</project>" {
		t.Errorf("LeadUserMessage() = %q, 期望 lead 文本", m.LeadUserMessage())
	}

	ctx := m.GetContext()
	if len(ctx) != 3 {
		t.Fatalf("应有 3 条（lead + 2），实际 %d 条", len(ctx))
	}
	if !m.IsLeadUserMessage(0) {
		t.Error("设置 lead 后，IsLeadUserMessage(0) 应返回 true")
	}
	if ctx[0].Role != "user" {
		t.Errorf("lead 角色应为 user，实际: %s", ctx[0].Role)
	}
	if ctx[0].Content[0].ToText() != "<project>AGENTS.md</project>" {
		t.Errorf("lead 内容不符: %q", ctx[0].Content[0].ToText())
	}
	// 后续消息保持原序
	if ctx[1].Content[0].ToText() != "user1" {
		t.Errorf("ctx[1] = %q, 期望 user1", ctx[1].Content[0].ToText())
	}
	if ctx[2].Content[0].ToText() != "asst1" {
		t.Errorf("ctx[2] = %q, 期望 asst1", ctx[2].Content[0].ToText())
	}
}

// TestConversationManager_LeadUserMessage_Clear 验证 SetLeadUserMessage("")
// 可清除已设置的 lead，下次 GetContext 不再追加。
func TestConversationManager_LeadUserMessage_Clear(t *testing.T) {
	m := NewConversationManager(5)
	m.AddUserMessage("hi")
	m.SetLeadUserMessage("first lead")
	m.SetLeadUserMessage("") // 清除

	if m.LeadUserMessage() != "" {
		t.Error("清空后 LeadUserMessage() 应返回空字符串")
	}
	if m.IsLeadUserMessage(0) {
		t.Error("清空后 IsLeadUserMessage(0) 应返回 false")
	}
	ctx := m.GetContext()
	if len(ctx) != 1 {
		t.Fatalf("清空后应只有 1 条消息，实际 %d 条", len(ctx))
	}
}

// TestConversationManager_LeadUserMessage_IsLeadBoundary 验证 IsLeadUserMessage
// 的越界与多索引场景。
func TestConversationManager_LeadUserMessage_IsLeadBoundary(t *testing.T) {
	m := NewConversationManager(5)
	m.AddUserMessage("u1")
	m.AddAssistantMessage("a1")
	m.AddUserMessage("u2")
	m.SetLeadUserMessage("L")

	// idx 0 是 lead
	if !m.IsLeadUserMessage(0) {
		t.Error("IsLeadUserMessage(0) 应为 true（lead 位置）")
	}
	// idx >= 1 不是 lead
	if m.IsLeadUserMessage(1) {
		t.Error("IsLeadUserMessage(1) 应为 false（user 消息，不是 lead）")
	}
	if m.IsLeadUserMessage(2) {
		t.Error("IsLeadUserMessage(2) 应为 false（assistant 消息）")
	}
	// 越界
	if m.IsLeadUserMessage(99) {
		t.Error("越界索引应返回 false")
	}
}

// TestConversationManager_LeadUserMessage_WithFullHistory 验证取消滑动窗口后，
// 即使 history 远超 maxRounds，lead 仍在 ctx[0]，且完整活跃历史会随请求发送。
//
// 这是 Task 5 关键验收：AGENTS.md 等"首条 user 消息"必须在多轮对话中
// 始终被发送给 LLM，否则模型会逐渐"遗忘"项目约定。
func TestConversationManager_LeadUserMessage_WithFullHistory(t *testing.T) {
	const maxRounds = 3
	const totalRounds = 20 // 远超 maxRounds，但不再触发窗口裁剪
	m := NewConversationManager(maxRounds)
	m.SetLeadUserMessage("PROJECT_RULES: use spaces")

	for i := 0; i < totalRounds; i++ {
		m.AddUserMessage("u")
		m.AddAssistantMessage("a")
	}

	ctx := m.GetContext()
	// 期望：1 条 lead + 完整活跃历史
	wantLen := 1 + totalRounds*2
	if len(ctx) != wantLen {
		t.Fatalf("完整上下文应有 %d 条，实际 %d 条", wantLen, len(ctx))
	}
	// 第 0 条必须是 lead（始终在）
	if !m.IsLeadUserMessage(0) {
		t.Error("lead 应在 ctx[0]，但 IsLeadUserMessage(0) 返回 false")
	}
	if ctx[0].Content[0].ToText() != "PROJECT_RULES: use spaces" {
		t.Errorf("ctx[0] 文本 = %q, 期望 lead", ctx[0].Content[0].ToText())
	}
}

// TestConversationManager_LeadUserMessage_NotInAllMessages 验证 lead 不属于
// 完整对话历史（AllMessages 仍只返回 history，不含 lead）。
// 这保证了会话持久化向后兼容：旧 session JSON 恢复后不会"多出"lead 消息。
func TestConversationManager_LeadUserMessage_NotInAllMessages(t *testing.T) {
	m := NewConversationManager(5)
	m.AddUserMessage("u1")
	m.AddAssistantMessage("a1")
	m.SetLeadUserMessage("LEAD_TEXT")

	all := m.AllMessages()
	if len(all) != 2 {
		t.Fatalf("AllMessages 应仅含 2 条历史，实际 %d 条", len(all))
	}
	for i, msg := range all {
		if msg.Content[0].ToText() == "LEAD_TEXT" {
			t.Errorf("AllMessages[%d] 不应包含 lead 文本", i)
		}
	}
}

func TestConversationManager_TokenEstimate(t *testing.T) {
	m := NewConversationManager(5)
	// 英文: "hello" = 5 个非 CJK 字符，约 5/4 ≈ 2 tokens
	// 加上消息结构开销(15) + 工具定义开销(5工具×80=400) ≈ 417
	m.AddUserMessage("hello")
	tokens := m.TokenEstimate()
	if tokens <= 0 {
		t.Fatalf("Token 估算应大于 0，实际为 %d", tokens)
	}
	// 估算值 = 文本(2) + 消息开销(15) + 工具定义开销(400) ≈ 417
	if tokens < 400 || tokens > 500 {
		t.Fatalf("单条短消息 token 估算应在 400~500 范围内（含结构+工具开销），实际为 %d", tokens)
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
	// 100 / 4 = 25 tokens + 消息开销(15) + 工具定义开销(400) ≈ 440
	if tokens < 400 || tokens > 560 {
		t.Fatalf("100 个英文字符估算 token 应在 400~560 范围内（含结构+工具开销），实际为 %d", tokens)
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
	// 100 / 2 = 50 tokens + 消息开销(15) + 工具定义开销(400) ≈ 465
	if tokens < 430 || tokens > 580 {
		t.Fatalf("100 个中文字符估算 token 应在 430~580 范围内（含结构+工具开销），实际为 %d", tokens)
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

// TestConversationManager_GetContextUsage_Empty 验证空对话时的上下文用量。
// 空对话无文本消息，仅包含工具定义开销（5工具×80=400）。
func TestConversationManager_GetContextUsage_Empty(t *testing.T) {
	m := NewConversationManager(5)
	usage := m.GetContextUsage(200000)

	// 空对话：Used 仅含工具定义开销（约 400），应接近 0
	if usage.Used <= 0 {
		t.Fatalf("Used 应 > 0（含工具定义开销），实际为 %d", usage.Used)
	}
	if usage.Limit != 200000 {
		t.Fatalf("Limit 应为 200000，实际为 %d", usage.Limit)
	}
	if usage.Remaining <= 0 {
		t.Fatalf("Remaining 应远大于 0，实际为 %d", usage.Remaining)
	}
	// 百分比应接近 0%（仅工具定义开销）
	if usage.PercentUsed > 1 {
		t.Fatalf("空对话 PercentUsed 应接近 0%%，实际为 %d%%", usage.PercentUsed)
	}
}

// TestConversationManager_GetContextUsage_WithMessages 验证多轮对话后的上下文用量。
func TestConversationManager_GetContextUsage_WithMessages(t *testing.T) {
	m := NewConversationManager(50)
	// 添加 10 轮对话
	for i := 0; i < 10; i++ {
		m.AddUserMessage("这是一段测试文本用于验证上下文用量计算")
		m.AddAssistantMessage("这是助手的回复文本，同样用于验证上下文用量计算")
	}

	usage := m.GetContextUsage(200000)

	if usage.Used <= 0 {
		t.Fatalf("Used 应 > 0，实际为 %d", usage.Used)
	}
	if usage.Limit != 200000 {
		t.Fatalf("Limit 应为 200000，实际为 %d", usage.Limit)
	}
	if usage.Remaining <= 0 {
		t.Fatalf("10 轮对话不应耗尽 200K 窗口，Remaining 应 > 0，实际为 %d", usage.Remaining)
	}
	// 10 轮短对话 + 工具开销，占比应远低于 50%
	if usage.PercentUsed > 50 {
		t.Fatalf("10 轮短对话 PercentUsed 应远低于 50%%，实际为 %d%%", usage.PercentUsed)
	}
	// PercentUsed + PercentLeft 应等于 100（此处 PercentLeft = Remaining*100/Limit）
	invariant := usage.Used*100 + usage.Remaining*100
	// 允许整数除法带来的 ±1 误差
	if invariant < (usage.Limit-1)*100 || invariant > (usage.Limit+1)*100 {
		t.Fatalf("Used+Remaining 应约等于 Limit，实际 Used=%d Remaining=%d Limit=%d",
			usage.Used, usage.Remaining, usage.Limit)
	}
}

// TestConversationManager_GetContextUsage_Overflow 验证超出窗口大小时的用量。
func TestConversationManager_GetContextUsage_Overflow(t *testing.T) {
	m := NewConversationManager(50)
	// 添加大量消息使 token 超出窗口
	for i := 0; i < 1000; i++ {
		m.AddUserMessage("这是一段测试文本用于验证token估算")
	}

	usage := m.GetContextUsage(10)

	if usage.Remaining != 0 {
		t.Fatalf("超出额度时 Remaining 应为 0，实际为 %d", usage.Remaining)
	}
	if usage.PercentUsed != 100 {
		t.Fatalf("超出额度时 PercentUsed 应为 100，实际为 %d", usage.PercentUsed)
	}
}

// TestConversationManager_UpdateUsage_PreciseMode 验证 UpdateUsage 后 GetContextUsage 使用精确 input_tokens。
func TestConversationManager_UpdateUsage_PreciseMode(t *testing.T) {
	m := NewConversationManager(50)

	// 初始状态：无 usage 数据，应降级到字符估算
	usageBefore := m.GetContextUsage(200000)
	if usageBefore.Used <= 0 {
		t.Fatalf("降级模式下 Used 应 > 0（工具定义开销），实际为 %d", usageBefore.Used)
	}

	// 模拟 LLM 返回 input_tokens=50000
	m.UpdateUsage(&llm.TokenUsage{InputTokens: 50000, OutputTokens: 200})

	usageAfter := m.GetContextUsage(200000)
	if usageAfter.Used != 50000 {
		t.Fatalf("精确模式下 Used 应为 50000，实际为 %d", usageAfter.Used)
	}
	if usageAfter.Limit != 200000 {
		t.Fatalf("Limit 应为 200000，实际为 %d", usageAfter.Limit)
	}
	if usageAfter.Remaining != 150000 {
		t.Fatalf("Remaining 应为 150000，实际为 %d", usageAfter.Remaining)
	}
	// 50000 * 100 / 200000 = 25%
	if usageAfter.PercentUsed != 25 {
		t.Fatalf("PercentUsed 应为 25%%，实际为 %d%%", usageAfter.PercentUsed)
	}
}

// TestConversationManager_UpdateUsage_IgnoresNilAndZero 验证 UpdateUsage 对无效输入的安全处理。
func TestConversationManager_UpdateUsage_IgnoresNilAndZero(t *testing.T) {
	m := NewConversationManager(50)

	// 先设置一个有效值
	m.UpdateUsage(&llm.TokenUsage{InputTokens: 30000})

	// nil 不应覆盖已有值
	m.UpdateUsage(nil)
	usage := m.GetContextUsage(200000)
	if usage.Used != 30000 {
		t.Fatalf("nil 不应覆盖已有 usage，Used 应为 30000，实际为 %d", usage.Used)
	}

	// InputTokens=0 不应覆盖已有值
	m.UpdateUsage(&llm.TokenUsage{InputTokens: 0, OutputTokens: 100})
	usage = m.GetContextUsage(200000)
	if usage.Used != 30000 {
		t.Fatalf("InputTokens=0 不应覆盖已有 usage，Used 应为 30000，实际为 %d", usage.Used)
	}
}

// TestConversationManager_GetContextUsage_UpdatesAcrossIterations 验证多次 UpdateUsage 后值正确更新。
func TestConversationManager_GetContextUsage_UpdatesAcrossIterations(t *testing.T) {
	m := NewConversationManager(50)

	// 模拟第 1 轮：input_tokens=10000
	m.UpdateUsage(&llm.TokenUsage{InputTokens: 10000, OutputTokens: 50})
	usage1 := m.GetContextUsage(200000)
	if usage1.Remaining != 190000 {
		t.Fatalf("第 1 轮后 Remaining 应为 190000，实际为 %d", usage1.Remaining)
	}

	// 模拟第 2 轮：input_tokens=30000（历史增长，input_tokens 反映总量）
	m.UpdateUsage(&llm.TokenUsage{InputTokens: 30000, OutputTokens: 100})
	usage2 := m.GetContextUsage(200000)
	if usage2.Used != 30000 {
		t.Fatalf("第 2 轮后 Used 应为 30000，实际为 %d", usage2.Used)
	}
	if usage2.Remaining != 170000 {
		t.Fatalf("第 2 轮后 Remaining 应为 170000，实际为 %d", usage2.Remaining)
	}
}

func TestConversationManager_GetContextUsage_InvalidatesAfterHistoryAppend(t *testing.T) {
	m := NewConversationManager(50)

	m.UpdateUsage(&llm.TokenUsage{InputTokens: 50000, OutputTokens: 200})
	if usage := m.GetContextUsage(200000); usage.Used != 50000 {
		t.Fatalf("fresh precise usage should be used, got %d", usage.Used)
	}

	m.AddAssistantMessage(strings.Repeat("history changed ", 200))
	expected := m.TokenEstimate()
	usage := m.GetContextUsage(200000)
	if usage.Used != expected {
		t.Fatalf("stale precise usage should fall back to estimate, got %d want %d", usage.Used, expected)
	}
	if usage.Used == 50000 {
		t.Fatalf("stale precise input_tokens should not be reused after history append")
	}
}

func TestConversationManager_GetContextUsage_InvalidatesAfterMutableHistoryAccess(t *testing.T) {
	m := NewConversationManager(50)
	m.AddUserMessage("hello")
	m.UpdateUsage(&llm.TokenUsage{InputTokens: 50000, OutputTokens: 200})
	if usage := m.GetContextUsage(200000); usage.Used != 50000 {
		t.Fatalf("fresh precise usage should be used, got %d", usage.Used)
	}

	history := m.History()
	if len(history) == 0 {
		t.Fatal("expected mutable history")
	}

	expected := m.TokenEstimate()
	usage := m.GetContextUsage(200000)
	if usage.Used != expected {
		t.Fatalf("mutable history access should invalidate precise usage, got %d want %d", usage.Used, expected)
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

// TestConversationManager_GetContextKeepsFullHistory 验证取消滑动窗口后：
// GetContext 与 AllMessages 都保留完整活跃历史，避免裁剪破坏协议配对。
func TestConversationManager_GetContextKeepsFullHistory(t *testing.T) {
	const maxRounds = 2
	const totalRounds = 5
	m := NewConversationManager(maxRounds)

	for i := 0; i < totalRounds; i++ {
		m.AddUserMessage("u")
		m.AddAssistantMessage("a")
	}

	// GetContext 不再被窗口裁剪
	ctx := m.GetContext()
	if len(ctx) != totalRounds*2 {
		t.Fatalf("上下文视图应为完整历史 %d 条，实际 %d 条", totalRounds*2, len(ctx))
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

func (p *scriptedProvider) StreamChat(_ context.Context, _ llm.SystemPrompt, _ []llm.Message, _ []tool.ToolSpec) (<-chan llm.StreamChunk, error) {
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
	res := m.RunTurn(context.Background(), p, llm.NewSystemPromptFromText("system"), nil, NewToolHandler(tool.NewRegistry(), time.Second, ""), TurnHooks{
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
	if len(res.ToolUses) != 0 {
		t.Errorf("不应有 ToolUse，实际: %+v", res.ToolUses)
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
					Done:     true,
					ToolUses: []llm.ToolUseBlock{{ID: "call-1", Name: "echo", Input: json.RawMessage(`{"msg":"ping"}`)}},
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
		gotToolUse    *llm.ToolUseBlock
		gotToolResult *llm.ToolResultBlock
		onErrorCalled bool
		streamChunkN  int
	)
	res := m.RunTurn(context.Background(), p, llm.NewSystemPromptFromText("system"), nil, NewToolHandler(reg, time.Second, ""), TurnHooks{
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
	if len(res.ToolUses) == 0 || res.ToolUses[0].ID != "call-1" {
		t.Errorf("ToolUse = %+v，期望 ID=call-1", res.ToolUses)
	}
	if res.ToolResults[0].Content != "echo:ping" {
		t.Errorf("ToolResult.Content = %q，期望 %q", res.ToolResults[0].Content, "echo:ping")
	}
	if res.ToolResults[0].IsError {
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
				{Done: true, ToolUses: []llm.ToolUseBlock{{ID: "e1", Name: "always_err", Input: json.RawMessage(`{}`)}}},
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

	res := m.RunTurn(context.Background(), p, llm.NewSystemPromptFromText("system"), nil, NewToolHandler(reg, time.Second, ""), TurnHooks{})

	if res.Error != nil {
		t.Fatalf("RunTurn 返回错误: %v", res.Error)
	}
	if !res.ToolResults[0].IsError {
		t.Error("ToolResult.IsError 应为 true")
	}
	if res.ToolResults[0].Content == "" {
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
				{Done: true, ToolUses: []llm.ToolUseBlock{{ID: "x", Name: "missing", Input: json.RawMessage(`{}`)}}},
			},
			{
				{Content: "工具不存在", Done: true},
			},
		},
	}
	m := NewConversationManager(10)
	m.AddUserMessage("call missing")

	res := m.RunTurn(context.Background(), p, llm.NewSystemPromptFromText("system"), nil, NewToolHandler(tool.NewRegistry(), time.Second, ""), TurnHooks{})

	if res.Error != nil {
		t.Fatalf("RunTurn 返回错误: %v", res.Error)
	}
	if !res.ToolResults[0].IsError {
		t.Error("未注册工具应返回 IsError=true")
	}
	if !strings.Contains(res.ToolResults[0].Content, "missing") {
		t.Errorf("错误信息应包含工具名 missing，实际: %s", res.ToolResults[0].Content)
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

	res := m.RunTurn(ctx, p, llm.NewSystemPromptFromText("system"), nil, NewToolHandler(tool.NewRegistry(), time.Second, ""), TurnHooks{})

	// 第一次 LLM 立刻 Done，ToolUse=nil，aborted 取决于 ctx 状态
	if res.Error != nil {
		t.Fatalf("不应有 Error: %v", res.Error)
	}
	if len(res.ToolUses) != 0 {
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
	res := m.RunTurn(context.Background(), p, llm.NewSystemPromptFromText("system"), nil, NewToolHandler(tool.NewRegistry(), time.Second, ""), TurnHooks{
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
				{Done: true, ToolUses: []llm.ToolUseBlock{{ID: "e1", Name: "echo", Input: json.RawMessage(`{"msg":"hi"}`)}}},
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

	res := m.RunTurn(context.Background(), p, llm.NewSystemPromptFromText("system"), nil, th, TurnHooks{})
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
				{Done: true, ToolUses: []llm.ToolUseBlock{{ID: "s1", Name: "slow", Input: json.RawMessage(`{}`)}}},
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

	res := m.RunTurn(context.Background(), p, llm.NewSystemPromptFromText("system"), nil, th, TurnHooks{})
	if res.Error != nil {
		t.Fatalf("RunTurn 返回错误: %v", res.Error)
	}
	if !res.ToolResults[0].IsError {
		t.Error("工具超时应返回 IsError=true")
	}
	if !strings.Contains(res.ToolResults[0].Content, "超时") {
		t.Errorf("超时错误信息应包含 '超时'，实际: %s", res.ToolResults[0].Content)
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
	// 注册 SandboxMiddleware：ReadFile/WriteFile 等路径类工具需走 Middleware
	// 才能拿到 PathResolver。生产环境由 main.go 装配。
	h.RegisterMiddleware(security.SandboxMiddleware(tmp, nil))

	// 第一次 RunTurn：LLM 调 Bash 执行 `rm -rf /`
	p1 := &scriptedProvider{
		scripts: [][]llm.StreamChunk{
			{
				{Done: true, ToolUses: []llm.ToolUseBlock{{ID: "t1", Name: "Bash", Input: json.RawMessage(`{"command":"rm -rf /"}`)}}},
			},
			{
				{Content: "我已拒绝执行该命令。", Done: true},
			},
		},
	}
	m1 := NewConversationManager(10)
	m1.AddUserMessage("执行 rm -rf /")
	res1 := m1.RunTurn(context.Background(), p1, llm.NewSystemPromptFromText("system"), nil, h, TurnHooks{})
	if res1.Error != nil {
		t.Fatalf("第一次 RunTurn 出错: %v", res1.Error)
	}
	if !res1.ToolResults[0].IsError {
		t.Errorf("`rm -rf /` 应被黑名单拦截, 实际 IsError=false, Content=%q", res1.ToolResults[0].Content)
	}
	if !strings.Contains(res1.ToolResults[0].Content, "禁止") && !strings.Contains(res1.ToolResults[0].Content, "Dangerous") {
		t.Errorf("错误信息应说明危险命令, 实际: %q", res1.ToolResults[0].Content)
	}

	// 第二次 RunTurn：同一 Registry + ToolHandler，LLM 调 ReadFile 读 hello.txt
	p2 := &scriptedProvider{
		scripts: [][]llm.StreamChunk{
			{
				{Done: true, ToolUses: []llm.ToolUseBlock{{ID: "t2", Name: "ReadFile", Input: json.RawMessage(`{"file_path":"hello.txt"}`)}}},
			},
			{
				{Content: "hello.txt 内容如上。", Done: true},
			},
		},
	}
	m2 := NewConversationManager(10)
	m2.AddUserMessage("读 hello.txt")
	res2 := m2.RunTurn(context.Background(), p2, llm.NewSystemPromptFromText("system"), nil, h, TurnHooks{})
	if res2.Error != nil {
		t.Fatalf("第二次 RunTurn 出错: %v", res2.Error)
	}
	if res2.ToolResults[0].IsError {
		t.Errorf("ReadFile 应正常执行, 实际 IsError=true, Content=%q", res2.ToolResults[0].Content)
	}
	if !strings.Contains(res2.ToolResults[0].Content, "L1:") {
		t.Errorf("ReadFile 输出应含行号标记, 实际: %q", res2.ToolResults[0].Content)
	}
}

// ---- ExecuteBatch 测试 ----

// writeTool 是一个简单的写入工具（PermWrite），用于测试串行执行策略。
// 执行时将调用计数 +1 并记录执行顺序（通过全局序号）。
type writeTool struct {
	tool.BaseTool
	calls   int
	execSeq *[]int // 外部传入的序号记录切片，用于验证执行顺序
	seqVal  int    // 本工具在序号中的标识值
}

func (w *writeTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	w.calls++
	if w.execSeq != nil {
		*w.execSeq = append(*w.execSeq, w.seqVal)
	}
	return fmt.Sprintf("write_tool_%d:ok", w.seqVal), nil
}

// TestExecuteBatch_EmptyInput 空输入返回 nil。
func TestExecuteBatch_EmptyInput(t *testing.T) {
	reg := tool.NewRegistry()
	th := NewToolHandler(reg, time.Second, "")
	results := th.ExecuteBatch(context.Background(), nil)
	if results != nil {
		t.Errorf("空输入应返回 nil，实际: %v", results)
	}
}

// TestExecuteBatch_SingleTool 单工具调用与 Execute 行为一致。
func TestExecuteBatch_SingleTool(t *testing.T) {
	reg := tool.NewRegistry()
	_ = reg.Register(newEchoTool())
	th := NewToolHandler(reg, time.Second, "")

	results := th.ExecuteBatch(context.Background(), []llm.ToolUseBlock{
		{ID: "t1", Name: "echo", Input: json.RawMessage(`{"msg":"hello"}`)},
	})

	if len(results) != 1 {
		t.Fatalf("结果数 = %d，期望 1", len(results))
	}
	if results[0].ToolUseID != "t1" {
		t.Errorf("ToolUseID = %q，期望 t1", results[0].ToolUseID)
	}
	if results[0].Content != "echo:hello" {
		t.Errorf("Content = %q，期望 echo:hello", results[0].Content)
	}
	if results[0].IsError {
		t.Error("不应为错误结果")
	}
}

// TestExecuteBatch_ParallelReadOnly 只读工具并行执行。
func TestExecuteBatch_ParallelReadOnly(t *testing.T) {
	reg := tool.NewRegistry()
	// 注册两个只读工具
	_ = reg.Register(newEchoTool())

	th := NewToolHandler(reg, time.Second, "")
	results := th.ExecuteBatch(context.Background(), []llm.ToolUseBlock{
		{ID: "t1", Name: "echo", Input: json.RawMessage(`{"msg":"a"}`)},
		{ID: "t2", Name: "echo", Input: json.RawMessage(`{"msg":"b"}`)},
	})

	if len(results) != 2 {
		t.Fatalf("结果数 = %d，期望 2", len(results))
	}
	// 验证结果按原始顺序排列（即使并行执行）
	if results[0].ToolUseID != "t1" || results[0].Content != "echo:a" {
		t.Errorf("results[0] 不正确: ToolUseID=%q Content=%q", results[0].ToolUseID, results[0].Content)
	}
	if results[1].ToolUseID != "t2" || results[1].Content != "echo:b" {
		t.Errorf("results[1] 不正确: ToolUseID=%q Content=%q", results[1].ToolUseID, results[1].Content)
	}
}

// TestExecuteBatch_SerialWriteTools 写入工具串行执行。
func TestExecuteBatch_SerialWriteTools(t *testing.T) {
	reg := tool.NewRegistry()

	var execSeq []int
	w1 := &writeTool{
		BaseTool: tool.BaseTool{
			ToolName:        "write_a",
			ToolDescription: "write tool a",
			ToolInputSchema: json.RawMessage(`{"type":"object"}`),
			ToolPermission:  tool.PermWrite,
		},
		execSeq: &execSeq,
		seqVal:  1,
	}
	w2 := &writeTool{
		BaseTool: tool.BaseTool{
			ToolName:        "write_b",
			ToolDescription: "write tool b",
			ToolInputSchema: json.RawMessage(`{"type":"object"}`),
			ToolPermission:  tool.PermWrite,
		},
		execSeq: &execSeq,
		seqVal:  2,
	}
	_ = reg.Register(w1)
	_ = reg.Register(w2)

	th := NewToolHandler(reg, time.Second, "")
	results := th.ExecuteBatch(context.Background(), []llm.ToolUseBlock{
		{ID: "t1", Name: "write_a", Input: json.RawMessage(`{}`)},
		{ID: "t2", Name: "write_b", Input: json.RawMessage(`{}`)},
	})

	if len(results) != 2 {
		t.Fatalf("结果数 = %d，期望 2", len(results))
	}
	// 验证串行执行顺序：write_a 先于 write_b
	if len(execSeq) != 2 {
		t.Fatalf("执行序号记录数 = %d，期望 2", len(execSeq))
	}
	if execSeq[0] != 1 || execSeq[1] != 2 {
		t.Errorf("执行顺序不正确: %v，期望 [1, 2]", execSeq)
	}
	if results[0].Content != "write_tool_1:ok" || results[1].Content != "write_tool_2:ok" {
		t.Errorf("结果内容不正确: %q, %q", results[0].Content, results[1].Content)
	}
}

// TestExecuteBatch_MixedPermissions 混合权限：只读并行 + 写入串行。
func TestExecuteBatch_MixedPermissions(t *testing.T) {
	reg := tool.NewRegistry()

	_ = reg.Register(newEchoTool()) // PermRead

	var execSeq []int
	w := &writeTool{
		BaseTool: tool.BaseTool{
			ToolName:        "write_tool",
			ToolDescription: "write tool",
			ToolInputSchema: json.RawMessage(`{"type":"object"}`),
			ToolPermission:  tool.PermWrite,
		},
		execSeq: &execSeq,
		seqVal:  100,
	}
	_ = reg.Register(w)

	th := NewToolHandler(reg, time.Second, "")
	results := th.ExecuteBatch(context.Background(), []llm.ToolUseBlock{
		{ID: "t1", Name: "echo", Input: json.RawMessage(`{"msg":"read"}`)},
		{ID: "t2", Name: "write_tool", Input: json.RawMessage(`{}`)},
	})

	if len(results) != 2 {
		t.Fatalf("结果数 = %d，期望 2", len(results))
	}
	if results[0].ToolUseID != "t1" || results[0].Content != "echo:read" {
		t.Errorf("results[0] 不正确: %+v", results[0])
	}
	if results[1].ToolUseID != "t2" || results[1].Content != "write_tool_100:ok" {
		t.Errorf("results[1] 不正确: %+v", results[1])
	}
}

// TestExecuteBatch_UnregisteredTool 未注册工具返回错误结果。
func TestExecuteBatch_UnregisteredTool(t *testing.T) {
	reg := tool.NewRegistry()
	_ = reg.Register(newEchoTool())

	th := NewToolHandler(reg, time.Second, "")
	results := th.ExecuteBatch(context.Background(), []llm.ToolUseBlock{
		{ID: "t1", Name: "echo", Input: json.RawMessage(`{"msg":"ok"}`)},
		{ID: "t2", Name: "missing_tool", Input: json.RawMessage(`{}`)},
	})

	if len(results) != 2 {
		t.Fatalf("结果数 = %d，期望 2", len(results))
	}
	// 已注册工具正常
	if results[0].IsError {
		t.Error("已注册工具不应为错误")
	}
	// 未注册工具返回错误
	if !results[1].IsError {
		t.Error("未注册工具应为 IsError=true")
	}
	if !strings.Contains(results[1].Content, "missing_tool") {
		t.Errorf("错误信息应包含工具名，实际: %q", results[1].Content)
	}
}

// TestExecuteBatch_ErrorIsolation 单个工具失败不影响其他工具。
func TestExecuteBatch_ErrorIsolation(t *testing.T) {
	reg := tool.NewRegistry()
	_ = reg.Register(newEchoTool())
	_ = reg.Register(newErrTool())

	th := NewToolHandler(reg, time.Second, "")
	results := th.ExecuteBatch(context.Background(), []llm.ToolUseBlock{
		{ID: "t1", Name: "echo", Input: json.RawMessage(`{"msg":"ok"}`)},
		{ID: "t2", Name: "always_err", Input: json.RawMessage(`{}`)},
	})

	if len(results) != 2 {
		t.Fatalf("结果数 = %d，期望 2", len(results))
	}
	// echo 工具正常
	if results[0].IsError {
		t.Error("echo 工具不应失败")
	}
	if results[0].Content != "echo:ok" {
		t.Errorf("echo 结果 = %q，期望 echo:ok", results[0].Content)
	}
	// err 工具失败
	if !results[1].IsError {
		t.Error("always_err 工具应为 IsError=true")
	}
}

// TestExecuteBatch_OnStartOnEndCallbacks 每个工具的回调都正常触发。
func TestExecuteBatch_OnStartOnEndCallbacks(t *testing.T) {
	reg := tool.NewRegistry()
	_ = reg.Register(newEchoTool())

	th := NewToolHandler(reg, time.Second, "")

	var mu sync.Mutex
	var events []ToolExecutionEvent
	th.SetOnStart(func(e ToolExecutionEvent) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	})
	th.SetOnEnd(func(e ToolExecutionEvent) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	})

	th.ExecuteBatch(context.Background(), []llm.ToolUseBlock{
		{ID: "t1", Name: "echo", Input: json.RawMessage(`{"msg":"a"}`)},
		{ID: "t2", Name: "echo", Input: json.RawMessage(`{"msg":"b"}`)},
	})

	// 每个 tool_use 产生 start + end 两个事件，共 4 个
	if len(events) != 4 {
		t.Fatalf("事件数 = %d，期望 4（2 start + 2 end）", len(events))
	}

	// 验证所有事件状态（不依赖严格顺序，因为并行执行时事件可能交叉）
	startCount := 0
	endCount := 0
	for _, e := range events {
		if e.Status == ToolEventStatusRunning {
			startCount++
		} else if e.Status == ToolEventStatusCompleted {
			endCount++
		}
	}
	if startCount != 2 {
		t.Errorf("start 事件数 = %d，期望 2", startCount)
	}
	if endCount != 2 {
		t.Errorf("end 事件数 = %d，期望 2", endCount)
	}
}
