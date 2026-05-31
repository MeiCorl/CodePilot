package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"go.uber.org/zap"

	"github.com/MeiCorl/CodePilot/src/internal/config"
	"github.com/MeiCorl/CodePilot/src/internal/engine/conversation"
	"github.com/MeiCorl/CodePilot/src/internal/logger"
	"github.com/MeiCorl/CodePilot/src/internal/memory/session"
	"github.com/MeiCorl/CodePilot/src/llm"
)

// 默认上下文窗口大小（token 数），用于 token 估算展示
const defaultContextWindowTokens = 200000

// availableCommands 定义可用的会话管理命令及其描述和参数需求
var availableCommands = []struct {
	cmd         string
	description string
	needsArg    bool
}{
	{"/sessions", "列出历史会话", false},
	{"/new", "创建新会话", false},
		{"/resume <id>", "恢复指定会话", true},
		{"/clear", "清除当前会话上下文", false},
}

// filterCommands 根据输入前缀过滤匹配的命令列表
func filterCommands(prefix string) []string {
	var result []string
	for _, c := range availableCommands {
		if strings.HasPrefix(c.cmd, prefix) {
			result = append(result, c.cmd)
		}
	}
	return result
}

// commandNeedsArg 判断指定命令是否需要额外参数输入
func commandNeedsArg(cmd string) bool {
	for _, c := range availableCommands {
		if c.cmd == cmd {
			return c.needsArg
		}
	}
	return false
}

// getCommandDescription 获取指定命令的功能描述
func getCommandDescription(cmd string) string {
	for _, c := range availableCommands {
		if c.cmd == cmd {
			return c.description
		}
	}
	return ""
}

// chatMessage 表示对话区域中的一条展示消息
type chatMessage struct {
	Role    llm.Role
	Content string
	IsError bool
}

// AppModel 是 Bubble Tea 的主模型，管理整个 TUI 界面的状态和交互。
// 组合了 LLM Provider、对话管理器、会话管理器等依赖，
// 实现 Bubble Tea 的 Model 接口（Init/Update/View）。
type AppModel struct {
	// 依赖组件
	provider  llm.Provider
	convMgr   *conversation.ConversationManager
	sessMgr   *session.SessionManager
	config    *config.Config

	// UI 组件
	textarea textarea.Model
	viewport viewport.Model

	// 显示状态
	messages []chatMessage
	width    int
	height   int
	ready    bool

	// 流式响应状态
	isStreaming   bool
	cancelFunc    context.CancelFunc
	streamingText string
	streamCh      <-chan llm.StreamChunk

	// 会话状态
	currentSession *session.Session

	// Markdown 渲染器
	renderer *glamour.TermRenderer

	// 鼠标选择状态（用于 viewport 区域的双击拖拽字符级选择复制）
	selectionActive bool
	selectionCopied bool   // 复制完成后保持高亮，延时清除
	selectionStartX int
	selectionStartY int
	selectionEndX   int
	selectionEndY   int

	// 双击检测状态
	lastClickTime time.Time
	lastClickX    int
	lastClickY    int

	// UI 区域屏幕坐标（用于鼠标事件区域判定）
	logoHeight int
	vpTopY     int
	vpH        int
	taTopY     int

	// 命令选择模式状态
	commandMode        bool     // 是否处于命令选择模式（输入 / 开头文本时激活）
	commandSuggestions []string // 当前匹配的命令列表
	commandIndex       int      // 当前高亮的命令索引（0-based）

	// 复制成功通知
	copyNotif string
}

// NewAppModel 创建并初始化 TUI 主模型，注入所有依赖组件。
// sess 参数为恢复的会话（可为 nil 表示新建会话）。
func NewAppModel(
	provider llm.Provider,
	convMgr *conversation.ConversationManager,
	sessMgr *session.SessionManager,
	cfg *config.Config,
	sess *session.Session,
) AppModel {
	// 初始化输入框
	ta := textarea.New()
	ta.Placeholder = "输入消息，Enter 发送..."
	ta.Prompt = "> "
	ta.CharLimit = 10000
	ta.ShowLineNumbers = false
	ta.SetWidth(80)
	ta.SetHeight(3)
	// 将 InsertNewline 从 Enter 键解绑，使 Enter 用于发送消息而非换行
	ta.KeyMap.InsertNewline = key.Binding{}
	ta.Focus()

	// 初始化对话区域
	vp := viewport.New(80, 20)

	// 创建 Markdown 渲染器
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(78),
	)
	if err != nil {
		r = nil
	}

	m := AppModel{
		provider:       provider,
		convMgr:        convMgr,
		sessMgr:        sessMgr,
		config:         cfg,
		textarea:       ta,
		viewport:       vp,
		renderer:       r,
		currentSession: sess,
		messages:       []chatMessage{},
	}

	// 恢复会话历史消息到对话管理器和显示列表
	if sess != nil && len(sess.Messages) > 0 {
		for _, msg := range sess.Messages {
			content := extractTextFromBlocks(msg.Content)
			m.messages = append(m.messages, chatMessage{
				Role:    msg.Role,
				Content: content,
			})
			switch msg.Role {
			case llm.RoleUser:
				m.convMgr.AddUserMessage(content)
			case llm.RoleAssistant:
				m.convMgr.AddAssistantMessage(content)
			}
		}
	}

	return m
}

// extractTextFromBlocks 从 ContentBlock 数组中提取纯文本内容
func extractTextFromBlocks(blocks []llm.ContentBlock) string {
	var sb strings.Builder
	for _, block := range blocks {
		sb.WriteString(block.ToText())
	}
	return sb.String()
}

// Init 实现 Bubble Tea Model 接口，应用启动时调用
func (m AppModel) Init() tea.Cmd {
	return nil
}

// Update 实现 Bubble Tea Model 接口，处理所有消息和事件
func (m AppModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKeyMsg(msg)

	case tea.WindowSizeMsg:
		return m.handleWindowSize(msg)

	case tea.MouseMsg:
		return m.handleMouseMsg(msg)

	case StreamChunkMsg:
		return m.handleStreamChunk(msg)

	case StreamDoneMsg:
		return m.handleStreamDone()

	case StreamErrorMsg:
		return m.handleStreamError(msg)

	case clearCopyNotifMsg:
		m.copyNotif = ""
		return m, nil

	case clearSelectionHighlightMsg:
		m.selectionCopied = false
		return m, nil
	}

	// 将未匹配的消息传递给子组件
	return m.updateSubComponents(msg)
}

// handleKeyMsg 处理键盘输入事件
func (m AppModel) handleKeyMsg(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		// 优雅退出：保存会话、刷新日志
		m.persistSession()
		logger.Sync()
		return m, tea.Quit

	case "enter":
		if m.isStreaming {
			return m, nil
		}
		val := strings.TrimSpace(m.textarea.Value())
		if val == "" {
			return m, nil
		}

		// 命令检测：/ 开头的输入视为会话管理命令
		if strings.HasPrefix(val, "/") {
			m.textarea.Reset()
			m.commandMode = false
			return m.handleSessionCommand(val)
		}

		cmd := m.sendUserMessage(val)
		return m, cmd

	case "tab":
		// 命令选择模式：将高亮的候选命令填入输入框（不执行）
		if m.commandMode && len(m.commandSuggestions) > 0 {
			return m.fillCommandSuggestion()
		}

	case "up":
		// 命令选择模式：上移高亮
		if m.commandMode && len(m.commandSuggestions) > 0 {
			if m.commandIndex > 0 {
				m.commandIndex--
			}
			return m, nil
		}

	case "down":
		// 命令选择模式：下移高亮
		if m.commandMode && len(m.commandSuggestions) > 0 {
			if m.commandIndex < len(m.commandSuggestions)-1 {
				m.commandIndex++
			}
			return m, nil
		}

	case "esc":
		// 命令选择模式：取消
		if m.commandMode {
			m.commandMode = false
			m.commandSuggestions = nil
			m.commandIndex = 0
			return m, nil
		}
		// 中断当前流式响应
		if m.isStreaming && m.cancelFunc != nil {
			m.cancelFunc()
		}
		return m, nil

	case "ctrl+v":
		// Ctrl+V 粘贴剪贴板内容到输入框
		if !m.isStreaming {
			text, err := readClipboardFromSystem()
			if err == nil && text != "" {
				pasteIntoTextarea(&m.textarea, text)
			}
		}
		return m, nil
	}

	// 其他按键传递给子组件，并根据输入内容更新命令选择模式状态
	model, cmd := m.updateSubComponents(msg)
	result := model.(AppModel)
	result.updateCommandMode()
	return result, cmd
}

// handleWindowSize 处理终端窗口大小变化
func (m AppModel) handleWindowSize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	m.width = msg.Width
	m.height = msg.Height
	m.ready = true

	// 整个 UI 收窄到比终端宽度少 1 列，永远空出最右一列。
	// 原因：JoinVertical 会把所有行补齐到最宽块的宽度；若等于终端宽度，
	// 每行都会写到终端最后一列，在 Windows 控制台的 alt screen 下会触发
	// "pending wrap"（遅延折返し），内容铺满（出现滚动条）后引发整屏上移，
	// 导致顶部 Logo 与底部输入/状态栏被挤出可视区。留出最右列后，渲染器会
	// 对每行追加 EraseLineRight，光标不再触达最后一列，从根本上规避该问题。
	usableWidth := msg.Width - 1
	if usableWidth < 1 {
		usableWidth = 1
	}

	// 计算 Logo 区域实际行数（紧凑版固定 4 行：猫头鹰 3 行 + 分隔线 1 行）
	m.logoHeight = len(strings.Split(LogoView(usableWidth), "\n"))

	// 更新输入框宽度（留出边框/安全余量，且不超过 usableWidth）
	m.textarea.SetWidth(usableWidth - 3)

	// 更新对话区域尺寸：总高度 - Logo(4) - 输入框(3) - 状态栏(1) - 间距(2) - 安全余量(1)
	// 安全余量确保布局不精确填满终端，避免 alt screen 模式下触发行滚动导致 textarea 消失
	vpHeight := msg.Height - m.logoHeight - 7
	if vpHeight < 5 {
		vpHeight = 5
	}
	// viewport 宽度：usableWidth - 2（滚动条 1 字符 + 左边距 1 字符），
	// 使 viewport + 滚动条恰好等于 usableWidth，最右列保持空白。
	m.viewport.Width = usableWidth - 2
	if m.viewport.Width < 1 {
		m.viewport.Width = 1
	}
	m.viewport.Height = vpHeight
	m.vpH = vpHeight
	m.vpTopY = m.logoHeight
	m.taTopY = m.vpTopY + vpHeight + 1 // viewport + 1行分割线

	// 重新创建 Markdown 渲染器以适应新宽度。wordWrap 需略小于 viewport 宽度，
	// 为 glamour 自身的文档左边距预留空间，避免内容被 viewport 截断。
	wordWrap := m.viewport.Width - 2
	if wordWrap > 4 {
		r, err := glamour.NewTermRenderer(
			glamour.WithStandardStyle("dark"),
			glamour.WithWordWrap(wordWrap),
		)
		if err == nil {
			m.renderer = r
		}
	}

	m.updateViewportContent()
	return m, nil
}

// handleMouseMsg 处理鼠标事件，实现双击后拖拽字符级选择复制和右键粘贴功能。
// 交互流程：双击进入选择模式 → 拖拽选中文本（高亮实时反馈）→ 释放时复制并短暂保留高亮。
func (m AppModel) handleMouseMsg(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	// 右键点击 textarea 区域 → 粘贴剪贴板内容
	if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonRight {
		if m.isInTextarea(msg.Y) && !m.isStreaming {
			text, err := readClipboardFromSystem()
			if err == nil && text != "" {
				pasteIntoTextarea(&m.textarea, text)
			}
			return m, nil
		}
	}

	// viewport 区域的鼠标左键事件 → 双击后拖拽选择字符级文本
	// 只检查 Y 坐标（不检查 X），确保整个 viewport 宽度内的事件都能被捕获
	if m.isInViewportY(msg.Y) && msg.Button == tea.MouseButtonLeft {
		switch {
		case msg.Action == tea.MouseActionPress:
			if m.selectionActive {
				// 已处于选择模式（双击后），更新选择终点，准备后续拖拽
				m.selectionEndX = msg.X
				m.selectionEndY = msg.Y
				return m, nil
			}

			// 双击检测：两次按下间隔 < 500ms 且位置接近时视为双击
			now := time.Now()
			dx := msg.X - m.lastClickX
			dy := msg.Y - m.lastClickY
			if dx < 0 {
				dx = -dx
			}
			if dy < 0 {
				dy = -dy
			}
			isDouble := now.Sub(m.lastClickTime) < 500*time.Millisecond && dx < 4 && dy < 4
			m.lastClickTime = now
			m.lastClickX = msg.X
			m.lastClickY = msg.Y

			if isDouble {
				// 双击：进入选择模式，记录起始位置
				m.selectionActive = true
				m.selectionCopied = false
				m.selectionStartX = msg.X
				m.selectionStartY = msg.Y
				m.selectionEndX = msg.X
				m.selectionEndY = msg.Y
			} else {
				// 单击：取消选择
				m.selectionActive = false
				m.selectionCopied = false
			}
			return m, nil

		case msg.Action == tea.MouseActionMotion && m.selectionActive:
			// 拖拽中：实时更新选择终点，View() 会渲染高亮
			m.selectionEndX = msg.X
			m.selectionEndY = msg.Y
			return m, nil

		case msg.Action == tea.MouseActionRelease && m.selectionActive:
			// 仅当用户已拖拽选择了一段文本（起止不同）时才复制
			if m.selectionStartX != m.selectionEndX || m.selectionStartY != m.selectionEndY {
				m.selectionActive = false
				m.selectionCopied = true
				cmd := m.copySelectionToClipboard()
				// 1.5 秒后清除高亮，让用户有时间确认复制了哪些内容
				return m, tea.Batch(cmd, tea.Tick(1500*time.Millisecond, func(_ time.Time) tea.Msg {
					return clearSelectionHighlightMsg{}
				}))
			}
			// 双击后立即释放（未拖拽），保持选择模式，等待用户后续拖拽
			return m, nil
		}
	} else if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress && m.selectionActive {
		// 点击 viewport 外部时取消选择模式
		m.selectionActive = false
		m.selectionCopied = false
	}

	// 滚轮及其他事件传递给 viewport 处理滚动
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

// isInViewportY 判断屏幕 Y 坐标是否在 viewport 区域内（仅检查纵向范围）
func (m AppModel) isInViewportY(y int) bool {
	return y >= m.vpTopY && y < m.vpTopY+m.vpH
}

// isInTextarea 判断屏幕 Y 坐标是否在 textarea 区域内
func (m AppModel) isInTextarea(y int) bool {
	return y >= m.taTopY && y < m.taTopY+3
}

// copySelectionToClipboard 将双击拖拽选中的 viewport 字符范围复制到剪贴板。
// 支持行内选择和跨行选择。viewport.View() 仅返回可见区域，
// 行列索引直接基于可见区域计算（不加 YOffset）。
func (m *AppModel) copySelectionToClipboard() tea.Cmd {
	// 将屏幕坐标转为可见内容的行列索引（0-based）
	startLine := m.selectionStartY - m.vpTopY
	startCol := m.selectionStartX
	endLine := m.selectionEndY - m.vpTopY
	endCol := m.selectionEndX

	// viewport.View() 返回当前可见区域的内容
	rendered := m.viewport.View()
	contentLines := strings.Split(rendered, "\n")

	text := extractSelectedTextRange(contentLines, startLine, startCol, endLine, endCol)
	if text == "" {
		return nil
	}

	if err := copyTextToClipboard(text); err != nil {
		logger.Warn("复制到剪贴板失败", zap.Error(err))
		return nil
	}

	logger.Debug("文本已复制到剪贴板", zap.Int("行数", endLine-startLine+1))
	return nil
}

// handleStreamChunk 处理流式响应的文本片段
func (m AppModel) handleStreamChunk(msg StreamChunkMsg) (tea.Model, tea.Cmd) {
	m.streamingText += msg.Content
	m.updateViewportContent()
	return m, waitForChunk(m.streamCh)
}

// handleStreamDone 处理流式响应完成事件
func (m AppModel) handleStreamDone() (tea.Model, tea.Cmd) {
	if m.streamingText != "" {
		m.convMgr.AddAssistantMessage(m.streamingText)
		m.messages = append(m.messages, chatMessage{
			Role:    llm.RoleAssistant,
			Content: m.streamingText,
		})
	}
	m.streamingText = ""
	m.isStreaming = false
	m.cancelFunc = nil
	m.persistSession()
	m.updateViewportContent()
	return m, m.textarea.Focus()
}

// handleStreamError 处理流式响应错误
func (m AppModel) handleStreamError(msg StreamErrorMsg) (tea.Model, tea.Cmd) {
	m.isStreaming = false
	m.cancelFunc = nil
	m.messages = append(m.messages, chatMessage{
		Role:     llm.RoleAssistant,
		Content:  fmt.Sprintf("API 请求失败: %s", msg.Err.Error()),
		IsError:  true,
	})
	m.streamingText = ""
	m.updateViewportContent()
	m.persistSession()
	return m, m.textarea.Focus()
}

// View 实现 Bubble Tea Model 接口，渲染整个 TUI 界面
func (m AppModel) View() string {
	if !m.ready {
		return "\n  正在初始化 CodePilot...\n"
	}

	// 渲染 viewport 和滚动条
	vpView := m.viewport.View()

	// 对 viewport 可见内容应用选择区域高亮背景
	// selectionActive: 正在拖拽选择中；selectionCopied: 复制后短暂保留高亮
	if m.selectionActive || m.selectionCopied {
		vpView = renderSelectionHighlight(vpView, m.vpTopY, m.selectionStartY, m.selectionStartX, m.selectionEndY, m.selectionEndX)
	}

	sb := renderScrollbar(
		m.viewport.TotalLineCount(),
		m.viewport.VisibleLineCount(),
		m.viewport.YOffset,
		m.viewport.Height,
	)
	viewportWithScrollbar := lipgloss.JoinHorizontal(lipgloss.Bottom, vpView, sb)

	// 整个 UI 收窄一列，最右列留空，避免写满终端最后一列触发 pending wrap。
	usableWidth := m.width - 1
	if usableWidth < 1 {
		usableWidth = 1
	}

	// 输入框顶部分割线，与 Logo 底部分割线保持同风格，凸显输入区域边界
	taDivider := lipgloss.NewStyle().
		Foreground(lipgloss.Color("62")).
		Render(strings.Repeat("─", max(usableWidth, 20)))

	// 构建界面：Logo + Viewport(含滚动条) + [命令候选] + 分割线 + 输入框 + 状态栏
	sections := []string{
		LogoView(usableWidth),
		viewportWithScrollbar,
	}

	// 命令选择模式下，在输入框上方（光标上侧）渲染候选命令列表
	if m.commandMode && len(m.commandSuggestions) > 0 {
		sections = append(sections, m.renderCommandSuggestions())
	}

	sections = append(sections,
		taDivider,
		m.textarea.View(),
		StatusBarView(
			m.config.Model,
			m.convMgr.TokenEstimate(),
			defaultContextWindowTokens,
			m.isStreaming,
			usableWidth,
		),
	)

	// [J 清除屏幕残余内容，防止命令提示列表出现/消失后旧帧残留导致分割线被覆盖
	return lipgloss.JoinVertical(lipgloss.Left, sections...) + "[J"
}

// sendUserMessage 发送用户消息并启动 LLM 流式请求
func (m *AppModel) sendUserMessage(content string) tea.Cmd {
	// 添加用户消息到对话管理器和显示列表
	m.convMgr.AddUserMessage(content)
	m.messages = append(m.messages, chatMessage{
		Role:    llm.RoleUser,
		Content: content,
	})
	m.textarea.Reset()

	// 标记为流式响应中
	m.isStreaming = true
	m.streamingText = ""

	// 创建可取消的上下文，支持 Esc 中断
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelFunc = cancel

	// 启动流式请求
	ch, err := m.provider.StreamChat(ctx, "", m.convMgr.GetContext(""))
	if err != nil {
		m.isStreaming = false
		m.messages = append(m.messages, chatMessage{
			Role:     llm.RoleAssistant,
			Content:  fmt.Sprintf("启动流式请求失败: %s", err.Error()),
			IsError:  true,
		})
		m.updateViewportContent()
		return nil
	}

	m.streamCh = ch
	m.updateViewportContent()
	return waitForChunk(ch)
}

// waitForChunk 从流式 channel 读取一个 chunk 并转为 Bubble Tea 消息。
// 这是标准的 Bubble Tea Cmd 模式：每个 Cmd 读取一个 chunk，
// 返回消息后由 Update 调度下一个 Cmd 继续读取。
func waitForChunk(ch <-chan llm.StreamChunk) tea.Cmd {
	return func() tea.Msg {
		chunk, ok := <-ch
		if !ok {
			return StreamDoneMsg{}
		}
		if chunk.Err != nil {
			return StreamErrorMsg{Err: chunk.Err}
		}
		if chunk.Done {
			return StreamDoneMsg{}
		}
		return StreamChunkMsg{Content: chunk.Content}
	}
}

// updateViewportContent 更新对话区域的显示内容
func (m *AppModel) updateViewportContent() {
	content := m.buildConversationContent()
	if m.renderer != nil {
		rendered, err := m.renderer.Render(content)
		if err == nil {
			m.viewport.SetContent(rendered)
			m.viewport.GotoBottom()
			return
		}
	}
	// 渲染失败时降级为纯文本
	m.viewport.SetContent(content)
	m.viewport.GotoBottom()
}

// buildConversationContent 构建完整的对话内容（Markdown 格式）
func (m *AppModel) buildConversationContent() string {
	if len(m.messages) == 0 && m.streamingText == "" {
		return "欢迎使用 **CodePilot**！输入消息开始对话。\n"
	}

	var sb strings.Builder

	for i, msg := range m.messages {
		switch msg.Role {
		case llm.RoleUser:
			sb.WriteString(fmt.Sprintf("**你：**\n%s", msg.Content))
		case llm.RoleAssistant:
			if msg.IsError {
				sb.WriteString(fmt.Sprintf("~~**错误：** %s~~", msg.Content))
			} else {
				sb.WriteString(fmt.Sprintf("**CodePilot：**\n%s", msg.Content))
			}
		}
		if i < len(m.messages)-1 || m.streamingText != "" {
			sb.WriteString("\n\n---\n\n")
		}
	}

	if m.streamingText != "" {
		sb.WriteString(fmt.Sprintf("**CodePilot：**\n%s", m.streamingText))
		if m.isStreaming {
			sb.WriteString(" ⣻")
		}
	}

	return sb.String()
}

// persistSession 持久化当前会话到磁盘
func (m *AppModel) persistSession() {
	if m.sessMgr == nil {
		return
	}
	if m.currentSession == nil {
		m.currentSession = m.sessMgr.CreateNew()
	}
	// 持久化使用完整对话历史（唯一真相源），而非经滑动窗口裁剪的视图，
	// 避免超出窗口的早期消息在保存时被永久丢弃。
	m.currentSession.Messages = m.convMgr.AllMessages()
	if err := m.sessMgr.Save(m.currentSession); err != nil {
		logger.Error("保存会话失败", zap.Error(err))
	}
}

// handleSessionCommand 解析并执行会话管理命令（/sessions, /new, /resume）
func (m *AppModel) handleSessionCommand(input string) (tea.Model, tea.Cmd) {
	parts := strings.SplitN(input, " ", 2)
	cmd := parts[0]

	switch cmd {
	case "/sessions":
		return m.handleSessionsCommand()
	case "/new":
		return m.handleNewSessionCommand()
	case "/clear":
		return m.handleClearCommand()
	case "/resume":
		if len(parts) < 2 || strings.TrimSpace(parts[1]) == "" {
			m.messages = append(m.messages, chatMessage{
				Role:    llm.RoleAssistant,
				Content: "用法: /resume <会话ID>\n输入 /sessions 查看历史会话列表",
			})
			m.updateViewportContent()
			return *m, nil
		}
		return m.handleResumeCommand(strings.TrimSpace(parts[1]))
	default:
		m.messages = append(m.messages, chatMessage{
			Role:    llm.RoleAssistant,
			Content: fmt.Sprintf("未知命令: %s\n\n可用命令: /sessions, /new, /resume <id>, /clear", cmd),
		})
		m.updateViewportContent()
		return *m, nil
	}
}

// handleSessionsCommand 展示历史会话列表，格式化为 Markdown 表格。
// 当前所在会话 ID 前加 * 标记，无会话时显示友好提示。
func (m *AppModel) handleSessionsCommand() (tea.Model, tea.Cmd) {
	summaries, err := m.sessMgr.ListSessions()
	if err != nil {
		m.messages = append(m.messages, chatMessage{
			Role:    llm.RoleAssistant,
			Content: fmt.Sprintf("获取会话列表失败: %s", err.Error()),
			IsError: true,
		})
		m.updateViewportContent()
		return *m, nil
	}

	if len(summaries) == 0 {
		m.messages = append(m.messages, chatMessage{
			Role:    llm.RoleAssistant,
			Content: "暂无历史会话。输入消息开始对话，或输入 /new 创建新会话。",
		})
		m.updateViewportContent()
		return *m, nil
	}

	var sb strings.Builder
	sb.WriteString("**历史会话：**\n\n")
	sb.WriteString("| # | ID(前8位) | 更新时间 | 消息数 | 预览 |\n")
	sb.WriteString("|---|---|---|---|---|\n")

	for i, s := range summaries {
		idShort := s.ID
		if len(idShort) > 8 {
			idShort = idShort[:8]
		}
		if m.currentSession != nil && m.currentSession.ID == s.ID {
			idShort += "（当前）"
		}
		sb.WriteString(fmt.Sprintf("| %d | %s | %s | %d | %s |\n",
			i+1, idShort, s.UpdatedAt.Format("2006-01-02 15:04"), s.MessageCount, s.Preview))
	}
	sb.WriteString("\n输入 `/resume <id>` 恢复指定会话，输入 `/new` 创建新会话")

	m.messages = append(m.messages, chatMessage{
		Role:    llm.RoleAssistant,
		Content: sb.String(),
	})
	m.updateViewportContent()
	return *m, nil
}

// handleNewSessionCommand 创建新会话并清空当前对话窗口。
// 持久化旧会话后，创建新的 Session 和 ConversationManager 实例。
func (m *AppModel) handleNewSessionCommand() (tea.Model, tea.Cmd) {
	m.persistSession()

	m.currentSession = m.sessMgr.CreateNew()
	m.convMgr = conversation.NewConversationManager(50)
	m.messages = []chatMessage{}
	m.streamingText = ""

	m.updateViewportContent()
	return *m, nil
}

// handleClearCommand 清除当前会话的对话上下文。
// 保持会话 ID 不变，重置对话管理器并清空显示，相当于在同一个会话中"重新开始"。
func (m *AppModel) handleClearCommand() (tea.Model, tea.Cmd) {
	m.persistSession()

	m.convMgr = conversation.NewConversationManager(50)
	m.messages = []chatMessage{}
	m.streamingText = ""

	m.updateViewportContent()
	return *m, nil
}

// handleResumeCommand 切换到指定会话，支持 ID 前缀匹配（至少 1 位）。
// 匹配唯一时执行切换；多个匹配时提示歧义；无匹配时提示错误。
// 切换失败时停留在原会话，不影响当前状态。
func (m *AppModel) handleResumeCommand(idPrefix string) (tea.Model, tea.Cmd) {
	summaries, err := m.sessMgr.ListSessions()
	if err != nil {
		m.messages = append(m.messages, chatMessage{
			Role:    llm.RoleAssistant,
			Content: fmt.Sprintf("获取会话列表失败: %s", err.Error()),
			IsError: true,
		})
		m.updateViewportContent()
		return *m, nil
	}

	// 前缀匹配
	var matched []session.SessionSummary
	for _, s := range summaries {
		if strings.HasPrefix(s.ID, idPrefix) {
			matched = append(matched, s)
		}
	}

	switch len(matched) {
	case 0:
		m.messages = append(m.messages, chatMessage{
			Role:    llm.RoleAssistant,
			Content: "未找到匹配的会话",
		})
		m.updateViewportContent()
		return *m, nil
	case 1:
		// 唯一匹配，继续切换
	default:
		var sb strings.Builder
		sb.WriteString("匹配到多个会话，请输入更长的 ID 前缀：\n\n")
		for _, s := range matched {
			shortID := s.ID
			if len(shortID) > 8 {
				shortID = shortID[:8]
			}
			sb.WriteString(fmt.Sprintf("- %s\n", shortID))
		}
		m.messages = append(m.messages, chatMessage{
			Role:    llm.RoleAssistant,
			Content: sb.String(),
		})
		m.updateViewportContent()
		return *m, nil
	}

	targetID := matched[0].ID

	// 持久化当前会话
	m.persistSession()

	// 加载目标会话
	targetSession, err := m.sessMgr.Load(targetID)
	if err != nil {
		m.messages = append(m.messages, chatMessage{
			Role:    llm.RoleAssistant,
			Content: fmt.Sprintf("加载会话失败: %s", err.Error()),
			IsError: true,
		})
		m.updateViewportContent()
		return *m, nil
	}

	// 重置 ConversationManager，将目标会话的消息重新注入
	m.convMgr = conversation.NewConversationManager(50)
	m.messages = []chatMessage{}
	m.streamingText = ""

	for _, msg := range targetSession.Messages {
		content := extractTextFromBlocks(msg.Content)
		m.messages = append(m.messages, chatMessage{
			Role:    msg.Role,
			Content: content,
		})
		switch msg.Role {
		case llm.RoleUser:
			m.convMgr.AddUserMessage(content)
		case llm.RoleAssistant:
			m.convMgr.AddAssistantMessage(content)
		}
	}

	m.currentSession = targetSession
	m.updateViewportContent()
	return *m, nil
}

// updateCommandMode 根据当前 textarea 输入内容更新命令选择模式状态。
// 仅当输入以 / 开头且不含空格时进入命令选择模式（不含空格意味着尚未输入命令参数）。
func (m *AppModel) updateCommandMode() {
	val := m.textarea.Value()
	if strings.HasPrefix(val, "/") && !strings.Contains(val, " ") {
		m.commandMode = true
		newSuggestions := filterCommands(val)
		// 仅在候选列表变化时重置索引，避免每次按键都跳回第一项
		if len(newSuggestions) != len(m.commandSuggestions) {
			m.commandIndex = 0
		}
		m.commandSuggestions = newSuggestions
		if m.commandIndex >= len(m.commandSuggestions) {
			m.commandIndex = 0
		}
	} else {
		m.commandMode = false
		m.commandSuggestions = nil
		m.commandIndex = 0
	}
}

// fillCommandSuggestion 将高亮的候选命令填入输入框（Tab 触发）。
// 需要参数的命令填入命令名加空格，无需参数的命令填入完整命令名。
// 填入后用户可继续输入参数或按 Enter 执行。
func (m *AppModel) fillCommandSuggestion() (tea.Model, tea.Cmd) {
	if len(m.commandSuggestions) == 0 || m.commandIndex >= len(m.commandSuggestions) {
		return *m, nil
	}

	selectedCmd := m.commandSuggestions[m.commandIndex]
	m.commandMode = false
	m.commandSuggestions = nil
	m.commandIndex = 0

	// 提取命令名（去掉 <placeholder> 部分）
	cmdName := selectedCmd
	if commandNeedsArg(selectedCmd) {
		cmdName = strings.SplitN(selectedCmd, " ", 2)[0] + " "
	}

	m.textarea.SetValue(cmdName)
	m.textarea.Focus()
	return *m, nil
}

// renderCommandSuggestions 渲染命令候选列表，包含命令名和功能描述。
// 高亮项使用反色背景，其他项使用灰色文字。描述用浅灰色与命令名区分。
func (m AppModel) renderCommandSuggestions() string {
	if len(m.commandSuggestions) == 0 {
		return ""
	}

	// 计算最长命令名长度，用于对齐描述列
	maxCmdLen := 0
	for _, cmd := range m.commandSuggestions {
		if len(cmd) > maxCmdLen {
			maxCmdLen = len(cmd)
		}
	}

	highlightStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("62")).
		Foreground(lipgloss.Color("230"))
	normalStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252"))
	descStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("245"))

	var sb strings.Builder
	for i, cmd := range m.commandSuggestions {
		desc := getCommandDescription(cmd)
		line := fmt.Sprintf(" %s%s — %s", cmd, strings.Repeat(" ", maxCmdLen-len(cmd)+1), desc)
		if i == m.commandIndex {
			sb.WriteString(highlightStyle.Render(line))
		} else {
			sb.WriteString(normalStyle.Render(cmd + strings.Repeat(" ", maxCmdLen-len(cmd)+1)))
			sb.WriteString(descStyle.Render("— " + desc))
		}
		if i < len(m.commandSuggestions)-1 {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// updateSubComponents 将消息传递给 textarea 和 viewport 子组件
func (m AppModel) updateSubComponents(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	var cmd tea.Cmd

	if !m.isStreaming {
		m.textarea, cmd = m.textarea.Update(msg)
		cmds = append(cmds, cmd)
	}

	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}
