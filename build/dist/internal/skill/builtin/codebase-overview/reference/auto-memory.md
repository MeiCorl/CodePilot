# 自动学习记忆 — CodePilot 实现原理

> 隶属 Step 8（自动学习记忆）| 架构层:第 4 层 记忆层 | 核心入口:`src/internal/memory/autolearn/store.go`

## §1 模块定位

自动学习记忆位于第 4 层 记忆层,是 CodePilot 长期记忆的第三类——Agent 在使用过程中自主总结、按分类沉淀为独立 md 文件,跨会话持久化,让 Agent「想起」之前沉淀的用户偏好、反馈、项目知识与参考信息。

- **4 类记忆分级**(`types.go`):用户偏好 / 用户反馈 / 项目知识 / 参考信息
- **存储域**:`ScopeUser(跨项目 ~/.codepilot/memory)+ ScopeProject(跟随项目 <cwd>/.codepilot/memory)`
- **MEMORY.md 索引注入**:每会话启动时把两级索引注入到 SP LeadUserMessage(`memory_index.go`)
- **后台异步 Reviewer**:`Reviewer.OnLoopDone`(`src/internal/memory/autolearn/reviewer.go`)每轮 AgentLoop 完成后异步回顾
- **敏感脱敏两道防线**:`prompt 约束(LLM 自觉跳过)` + `Sanitize 正则兜底(sanitizer.go)`
- **ReadFile 附加只读根**:`buildMemoryReadRoots`(`src/main.go`)让 LLM 能读沙箱外的记忆文件

## §2 核心数据结构

- `MemoryType`(`src/internal/memory/autolearn/types.go`)— 四类记忆,常量 `MemoryTypeUserPreference / MemoryTypeUserFeedback / MemoryTypeProjectKnowledge / MemoryTypeReference`
- `StorageScope`(types.go)— 存储域,常量 `ScopeUser / ScopeProject`
- `ScopeOf(t MemoryType) StorageScope`(types.go)— 偏好 / 反馈 → 用户级;项目知识 / 参考 → 项目级
- `Frontmatter`(types.go)— YAML 头,字段 `Type / Title / CreatedAt / UpdatedAt`
- `Memory`(types.go)— 单条记忆,字段 `Frontmatter + Slug + Content`
- `IndexEntry`(types.go)— MEMORY.md 索引行,字段 `Type / Slug / Summary`,渲染格式 `- [user_preference](indent-style.md)——使用4个空格代替TAB`
- `Store`(`src/internal/memory/autolearn/store.go`)— 文件持久化抽象,字段 `userRoot / projectRoot / mu`
- `memoryTypeOrder`(types.go)— 4 类记忆在 MEMORY.md 索引中的渲染顺序(固定)
- `IsValidType(t MemoryType) bool`(types.go)— 校验类型合法
- `Reviewer`(`src/internal/memory/autolearn/reviewer.go`)— 后台异步回顾器,字段 `provider / store / cfg / inflight / wg`
- `ReviewRequest` / `ReviewEvent`(reviewer.go)— 回顾请求 / 事件
- `ReviewerConfig`(reviewer.go)— `Enabled / ReviewTimeout`
- `redactPattern`(sanitizer.go)— 敏感凭证正则 + 替换模板
- `sensitivePatterns`(sanitizer.go)— 三类敏感模式(高熵凭证 / Bearer token / 键值对口令)
- `MemoryIndexSource`(`src/internal/engine/prompt/sources/memory_index.go`)— SP 索引注入 Source
- `MemoryIndexOptions`(memory_index.go)— `Enabled / MaxLines / MaxBytes`

## §3 关键流程

### 3.1 4 类记忆分级与存储映射

`ScopeOf(t MemoryType)`(`types.go`)映射规则:

- **用户级**(`~/.codepilot/memory/`):`MemoryTypeUserPreference`(用户偏好,如「缩进用 4 个空格」)+ `MemoryTypeUserFeedback`(用户反馈,如「上次生成的代码漏了错误处理」)
- **项目级**(`<cwd>/.codepilot/memory/`):`MemoryTypeProjectKnowledge`(项目知识,如架构 / 部署 / 内部约定)+ `MemoryTypeReference`(参考信息,如 API 文档 / 内部 wiki 链接)

[Why] 按存储域分级:**Why** 用户偏好与项目无关(跨项目生效),项目知识与项目强相关(随项目目录);分级存储避免「换项目就丢失用户偏好」或「用户级混入项目特定知识」。

### 3.2 MEMORY.md 索引渲染

`Store.RewriteIndex(scope, entries)`(`store.go`)原子覆盖索引:

1. `os.MkdirAll(root, 0o755)` 惰性创建目录
2. `renderIndex(entries)` 按 4 类分块渲染文本(`memoryTypeOrder` 固定顺序)
3. `atomicWriteFile(path, content)` 写临时文件 + rename 原子覆盖

`renderIndex`(`store.go`)按 4 类分块,每类下用 `- [type](slug.md)——一句话简介` 格式。**Why** 索引行类型标签同时出现在 `[type]` 中,使解析只依赖行内标签;渲染时的 H2 分块标题仅供人类阅读。

### 3.3 SP 索引注入(`MemoryIndexSource`)

`MemoryIndexSource.Assemble`(`memory_index.go`)每会话启动时被 `Builder.Assemble` 调用:

1. `store.ReadIndex(scope)` 读两级记忆索引(用户级 + 项目级)
2. `truncateMemoryIndex(body)` 按 `MaxLines / MaxBytes` 双维度截断
3. 拼为单条 `LeadUserMessage`,Placement=UserMessage

`store==nil || opts.Enabled=false` 时返回空 Section,整体降级。

[Why] Placement=UserMessage 而非 System:**Why** 记忆索引可能很长(上百条),塞进 System 字段会稀释注意力;UserMessage 位置便于 LLM 在多轮迭代中动态引用。

### 3.4 后台异步 Reviewer

`Reviewer.OnLoopDone(result AgentLoopResult)`(reviewer.go)是每轮 AgentLoop 完成时触发的回调:

1. `shouldReview(req)`(`reviewer.go`)过滤:`req.Completed == true && UserInput 非空 && 非闲聊词`
2. 通过 `r.markInflight(sessionID)` 检查 inflight map,同 session 上一回顾未完成则 `drop`(避免并发回顾互覆盖)
3. `r.wg.Add(1)` + `go r.asyncReview(req)` 启动后台 goroutine 异步执行

`r.asyncReview(req)`(reviewer.go)defer 链:
- `defer r.wg.Done()`
- `defer r.recoverReview(req)`(panic 兜底)
- `defer r.clearInflight(req.SessionID)`
- `ctx, cancel := r.deriveContext(req.SessionID)` 带超时
- `r.runReview(ctx, req)` 实际执行回顾

[Why] drop 而非排队:**Why** 同一 session 上一回顾未完成时,新回顾多半是基于相似上下文的重复请求;drop 避免「回顾堆积」造成的 token 浪费与索引互覆盖。

### 3.5 敏感脱敏两道防线

**第一道防线(prompt 约束)**:`reviewSystemPrompt` 明确禁止记录敏感凭证(模型自觉跳过)。

**第二道防线(正则兜底)**:`Sanitize(text) string`(sanitizer.go)落盘前兜底扫描:

- **三类敏感模式**(`sensitivePatterns` sanitizer.go):
  - 高熵凭证:OpenAI sk- / AWS AKIA / Slack xox- / GitHub ghp_ 等
  - Bearer token:保留 `Bearer ` 前缀,仅 token 主体脱敏
  - 键值对口口:保留键名+分隔符,仅值脱敏
- **三类替换模板独立**:
  - 高熵凭证:整体替换为 `[REDACTED]`
  - Bearer:替换模板 `${1}[REDACTED]`(保留前缀)
  - 键值对:替换模板 `${1}[REDACTED]`(保留键名)

[Why] 三类独立替换模板:**Why** 若共用同一模板,高熵凭证串会被 `${1}` 引用原样保留(凭证本身),脱敏失效。

### 3.6 ReadFile 附加只读根

`buildMemoryReadRoots(toolWorkdir)`(`src/main.go`)计算记忆附加根:

1. `userRoot = autolearn.UserMemoryRoot(homeDir) = <home>/.codepilot/memory`
2. `projectRoot = autolearn.ProjectMemoryRoot(toolWorkdir) = <cwd>/.codepilot/memory`
3. 过滤空串后合并到 `WithReadRoots` 参数

`SandboxMiddleware` 在 PermRead 类工具调用时,把这些根作为合法可读范围放行;PermWrite / PermExec 仍仅认 workdir(纵深防御,防止 memory 目录被 WriteFile/EditFile 直接写入)。

[Why] 仅 PermRead 启用附加根:**Why** 「能读 memory」不等于「能写 memory」;写入仍走 workdir 沙箱,纵深防御。

## §4 与其他模块的依赖

- **上游**(记忆模块依赖):
  - `internal/llm.Provider`— 后台 Reviewer 调 LLM 回顾 + 敏感凭证正则兜底
  - `internal/logger`(`src/internal/logger/`)
  - `internal/security`(`src/internal/security/sandbox_middleware.go`)— 附加只读根机制复用 `ResolveInSandboxWithRoots`
- **下游被依赖**:
  - `internal/engine/prompt/sources/memory_index.go`— SP 索引注入
  - `internal/engine/conversation.manager`(`OnLoopDone` 回调)— Reviewer 装配
  - `main.go`(`buildMemoryReadRoots`)— 沙箱附加根装配

## §5 设计决策

### 决策 1:4 类记忆 + 2 个存储域固定映射

- **问题**:记忆类型与存储域如何设计才既灵活又不冗余
- **方案**:4 类固定(偏好 / 反馈 / 项目知识 / 参考),按 `ScopeOf` 映射到 2 个存储域(用户级 / 项目级)
- **理由**:**Why** 4 类覆盖「用户维度」+「项目维度」+「知识维度」+「参考维度」,是 agent memory 主流分类;固定映射避免「存哪」决策复杂度

### 决策 2:MEMORY.md 索引 + 单文件记忆正文

- **问题**:记忆条目可能成百上千,全量加载会撑爆上下文;每条单独文件又不便统一查看
- **方案**:`MEMORY.md` 是轻量索引(每行 1 条),`<slug>.md` 是单条记忆完整内容
- **理由**:**Why** 索引注入轻量(token 少);LLM 读到相关条目后再用 `ReadFile` 按需加载详情(渐进式披露)

### 决策 3:Reviewer 异步后台 + 同 session drop

- **问题**:回顾是 LLM 调用,放同步路径会阻塞主对话;并发回顾又会索引互覆盖
- **方案**:`asyncReview` 后台 goroutine + `inflight map[string]struct{}` 串行化同 session
- **理由**:**Why** 异步不阻塞主对话体验;同 session 串行化避免并发回顾读到旧索引后覆盖丢条目;drop 策略(而非排队)避免「回顾堆积」

### 决策 4:敏感脱敏两道防线(prompt + 正则)

- **问题**:记忆文件跨会话持久化,敏感凭证泄露风险高
- **方案**:第一道 prompt 约束(LLM 自觉跳过)+ 第二道 Sanitize 正则兜底
- **理由**:**Why** 单层 prompt 约束不够鲁棒——模型可能疏忽;正则兜底以「保守低误报」策略扫常见凭证特征串

### 决策 5:PermRead 启用附加根,PermWrite/Exec 仍仅认 workdir

- **问题**:LLM 需要读 memory 文件,但不能让 WriteFile/EditFile 直接写入 memory
- **方案**:`SandboxMiddleware` 仅在 `perm == tool.PermRead && len(readRoots) > 0` 时调 `ResolveInSandboxWithRoots`;写/执行类工具仍走 `ResolveInSandbox`
- **理由**:**Why** 「能读」不等于「能写」;纵深防御防止 memory 目录被 WriteFile 直接篡改

## §6 关键文件索引

| 路径 | 角色 |
|------|------|
| `src/internal/memory/autolearn/types.go` | `MemoryType` 4 类枚举 |
| `src/internal/memory/autolearn/types.go` | `ScopeOf` 类型→存储域映射 |
| `src/internal/memory/autolearn/types.go` | `Frontmatter` YAML 头 |
| `src/internal/memory/autolearn/types.go` | `Memory` 单条记忆 |
| `src/internal/memory/autolearn/types.go` | `IndexEntry` 索引行 |
| `src/internal/memory/autolearn/store.go` | `Store` 文件持久化 |
| `src/internal/memory/autolearn/store.go` | `ReadIndex` 读两级索引 |
| `src/internal/memory/autolearn/store.go` | `RewriteIndex` 原子重写索引 |
| `src/internal/memory/autolearn/reviewer.go` | `Reviewer` 后台异步回顾 |
| `src/internal/memory/autolearn/reviewer.go` | `asyncReview` defer 链 panic 兜底 |
| `src/internal/memory/autolearn/reviewer.go` | `shouldReview` 过滤逻辑 |
| `src/internal/memory/autolearn/sanitizer.go` | `sensitivePatterns` 三类敏感正则 |
| `src/internal/memory/autolearn/sanitizer.go` | `Sanitize` 脱敏主入口 |
| `src/internal/engine/prompt/sources/memory_index.go` | `MemoryIndexSource` SP 注入 |
| `src/internal/engine/prompt/sources/memory_index.go` | `truncateMemoryIndex` 双维度截断 |
| `src/main.go` | `buildMemoryReadRoots` 沙箱附加根 |