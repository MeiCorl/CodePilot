# 上下文管理 — CodePilot 实现原理

> 隶属 Step 7（上下文管理）| 架构层:第 4 层 记忆层 | 核心入口:`src/internal/memory/context/compactor.go`

## §1 模块定位

上下文管理位于第 4 层 记忆层,在 LLM 上下文窗口有限的前提下,自动压缩历史消息以保留最相关信息,避免对话因 token 超限而中断。

- **两层压缩** — L1(工具结果存盘预览,`src/internal/memory/context/light_compactor.go`)+ L2(LLM 摘要,`src/internal/memory/context/summary_compactor.go`)
- **撞墙紧急压缩** — Provider 返回 `prompt_too_long` 时强制第二层摘要 + 重试一次(`manager.go`)
- **会话级熔断** — `Compactor.tripped map[string]bool`(`compactor.go`)按 sessionID 隔离,连续摘要失败达阈值后自动模式跳过第二层
- **历史归档** — `HistoryArchiver` 接口(`summary_compactor.go`)把原文归档到磁盘,活跃历史用摘要 + 最近 N 条消息替代
- **可观测性** — `OnCompaction` 回调(manager.go)推 `compaction_event` 给前端

## §2 核心数据结构

- `Compactor`(`src/internal/memory/context/compactor.go`)— 压缩协调器,字段 `light / summary / cfg / failures / tripped`,所有读写经 `mu sync.Mutex`
- `CompactionResult`(compactor.go)— 单轮压缩结果,字段 `Level / BeforeTokens / AfterTokens / Applied / Tripped / Err`
- `CompactionLevel`(compactor.go)— `CompactionLevelNone / CompactionLevelLight / CompactionLevelSummary`
- `ConversationHistory` interface(`compactor.go`)— 协调器与 ConversationManager 协作契约,方法 `History() / ReplaceHistory(msgs) / Remaining() int`
- `LightCompactor`(`src/internal/memory/context/light_compactor.go`)— 第一层压缩器,无状态;把超长工具结果存盘预览(替换为 `<preview>...</preview>` 引用)
- `SummaryCompactor`(`src/internal/memory/context/summary_compactor.go`)— 第二层压缩器,无状态;调 LLM 摘要 + 归档原文
- `HistoryArchiver`(summary_compactor.go)— 摘要后原文归档接口,通常由 `*session.SessionManager` 实现
- `splitByTailTokens`(summary_compactor.go)— 按 token 维度切分消息,工具对 (tool_use / tool_result) 对齐到同侧(`alignSplitForToolPairs`)
- `alignSplitForToolPairs`(summary_compactor.go)— 防止 tool_result 留在活跃 tail 但其 tool_use 已被摘要掉(Anthropic 协议校验失败)

## §3 关键流程

### 3.1 每轮 API 请求前的自动压缩(`runOneLLM`)

`runOneLLM`(manager.go)在调 `provider.StreamChat` 前先调 `m.runAutoCompaction(ctx, provider, hooks)`(manager.go):

1. **第一层预防压缩必跑**:`compactor.Compact` 第一阶段调 `light.Compact(history)`,把超长工具结果存盘替换为预览引用
2. **第二层摘要按余量 + 熔断判定**:
   - 余量低于 `AutoTriggerMargin` 时触发
   - 手动模式 / 紧急模式绕过熔断
   - `tripped[sessionID] == true` 时自动模式跳过(手动模式仍可执行并重置标志)
3. 压缩失败不中断主流程(失败时 history 仍可用),产生变更时通过 `hooks.OnCompaction` 外推

[Why] 压缩失败不中断:**Why** 主流程语义是「完成用户任务」,压缩是辅助;若摘要 LLM 暂时不可用,降级到无压缩继续请求比直接中断更友好。

### 3.2 两层压缩编排(`Compactor.Compact`)

`Compactor.Compact(ctx, provider, ch, sessionID, manual) (CompactionResult, error)`(`compactor.go`)流程:

1. **第一层(Light)**:`c.light.Compact(ctx, msgs)` 把超长工具结果替换为预览
   - 预览判定:复用 `preview.go isPreview`(以固定尾注锚点 HasSuffix)与 LightCompactor 跳过已处理 block 同口径
   - 变更后 `history` 立即生效(本地复制,不持久化,等第二层摘要成功才落盘)
2. **第二层判定(Summary)**:`c.decideSummary(...)` 检查余量 + 熔断
3. **第二层执行(若判定通过)**:`c.summary.Compact(ctx, provider, ...)` 调 LLM 摘要
   - `splitByTailTokens` 按 token 维度 + 条数维度切分
   - `alignSplitForToolPairs` 保证 tool_use / tool_result 同侧
   - 摘要原文经 `HistoryArchiver.Archive` 归档到磁盘
   - `ConversationHistory.ReplaceHistory(newHistory)` 替换活跃历史
4. **熔断状态更新**:摘要失败 `failures[sessionID]++`,达 `BreakerThreshold` 后 `tripped[sessionID] = true`;成功则清零

[Why] 手动模式绕过熔断:**Why** 用户主动 `/compact` 是「我接受这次摘要的成本」,不应被自动模式的熔断阻止;同时手动摘要成功时重置 `tripped` 标志,给自动模式重试机会。

### 3.3 撞墙紧急压缩(`emergencyCompactOnWallHit`)

`runOneLLM`(manager.go)中 Provider 返回 `IsContextTooLongError(err)` 时(`llm/context_error.go`):

1. `m.emergencyCompactOnWallHit(ctx, provider, hooks)`(manager.go)调 `compactor.EmergencyCompact`(compactor.go)
2. `EmergencyCompact` 内部走 `Compact(manual=true)` 路径——强制第二层摘要 + 无视余量 + 临时豁免熔断
3. 紧急压缩成功后 `messages = m.GetContext()` 拿压缩后历史,重试一次 `provider.StreamChat`
4. **重试成功** → 继续正常消费流
5. **重试仍失败 / 紧急压缩失败** → 返回【原始的】超长错误(不吞异常,让上层如实上报根因)

[Why] 复用 Compact(manual=true) 路径:**Why** 紧急模式语义上等价于 manual 触发(都无视余量、临时豁免熔断);不另起一套实现避免与普通手动触发逻辑分叉,产生不一致的熔断/计数行为。

### 3.4 tool_use / tool_result 对齐

`splitByTailTokens` 切分点对齐到消息边界(绝不拆单条消息),`alignSplitForToolPairs`(summary_compactor.go)额外保证 Anthropic 协议约束:

- **问题**:Anthropic 协议校验要求每条 `tool_result` 必须在历史中能找到对应的 `assistant` `tool_use`;若 `tool_result` 留在活跃 tail 但 `tool_use` 已被摘要掉,请求会被 Anthropic 拒绝
- **方案**:`alignSplitForToolPairs` 检查 `startsWithToolResult(history[split])` 且 `assistantHasToolUseFor(history[split-1], history[split])` 时,把 split 往前挪,让配对留在同侧

[Why] 严格按协议对齐:**Why** Anthropic 服务端会校验「每个 tool_result 必须有对应 tool_use」;若切分点违反约束,Provider 立即返回 400 错误,即使摘要压缩了也无法挽回。

### 3.5 历史归档(`HistoryArchiver`)

摘要压缩成功后,原文经 `HistoryArchiver.Archive(sessionID, originalMsgs)`(summary_compactor.go)归档:

- 默认实现是 `*session.SessionManager`(`src/internal/memory/session/`)
- 归档格式:与原始 messages.jsonl 同结构,文件名带时间戳后缀
- 用户可在 `/dump` 导出会话时一并看到归档内容(便于排查「摘要是否丢失关键信息」)

## §4 与其他模块的依赖

- **上游**(上下文模块依赖):
  - `internal/llm.Provider`(`src/llm/provider.go`)— 第二层摘要 LLM 调用
  - `internal/memory/autolearn.Store`(`src/internal/memory/autolearn/store.go`)— MemoryIndexSource 读取记忆索引
  - `internal/memory/session.SessionManager`(HistoryArchiver 实现)— 历史归档目标
- **下游被依赖**:
  - `internal/engine/conversation.ConversationManager`— `runOneLLM` 调压缩;`ReplaceHistory` 注入压缩后历史
  - `internal/interaction/web/handler`— `OnCompaction` 回调推 `compaction_event` 给 WebUI
  - `/compact` slash 命令— 手动触发压缩(Step 7 引入)

## §5 设计决策

### 决策 1:两层压缩(L1 预览 + L2 摘要)

- **问题**:单层 LLM 摘要成本高(每次摘要消耗额外 token),但纯滑动窗口丢关键信息
- **方案**:L1 工具结果存盘预览(零成本、保留全文引用)+ L2 LLM 摘要(高成本、生成凝练摘要)
- **理由**:**Why** L1 处理 80% 高频场景(单工具结果过长),L2 仅在 L1 仍不够时触发;两层串联把 token 消耗控制到最小

### 决策 2:会话级熔断而非全局熔断

- **问题**:摘要 LLM 暂时不可用时,频繁失败重试会浪费 token 配额
- **方案**:`Compactor.tripped map[string]bool` 按 sessionID 隔离熔断;某会话连续失败达阈值后该会话跳过自动第二层,其他会话不受影响
- **理由**:**Why** 多会话并行场景下,某会话的 LLM 配额耗尽不应拖累其他会话;按 sessionID 隔离是「故障域最小化」的工业惯例

### 决策 3:手动模式绕过熔断

- **问题**:用户主动 `/compact` 是「我接受这次摘要的成本」,不应被自动熔断阻止
- **方案**:`Compact(manual=true)` 临时豁免熔断,执行成功后 `tripped[sessionID] = false`
- **理由**:**Why** 用户主动行为是「显式信号」,比自动模式的启发式判定更可靠;执行成功重置熔断给自动模式重试机会

### 决策 4:tool_use / tool_result 对齐切分点

- **问题**:Anthropic 协议要求 tool_result 在历史中能找到对应 tool_use;切分点违反约束会导致请求被拒绝
- **方案**:`alignSplitForToolPairs` 在 `splitByTailTokens` 后额外检查并前移 split
- **理由**:**Why** 严格按协议对齐是 Anthropic 服务端硬性校验;即使摘要压缩了也不能违反协议约束

### 决策 5:撞墙紧急压缩只重试一次

- **问题**:Provider 撞墙后如何兜底?无限重试会陷入死循环
- **方案**:`wallHitRetried` 局部变量 + 结构性「本分支只进一次」保证单次 `runOneLLM` 内只重试 1 次
- **理由**:**Why** AgentLoop 每轮迭代调一次 `runOneLLM`,每轮都享有一次兜底机会;局部变量天然限定作用域,避免用结构体字段带来的跨调用状态污染

## §6 关键文件索引

| 路径 | 角色 |
|------|------|
| `src/internal/memory/context/compactor.go` | `Compactor` 压缩协调器 |
| `src/internal/memory/context/compactor.go` | `Compact` 两层编排 |
| `src/internal/memory/context/compactor.go` | `EmergencyCompact` 紧急压缩 |
| `src/internal/memory/context/light_compactor.go` | `Compact` 第一层工具结果预览 |
| `src/internal/memory/context/summary_compactor.go` | `SummaryCompactor` 第二层摘要 |
| `src/internal/memory/context/summary_compactor.go` | `splitByTailTokens` 按 token 切分 |
| `src/internal/memory/context/summary_compactor.go` | `alignSplitForToolPairs` 协议对齐 |
| `src/internal/engine/conversation/manager.go` | `runOneLLM` 含撞墙兜底 |
| `src/internal/engine/conversation/manager.go` | `runAutoCompaction` 每轮自动压缩 |
| `src/internal/engine/conversation/manager.go` | `emergencyCompactOnWallHit` 紧急压缩入口 |
| `src/llm/context_error.go` | `IsContextTooLongError` 撞墙判定 |