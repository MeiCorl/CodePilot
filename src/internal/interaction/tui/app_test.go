package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/MeiCorl/CodePilot/src/internal/config"
	"github.com/MeiCorl/CodePilot/src/internal/engine/conversation"
	"github.com/MeiCorl/CodePilot/src/internal/memory/session"
	"github.com/MeiCorl/CodePilot/src/llm"
)

// mockProvider 实现 llm.Provider 接口，用于测试
type mockProvider struct{}

func (m *mockProvider) StreamChat(_ context.Context, _ string, _ []llm.Message) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, 3)
	go func() {
		ch <- llm.StreamChunk{Content: "Hello"}
		ch <- llm.StreamChunk{Content: " World"}
		ch <- llm.StreamChunk{Done: true}
	}()
	return ch, nil
}

// TestNewAppModel 验证 TUI 主模型的初始化
func TestNewAppModel(t *testing.T) {
	cfg := &config.Config{
		Provider:  "anthropic",
		Model:     "test-model",
		APIKey:    "test-key",
		MaxTokens: 4096,
	}
	convMgr := conversation.NewConversationManager(10)
	sessMgr := &session.SessionManager{}

	model := NewAppModel(&mockProvider{}, convMgr, sessMgr, cfg, nil)

	if model.config.Model != "test-model" {
		t.Errorf("模型名称不匹配: got %s, want test-model", model.config.Model)
	}
	if model.isStreaming {
		t.Error("初始状态不应为流式中")
	}
	if model.ready {
		t.Error("初始状态不应为就绪")
	}
	if len(model.messages) != 0 {
		t.Errorf("初始消息列表应为空, got %d", len(model.messages))
	}
}

// TestNewAppModelWithSession 验证从会话恢复消息
func TestNewAppModelWithSession(t *testing.T) {
	cfg := &config.Config{
		Provider:  "anthropic",
		Model:     "test-model",
		APIKey:    "test-key",
		MaxTokens: 4096,
	}
	convMgr := conversation.NewConversationManager(10)
	sessMgr := &session.SessionManager{}

	sess := &session.Session{
		ID: "test-session",
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("你好")}},
			{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.NewTextBlock("你好！有什么可以帮你的？")}},
		},
	}

	model := NewAppModel(&mockProvider{}, convMgr, sessMgr, cfg, sess)

	if len(model.messages) != 2 {
		t.Fatalf("恢复消息数量不匹配: got %d, want 2", len(model.messages))
	}
	if model.messages[0].Content != "你好" {
		t.Errorf("第一条消息内容不匹配: got %s", model.messages[0].Content)
	}
	if model.messages[1].Role != llm.RoleAssistant {
		t.Errorf("第二条消息角色不匹配: got %v", model.messages[1].Role)
	}
	// 验证对话管理器也恢复了
	if convMgr.MessageCount() != 2 {
		t.Errorf("对话管理器消息数不匹配: got %d, want 2", convMgr.MessageCount())
	}
}

// TestExtractTextFromBlocks 验证从 ContentBlock 提取文本
func TestExtractTextFromBlocks(t *testing.T) {
	blocks := []llm.ContentBlock{
		llm.NewTextBlock("Hello "),
		llm.NewTextBlock("World"),
	}
	result := extractTextFromBlocks(blocks)
	if result != "Hello World" {
		t.Errorf("文本提取不匹配: got %s, want Hello World", result)
	}
}

// TestBuildConversationContent 验证对话内容构建
func TestBuildConversationContent(t *testing.T) {
	cfg := &config.Config{Model: "test", MaxTokens: 4096}
	convMgr := conversation.NewConversationManager(10)
	model := NewAppModel(&mockProvider{}, convMgr, nil, cfg, nil)

	// 空对话应显示欢迎信息
	content := model.buildConversationContent()
	if !strings.Contains(content, "CodePilot") {
		t.Error("空对话应包含欢迎信息")
	}

	// 添加消息后验证
	model.messages = []chatMessage{
		{Role: llm.RoleUser, Content: "你好"},
		{Role: llm.RoleAssistant, Content: "你好！"},
	}
	content = model.buildConversationContent()
	if !strings.Contains(content, "你：") {
		t.Error("应包含用户消息标记")
	}
	if !strings.Contains(content, "CodePilot：") {
		t.Error("应包含助手消息标记")
	}
	if !strings.Contains(content, "你好") {
		t.Error("应包含消息内容")
	}
}

// TestBuildConversationContentWithError 验证错误消息的展示
func TestBuildConversationContentWithError(t *testing.T) {
	cfg := &config.Config{Model: "test", MaxTokens: 4096}
	convMgr := conversation.NewConversationManager(10)
	model := NewAppModel(&mockProvider{}, convMgr, nil, cfg, nil)

	model.messages = []chatMessage{
		{Role: llm.RoleAssistant, Content: "API 请求失败: timeout", IsError: true},
	}
	content := model.buildConversationContent()
	if !strings.Contains(content, "错误") {
		t.Error("错误消息应包含错误标记")
	}
}

// TestBuildConversationContentWithStreaming 验证流式中的内容构建
func TestBuildConversationContentWithStreaming(t *testing.T) {
	cfg := &config.Config{Model: "test", MaxTokens: 4096}
	convMgr := conversation.NewConversationManager(10)
	model := NewAppModel(&mockProvider{}, convMgr, nil, cfg, nil)

	model.isStreaming = true
	model.streamingText = "正在思考..."
	content := model.buildConversationContent()
	if !strings.Contains(content, "正在思考...") {
		t.Error("应包含流式文本")
	}
	if !strings.Contains(content, "⣻") {
		t.Error("流式中应包含思考指示器")
	}
}

// TestLogoView 验证紧凑版 Logo 渲染
func TestLogoView(t *testing.T) {
	logo := LogoView(80)
	if !strings.Contains(logo, "CodePilot") {
		t.Error("Logo 应包含产品名称 CodePilot")
	}
	if !strings.Contains(logo, "v"+Version) {
		t.Error("Logo 应包含版本号")
	}
	if !strings.Contains(logo, "o  o") {
		t.Error("Logo 应包含猫头鹰面部图案")
	}
	if !strings.Contains(logo, "Your AI Coding Agent") {
		t.Error("Logo 应包含标语")
	}
	// 验证紧凑布局为 4 行（猫头鹰 3 行 + 分隔线 1 行）
	lines := strings.Split(logo, "\n")
	if len(lines) != 4 {
		t.Errorf("紧凑 Logo 应为 4 行, got %d", len(lines))
	}
}

// TestStatusBarView 验证状态栏渲染（含上下文窗口进度条）
func TestStatusBarView(t *testing.T) {
	bar := StatusBarView("claude-sonnet-4", 500, 200000, false, 80)
	if !strings.Contains(bar, "claude-sonnet-4") {
		t.Error("状态栏应包含模型名称")
	}
	if !strings.Contains(bar, "就绪") {
		t.Error("非流式状态应显示就绪")
	}
	// 验证包含上下文窗口进度条（█ 可用额度）
	if !strings.Contains(bar, "█") {
		t.Error("状态栏应包含上下文窗口进度条")
	}
	// 验证包含百分比显示（500/200000 ≈ 0.25%，剩余约 100%）
	if !strings.Contains(bar, "%") {
		t.Error("状态栏应包含上下文剩余百分比")
	}

	barStreaming := StatusBarView("test-model", 0, 200000, true, 80)
	if !strings.Contains(barStreaming, "思考中") {
		t.Error("流式状态应显示思考中")
	}

	// 验证高使用率场景（90% 已用）
	barHigh := StatusBarView("test-model", 180000, 200000, false, 80)
	if !strings.Contains(barHigh, "█") {
		t.Error("高使用率状态栏应包含进度条")
	}
	if !strings.Contains(barHigh, "就绪") {
		t.Error("高使用率非流式状态应显示就绪")
	}
}

// TestInit 验证 Init 方法不返回错误命令
func TestInit(t *testing.T) {
	cfg := &config.Config{Model: "test", MaxTokens: 4096}
	convMgr := conversation.NewConversationManager(10)
	model := NewAppModel(&mockProvider{}, convMgr, nil, cfg, nil)
	cmd := model.Init()
	if cmd != nil {
		t.Error("Init 应返回 nil Cmd")
	}
}

// TestViewNotReady 验证未就绪时的视图
func TestViewNotReady(t *testing.T) {
	cfg := &config.Config{Model: "test", MaxTokens: 4096}
	convMgr := conversation.NewConversationManager(10)
	model := NewAppModel(&mockProvider{}, convMgr, nil, cfg, nil)

	view := model.View()
	if !strings.Contains(view, "初始化") {
		t.Error("未就绪时应显示初始化信息")
	}
}
