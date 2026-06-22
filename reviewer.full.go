// reviewer.go 实现「自动学习记忆」的后台异步回顾器（Task 5）。
//
// 职责定位：它是记忆系统的「写入侧发动机」——监听每轮 Agent Loop 结束事件，
// 在满足智能节流条件时，用本轮对话快照做一次【独立无状态 LLM 调用】，让回顾
// 模型判断「是否有值得长期记住的信息」，解析其产出的 new/update 决策，经 store
// 落盘为记忆文件并刷新 MEMORY.md 索引。
//
// [四大设计要求（对应 spec 非功能「高性能/高可用/高安全/高扩展」）]
//  1. 异步不阻塞：OnLoopDone 立即返回，回顾在后台 goroutine 执行，主响应延迟不受影响。
//  2. 静默降级：回顾全链路（LLM 调用 / JSON 解析 / 文件写入 / 索引刷新）任一环节失败
//     均只记结构化日志、绝不向主流程抛异常；goroutine 内 panic recover 兜底，进程不崩。
//  3. 不污染主对话历史：reviewer 只持有 provider + store，独立构造回顾 messages 调 LLM，
//     主 ConversationManager.history 对它不可见——「不回写主历史」由设计天然保证，
//     而非靠约定。
//  4. per-session 串行：同一会话的回顾请求串行（同 session 上一回顾未完成时新请求丢弃），
//     避免并发回顾各自读到旧索引后互相覆盖丢条目；落盘索引并发另由 store.mu 兜底。
//
// [架构分层] reviewer 归 autolearn 包（记忆层，第 4 层）。为遵守「下层禁止依赖上层」，
// 本包【不 import conversation 包】（引擎层，第 2 层）——节流所需的「是否正常完成」
// 通过自有的 ReviewRequest.Completed bool 字段表达，由接入层（Task 7 handler）把
// conversation.AgentLoopResult 适配为 ReviewRequest 后传入。本包仅依赖 llm（底座）
// 与 logger（横切），架构合规。

package autolearn

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/MeiCorl/CodePilot/src/internal/logger"
	"github.com/MeiCorl/CodePilot/src/llm"
	"go.uber.org/zap"
)

// ReviewRequest 描述一次「值得回顾」的本轮对话快照，是接入层与回顾器之间的解耦契约。
//
// [Why 不直接复用 conversation.AgentLoopResult] autolearn（记忆层）若依赖 conversation
// （引擎层），将构成「下层依赖上层」的架构违规。故本结构作为标准接口：接入层（Task 7
// handler，处引擎/交互层）负责从 conversation.AgentLoopResult + 本轮 history 提取字段
// 填入本结构，再交给 Reviewer。autolearn 包对 conversation 包零依赖。
//
// 字段语义：
//   - SessionID：会话标识，用于 per-session 串行的 key 与回顾日志的会话级路由。
//   - Completed：本轮是否以 StopReason=completed 正常终止。非 completed（aborted /
//     error / max_iterations / context_overflow）时回顾器直接跳过，避免回顾异常/未完成轮。
//   - UserInput：本轮用户原始输入。用于节流（空/纯闲聊不回顾）与回顾快照。
//   - FinalReply：本轮 Agent 最终回复文本（取自 AgentLoopResult.FinalText）。
//   - ToolCallNames：本轮工具调用名摘要（去重保序，仅工具名不含入参出参全文，
//     既控制回顾成本又避免敏感数据进回顾上下文）。可为空（本轮未调工具）。
type ReviewRequest struct {
	SessionID     string
	Completed     bool
	UserInput     string
	FinalReply    string
	ToolCallNames []string
}

// ReviewerConfig 回顾器配置。
//
// 由主流程（main.go Task 7）从 setting.json 的 memory 配置段（config.MemoryConfig）
// 映射构造后注入：Enabled ← config.MemoryConfig.IsEnabled()；ReviewTimeout ← 固定 60s
// （首版不纳入 setting.json，预留后续可配）。review_model 等热切换字段首版不启用，
// 预留扩展（见 spec Out of Scope）。
//
// [架构约束] 本结构是 autolearn 包与配置层之间的解耦边界——autolearn 不 import config 包
// （保持仅依赖 llm + logger，符合「记忆层不依赖配置层以外上层」的纯净度），由接入层
// （main.go）负责 config.MemoryConfig → ReviewerConfig 的适配。enabled=false 降级由
// OnLoopDone 内部 `!r.cfg.Enabled` 短路保证（见 TestOnLoopDone_DisabledShortCircuits）。
type ReviewerConfig struct {
	// Enabled 为记忆回顾总开关。false 时 OnLoopDone 直接短路返回，不触发任何回顾。
	Enabled bool
	// ReviewTimeout 为单次回顾 LLM 调用的超时上限。回顾在后台异步执行，但仍需超时
	// 兜底，避免 Provider 挂起导致 goroutine 长期堆积泄漏。<=0 表示不限时（不推荐）。
	ReviewTimeout time.Duration
}

// DefaultReviewerConfig 返回回顾器的默认配置。
//
// 默认开启（Enabled=true）；回顾超时 60s——回顾是非关键路径，超时即放弃本轮记忆
// 沉淀，不影响主流程与下一轮回顾。
func DefaultReviewerConfig() ReviewerConfig {
	return ReviewerConfig{
		Enabled:       true,
		ReviewTimeout: 60 * time.Second,
	}
}

// Reviewer 是自动学习记忆的后台异步回顾器。
//
// 持有三个只读依赖（provider / store / cfg）与一个并发状态（inflight）。无其它业务
// 状态——回顾的「输入」全部由调用方经 ReviewRequest 现场传入，reviewer 本身不缓存
// 对话内容，天然适配「一轮一回顾、回顾完即释放」的无状态语义。
//
// 生命周期：由 main.go（Task 7）在启动时构造一次，注入 provider + store + cfg，
// 随后其 OnLoopDone 方法被 handler 装配到每轮 AgentLoop 的 AgentLoopHooks.OnLoopDone
// 回调上，随会话生命周期常驻。
type Reviewer struct {
	// provider 用于发起【独立无状态】的回顾 LLM 调用（toolSpecs=nil 强制禁工具）。
	provider llm.Provider
	// store 记忆文件持久化抽象，回顾决策经它落盘 + 刷索引。
	store *Store
	// cfg 回顾器配置（只读）。
	cfg ReviewerConfig

	// mu 仅保护 inflight map 的并发访问。
	mu sync.Mutex
	// inflight 记录当前正在回顾中的 sessionID 集合，实现 per-session 串行。
	// 同一 session 上一回顾未完成时，新到达的回顾请求直接丢弃（drop 策略），
	// 避免并发回顾互相覆盖索引。空字符串 key 也生效（无 sessionID 的回顾彼此串行，
	// 这种场景罕见，首版不做特殊处理）。
	inflight map[string]struct{}

	// wg 跟踪所有进行中的异步回顾 goroutine。仅供测试同步等待（Wait）使用，
	// 生产代码不应调用 Wait——回顾是 fire-and-forget 的后台任务。
	wg sync.WaitGroup
}

// NewReviewer 创建一个回顾器。
//
// provider / store 为已装配好的依赖（store 可与 memory Source 共用同一实例）；
// cfg 零值时回退到 DefaultReviewerConfig，保证即便接入层忘记填配置也能正常工作。
// provider 或 store 为 nil 时仍可构造（OnLoopDone 内部判空短路），便于「记忆关闭」
// 场景下构造一个 no-op 回顾器而无需到处加 nil 判断。
func NewReviewer(provider llm.Provider, store *Store, cfg ReviewerConfig) *Reviewer {
	if !cfg.Enabled && cfg.ReviewTimeout == 0 {
		// 未显式启用且未设超时：无法区分「故意禁用」与「零值未初始化」，保守起见
		// 仍给默认超时；Enabled 维持调用方意图（false 即禁用）。
		cfg.ReviewTimeout = DefaultReviewerConfig().ReviewTimeout
	}
	if cfg.ReviewTimeout == 0 {
		cfg.ReviewTimeout = DefaultReviewerConfig().ReviewTimeout
	}
	return &Reviewer{
		provider: provider,
		store:    store,
		cfg:      cfg,
		inflight: make(map[string]struct{}),
	}
}

// OnLoopDone 是回顾器对外的主入口，由接入层（Task 7）装配到 AgentLoopHooks.OnLoopDone。
//
// 行为契约（对应 spec「高性能」）：
//  1. 【立即返回】本方法只做节流判断 + 异步派发，绝不阻塞——即便回顾耗时数秒，
//     主响应流也不受影响。
//  2. 【节流】未开启 / 依赖缺失 / 非正常完成 / 空输入 / 纯闲聊 → 直接 return，不回顾。
//  3. 【per-session 串行】同 session 上一回顾未完成时，本次请求丢弃 + warn。
//  4. 【异步隔离】满足条件则在独立 goroutine 中执行回顾，独立 ctx（不随主请求取消）+
//     defer recover + defer 清理 inflight。
func (r *Reviewer) OnLoopDone(req ReviewRequest) {
	if r == nil {
		return
	}
	// 依赖缺失或总开关关闭：短路返回（no-op）。允许 provider/store 为 nil 时安全调用。
	if !r.cfg.Enabled || r.provider == nil || r.store == nil {
		return
	}
	if !shouldReview(req) {
		return
	}

	// per-session 串行：抢占 inflight 标记，失败则丢弃本次请求。
	if !r.markInflight(req.SessionID) {
		logger.Warn("autolearn: 该会话已有回顾进行中，丢弃本次回顾请求（drop 策略）",
			zap.String("sessionID", req.SessionID),
		)
		return
	}

	r.wg.Add(1)
	go r.asyncReview(req)
}

// Review 同步执行一次回顾（不经节流、不异步派发），供「强制回顾」场景与单元测试使用。
//
// 与 OnLoopDone 的区别：OnLoopDone 做节流 + 异步；本方法假定调用方已确认要回顾，
// 直接在【当前 goroutine】同步跑完回顾主流程。失败一律静默降级（仅日志），不返回 error
// ——回顾是非关键路径，调用方无需也无从处理其失败。panic 不在本方法捕获（同步调用方
// 的 panic 由调用方自己处理）；异步路径的 panic 兜底在 asyncReview。
func (r *Reviewer) Review(ctx context.Context, req ReviewRequest) {
	if r == nil || r.provider == nil || r.store == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.runReview(ctx, req)
}

// Wait 阻塞直到所有已派发的异步回顾 goroutine 完成。
//
// [Why] 仅用于测试同步断言：测试调 OnLoopDone 后用 Wait 等回顾跑完，再检查落盘文件。
// 生产代码不应调用——回顾是后台 fire-and-forget 任务，无需也无需等待其完成。
func (r *Reviewer) Wait() {
	r.wg.Wait()
}

// ---- 内部实现 ----

// asyncReview 是异步回顾 goroutine 的主体。
//
// defer 顺序（LIFO 执行）经精心编排，保证任意退出路径（正常 / error / panic）下：
//  1. cancel()：释放 ReviewTimeout 计时器；
//  2. clearInflight：释放串行标记（让同 session 下一请求能进入）；
//  3. recoverReview：捕获 panic（若有），防进程崩溃；
//  4. wg.Done：通知 Wait 完成。
// panic 被 recover 捕获后，后续 defer 仍照常执行（Go defer 语义），故 clearInflight /
// wg.Done 不会被遗漏。
func (r *Reviewer) asyncReview(req ReviewRequest) {
	// 注册顺序：先注册的最后执行。希望执行顺序为 cancel→clearInflight→recover→wgDone，
	// 故注册顺序须为 wgDone（最先注册=最后执行）→ recover → clearInflight → cancel 调用前。
	defer r.wg.Done()
	defer r.recoverReview(req.SessionID)
	defer r.clearInflight(req.SessionID)

	ctx, cancel := r.deriveContext(req.SessionID)
	defer cancel()
	r.runReview(ctx, req)
}

// runReview 是回顾的核心流程（节流之后的部分），同步执行、全程静默降级。
//
// 步骤：
//  1. 构造回顾输入快照（读两级 MEMORY.md 索引 + 填本轮快照槽位）；读索引失败降级为空索引。
//  2. 独立无状态 LLM 调用（system=回顾 prompt，user=快照，toolSpecs=nil 禁工具）。
//  3. 解析 LLM 产出的 JSON 决策（非法/空数组均降级为不写文件）。
//  4. 逐条落盘记忆文件 + 刷新索引行（update 校验 slug 存在性防虚构覆盖）。
//
// 任一步失败只记结构化日志后 return，绝不 panic、绝不向主流程外溢。
func (r *Reviewer) runReview(ctx context.Context, req ReviewRequest) {
	in := r.buildReviewInput(ctx, req)

	raw, err := r.callReviewLLM(ctx, in)
	if err != nil {
		// 回顾 LLM 调用失败：静默降级。常见于网络抖动、Provider 限流、超时。
		// 不影响主流程与下一轮回顾（每轮独立）。
		logger.WarnCtx(ctx, "autolearn: 回顾 LLM 调用失败，跳过本轮记忆沉淀",
			zap.String("sessionID", req.SessionID),
			zap.Error(err),
		)
		return
	}

	decisions := parseReviewDecisions(raw)
	if len(decisions) == 0 {
		// 空数组语义：模型判断本轮无值得长期记住的信息，属正常情况，记 Info 即可。
		logger.InfoCtx(ctx, "autolearn: 本轮无值得沉淀的记忆",
			zap.String("sessionID", req.SessionID),
		)
		return
	}

	r.applyDecisions(ctx, req.SessionID, decisions)
}

// buildReviewInput 从 ReviewRequest + 两级 MEMORY.md 索引构造回顾输入快照。
//
// 读索引失败时降级为空索引文本（warn），不中断回顾——索引读不出来不代表不能回顾，
// 大不了模型把本轮信息当「新主题」处理（最坏只是可能产生重复条目，由后续 update 收敛）。
func (r *Reviewer) buildReviewInput(ctx context.Context, req ReviewRequest) ReviewInput {
	in := ReviewInput{
		UserInput:      req.UserInput,
		FinalReply:     req.FinalReply,
		ToolCallNames:  req.ToolCallNames,
		UserIndexText:  "",
		ProjectIndexText: "",
	}

	userEntries, err := r.store.ReadIndex(ScopeUser)
	if err != nil {
		logger.WarnCtx(ctx, "autolearn: 读取用户级记忆索引失败，降级为空索引",
			zap.String("sessionID", req.SessionID),
			zap.Error(err),
		)
	} else {
		in.UserIndexText = RenderEntries(userEntries)
	}

	projEntries, err := r.store.ReadIndex(ScopeProject)
	if err != nil {
		logger.WarnCtx(ctx, "autolearn: 读取项目级记忆索引失败，降级为空索引",
			zap.String("sessionID", req.SessionID),
			zap.Error(err),
		)
	} else {
		in.ProjectIndexText = RenderEntries(projEntries)
	}

	return in
}

// callReviewLLM 发起一次【独立无状态】的回顾 LLM 调用，返回模型输出的完整文本。
//
// 关键（对应 spec「不回写主对话历史」）：
//   - messages 是 reviewer 现场构造的【单条 user 消息】，与主 ConversationManager.history
//     完全无关，Provider 的响应只写进本方法的 buf，绝无路径回写主历史；
//   - toolSpecs=nil 在协议层强制禁工具，配合 prompt 内「禁止调用工具」双保险，避免模型
//     在回顾里虚构 tool_use。
//
// 流式消费范式参照 summary_compactor.summarize：select ctx.Done / chunkCh，累加 Content，
// chunk.Err 中断、chunk.Done 或 channel 关闭即返回。
func (r *Reviewer) callReviewLLM(ctx context.Context, in ReviewInput) (string, error) {
	sp := llm.NewSystemPromptFromText(reviewSystemPrompt)
	userText := renderReviewUserPrompt(in)
	messages := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock(userText)}},
	}

	// nil toolSpecs = 禁用所有工具，回顾模型只能输出 JSON 文本。
	chunkCh, err := r.provider.StreamChat(ctx, sp, messages, nil)
	if err != nil {
		return "", err
	}

	var buf strings.Builder
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case chunk, ok := <-chunkCh:
			if !ok {
				// channel 关闭：流正常结束（部分 Provider 直接 close 不发 Done）。
				return buf.String(), nil
			}
			if chunk.Err != nil {
				return "", chunk.Err
			}
			if chunk.Content != "" {
				buf.WriteString(chunk.Content)
			}
			if chunk.Done {
				return buf.String(), nil
			}
		}
	}
}

// applyDecisions 逐条落盘记忆文件并刷新 MEMORY.md 索引。
//
// 对每条决策独立 try（失败 warn 后继续下一条，不因单条失败放弃整批）：
//  1. 按 type 确定存储域（ScopeOf）；Sanitize 兜底脱敏正文；
//  2. update 决策：先校验 slug 在目标域索引中真实存在（防模型虚构 slug 覆盖错文件），
//     不存在则跳过 + warn；
//  3. update 且旧文件存在时，保留原 CreatedAt（避免每次刷新都重置创建时间）；
//  4. WriteMemory 覆盖/新建文件 + UpsertIndexEntry 刷新索引行（store 内部原子写 + mu 保护）。
//
// [Why update 校验存在性] WriteMemory/UpsertIndexEntry 对 new/update 行为一致（覆盖/upsert），
// 故 update 的额外校验纯粹为「防模型虚构一个不存在的 slug」——若放任，模型可能凭空 update
// 一个 slug，等价于偷偷新建了用户从未确认的记忆。校验存在性后跳过，最保守。
func (r *Reviewer) applyDecisions(ctx context.Context, sessionID string, decisions []ReviewDecision) {
	now := time.Now()
	applied := 0
	for _, d := range decisions {
		if err := r.applyOne(ctx, sessionID, d, now); err != nil {
			logger.WarnCtx(ctx, "autolearn: 落盘单条记忆失败，跳过",
				zap.String("sessionID", sessionID),
				zap.String("type", string(d.Type)),
				zap.String("slug", d.Slug),
				zap.String("action", string(d.Action)),
				zap.Error(err),
			)
			continue
		}
		applied++
	}
	logger.InfoCtx(ctx, "autolearn: 回顾完成",
		zap.String("sessionID", sessionID),
		zap.Int("total", len(decisions)),
		zap.Int("applied", applied),
	)
}

// applyOne 落盘单条决策，返回 error 表示该条失败（调用方降级跳过）。
func (r *Reviewer) applyOne(ctx context.Context, sessionID string, d ReviewDecision, now time.Time) error {
	scope := ScopeOf(d.Type)
	content := Sanitize(d.Content)

	// update 校验：目标域索引须真实存在该 slug，否则视为模型虚构，跳过。
	if d.Action == ReviewActionUpdate {
		exists, err := r.slugExists(scope, d.Slug)
		if err != nil {
			return err
		}
		if !exists {
			logger.WarnCtx(ctx, "autolearn: update 决策的目标 slug 在索引中不存在，跳过（防虚构覆盖）",
				zap.String("sessionID", sessionID),
				zap.String("slug", d.Slug),
				zap.String("type", string(d.Type)),
			)
			return nil
		}
	}

	// 保留原 CreatedAt：update 时读旧文件取其 CreatedAt（文件缺失/解析失败则用 now 兜底）。
	createdAt := now
	if d.Action == ReviewActionUpdate {
		if old, err := r.store.ReadMemory(scope, d.Slug); err == nil && !old.CreatedAt.IsZero() {
			createdAt = old.CreatedAt
		}
	}

	m := Memory{
		Frontmatter: Frontmatter{
			Type:      d.Type,
			Title:     d.Title,
			CreatedAt: createdAt,
			UpdatedAt: now,
		},
		Slug:    d.Slug,
		Content: content,
	}
	if _, err := r.store.WriteMemory(scope, m); err != nil {
		return err
	}
	if err := r.store.UpsertIndexEntry(scope, IndexEntry{
		Type:    d.Type,
		Slug:    d.Slug,
		Summary: d.Summary,
	}); err != nil {
		return err
	}
	return nil
}

// slugExists 查询某 slug 是否已存在于指定域的 MEMORY.md 索引中。
// 读索引失败时返回 false（保守视为不存在，让 update 被跳过）+ error 由调用方决定降级。
func (r *Reviewer) slugExists(scope StorageScope, slug string) (bool, error) {
	entries, err := r.store.ReadIndex(scope)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.Slug == slug {
			return true, nil
		}
	}
	return false, nil
}

// ---- 节流判断 ----

// shouldReview 判断本轮是否值得触发回顾（纯函数，便于单测）。
//
// 条件（全部满足才触发）：
//  1. Completed=true：仅正常完成轮才回顾。aborted/error/max_iterations/context_overflow
//     这类异常或未完成轮，对话内容往往不完整或带情绪，不适合沉淀为长期记忆。
//  2. 用户输入非空且有实质内容：纯闲聊（问候/客套/测试词）不回顾，避免噪音记忆。
func shouldReview(req ReviewRequest) bool {
	if !req.Completed {
		return false
	}
	input := strings.TrimSpace(req.UserInput)
	if input == "" {
		return false
	}
	if isChitchat(input) {
		return false
	}
	return true
}

// chitchatSet 常见闲聊/客套/测试词集合（精确匹配，小写归一）。
// 命中即视为「本轮无实质内容」，不触发回顾，避免把「你好」「谢谢」「测试」沉淀成记忆。
var chitchatSet = map[string]struct{}{
	// 英文问候/客套
	"hi": {}, "hello": {}, "hey": {}, "thanks": {}, "thank you": {},
	"ok": {}, "okay": {}, "cool": {}, "nice": {},
	// 中文问候/客套
	"你好": {}, "您好": {}, "在吗": {}, "谢谢": {}, "感谢": {},
	"好的": {}, "嗯": {}, "嗯嗯": {}, "收到": {}, "辛苦了": {},
	// 测试词
	"test": {}, "测试": {}, "ping": {},
}

// minInputRunes 用户输入的最小有效长度（rune 计数）。短于此视为闲聊。
// 4 个 rune 可过滤「hi/你好/ok」等极短输入，同时不误杀「fix bug」这类简短但有实质的指令。
const minInputRunes = 4

// isChitchat 用保守启发式判断输入是否为纯闲聊。
//
// 两道过滤：
//  1. 长度过滤：去空白后 rune 数 < minInputRunes → 闲聊（覆盖 hi/你好/ok 等极短输入）；
//  2. 关键词过滤：小写归一后精确命中 chitchatSet → 闲聊（覆盖 thanks/收到 等长度达标但仍是客套）。
//
// [Why 保守] 宁可漏判（放过一条可能的闲聊让它进回顾），也不误判（把用户的简短指令当成
// 闲聊而错过沉淀）。回顾模型自身也会过滤无意义内容，启发式只需挡掉最明显的噪音。
func isChitchat(input string) bool {
	trimmed := strings.TrimSpace(input)
	if len([]rune(trimmed)) < minInputRunes {
		return true
	}
	if _, ok := chitchatSet[strings.ToLower(trimmed)]; ok {
		return true
	}
	return false
}

// ---- 并发与生命周期辅助 ----

// markInflight 尝试为 sessionID 标记「回顾进行中」。
// 返回 true 表示抢占成功（调用方应派发 goroutine）；false 表示该 session 已有回顾
// 在进行（调用方应丢弃本次请求）。操作在 mu 保护下原子完成。
func (r *Reviewer) markInflight(sessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.inflight[sessionID]; ok {
		return false
	}
	r.inflight[sessionID] = struct{}{}
	return true
}

// clearInflight 清除 sessionID 的「回顾进行中」标记。
// 由 asyncReview 的 defer 调用，保证 goroutine 退出时释放串行槽位。
func (r *Reviewer) clearInflight(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.inflight, sessionID)
}

// deriveContext 为异步回顾派生独立 context。
//
// [Why 独立 ctx] 回顾发生在主请求结束之后，若复用主请求 ctx，主请求结束即取消会中断
// 回顾。故用 context.Background() 派生：
//   - logger.WithSession 注入 sessionID，让回顾日志仍路由到对应会话日志文件（可观测）；
//   - context.WithTimeout 叠加 ReviewTimeout，防 Provider 挂起致 goroutine 泄漏。
func (r *Reviewer) deriveContext(sessionID string) (context.Context, context.CancelFunc) {
	ctx := logger.WithSession(context.Background(), sessionID)
	if r.cfg.ReviewTimeout > 0 {
		return context.WithTimeout(ctx, r.cfg.ReviewTimeout)
	}
	return ctx, func() {}
}

// recoverReview 捕获回顾 goroutine 内的 panic，防进程崩溃。
//
// [Why] spec「高可用」要求回顾全链路任一环节失败均不影响主流程。panic 是最严重的失败
// 形态（如 Provider 内部 nil 解引用、store 写入意外 panic），必须兜底。用全局 logger.Error
// 记录（不依赖可能已失效的 ctx），保证 panic 信息一定落盘，便于排查。
func (r *Reviewer) recoverReview(sessionID string) {
	if rec := recover(); rec != nil {
		logger.Error("autolearn: 回顾 goroutine panic，已恢复，主流程不受影响",
			zap.String("sessionID", sessionID),
			zap.Any("panic", rec),
		)
	}
}
