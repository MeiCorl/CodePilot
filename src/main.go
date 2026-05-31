// Package main 是 CodePilot 终端 AI Coding Agent 的程序入口。
// 负责启动流程的编排：日志初始化 → 配置加载 → Provider 初始化 →
// 会话恢复 → TUI 界面启动，以及退出时的资源清理。
package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"go.uber.org/zap"

	"github.com/MeiCorl/CodePilot/src/internal/config"
	"github.com/MeiCorl/CodePilot/src/internal/engine/conversation"
	"github.com/MeiCorl/CodePilot/src/internal/interaction/tui"
	"github.com/MeiCorl/CodePilot/src/internal/logger"
	"github.com/MeiCorl/CodePilot/src/internal/memory/session"
	"github.com/MeiCorl/CodePilot/src/llm"
)

func main() {
	// 1. 初始化日志系统（失败不阻塞启动，降级为无日志模式）
	if err := logger.Init(); err != nil {
		fmt.Fprintf(os.Stderr, "警告: 日志初始化失败 (%v)，继续运行...\n", err)
	}
	defer logger.Sync()

	// 2. 加载配置文件
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %s\n", err)
		os.Exit(1)
	}
	logger.Info("配置加载成功",
		zap.String("provider", cfg.Provider),
		zap.String("model", cfg.Model),
	)

	// 3. 根据配置创建 LLM Provider 实例
	provider, err := llm.NewProvider(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: Provider 初始化失败: %s\n", err)
		os.Exit(1)
	}
	logger.Info("Provider 初始化成功", zap.String("provider", cfg.Provider))

	// 4. 创建对话管理器（50 轮对话窗口）
	convMgr := conversation.NewConversationManager(50)

	// 5. 创建会话管理器，尝试恢复上次会话
	sessMgr, err := session.NewSessionManager()
	if err != nil {
		logger.Warn("创建会话管理器失败", zap.Error(err))
	}

	var sess *session.Session
	if sessMgr != nil {
		sess, _ = sessMgr.LoadLatest()
		if sess != nil {
			logger.Info("恢复上次会话", zap.String("session_id", sess.ID))
		}
	}

	// 6. 创建 TUI 主模型，注入所有依赖
	model := tui.NewAppModel(provider, convMgr, sessMgr, cfg, sess)

	// 7. 启动 Bubble Tea 程序（启用鼠标事件捕获，支持滚轮滚动、文本选择和右键粘贴）
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "程序运行出错: %s\n", err)
		os.Exit(1)
	}
}
