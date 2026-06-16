// compactor.go 实现上下文压缩的顶层协调器（Step 7 Task 5）。
//
// 第一层（light_compactor.go）管「单条消息内工具结果体积」，第二层（summary_compactor.go）
// 管「整体累积历史长度」。两者职责互补但【触发条件与执行代价截然不同】：
//   - 第一层纯本地估算 + 文件 IO，无 LLM 调用，代价低 → 每次 API 请求前都跑（预防性）；
//   - 第二层需调 LLM 生成摘要，代价高 → 仅在「剩余 token 逼近窗口」或「用户手动触发」时跑。
//
// 协调器（本文件）就是把这两层按正确顺序、正确条件编排起来的「指挥」：
//
//	每次 API 请求前 → 先跑第一层（必跑）→ 判定是否需要第二层 → 视情况跑第二层。
//
// 并在编排之上叠加两个横切能力：
//
//   - 会话级熔断：摘要连续失败达到阈值（默认 3 次）即熔断，本会话停止【自动】第二层
//     （避免 LLM 不可用时反复撞墙拖慢每轮请求）；但允许用户【手动】触发——手动会重置
//     失败计数，给一次重试机会（用户主动操作，理应绕过自动熔断）。
//   - 自动/手动两种安全余量：自动触发用 AutoTriggerMargin（默认 13K，留较大缓冲防估算
//     误差）；手动触发无视当前剩余量立即执行（用户主动要压）。
//
// 【架构合规：用接口而非具体类型承接 manager】
// 协调器需要读写「对话历史 + 剩余 token」，这些能力本属引擎层 conversation.ConversationManager。
// 但记忆层（context 包）【禁止反向依赖】引擎层——conversation 包已 import 本包（memctx），
// 若本包再 import conversation 会形成循环依赖（Go 编译器直接报错）。故定义 ConversationHistory
// 接口抽象所需能力，由 ConversationManager 在 Task 7 实现该接口（鸭子类型自动满足）。
// 这与 HistoryArchiver、ToolResultStore 的解耦思路完全一致：依赖抽象而非具体，保持记忆层
// 与引擎层松耦合。
//
// 【失败语义】
// 第一层恒不返回错误（单点 IO 失败已在 LightCompactor 内降级为保留原文）。第二层失败时
// 协调器返回 err，但【不修改内存 history】（SummaryCompactor 保证失败不动内存），调用方
// （Task 7 的 runOneLLM）捕获 err 后仍可用当前 history 正常发请求——一次摘要失败不中断对话。

package context

import (
	stdctx "context" // 别名：本包名也是 context，需规避与标准库 context 的命名冲突
	"sync"

	"github.com/MeiCorl/CodePilot/src/internal/config"
	"github.com/MeiCorl/CodePilot/src/internal/logger"
	"github.com/MeiCorl/CodePilot/src/llm"
	"go.uber.org/zap"
)

// ConversationHistory 抽象协调器所需的「对话历史 + 剩余量」访问能力。
//
// 由引擎层 conversation.ConversationManager 实现（Task 7 新增 History / ReplaceHistory /
// RemainingTokens 三个方法即可满足）。定义在记忆层 context 包是为了让协调器依赖【抽象】
// 而非 conversation 具体类型，避免记忆层 → 引擎层的反向依赖（见文件头架构合规说明）。
//
// 方法语义：
//   - History：返回当前完整对话历史的可变视图。返回切片中的 *ToolResultBlock 指针必须与
//     manager 内部 history 共享，使 LightCompactor 的 in-place 预览替换能反映回 manager
//     （第一层压缩的关键——改指针指向对象的 Content 字段，manager 侧立即可见）。
//   - ReplaceHistory：用压缩后的新历史（摘要 + 近期原文）整体替换内部 history，供第二层
//     摘要压缩成功后调用，使内存与落盘的活跃视图一致。
//   - Remaining：返回当前上下文窗口的剩余可用 token（= 窗口大小 - 已用量）。
//     优先用 API 返回的精确 input_tokens（manager 内部已实现精确/降级两档），供第二层
//     自动触发判定使用真实剩余量而非粗估。
//
// 命名说明：方法取名 Remaining 而非 RemainingTokens，是为【刻意避开】引擎层
// ConversationManager 既有的 RemainingTokens(maxTokens int) 方法——Go 不允许同类型
// 存在两个同名不同签名的方法，故接口方法用更短的名字，由 manager 实现（manager 内部
// 用自己持有的 contextWindowSize 调 RemainingTokens(windowSize) 来满足本接口）。
type ConversationHistory interface {
	// History 返回当前完整对话历史的可变视图（含共享的 *ToolResultBlock 指针）。
	History() []llm.Message
	// ReplaceHistory 用新历史整体替换内部 history（第二层摘要压缩成功后调用）。
	ReplaceHistory(msgs []llm.Message)
	// Remaining 返回当前上下文窗口的剩余可用 token 数（下界 0）。
	Remaining() int
}

// CompactionLevel 描述一轮压缩编排实际生效的最高层级，供日志与 WebUI 可观测性区分展示。
type CompactionLevel string

const (
	// CompactionLevelNone 本轮未发生任何压缩（两层都未触发或未产生变更）。
	CompactionLevelNone CompactionLevel = "none"
	// CompactionLevelLight 本轮仅第一层轻量预防生效（工具结果存盘 + 预览替换）。
	CompactionLevelLight CompactionLevel = "light"
	// CompactionLevelSummary 本轮第二层摘要压缩生效（整体历史摘要化，重量级）。
	// 当两层都生效时取本值（重量级覆盖轻量级作为「最显著层级」）。
	CompactionLevelSummary CompactionLevel = "summary"
)

// CompactionResult 描述一轮压缩编排的结果，供调用方日志与 WebUI 可观测性展示。
//
// 字段语义：
//   - Level：本轮生效的最高层级（none/light/summary），UI 据此决定提示强度
//     （summary 强提示、light 轻量标记、none 不提示）。
//   - LightChanged / SummaryChanged：两层各自是否产生变更，便于精细展示。
//   - ReplacedBlocks：第一层本轮替换为预览的工具结果数（可观测「压缩了多少大块」）。
//   - BeforeTokens / AfterTokens：压缩前后历史 token 估算（同口径 measure），差值即压缩收益。
//   - Tripped：本轮结束时该会话是否处于熔断态（UI 展示熔断标识）。
//   - Err：第二层摘要失败时的错误（第一层恒 nil）。非 nil 时调用方可据此降级，但 history
//     仍可用（未被改动）。
type CompactionResult struct {
	// Level 为本轮生效的最高压缩层级。
	Level CompactionLevel
	// LightChanged 表示第一层是否发生至少一次工具结果预览替换。
	LightChanged bool
	// SummaryChanged 表示第二层是否成功完成摘要压缩。
	SummaryChanged bool
	// ReplacedBlocks 为第一层本轮替换为预览的工具结果数。
	ReplacedBlocks int
	// BeforeTokens 为压缩前历史的 token 估算。
	BeforeTokens int
	// AfterTokens 为压缩后历史的 token 估算。
	AfterTokens int
	// Tripped 表示本轮结束时该会话是否已熔断（自动第二层被禁用）。
	Tripped bool
	// Err 为第二层摘要失败时的错误；nil 表示本轮无错误（第一层恒不产生错误）。
	Err error
}

// Compactor 是上下文压缩的顶层协调器，编排第一层轻量预防与第二层摘要兜底，
// 并维护会话级熔断状态。
//
// 持有两个下层压缩器（light / summary，无状态）、配置（只读）与熔断状态（map，需锁保护）。
// 同一 Compactor 可被多个会话并发调用——熔断状态按 sessionID 隔离，map 访问由 mu 保护；
// 下层压缩器自身的线程安全性见各自文件头说明（LightCompactor / SummaryCompactor 均无状态，
// 真正的并发隔离由调用方按 sessionID 串行化 + 文件系统 O_EXCL 兜底）。
type Compactor struct {
	// light 为第一层轻量预防压缩器（无状态）。
	light *LightCompactor
	// summary 为第二层摘要压缩器（无状态，持久化经 HistoryArchiver 接口承担）。
	summary *SummaryCompactor
	// cfg 为压缩配置（只读，取 AutoTriggerMargin / BreakerThreshold 等字段）。
	cfg config.CompactionConfig

	// mu 保护下面两个 map 的并发访问。熔断状态是会话级、跨多轮请求累积的，必须线程安全。
	mu sync.Mutex
	// failures 记录每个会话的摘要连续失败次数（成功清零）。
	failures map[string]int
	// tripped 记录每个会话是否已熔断（连续失败达 BreakerThreshold）。熔断后自动模式跳过
	// 第二层；手动模式仍可执行并重置该标志。用独立 map 而非由 failures 推导，是为了
	// 让「是否熔断」这一对外可见状态显式化（WebUI 可直接展示），且语义更清晰。
	tripped map[string]bool
}

// NewCompactor 创建压缩协调器。
//
// light / summary 为已装配好的两层压缩器；cfg 应已过 applyCompactionDefaults 填充默认值
// （main.go 装配时保证），故本构造不再兜底。总开关（Enabled）的判定由调用方（main.go）
// 负责——Enabled=false 时不装配协调器、整体降级为纯滑动窗口，故协调器假定被调用时压缩开启。
func NewCompactor(light *LightCompactor, summary *SummaryCompactor, cfg config.CompactionConfig) *Compactor {
	return &Compactor{
		light:    light,
		summary:  summary,
		cfg:      cfg,
		failures: make(map[string]int),
		tripped:  make(map[string]bool),
	}
}

// Compact 编排一轮压缩，返回本轮结果与可能的第二层错误。
//
// 编排顺序（满足「先轻量预防、再重量兜底」）：
//  1. 第一层（每次必跑）：对当前 history 跑 LightCompactor.Compact，in-place 把超阈值工具
//     结果替换为预览。由于 history 中的 *ToolResultBlock 与 manager 共享指针，替换立即生效。
//  2. 第二层判定（decideSummary）：自动模式仅当「剩余 ≤ AutoTriggerMargin 且未熔断」时触发；
//     手动模式无视剩余量与熔断立即触发。
//  3. 第二层执行：调 SummaryCompactor.Compact。成功 → ReplaceHistory 应用新历史、清零失败计数；
//     失败 → recordFailure 累计、可能置熔断，返回 err 但【不改 history】。
//
// 参数：
//   - ctx：用于第二层调 LLM 时的取消传播。
//   - provider：第二层摘要调用的 LLM Provider（第一层不使用）。
//   - ch：对话历史访问接口（由 ConversationManager 实现）。
//   - sessionID：会话标识，决定存盘子目录归属与熔断状态隔离。
//   - manual：true 表示用户手动触发（/compact 或 WebUI 按钮），false 表示每轮自动编排。
//
// 返回 (CompactionResult, error)：error 非 nil 仅当第二层失败（第一层恒 nil）。调用方捕获
// error 后仍可用当前 history 正常发请求——第二层失败不修改内存 history。
func (c *Compactor) Compact(
	ctx stdctx.Context,
	provider llm.Provider,
	ch ConversationHistory,
	sessionID string,
	manual bool,
) (CompactionResult, error) {
	result := CompactionResult{Level: CompactionLevelNone}

	history := ch.History()
	result.BeforeTokens = EstimateMessagesTokens(history)

	// ---- 第一层：每次都跑（轻量预防，无 LLM 调用，代价低）----
	// 统计本轮替换数：对比第一层前后「处于预览态」的工具结果数差值。
	beforePreview := countPreviewToolResults(history)
	lightChanged, _ := c.light.Compact(ctx, history, sessionID)
	afterPreview := countPreviewToolResults(history)

	result.LightChanged = lightChanged
	result.ReplacedBlocks = afterPreview - beforePreview
	if lightChanged {
		// 第一层产生变更时升级层级为 light（若后续第二层也生效则再升级为 summary）。
		result.Level = CompactionLevelLight
		logger.InfoCtx(ctx, "第一层轻量压缩完成",
			zap.String("level", string(CompactionLevelLight)),
			zap.Int("replacedBlocks", result.ReplacedBlocks),
			zap.Int("beforeTokens", result.BeforeTokens),
		)
	}

	// ---- 第二层：判定是否需要重量兜底 ----
	runSummary, _, remaining := c.decideSummary(sessionID, ch, manual)

	// 无需第二层：直接结算 token 返回。
	if !runSummary {
		result.AfterTokens = EstimateMessagesTokens(ch.History())
		result.Tripped = c.IsTripped(sessionID)
		return result, nil
	}

	// 手动触发：重置熔断状态，给一次重试机会（即使此前已熔断，用户主动操作理应放行）。
	if manual {
		c.resetBreaker(sessionID)
	}

	// 第二层基于【第一层处理后的当前 history】做摘要（ch.History() 返回同一引用，已含预览化）。
	newHistory, summaryChanged, err := c.summary.Compact(ctx, provider, ch.History(), sessionID)
	if err != nil {
		// 第二层失败：累计失败计数（可能触发熔断），但不修改内存 history（SummaryCompactor 已保证）。
		// 返回 err 供调用方决策；调用方捕获后仍可用当前 history 发请求，对话不中断。
		nowTripped := c.recordFailure(sessionID)
		result.Err = err
		result.Tripped = nowTripped
		result.AfterTokens = EstimateMessagesTokens(ch.History())
		if nowTripped {
			logger.WarnCtx(ctx, "摘要压缩失败并触发熔断，本会话暂停自动第二层",
				zap.Int("beforeTokens", result.BeforeTokens),
				zap.Int("remaining", remaining),
				zap.Error(err),
			)
		} else {
			logger.WarnCtx(ctx, "摘要压缩失败（未达熔断阈值，自动模式稍后仍可重试）",
				zap.Int("beforeTokens", result.BeforeTokens),
				zap.Error(err),
			)
		}
		return result, err
	}

	// 第二层成功且确实产生摘要变更：应用新历史、清零失败计数、升级层级为 summary。
	if summaryChanged {
		ch.ReplaceHistory(newHistory)
		result.SummaryChanged = true
		result.Level = CompactionLevelSummary
		c.recordSuccess(sessionID)
		logger.InfoCtx(ctx, "第二层摘要压缩完成",
			zap.String("level", string(CompactionLevelSummary)),
			zap.Int("beforeTokens", result.BeforeTokens),
			zap.Int("afterTokens", EstimateMessagesTokens(newHistory)),
		)
	}

	result.AfterTokens = EstimateMessagesTokens(ch.History())
	result.Tripped = c.IsTripped(sessionID)
	return result, nil
}

// decideSummary 判定本轮是否应执行第二层摘要压缩。
//
// 返回 (runSummary, tripped, remaining)：
//   - runSummary：是否执行第二层。
//   - tripped：判定时该会话是否已熔断（仅供日志展示；手动模式下即使 tripped 也执行）。
//   - remaining：当前剩余 token（ch.Remaining()，供日志展示判定依据）。
//
// 判定规则：
//   - 手动模式（manual=true）：立即执行（用户主动要压），runSummary 恒 true。
//   - 自动模式：仅当「remaining ≤ AutoTriggerMargin 且未熔断」时执行。
//     已熔断则跳过（避免 LLM 不可用时反复撞墙）；剩余充足则跳过（无需压）。
func (c *Compactor) decideSummary(sessionID string, ch ConversationHistory, manual bool) (runSummary, tripped bool, remaining int) {
	remaining = ch.Remaining()

	c.mu.Lock()
	tripped = c.tripped[sessionID]
	c.mu.Unlock()

	if manual {
		// 手动触发：无视剩余量与熔断，立即执行。tripped 仍读出供日志展示「手动重置了熔断」。
		return true, tripped, remaining
	}
	if tripped {
		// 已熔断：自动模式跳过第二层，等待用户手动重试。
		return false, true, remaining
	}
	if remaining <= c.cfg.AutoTriggerMargin {
		return true, false, remaining
	}
	return false, false, remaining
}

// resetBreaker 重置指定会话的熔断状态（连续失败计数清零、熔断标志清除）。
//
// 供手动触发在执行摘要前调用——用户主动压缩理应绕过自动熔断，给一次重试机会。
func (c *Compactor) resetBreaker(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failures[sessionID] = 0
	delete(c.tripped, sessionID)
}

// recordFailure 记录一次摘要失败，返回此次失败是否导致该会话熔断（达到 BreakerThreshold）。
//
// 失败计数 +1；达到阈值时置熔断标志。熔断阈值来自配置（默认 3）。
func (c *Compactor) recordFailure(sessionID string) (tripped bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failures[sessionID]++
	if c.failures[sessionID] >= c.cfg.BreakerThreshold {
		c.tripped[sessionID] = true
		return true
	}
	return false
}

// recordSuccess 记录一次摘要成功，清零该会话的连续失败计数与熔断标志。
//
// 摘要成功意味着 LLM 可用，之前累积的失败计数不再有意义，清零以恢复自动模式的正常触发。
func (c *Compactor) recordSuccess(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failures[sessionID] = 0
	delete(c.tripped, sessionID)
}

// IsTripped 返回指定会话当前是否已熔断。
//
// 导出方法供外部查询（如 WebUI 状态栏展示熔断标识、Task 7 handler 判定）。
func (c *Compactor) IsTripped(sessionID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tripped[sessionID]
}

// EmergencyCompact 紧急压缩——当 Provider 返回「上下文超长」错误（prompt_too_long /
// context_length_exceeded）时，由 conversation 包 runOneLLM 的撞墙兜底路径调用。
//
// 与普通 Compact 的区别（这正是「撞墙」场景需要的更激进策略）：
//   - 【无视余量阈值】：普通自动模式仅在 remaining ≤ AutoTriggerMargin 时跑第二层；
//     紧急模式无论 remaining 多少都强制跑——既然 Provider 已经报超长，说明历史确实过长，
//     再算余量毫无意义。
//   - 【临时豁免熔断】：普通自动模式熔断后跳过第二层；紧急模式临时重置熔断状态给【一次】
//     重试机会——撞墙已是真实故障，宁可再试一次摘要也不能直接放弃（用户最新输入在历史尾部，
//     不压缩就只能丢掉）。注意：与手动模式一样调用 resetBreaker，失败后仍会按正常逻辑重新
//     计数，不会无限豁免。
//   - 【仍跑第一层】：先跑轻量预防（管单条消息内工具结果体积），再跑第二层摘要——与普通
//     编排顺序一致，确保撞墙压缩后的历史同时受两层保护，最大化腾出空间。
//
// 返回 (CompactionResult, error)：
//   - 第二层成功 → summary 已应用（ReplaceHistory）、返回 nil err，调用方据此用压缩后历史重试 1 次。
//   - 第二层失败（如摘要 LLM 也不可用）→ 返回 err，调用方应将【原始的 prompt_too_long 错误】
//     上报（而非这个摘要错误），避免吞掉真实根因——「不吞异常」是 spec 的高可用要求。
//
// 注意：紧急压缩是【兜底】路径，正常情况下每次 API 请求前的自动 Compact（Task 7）应已避免
// 撞墙；本方法仅在估算误差导致 Provider 仍判定超长时才被触发，频率极低。
func (c *Compactor) EmergencyCompact(
	ctx stdctx.Context,
	provider llm.Provider,
	ch ConversationHistory,
	sessionID string,
) (CompactionResult, error) {
	// 紧急模式语义上等价于「manual=true」——两者都无视余量与熔断立即跑第二层。
	// 复用 Compact(manual=true) 路径：其内部会 resetBreaker（临时豁免熔断）、无视 remaining
	// 判定（decideSummary 在 manual 下恒返回 true），与本方法的紧急语义完全吻合。
	// 不另起一套实现，避免与普通手动触发逻辑分叉、产生不一致的熔断/计数行为。
	return c.Compact(ctx, provider, ch, sessionID, true)
}

// countPreviewToolResults 统计消息切片中「已处于预览态」的工具结果数。
//
// 用于协调器计算第一层【本轮】替换数（压缩前后预览数差值）。预览态判定复用 preview.go
// 的 isPreview（以固定尾注锚点 HasSuffix），与 LightCompactor 跳过已处理 block 的判定同口径。
func countPreviewToolResults(msgs []llm.Message) int {
	n := 0
	for i := range msgs {
		for _, b := range msgs[i].Content {
			if tr, ok := b.(*llm.ToolResultBlock); ok && isPreview(tr.Content) {
				n++
			}
		}
	}
	return n
}
