package context

import (
	"testing"

	"github.com/MeiCorl/CodePilot/src/llm"
)

// userMsg / assistantMsg 为构造测试消息的辅助函数。
func userMsg(text string) llm.Message {
	return llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock(text)}}
}

func assistantMsg(text string) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.NewTextBlock(text)}}
}

func TestSlidingWindow_ViewBasic(t *testing.T) {
	w := NewSlidingWindow(3)
	history := []llm.Message{userMsg("hello"), assistantMsg("hi")}

	msgs := w.View(history, "")
	if len(msgs) != 2 {
		t.Fatalf("期望 2 条消息，实际 %d 条", len(msgs))
	}
}

func TestSlidingWindow_SystemPromptAlwaysFirst(t *testing.T) {
	w := NewSlidingWindow(3)
	history := []llm.Message{userMsg("hello")}

	msgs := w.View(history, "你是一个助手")
	if len(msgs) != 2 {
		t.Fatalf("期望 2 条消息，实际 %d 条", len(msgs))
	}
	if msgs[0].Role != llm.RoleSystem {
		t.Fatalf("第一条消息应为 system，实际为 %s", msgs[0].Role)
	}
	if msgs[0].Content[0].ToText() != "你是一个助手" {
		t.Fatalf("System Prompt 内容不匹配")
	}
}

func TestSlidingWindow_EvictOldest(t *testing.T) {
	// 窗口大小为 2 轮，完整历史有 3 轮，视图中最早 1 轮被裁剪
	w := NewSlidingWindow(2)
	history := []llm.Message{
		userMsg("round1-user"), assistantMsg("round1-assistant"),
		userMsg("round2-user"), assistantMsg("round2-assistant"),
		userMsg("round3-user"), assistantMsg("round3-assistant"),
	}

	msgs := w.View(history, "")
	// 应该只剩 2 轮 = 4 条消息，第 1 轮被裁剪
	if len(msgs) != 4 {
		t.Fatalf("期望 4 条消息（2 轮），实际 %d 条", len(msgs))
	}
	// 第一条应该是 round2-user
	if msgs[0].Content[0].ToText() != "round2-user" {
		t.Fatalf("第一条消息应为 round2-user，实际为 %s", msgs[0].Content[0].ToText())
	}
}

func TestSlidingWindow_SystemPromptNotEvicted(t *testing.T) {
	w := NewSlidingWindow(1)
	history := []llm.Message{
		userMsg("r1"), assistantMsg("r1-a"),
		userMsg("r2"), assistantMsg("r2-a"),
	}

	msgs := w.View(history, "system-prompt")
	// 1 条 system + 1 轮（2 条）
	if len(msgs) != 3 {
		t.Fatalf("期望 3 条消息，实际 %d 条", len(msgs))
	}
	if msgs[0].Role != llm.RoleSystem {
		t.Fatalf("第一条消息应为 system")
	}
	if msgs[0].Content[0].ToText() != "system-prompt" {
		t.Fatalf("System Prompt 内容不匹配")
	}
}

func TestSlidingWindow_ViewDoesNotMutateHistory(t *testing.T) {
	// View 不应修改入参 history（完整历史是唯一真相源，必须保持完整）
	w := NewSlidingWindow(1)
	history := []llm.Message{
		userMsg("r1"), assistantMsg("r1-a"),
		userMsg("r2"), assistantMsg("r2-a"),
	}

	_ = w.View(history, "sp")
	if len(history) != 4 {
		t.Fatalf("View 不应修改入参 history，期望长度 4，实际 %d", len(history))
	}
	if history[0].Content[0].ToText() != "r1" {
		t.Fatalf("View 不应修改入参 history 的内容")
	}
}

func TestSlidingWindow_DefaultMaxRounds(t *testing.T) {
	// maxRounds <= 0 时默认为 10
	w := NewSlidingWindow(0)
	if w.MaxRounds() != 10 {
		t.Fatalf("期望默认 maxRounds=10，实际为 %d", w.MaxRounds())
	}
	w = NewSlidingWindow(-1)
	if w.MaxRounds() != 10 {
		t.Fatalf("期望默认 maxRounds=10，实际为 %d", w.MaxRounds())
	}
}
