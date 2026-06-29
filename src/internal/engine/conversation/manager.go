// Package conversation 实现对话历史管理，负责消息的构造、添加和上下文获取。
// 它持有完整对话历史作为唯一真相源，并派生出发送给 LLM 的上下文视图，
// 为上层提供简洁的对话管理接口。
//
// Step 2 在此基础上扩展 RunTurn 入口：把 LLM 流式响应与工具执行串联成
// "单轮闭环"——LLM 返回 tool_use → 调度工具 → tool_result 回传 →
// 二次 LLM 拿到最终回复，并把过程中产生的所有消息写回 history。
// RunTurn 假设由调用方（web handler）在已串行化的上下文中调用，
// 内部直接读写 history，不加锁（与现有 AddXxx 方法保持一致的并发契约）。
package conversation

import (
	"context"
	"strings"
	"sync"

	"go.uber.org/zap"

	"github.com/MeiCorl/CodePilot/src/internal/hook"
	"github.com/MeiCorl/CodePilot/src/internal/logger"
	memctx "github.com/MeiCorl/CodePilot/src/internal/memory/context"
	"github.com/MeiCorl/CodePilot/src/internal/tool"
	"github.com/MeiCorl/CodePilot/src/llm"
)

// ConversationManager 管理多轮对话的消息历史。
// 它持有完整对话历史（history，唯一真相源）。GetContext 直接返回完整活跃历史，
// 由 Step 7 压缩系统控制上下文体积，避免滑动窗口裁剪破坏 tool_use/tool_result 配对。
//
// Step 4 新增：leadUserMessage 字段用于保存「首条 user 消息」内容（通常
// 来自 prompt.Builder 的 AGENTS.md 合并结果）。它在 history 之外单独持有，
// 由 GetContext 在完整历史前拼接到 messages 最前。
type ConversationManager struct {
	// history 为完整对话历史，作为唯一真相源；持久化与窗口派生均以此为基础
	history []llm.Message
	// lastInputTokens 为最近一次 LLM 调用返回的 input_tokens（精确值）。
	// 用于计算上下文窗口剩余额度：remaining = context_window_size - lastInputTokens。
	// 为 0 时表示尚无 LLM 调用（首次调用前、会话恢复后），需降级到字符估算。
	lastInputTokens int
	// contextVersion is bumped whenever the next request view may change.
	contextVersion int64
	// usageContextVersion records the request view version that lastInputTokens
	// belongs to. The precise usage is valid only while the versions match.
	usageContextVersion int64
	// leadUserMessage 为会话级「首条 user 消息」内容。
	// 通常由 prompt.Builder 的 AGENTS.md Source 注入；空字符串时 GetContext
	// 不构造空消息。
	leadUserMessage string
	// promptInjections 暂存 prompt action 产生的 user-message 尾部注入文本。
	// 它只影响下一次发送给 LLM 的上下文视图,不写入持久化 history。
	promptMu         sync.Mutex
	promptInjections []string

	// hookEngine 为 Hook 系统集成点。ConversationManager 位于引擎层,只向下依赖
	// hook 的公共接口;hook 包本身不反向依赖本包。
	hookEngine       *hook.Engine
	hookWorkdir      string
	currentIteration int

	// ---- Step 7：上下文压缩相关（Task 6 撞墙兜底 + Task 7 每轮自动压缩共用）----
	//
	// compactor 为上下文压缩协调器（可选，nil 表示未装配——压缩总开关关闭）。
	// 由 main.go 顶层装配后通过 SetCompactor 注入。runOneLLM 的撞墙兜底路径依赖它做紧急压缩。
	compactor *memctx.Compactor
	// sessionID 为当前活跃会话标识，供压缩协调器定位工具结果存盘子目录与熔断状态隔离。
	// 由 handler 在 RunAgentLoop 前通过 SetSessionID 注入（每个会话独立）。
	sessionID string
	// contextWindowSize 为当前模型的上下文窗口总大小（token 数），供协调器接口 Remaining() 计算。
	// 由 handler/main 通过 SetContextWindowSize 注入。
	contextWindowSize int
}

// NewConversationManager 创建一个对话管理器。
// maxRounds 已不再用于裁剪上下文；保留参数是为了兼容现有调用方配置。
func NewConversationManager(maxRounds int) *ConversationManager {
	_ = maxRounds
	return &ConversationManager{
		history: make([]llm.Message, 0),
	}
}

// SetLeadUserMessage 设置会话级「首条 user 消息」内容。
// 应在会话启动时调用一次（典型时机：handler.NewSession / ResumeSession 内部
// 从 prompt.Builder 拿到组装结果后调用）；同会话内多次调用以最后一次为准。
//
// lead 非空时，GetContext 会在窗口派生结果的最前追加一条 Role=User 的消息，
// 该消息同时会被 runOneLLM 透传给 Provider.StreamChat 作为 LeadUserMessage。
// lead 为空字符串时清除已设置的内容（下次 GetContext 不再追加）。
func (m *ConversationManager) SetLeadUserMessage(text string) {
	if m.leadUserMessage != text {
		m.invalidateContextUsage()
	}
	m.leadUserMessage = text
}

// LeadUserMessage 返回当前设置的「首条 user 消息」内容。
// 用于 WebUI 可观测性展示（状态栏 tooltip、开发者模式导出）。
func (m *ConversationManager) LeadUserMessage() string {
	return m.leadUserMessage
}

// SetHookEngine 注入 HookEngine。nil 表示关闭 Hook 派发,主流程保持原行为。
func (m *ConversationManager) SetHookEngine(engine *hook.Engine, workdir string) {
	m.hookEngine = engine
	m.hookWorkdir = workdir
}

// AppendToCurrentMessage 实现 hook.PromptSink。
// 文本会追加到下一次发送给 LLM 的最后一条 user 消息尾部,但不会写入 history。
func (m *ConversationManager) AppendToCurrentMessage(text string) error {
	if text == "" {
		return nil
	}
	m.promptMu.Lock()
	defer m.promptMu.Unlock()
	m.promptInjections = append(m.promptInjections, text)
	m.invalidateContextUsage()
	return nil
}

func (m *ConversationManager) dispatchHook(ctx context.Context, event string, hookCtx *hook.HookContext) {
	if m.hookEngine == nil || hookCtx == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			logger.ErrorCtx(ctx, "Hook 派发 panic，已恢复", zap.String("event", event), zap.Any("panic", r))
		}
	}()
	m.hookEngine.Dispatch(ctx, event, hookCtx)
}

func (m *ConversationManager) contextForLLM() []llm.Message {
	messages := m.GetContext()
	m.promptMu.Lock()
	if len(m.promptInjections) > 0 {
		m.promptInjections = nil
	}
	m.promptMu.Unlock()
	return messages
}

func (m *ConversationManager) appendPendingPrompt(messages []llm.Message) []llm.Message {
	m.promptMu.Lock()
	if len(m.promptInjections) == 0 {
		m.promptMu.Unlock()
		return messages
	}
	text := strings.Join(m.promptInjections, "\n\n")
	m.promptMu.Unlock()
	if text == "" {
		return messages
	}
	idx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == llm.RoleUser {
			idx = i
			break
		}
	}
	if idx < 0 {
		return append(messages, llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock(text)}})
	}
	content := make([]llm.ContentBlock, 0, len(messages[idx].Content)+1)
	content = append(content, messages[idx].Content...)
	content = append(content, llm.NewTextBlock(text))
	messages[idx].Content = content
	return messages
}

func messageText(msg llm.Message) string {
	parts := make([]string, 0, len(msg.Content))
	for _, block := range msg.Content {
		if block == nil {
			continue
		}
		if s := block.ToText(); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n")
}

func lastUserMessageText(messages []llm.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == llm.RoleUser {
			return messageText(messages[i])
		}
	}
	return ""
}

// ---- Step 7：压缩协调器装配 + ConversationHistory 接口实现 ----
//
// 以下方法让 ConversationManager 满足 memctx.ConversationHistory 接口（History / ReplaceHistory /
// Remaining），使协调器能在不反向依赖 conversation 包的前提下读写「对话历史 + 剩余量」。
// 接口方法刻意取短名 Remaining（而非 RemainingTokens），避开与本类型既有的
// RemainingTokens(maxTokens int) 同名不同签名冲突——Go 不允许同类型存在两个同名方法。

// SetCompactor 注入上下文压缩协调器。由 main.go 顶层装配后调用；nil 表示压缩关闭
// （runOneLLM 的撞墙兜底也将透传错误不做压缩）。
func (m *ConversationManager) SetCompactor(c *memctx.Compactor) { m.compactor = c }

// SetSessionID 设置当前活跃会话标识，供压缩协调器定位工具结果存盘子目录与熔断状态隔离。
// 应在 handler 启动/切换会话（NewSession / ResumeSession）时调用。
func (m *ConversationManager) SetSessionID(id string) { m.sessionID = id }

// SetContextWindowSize 设置当前模型上下文窗口总大小（token 数），供 Remaining() 计算。
// 通常来自 Config.ContextWindowSize。
func (m *ConversationManager) SetContextWindowSize(size int) { m.contextWindowSize = size }

// History 实现 memctx.ConversationHistory。
//
// 返回内部 history 的【可变视图】（直接返回切片，不 copy），使 LightCompactor 的 in-place
// 预览替换（改 *ToolResultBlock.Content）能反映回 manager——这是第一层压缩生效的关键。
// 注意：调用方不得对返回切片本身做追加/截断（会改 manager 的 history 切片头），
// 只应改切片内元素指向的对象字段；整体替换历史走 ReplaceHistory。
func (m *ConversationManager) History() []llm.Message {
	// Callers receive a mutable view and may rewrite block contents in-place
	// (the light compactor does this), so any cached precise usage is stale.
	m.invalidateContextUsage()
	return m.history
}

// ReplaceHistory 实现 memctx.ConversationHistory。
//
// 用新历史整体替换内部 history（第二层摘要压缩成功后调用），使内存与落盘的活跃视图一致。
// 同时把 lastInputTokens 清零——历史已变，旧的精确 input_tokens 不再有效，需等下一次
// LLM 调用重新获取。语义与 Reset 对齐，但 Reset 还面向「恢复会话」语义，这里单独命名
// 以匹配协调器接口，并避免触发 Reset 对其他字段的连带重置。
func (m *ConversationManager) ReplaceHistory(msgs []llm.Message) {
	m.history = make([]llm.Message, len(msgs))
	copy(m.history, msgs)
	m.invalidateContextUsage()
}

// Remaining 实现 memctx.ConversationHistory，返回当前窗口的剩余可用 token（下界 0）。
// 委托给 GetContextUsage(m.contextWindowSize).Remaining，复用「精确优先、估算降级」逻辑。
func (m *ConversationManager) Remaining() int {
	return m.GetContextUsage(m.contextWindowSize).Remaining
}

// IsLeadUserMessage 判断 GetContext 返回的 messages 切片中索引 idx
// 处的消息是否为「首条 user 消息」。
//
// 返回 true 的条件（同时满足）：
//  1. leadUserMessage 非空
//  2. idx == 0（首条）
//  3. idx 在切片范围内
//
// 返回 false 的场景：
//   - lead 未设置
//   - idx 越界
//   - idx == 0 但切片为空（防御性）
//
// 该方法主要用于单元测试断言与外部代码做语义判定；不参与流式逻辑。
func (m *ConversationManager) IsLeadUserMessage(idx int) bool {
	if m.leadUserMessage == "" {
		return false
	}
	return idx == 0
}

// AddUserMessage 添加一条用户消息到完整对话历史。
// content 为用户输入的文本，内部构造为 Message{Role: RoleUser, Content: []ContentBlock{TextBlock}}。
func (m *ConversationManager) AddUserMessage(content string) {
	m.history = append(m.history, llm.Message{
		Role:    llm.RoleUser,
		Content: []llm.ContentBlock{llm.NewTextBlock(content)},
	})
	m.invalidateContextUsage()
}

// AddAssistantMessage 添加一条助手消息到完整对话历史。
// content 为助手回复的文本，内部构造为 Message{Role: RoleAssistant, Content: []ContentBlock{TextBlock}}。
func (m *ConversationManager) AddAssistantMessage(content string) {
	m.history = append(m.history, llm.Message{
		Role:    llm.RoleAssistant,
		Content: []llm.ContentBlock{llm.NewTextBlock(content)},
	})
	m.invalidateContextUsage()
}

// AddMessage 添加一条任意角色/内容的消息到完整对话历史。
// 用于 RunTurn 内部插入 tool_use、tool_result 消息以及最终的
// assistant 文本消息；调用方需保证 msg 的 Role/Content 合法性。
func (m *ConversationManager) AddMessage(msg llm.Message) {
	m.history = append(m.history, msg)
	m.invalidateContextUsage()
}

// Reset 用给定消息替换完整对话历史。
// 用于恢复历史会话时把磁盘加载的消息注入到管理器；调用后
// 后续 AddXxx / GetContext / AllMessages 均以新历史为基础。
// 传入 nil 等价于清空历史。
// 同时重置 lastInputTokens，因为历史变更后旧的 input_tokens 不再有效，
// 需要等待下一次 LLM 调用获取新的精确值。
func (m *ConversationManager) Reset(messages []llm.Message) {
	m.history = make([]llm.Message, len(messages))
	copy(m.history, messages)
	m.invalidateContextUsage()
}

// GetContext 返回发送给 LLM 的上下文窗口视图。
//
// 视图组成（Step 4 起）：
//  1. 可选的首条 user 消息（leadUserMessage），来自 SetLeadUserMessage；
//     通常包含 AGENTS.md 合并结果 + Step 8 自动记忆
//  2. 完整活跃 history。历史体积由 Step 7 的轻量/摘要压缩负责控制。
//
// 重要：返回结果**不**包含 system 字段消息——system 字段已迁移到
// llm.SystemPrompt（由 StreamChat 的 sp 参数携带），不在 messages 内。
//
// 注意：返回结果是活跃历史副本；持久化请使用 AllMessages。
// leadUserMessage 也不会出现在 AllMessages 中（它由 SetLeadUserMessage 单独管理）。
func (m *ConversationManager) GetContext() []llm.Message {
	history := make([]llm.Message, len(m.history))
	copy(history, m.history)

	// 拼接 lead user message（如果存在）到最前
	if m.leadUserMessage == "" {
		return m.appendPendingPrompt(history)
	}
	out := make([]llm.Message, 0, len(history)+1)
	out = append(out, llm.Message{
		Role:    llm.RoleUser,
		Content: []llm.ContentBlock{llm.NewTextBlock(m.leadUserMessage)},
	})
	out = append(out, history...)
	return m.appendPendingPrompt(out)
}

// AllMessages 返回完整对话历史的副本，用于会话持久化归档。
// 与 GetContext 不同，该结果不包含 leadUserMessage，只包含真实历史消息，
// 是持久化时应当使用的唯一真相源。
func (m *ConversationManager) AllMessages() []llm.Message {
	out := make([]llm.Message, len(m.history))
	copy(out, m.history)
	return out
}

// TokenEstimate 估算当前发送给 LLM 的窗口视图已使用的 token 数。
// 采用粗估策略：文本按字符类型估算 + 每条消息固定结构开销 + 工具定义开销。
// 不需要精确，仅用于状态栏展示和溢出保护参考，但需要比纯文本估算更接近真实值。
// 注意：此处基于窗口视图（而非完整历史）估算，反映的是实际发送给 LLM 的上下文量。
// Step 4 起：会把 leadUserMessage 计入（它也是发送给 LLM 的实际内容之一）。
func (m *ConversationManager) TokenEstimate() int {
	// 1. 消息体 token：复用记忆层 measure 包的统一估算（CJK/非 CJK 字符比例 +
	//    每条消息固定结构开销），保证状态栏、压缩器、协调器用同一把尺子度量，
	//    避免估算口径不一致导致压缩阈值判断抖动、prompt cache 命中率波动。
	//    （Step 7 把原 estimateTextTokens/isCJK/messageOverhead 下沉到
	//    memory/context 包的 EstimateMessagesTokens，本函数行为不变。）
	totalTokens := memctx.EstimateMessagesTokens(m.GetContext())

	// 2. 补充估算：工具定义的 token 开销（不属于消息体，不下沉到 measure 包——
	//    measure 只负责文本/消息度量，不应依赖 tool 包，保持记忆层无反向依赖）。
	// 每个注册工具的 JSON Schema 定义约占用 50-100 个 token（含名称、描述、参数结构），
	// 通过注册表获取已注册的工具数量来估算。
	totalTokens += estimateToolDefinitionTokens()

	return totalTokens
}

// estimateToolDefinitionTokens 估算当前注册的所有工具定义的 token 开销。
// 每个工具的 JSON Schema（名称+描述+参数结构）约占用 80 个 token。
// 这个值在 AgentLoop 每次迭代检查上下文溢出时使用，偏高比偏低安全。
func estimateToolDefinitionTokens() int {
	registry := tool.DefaultRegistry()
	if registry == nil {
		return 0
	}
	toolCount := registry.Count()
	if toolCount == 0 {
		return 0
	}
	// 每个工具约 80 token（name + description + input_schema JSON）
	return toolCount * 80
}

// RemainingTokens 返回在给定的最大 token 额度下，剩余可用的 token 数。
// 如果已超出额度，返回 0。
// 内部委托给 GetContextUsage 以消除重复计算逻辑。
func (m *ConversationManager) RemainingTokens(maxTokens int) int {
	return m.GetContextUsage(maxTokens).Remaining
}

// UpdateUsage 从 LLM 响应中更新 token 用量统计。
// 当 usage 非空且 InputTokens > 0 时，将 input_tokens 记录到 lastInputTokens，
// 用于后续 GetContextUsage 的精确计算。
func (m *ConversationManager) UpdateUsage(usage *llm.TokenUsage) {
	if usage != nil && usage.InputTokens > 0 {
		m.lastInputTokens = usage.InputTokens
		m.usageContextVersion = m.contextVersion
	}
}

func (m *ConversationManager) invalidateContextUsage() {
	m.contextVersion++
}

// ContextUsage 描述上下文窗口的使用情况，供上层（状态栏展示、溢出检查）统一使用。
// 包含已用量、窗口总大小、剩余量和已使用百分比，避免上层各自拼装计算逻辑。
type ContextUsage struct {
	// Used 为已使用的 token 数（优先使用 API 返回的精确值，降级到字符估算）
	Used int
	// Limit 为上下文窗口总大小（token 数）
	Limit int
	// Remaining 为剩余可用 token 数（= Limit - Used），下界为 0
	Remaining int
	// PercentUsed 为已使用百分比（0~100），用于前端状态栏展示
	PercentUsed int
}

// GetContextUsage 返回在给定的上下文窗口大小下的完整使用情况。
//
// 已用量（Used）计算策略：
//   - 精确模式：当 lastInputTokens > 0 时，使用最近一次 LLM 调用返回的 input_tokens。
//     该值是 API 对本次请求的精确计量，已包含 system_prompt、全部历史消息、
//     工具定义等所有开销，无需额外估算。
//   - 降级模式：当 lastInputTokens == 0 时（首次调用前、会话恢复后、流中断），
//     使用字符粗估（TokenEstimate）作为兜底。
//
// windowSize 为上下文窗口总大小（token 数），通常来自 Config.ContextWindowSize。
func (m *ConversationManager) GetContextUsage(windowSize int) ContextUsage {
	var used int
	if m.lastInputTokens > 0 && m.usageContextVersion == m.contextVersion {
		// 精确模式：remaining = context_window_size - last_input_tokens
		used = m.lastInputTokens
	} else {
		// 降级模式：字符粗估
		used = m.TokenEstimate()
	}
	remaining := windowSize - used
	if remaining < 0 {
		remaining = 0
	}
	percentUsed := 0
	if windowSize > 0 {
		percentUsed = used * 100 / windowSize
		if percentUsed > 100 {
			percentUsed = 100
		}
	}
	return ContextUsage{
		Used:        used,
		Limit:       windowSize,
		Remaining:   remaining,
		PercentUsed: percentUsed,
	}
}

// MessageCount 返回完整对话历史中的消息数量。
func (m *ConversationManager) MessageCount() int {
	return len(m.history)
}

// ---- Step 2: 单轮 LLM + 工具执行闭环 ----

// TurnHooks 把 RunTurn 执行过程中的流式事件外推给上层。
//
// 所有回调均为可选（nil 表示不关心该事件）；RunTurn 在收到对应事件
// 时同步调用，**不要在回调里做阻塞或耗时的操作**（如同步 I/O、长计算），
// 否则会拖慢 LLM 流式吞吐。
//
// OnStreamChunk 在每次收到 StreamChunk 时被调用一次（含 Done=true 的
// 最后一个 chunk）。OnToolUse 在第一次 LLM 决定调用工具时被调用一次。
// OnToolResult 在工具执行完成（成功/失败/超时/取消）后被调用一次。
// OnError 在 RunTurn 内部出现不可恢复错误时被调用一次（如 StreamChat
// 初始化失败、StreamChunk 携带 Err）；调用后 RunTurn 立即返回。
type TurnHooks struct {
	// OnStreamChunk 推送流式 chunk 文本与结束信号（流结束时 Done=true）
	OnStreamChunk func(llm.StreamChunk)
	// OnToolUse 在 LLM 决定调用工具时回调（不区分成功失败，仅通知）
	OnToolUse func(llm.ToolUseBlock)
	// OnToolResult 在工具执行结束后回调，含执行结果/错误
	OnToolResult func(llm.ToolResultBlock)
	// OnError 在 RunTurn 内部出现错误时回调
	OnError func(error)
	// OnCompaction 在每轮 LLM 请求前的自动压缩产生变更时回调（Step 7）。
	// 仅当本轮实际发生压缩（Level != none）时触发，供交互层推送 compaction_event。
	// 回调在 runOneLLM 内同步调用（与 OnStreamChunk 同 goroutine），不应阻塞。
	OnCompaction func(memctx.CompactionResult)
}

// TurnResult 是 RunTurn 的返回值，描述本轮对话的最终结果。
//
// 字段语义：
//   - FinalText: 最终 LLM 回复累积的完整文本；
//     当本轮没有触发 tool_use 时，等价于第一次 LLM 的回复文本
//   - ToolUses: 本轮触发的所有 tool_use 块（如有），未触发时为 nil
//   - ToolResults: 与 ToolUses 对应的工具执行结果（如有），未触发时为 nil
//   - Aborted: 本轮是否被 ctx 取消
//   - Error: 本轮发生的不可恢复错误（StreamChat 失败、chunk.Err 等）
type TurnResult struct {
	// FinalText 为本轮用户可见的最终回复文本（流式累积）
	FinalText string
	// ToolUses 为触发的所有 tool_use 块（如有）
	ToolUses []llm.ToolUseBlock
	// ToolResults 为工具执行结果列表（如有），与 ToolUses 一一对应
	ToolResults []llm.ToolResultBlock
	// Aborted 标识本轮是否被 ctx 取消中断
	Aborted bool
	// Error 为本轮发生的不可恢复错误（如有）
	Error error
}

// RunTurn 执行一轮 LLM 对话 + Agent Loop 循环。
//
// RunTurn 是 AgentLoop 的兼容包装：内部构造 AgentLoopConfig 并委托给 AgentLoop，
// 保持原有的调用签名不变。调用方（如 web/handler）无需感知内部重构。
//
// 当 AgentLoopConfig 未指定时，默认使用 MaxIterations=50 的循环配置。
// 并发约束：RunTurn 直接读写 history，调用方需保证同一时刻只有一个
// RunTurn 活跃（实际由 web/handler 的 streamState 状态机串行化）。
//
// Step 4 起：systemPrompt 升级为 llm.SystemPrompt，调用方传 nil 时回退到
// buildSystemPromptFromString 构造的空 SP（保持旧版调用兼容）。
func (m *ConversationManager) RunTurn(
	ctx context.Context,
	provider llm.Provider,
	sp llm.SystemPrompt,
	toolSpecs []tool.ToolSpec,
	toolHandler *ToolHandler,
	hooks TurnHooks,
) TurnResult {
	// 记录当前 history 长度，用于事后提取本轮新增的工具调用信息
	historyBefore := len(m.history)

	// 委托给 AgentLoop，使用默认配置
	loopResult := m.AgentLoop(ctx, provider, sp, toolSpecs, toolHandler,
		AgentLoopConfig{
			MaxIterations: 50,
		},
		AgentLoopHooks{
			TurnHooks: hooks,
		},
	)

	// 将 AgentLoopResult 转换为 TurnResult
	result := TurnResult{
		FinalText: loopResult.FinalText,
		Aborted:   loopResult.Aborted,
		Error:     loopResult.Error,
	}

	// 从本轮新增的 history 中提取 tool_use 和 tool_result，填充兼容字段
	var toolUses []llm.ToolUseBlock
	var toolResults []llm.ToolResultBlock
	for i := historyBefore; i < len(m.history); i++ {
		for _, block := range m.history[i].Content {
			if tu, ok := block.(*llm.ToolUseBlock); ok {
				toolUses = append(toolUses, *tu)
			}
			if tr, ok := block.(*llm.ToolResultBlock); ok {
				toolResults = append(toolResults, *tr)
			}
		}
	}
	result.ToolUses = toolUses
	result.ToolResults = toolResults

	return result
}

// RunAgentLoop 是 RunTurn 的增强版，支持传入完整的 AgentLoopConfig。
//
// 与 RunTurn 的区别：
//   - 支持配置 MaxIterations、ContextSafetyMargin、ContextWindowSize
//   - 支持 OnIterationStart / OnLoopDone 回调
//   - 返回 AgentLoopResult（含 Iterations、TotalToolCalls、StopReason）
//
// Step 4 起：systemPrompt 升级为 llm.SystemPrompt 结构体。
func (m *ConversationManager) RunAgentLoop(
	ctx context.Context,
	provider llm.Provider,
	sp llm.SystemPrompt,
	toolSpecs []tool.ToolSpec,
	toolHandler *ToolHandler,
	cfg AgentLoopConfig,
	hooks AgentLoopHooks,
) AgentLoopResult {
	return m.AgentLoop(ctx, provider, sp, toolSpecs, toolHandler, cfg, hooks)
}

// RunOneTurnResult 是 runOneLLM 的内部返回值，描述单次 LLM 流式调用的结果。
type RunOneTurnResult struct {
	// Text 为累积的 assistant 文本
	Text string
	// ToolUses 为流结束时累积的所有 tool_use 块（支持并行工具调用）
	ToolUses []llm.ToolUseBlock
	// Usage 为本次 LLM 调用的 token 用量（从 Done chunk 提取），可能为 nil
	Usage *llm.TokenUsage
	// Aborted 标识本次流是否被 ctx 取消
	Aborted bool
	// Err 为 StreamChat 初始化失败或 chunk.Err 携带的错误
	Err error
	// LLMStopReason 为 LLM API 返回的停止原因，用于诊断输出是否被截断。
	// 可能值见 llm.StreamChunk.LLMStopReason 注释。
	LLMStopReason string
}

// HasToolUse 返回本次 LLM 响应是否包含 tool_use 块。
func (r RunOneTurnResult) HasToolUse() bool {
	return len(r.ToolUses) > 0
}

// FirstToolUse 返回第一个 tool_use 块（如无则返回 nil）。
// 便捷方法，适用于单工具调用场景。
func (r RunOneTurnResult) FirstToolUse() *llm.ToolUseBlock {
	if len(r.ToolUses) == 0 {
		return nil
	}
	return &r.ToolUses[0]
}

// runOneLLM 发起一次 LLM 流式调用并消费完整流。
//
// 不修改 history；返回累积的文本与 ToolUses 供上层决策下一步。
// 通过 hooks.OnStreamChunk 把每个 chunk 推给上层（含 Done=true 的结束 chunk）。
//
// Step 4 起：sp 已经是结构化的 llm.SystemPrompt，Provider 据此构造 system
// 字段与首条 user 消息；LeadUserMessage 由 ConversationManager 内部管理
// （SetLeadUserMessage 设置后由 GetContext 自动拼到 messages 最前）。
func (m *ConversationManager) runOneLLM(
	ctx context.Context,
	provider llm.Provider,
	sp llm.SystemPrompt,
	toolSpecs []tool.ToolSpec,
	hooks TurnHooks,
) RunOneTurnResult {
	// Step 7：每轮 API 请求前先跑自动压缩编排（第一层必跑 + 第二层按余量/熔断判定）。
	// 压缩失败不中断主流程（第二层失败时 history 仍可用），产生变更时通过 hooks.OnCompaction 外推。
	m.runAutoCompaction(ctx, provider, hooks)

	previewMessages := m.GetContext()
	m.dispatchHook(ctx, hook.EventPreMessage,
		hook.NewMessageContext(hook.EventPreMessage, lastUserMessageText(previewMessages), string(llm.RoleUser), m.sessionID, m.hookWorkdir, m.currentIteration))
	messages := m.contextForLLM()
	chunkCh, err := provider.StreamChat(ctx, sp, messages, toolSpecs)
	if err != nil {
		// ---- Task 6：撞墙兜底——上下文超长时紧急压缩 + 重试一次 ----
		//
		// Provider 返回「上下文超长」错误（prompt_too_long / context_length_exceeded，均为 400）
		// 时，不直接报错中断（否则用户最新输入可能因历史过长被拒而丢失），而是：
		//  1. 调协调器 EmergencyCompact 做一次更激进的紧急压缩（强制第二层摘要 + 无视余量 +
		//     临时豁免熔断），最大化腾出上下文空间；
		//  2. 用压缩后的 history 重新 GetContext 再 StreamChat 一次（仅重试 1 次，防无限循环）；
		//  3. 重试成功 → 继续正常消费流；重试仍失败 / 紧急压缩失败 → 返回【原始的】超长错误
		//     （不吞异常，让上层如实上报根因）。即使重试失败，历史已被压缩、用户最新输入仍在
		//     历史尾部未丢失，下一轮可正常进行。
		//
		// 触发条件同时要求 compactor 已注入（nil 表示压缩总开关关闭，此时降级为透传错误，
		// 行为与 Task 7 未接入时一致，保证不回归）。
		//
		// wallHitRetried 为【局部】标志——语义是「单次 runOneLLM 调用内只重试一次」，用局部变量
		// 天然限定作用域（函数返回即失效），避免用结构体字段带来的跨调用状态污染与多返回点
		// 重置遗漏。AgentLoop 每轮迭代调一次 runOneLLM，每轮都享有一次兜底机会。
		wallHitRetried := false
		if llm.IsContextTooLongError(err) && m.compactor != nil {
			wallHitRetried = true
			if compactErr := m.emergencyCompactOnWallHit(ctx, provider, hooks); compactErr == nil {
				// 紧急压缩成功：用压缩后的历史重试一次请求。
				messages = m.contextForLLM()
				chunkCh, err = provider.StreamChat(ctx, sp, messages, toolSpecs)
			}
			// 紧急压缩失败（compactErr != nil）：保留原始 err（超长错误）上报，不吞异常。
		}
		_ = wallHitRetried // 仅用于语义标注；重试次数由「本分支只进一次」结构性保证
		if err != nil {
			if hooks.OnError != nil {
				hooks.OnError(err)
			}
			return RunOneTurnResult{Err: err}
		}
	}

	var (
		textBuf       []byte
		toolUses      []llm.ToolUseBlock
		usage         *llm.TokenUsage
		aborted       bool
		streamDone    bool
		llmStopReason string
	)
	for {
		select {
		case <-ctx.Done():
			// ctx 取消：标记 aborted 并尝试排空 channel 以让 Provider goroutine 退出
			aborted = true
			for {
				select {
				case chunk, ok := <-chunkCh:
					if !ok {
						return RunOneTurnResult{Text: string(textBuf), ToolUses: toolUses, Usage: usage, Aborted: true, LLMStopReason: llmStopReason}
					}
					// 排空时顺便提取最后的 stop_reason 和 usage
					if chunk.Usage != nil {
						usage = chunk.Usage
					}
					if chunk.LLMStopReason != "" {
						llmStopReason = chunk.LLMStopReason
					}
				default:
					return RunOneTurnResult{Text: string(textBuf), ToolUses: toolUses, Usage: usage, Aborted: true, LLMStopReason: llmStopReason}
				}
			}
		case chunk, ok := <-chunkCh:
			if !ok {
				// channel 关闭：流正常结束
				if !streamDone {
					// 合成一个 Done=true 的 chunk 推给上层（部分 Provider 在脚本耗尽时直接 close，不发 Done）
					if hooks.OnStreamChunk != nil {
						hooks.OnStreamChunk(llm.StreamChunk{Done: true})
					}
				}
				m.logLLMStopReason(ctx, llmStopReason, usage, aborted)
				return RunOneTurnResult{Text: string(textBuf), ToolUses: toolUses, Usage: usage, Aborted: aborted, LLMStopReason: llmStopReason}
			}
			if chunk.Err != nil {
				if hooks.OnError != nil {
					hooks.OnError(chunk.Err)
				}
				return RunOneTurnResult{Text: string(textBuf), ToolUses: toolUses, Usage: usage, Aborted: aborted, Err: chunk.Err, LLMStopReason: llmStopReason}
			}
			// 收集所有 tool_use 块（Done chunk 上携带）
			if chunk.HasToolUse() {
				toolUses = append(toolUses, chunk.ToolUses...)
			}
			// 提取 token 用量（Done chunk 上携带）
			if chunk.Usage != nil {
				usage = chunk.Usage
			}
			// 提取 LLM 停止原因（Done chunk 上携带）
			if chunk.LLMStopReason != "" {
				llmStopReason = chunk.LLMStopReason
			}
			if chunk.Content != "" {
				textBuf = append(textBuf, chunk.Content...)
			}
			if hooks.OnStreamChunk != nil {
				hooks.OnStreamChunk(chunk)
			}
			if chunk.Done {
				streamDone = true
				m.logLLMStopReason(ctx, llmStopReason, usage, aborted)
				return RunOneTurnResult{Text: string(textBuf), ToolUses: toolUses, Usage: usage, Aborted: aborted, LLMStopReason: llmStopReason}
			}
		}
	}
}

// runAutoCompaction 执行每轮 API 请求前的自动压缩编排（Step 7 接入主流程）。
//
// 触发条件：compactor 已注入（总开关开启）。调用 Compact(manual=false)：
//   - 第一层（必跑）：超阈值工具结果存盘 + in-place 预览替换，in-place 改 *ToolResultBlock
//     指针指向对象，随后 GetContext 自然返回压缩后视图。
//   - 第二层（按余量/熔断判定）：仅当剩余 ≤ AutoTriggerMargin 且未熔断时跑历史摘要压缩。
//
// 失败语义：Compact 返回的 err 仅在第二层摘要失败时非 nil（第一层恒 nil）。此时 history
// 未被改动（SummaryCompactor 保证），主流程仍可用当前 history 发请求——故仅记 Warn 日志、
// 不中断、不向上抛。这与撞墙兜底（emergencyCompactOnWallHit）的区别：撞墙时若紧急压缩失败
// 需如实上报原始超长错误；而这里是每轮的预防性压缩，失败应静默降级继续。
//
// 可观测性：本轮 Level != none（实际发生压缩）时通过 hooks.OnCompaction 外推，
// 由交互层（handler）转 compaction_event 推送给前端。Level == none（未压缩）时不打扰。
func (m *ConversationManager) runAutoCompaction(
	ctx context.Context,
	provider llm.Provider,
	hooks TurnHooks,
) {
	if m.compactor == nil {
		// 压缩总开关关闭（未装配协调器）：直接返回。
		return
	}
	// ctx 已取消（用户中断等）：本轮即将返回，跳过压缩——避免无谓的第二层 LLM 调用，
	// 也避免 summarize 因 ctx.Err 失败而误增熔断计数（中断不应计入摘要失败）。
	if ctx.Err() != nil {
		return
	}
	res, err := m.compactor.Compact(ctx, provider, m, m.sessionID, false)
	if err != nil {
		logger.WarnCtx(ctx, "自动压缩编排失败，继续使用当前历史发请求",
			zap.String("level", string(res.Level)),
			zap.Bool("tripped", res.Tripped),
			zap.Error(err),
		)
	}
	if res.Level != memctx.CompactionLevelNone && hooks.OnCompaction != nil {
		hooks.OnCompaction(res)
	}
}

// emergencyCompactOnWallHit 在 Provider 返回「上下文超长」错误后执行紧急压缩。
//
// 由 runOneLLM 撞墙兜底分支调用（仅当 compactor 已注入时）。封装「调协调器 EmergencyCompact +
// 记录结构化日志 + 处理 CompactionResult 中的第二层错误」三件事，使 runOneLLM 主流程保持简洁。
//
// 返回值语义：
//   - nil：紧急压缩成功（第二层摘要已应用，history 已更新），调用方应据此用压缩后历史重试请求。
//   - 非 nil err：紧急压缩失败（如摘要 LLM 也不可用），调用方应【保留原始的超长错误】上报，
//     不把本 err 当根因（避免吞掉真实的 prompt_too_long；本 err 仅用于判定「不可重试」）。
//
// 注意：CompactionResult.Err 携带的是第二层摘要失败错误（紧急压缩内部也走 manual 语义，
// 失败会按正常逻辑重新计数熔断）。本方法把它透传出去供调用方判定重试与否。
func (m *ConversationManager) emergencyCompactOnWallHit(
	ctx context.Context,
	provider llm.Provider,
	hooks TurnHooks,
) error {
	res, err := m.compactor.EmergencyCompact(ctx, provider, m, m.sessionID)
	if err != nil {
		// 紧急压缩失败：摘要 LLM 不可用或其它错误。记录 Warn 后返回 err（调用方据此放弃重试）。
		logger.WarnCtx(ctx, "撞墙紧急压缩失败，将上报原始超长错误",
			zap.Int("beforeTokens", res.BeforeTokens),
			zap.Int("afterTokens", res.AfterTokens),
			zap.Bool("tripped", res.Tripped),
			zap.Error(err),
		)
		return err
	}
	logger.InfoCtx(ctx, "撞墙紧急压缩成功，将用压缩后历史重试请求",
		zap.String("level", string(res.Level)),
		zap.Int("beforeTokens", res.BeforeTokens),
		zap.Int("afterTokens", res.AfterTokens),
	)
	return nil
}

// logLLMStopReason 在 LLM 流结束时记录停止原因日志，便于排查输出截断等问题。
//
// 根据 LLMStopReason 区分不同场景：
//   - "end_turn"/"stop"（正常结束）→ Info 级别
//   - "max_tokens"/"length"（输出被截断）→ Warn 级别，提示 max_tokens 配置不足
//   - "tool_use"/"tool_calls"（工具调用）→ Info 级别
//   - "canceled"（用户取消）→ Info 级别
//   - 其他/空值 → Debug 级别
func (m *ConversationManager) logLLMStopReason(ctx context.Context, stopReason string, usage *llm.TokenUsage, aborted bool) {
	fields := []zap.Field{
		zap.String("stop_reason", stopReason),
	}
	if usage != nil {
		fields = append(fields,
			zap.Int("input_tokens", usage.InputTokens),
			zap.Int("output_tokens", usage.OutputTokens),
		)
	}

	switch stopReason {
	case "end_turn", "stop":
		logger.InfoCtx(ctx, "LLM 流正常结束", fields...)
	case "max_tokens", "length":
		// 输出被截断：这是用户最常遇到的问题，需醒目提示
		outputTokens := 0
		if usage != nil {
			outputTokens = usage.OutputTokens
		}
		logger.WarnCtx(ctx, "LLM 输出被截断（达到 max_tokens 上限），建议增大配置中的 max_tokens",
			append(fields, zap.Int("output_tokens", outputTokens))...)
	case "tool_use", "tool_calls":
		logger.InfoCtx(ctx, "LLM 请求工具调用", fields...)
	case "canceled":
		logger.InfoCtx(ctx, "LLM 流被用户取消", fields...)
	default:
		if aborted {
			logger.InfoCtx(ctx, "LLM 流被中断", fields...)
		} else {
			logger.DebugCtx(ctx, "LLM 流结束", fields...)
		}
	}
}

// ---- Step 4 辅助：把字符串形式的 system prompt 封装为 SystemPrompt ----

// buildSystemPromptFromString 把 string 形式的 system prompt 封装为 llm.SystemPrompt。
//
// 这是 Step 4 改造期的过渡辅助：AgentLoop / RunTurn 等 API 表面仍接受
// systemPrompt string（避免大幅签名改动），runOneLLM 内部用本函数把字符串
// 转成单段可缓存的 SystemPrompt 结构。
//
// 未来 Task 6 接入主流程时，会改为接收 prompt.Builder.Assemble 的产物，
// 该函数将被废弃（保留作为兜底兼容路径）。
//
// 为空字符串时返回零值 SystemPrompt（IsEmpty=true），Provider 据此跳过
// system 字段构造。
func buildSystemPromptFromString(systemPrompt string) llm.SystemPrompt {
	if systemPrompt == "" {
		return llm.SystemPrompt{}
	}
	return llm.SystemPrompt{
		SystemBlocks: []llm.SystemBlock{
			{Text: systemPrompt, Cacheable: true},
		},
	}
}
