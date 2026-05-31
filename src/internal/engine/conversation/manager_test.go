package conversation

import (
	"testing"
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
