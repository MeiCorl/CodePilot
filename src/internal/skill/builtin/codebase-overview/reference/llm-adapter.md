# 多 LLM 适配 — CodePilot 实现原理

> 隶属 Step 1（LLM 打通）| 架构层:第 2 层 引擎层 | 核心入口:`src/main.go`

## §1 模块定位

LLM 适配层位于第 2 层 引擎层,通过 `Provider` 接口统一抽象不同 LLM 服务(Anthropic Claude / OpenAI GPT),把内部通用消息格式转为各 Provider 的 SDK 格式,处理流式响应、Prompt Caching、中断机制等。

- **统一抽象**:`ContentBlock` 接口(文本 / 工具调用 / 工具结果)+ `Message{Role, Content}` 结构
- **双 Provider 适配**:`AnthropicProvider`(`src/llm/anthropic.go`)与 `OpenAIProvider`(`src/llm/openai.go`)
- **流式响应**:统一 `StreamChat(ctx, sp, messages, tools) (<-chan StreamChunk, error)`,chunk 含 `Content / ToolUses / Usage / LLMStopReason / Done`
- **Prompt Caching**:Anthropic 协议下 `Placement=System` 段自动打 `cache_control`;OpenAI 协议下由服务端自动决定
- **中断机制**:`ctx.Done()` 监听,Provider goroutine 排空 channel 后退出

## §2 核心数据结构

- `ContentBlock`(`src/llm/types.go`)— 消息内容统一接口,方法 `Type() ContentBlockType` + `ToText() string`
- `TextBlock`(`src/llm/types.go`)— 文本块,字段 `Text string`
- `ToolUseBlock`(`src/llm/types.go`)— LLM 发出的工具调用,字段 `ID / Name / Input(json.RawMessage)`,对应 Anthropic `tool_use` 与 OpenAI `tool_calls.function`
- `ToolResultBlock`(`src/llm/types.go`)— 工具执行结果,字段 `ToolUseID / Content / IsError`,对应 Anthropic `tool_result` 与 OpenAI `role=tool`
- `Message{Role, Content []ContentBlock}`(`src/llm/types.go`)— 通用对话消息
- `TokenUsage`(types.go)— token 用量统计,挂在流式结束 chunk(`Done=true`)上携带
- `StreamChunk{Content, ToolUses, Usage, LLMStopReason, Done, Err}`(`src/llm/types.go`)— 流式响应最小单元
- `Provider`(`src/llm/provider.go`)— Provider 接口:`Chat(ctx, sp, msgs, tools) (Message, error)` + `StreamChat(ctx, sp, msgs, tools) (<-chan StreamChunk, error)`
- `SystemPrompt`(`src/internal/engine/prompt/sources/source.go`)— 结构化 SP,含 `SystemBlocks[]` + `LeadUserMessage` + `Stats` + `TotalTokens`
- `AnthropicProvider`(`src/llm/anthropic.go`)— Anthropic Claude 适配,字段 `client *anthropic.Client / model / maxTokens / timeout / maxRetries`
- `OpenAIProvider`(`src/llm/openai.go`)— OpenAI GPT 适配,同构字段

## §3 关键流程

### 3.1 Provider 选择与构造

`main.go` 启动期根据 `config.Config.Provider` 选择适配器:

1. `cfg.APIKey + cfg.BaseURL`(非空时)+ `cfg.Model / MaxTokens / Timeout / MaxRetries` 构造 `NewAnthropicProvider(cfg)`(`anthropic.go`)或 `NewOpenAIProvider(cfg)`(`openai.go`)
2. 注入到 `ConversationManager` 作为 `provider llm.Provider`

[Why] Provider 是接口而非 struct:**Why** 新增 Gemini / DeepSeek 等只需新写一个 `xxxProvider` 实现 Provider 接口,无需改 engine 层(架构「高扩展」要求)。

### 3.2 工具 schema 转换

`AnthropicProvider.convertTools(specs []tool.ToolSpec) []anthropic.ToolUnionParam`(`anthropic.go`)流程:

1. 从 `ToolSpec.InputSchema`(完整 JSON Schema)挑出 `properties` + `required` 两个字段
2. 构造 `anthropic.ToolInputSchemaParam{Properties, Required}`(SDK 限定 `type=object`,其他字段忽略)
3. 解析失败时退化为空 schema(LLM 看不到参数约束但工具仍可注册)

`OpenAIProvider.convertTools(specs) []openai.ChatCompletionToolParam`(`openai.go`)流程:

1. 把 `InputSchema` 解析为 `map[string]any`,整体塞入 `shared.FunctionDefinitionParam.Parameters`
2. OpenAI 不限制 type=object,直接复用完整 JSON Schema

[Why] Anthropic 与 OpenAI 协议差异:**Why** Anthropic SDK 强制 `type=object` 字段,其他字段(如 `description`)由 LLM 端按规范自动处理;OpenAI 用 `function` 类型 + FunctionDefinition。

### 3.3 消息转换(支持 tool_use / tool_result)

`AnthropicProvider.convertMessages(messages) []anthropic.MessageParam`(`anthropic.go`)按 Role(User/Assistant)+ ContentBlock 类型分发:

- `*TextBlock` → `anthropic.NewTextBlock(text)`
- `*ToolUseBlock` → `anthropic.NewToolUseBlock(id, input, name)`(`input` 是 `json.RawMessage`)
- `*ToolResultBlock` → `anthropic.NewToolResultBlock(toolUseID, content, isError)`

`OpenAIProvider.convertMessages`(`openai.go`)按 OpenAI 协议转:ToolUse → `tool_calls`,ToolResult → `role=tool`。

### 3.4 流式响应 + 中断

`AnthropicProvider.StreamChat(ctx, sp, messages, tools) (<-chan StreamChunk, error)` 流程:

1. 构造 `anthropic.MessageNewParams`,调用 `client.Messages.NewStreaming(...)`
2. 启动内部 goroutine 读取 SDK 流,逐 chunk 转换为 `StreamChunk{Content, ToolUses, Usage, Done}` 投递到 `chunkCh`
3. `ctx.Done()` 时 Provider 主动停止读 SDK stream、关闭 `chunkCh`(让消费者退出)
4. 最后一个 chunk(`Done=true`)携带 `TokenUsage` + `LLMStopReason`(`end_turn` / `max_tokens` / `tool_use` / `refusal`)

### 3.5 Prompt Caching(Anthropic)

`buildAnthropicSystemText(blocks []SystemBlock)`(`anthropic.go` 内部)流程:

- 把 `blocks` 按 `Cacheable=true/false` 切片,在最后一个 `Cacheable=true` 段打 `cache_control: {type: ephemeral}`
- `CodebaseAwarenessSource` / `ConfigAwarenessSource` / `StaticSource` 等 `Placement=System` 段默认 `Cacheable=true`(由 `Builder.Assemble` 统一设置,见 `src/internal/engine/prompt/builder.go`)

[Why] Anthropic 协议下 Prompt Caching 按段标记,SDK 会自动处理缓存命中与计费;**Why** 常驻 SP 段(配置自感知 / 代码自感知 / 静态规则)都是稳定的,缓存可大幅降低多轮迭代的 token 成本。

## §4 与其他模块的依赖

- **上游**(LLM 模块依赖):
  - `anthropic-sdk-go`(Anthropic 官方 SDK,go.mod)
  - `openai-go`(OpenAI 官方 SDK)
  - `internal/config.Config`(`src/internal/config/config.go`)— 注入 APIKey / BaseURL / Model / MaxTokens / Timeout / MaxRetries
  - `internal/tool.ToolSpec`(`src/internal/tool/tool_spec.go`)— 工具 schema 转 SDK 格式
- **下游被依赖**:
  - `internal/engine/conversation`(ConversationManager)—— 持 `provider llm.Provider` 字段,`runOneLLM` 调用 `provider.StreamChat`
  - `internal/memory/context` SummaryCompactor(Step 7)—— 摘要压缩也走 Provider(`reviewProvider`)

## §5 设计决策

### 决策 1:`ContentBlock` 接口统一抽象

- **问题**:Anthropic `tool_use`/`tool_result` 与 OpenAI `tool_calls`/`role=tool` 协议不同,直接用 SDK 类型会让 engine 层耦合到具体 Provider
- **方案**:定义 `ContentBlock` 接口 + `TextBlock`/`ToolUseBlock`/`ToolResultBlock` 三种实现;Provider 负责转换
- **理由**:**Why** engine 层只与 `ContentBlock` 交互,新增 Provider 只需在 `convertMessages` 加分支,不改 engine 代码

### 决策 2:流式响应统一为 `<-chan StreamChunk`

- **问题**:LLM 是长耗时 IO,同步等待会阻塞 UI;不同 SDK 流式 API 不一致
- **方案**:Provider 内部 goroutine 转 SDK 流为统一 `StreamChunk` 通道,消费者按需读
- **理由**:**Why** Go channel 是天然的多生产者-单消费者并发原语;`<-chan` 单向消费语义清晰;`ctx.Done()` 即可中断

### 决策 3:Prompt Caching 仅 Anthropic

- **问题**:Anthropic Prompt Caching 按段标记(可控),OpenAI 由服务端自动决定(前 1024 token 命中)
- **方案**:Anthropic 路径主动设置 `cache_control`,OpenAI 路径不传
- **理由**:**Why** OpenAI 协议约束,服务端决定命中区,客户端无法干预;Anthropic 协议允许客户端精细控制

### 决策 4:工具调用 Input 用 `json.RawMessage`

- **问题**:`ToolSpec.InputSchema` 是完整 JSON Schema,`ToolUseBlock.Input` 是 LLM 实际传入的参数 JSON;两者都是 `[]byte` 但语义不同
- **方案**:`json.RawMessage` 别名 `[]byte`,延迟解析到工具 `Execute` 内
- **理由**:**Why** Provider 转换层不感知工具内部结构,延迟到工具自身 `json.Unmarshal` 到内部 struct;转换层零反射开销

### 决策 5:`LLMStopReason` 上报而非吞掉

- **问题**:Anthropic `max_tokens` 截断、`tool_use` 正常结束、`refusal` 拒绝 三种结束原因诊断价值差异大
- **方案**:每个 chunk 都携带 `LLMStopReason`,流结束由 `manager.logLLMStopReason` 记录(`src/internal/engine/conversation/manager.go`)
- **理由**:**Why** 用户能看到「为什么 LLM 没回完整」便于排查,生产环境也能据此统计 `max_tokens` 占比调 `MaxTokens` 配置

## §6 关键文件索引

| 路径 | 角色 |
|------|------|
| `src/llm/provider.go` | `Provider` 接口定义(Chat + StreamChat) |
| `src/llm/types.go` | `ContentBlock` 接口 + 三种实现 |
| `src/llm/types.go` | `ToolUseBlock` 工具调用块 |
| `src/llm/types.go` | `ToolResultBlock` 工具结果块 |
| `src/llm/types.go` | `Message` 通用消息结构 |
| `src/llm/anthropic.go` | `AnthropicProvider` Anthropic 适配 |
| `src/llm/anthropic.go` | `convertTools` Anthropic 工具 schema 转换 |
| `src/llm/anthropic.go` | `convertMessages` Anthropic 消息转换 |
| `src/llm/openai.go` | `OpenAIProvider` OpenAI 适配 |
| `src/llm/openai.go` | `convertTools` OpenAI 工具 schema 转换 |
| `src/llm/context_error.go` | `IsContextTooLongError` 撞墙判定 |
| `src/internal/engine/prompt/sources/source.go` | `SystemPrompt` 结构化 SP |
| `src/internal/engine/prompt/builder.go` | `Builder.Assemble` 把 Placement=System 标记 Cacheable=true |
| `src/internal/engine/conversation/manager.go` | `runOneLLM` 调用 provider.StreamChat |
| `src/internal/engine/conversation/manager.go` | `logLLMStopReason` 停止原因日志 |