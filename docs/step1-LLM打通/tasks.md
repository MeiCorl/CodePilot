# Step 1 Tasks — LLM 接入与 TUI 交互

---

## Task 1: 项目初始化与基础骨架搭建

**状态**：已完成

**目标**：初始化 Go Module，搭建与 5 层架构对应的目录结构，确保项目可编译运行。

**影响文件**：
- `go.mod` — 新建，Go 模块定义（`github.com/MeiCorl/CodePilot`）
- `cmd/codepilot/main.go` — 新建，程序入口
- `internal/interaction/tui/` — 新建目录
- `internal/engine/conversation/` — 新建目录
- `internal/engine/prompt/` — 新建目录
- `internal/memory/context/` — 新建目录
- `internal/memory/session/` — 新建目录
- `internal/config/` — 新建目录
- `internal/logger/` — 新建目录
- `llm/` — 新建目录

**依赖**：无

**具体内容**：
1. 执行 `go mod init github.com/MeiCorl/CodePilot` 初始化 Go 模块
2. 创建架构分层的目录结构（`cmd/`、`internal/`、``），每层按架构图创建子包目录，包含 `internal/logger/`
3. 编写 `cmd/codepilot/main.go` 入口文件，暂时只做 `fmt.Println("Hello CodePilot")` 验证编译链路
4. 执行 `go build ./cmd/codepilot/` 验证编译通过

**参考资料**：
- Go Module 初始化：`go mod init <module-path>`
- Go 项目布局规范：`cmd/` 放可执行入口，`internal/` 放私有包，`` 放可复用公共包

---

## Task 2: 配置系统实现

**状态**：已完成

**目标**：实现配置文件加载，支持从 `~/codepilot/config.json` 读取 LLM 供应商配置，提供默认值和校验。

**影响文件**：
- `internal/config/config.go` — 新建，配置结构体定义、加载、校验
- `config/config.example.json` — 新建，配置文件示例

**依赖**：Task 1

**具体内容**：
1. 定义配置结构体，包含以下字段：
   - `Provider`（string）：供应商名称，`"anthropic"` 或 `"openai"`
   - `Model`（string）：模型名称，如 `"claude-sonnet-4-20250514"` 或 `"gpt-4o"`
   - `BaseURL`（string）：模型 API 地址（可选，留空使用供应商默认地址）
   - `APIKey`（string）：模型密钥
   - `MaxTokens`（int）：单次最大输出 token 数
   - `Timeout`（int）：请求超时秒数，默认 60
   - `MaxRetries`（int）：最大重试次数，默认 2
2. 实现配置加载函数：
   - 读取 `~/codepilot/config.json` 文件
   - 如文件不存在，在控制台提示用户创建并展示示例配置
   - 解析 JSON 到结构体，校验必填字段（Provider、Model、APIKey）
   - 未填写的可选字段使用默认值（Timeout=60, MaxRetries=2）
3. 实现配置校验：Provider 必须是 `"anthropic"` 或 `"openai"`，不合法时报错退出
4. 创建 `config/config.example.json` 示例文件（含两套供应商注释示例）

**参考资料**：
- Go 标准库 `os.UserHomeDir()` 获取用户主目录
- Go 标准库 `encoding/json` 做 JSON 解析
- Anthropic 默认地址：`https://api.anthropic.com`
- OpenAI 默认地址：`https://api.openai.com/v1`

---

## Task 3: LLM Provider 抽象接口与通用消息类型定义

**状态**：已完成

**目标**：定义 LLM 供应商的统一抽象接口和基于 ContentBlock 的通用消息类型，为多供应商适配和多模态扩展奠定基础。

**影响文件**：
- `llm/types.go` — 新建，通用消息类型（ContentBlock 体系）
- `llm/provider.go` — 新建，Provider 接口定义 + 工厂函数

**依赖**：Task 1

**具体内容**：
1. 在 `types.go` 中定义 ContentBlock 体系：
   - `ContentBlockType` 枚举：`ContentBlockTypeText`
   - `ContentBlock` 接口：`Type() ContentBlockType`、`ToText() string`
   - `TextBlock` 结构体：实现 `ContentBlock`，持有 `Text string`
   - `NewTextBlock(text string) ContentBlock` 工厂函数
   - 附带注释说明后续将扩展 ImageBlock、ToolUseBlock 等类型
2. 在 `types.go` 中定义通用消息与流式类型：
   - `Role` 枚举：`RoleUser`、`RoleAssistant`、`RoleSystem`
   - `Message` 结构体：`Role Role`、`Content []ContentBlock`（注意：是 ContentBlock 数组而非 string）
   - `StreamChunk` 结构体：`Content string`、`Done bool`、`Err error`（标识流结束或错误）
3. 在 `provider.go` 中定义 Provider 接口：
   - `StreamChat(ctx context.Context, systemPrompt string, messages []Message) (<-chan StreamChunk, error)`
   - 此接口返回一个只读 channel，TUI 层从 channel 消费 chunk 实现流式输出
   - 接口签名预留 `systemPrompt` 参数，为后续 Step 4 System Prompt 设计做准备
   - 通过 `ctx` 参数支持调用方取消（cancel）流式请求
4. 实现工厂函数 `NewProvider(cfg config.Config) (Provider, error)`：
   - 根据 `cfg.Provider` 字段返回对应的 Provider 实例
   - 不支持的供应商返回明确错误

**参考资料**：
- Go 接口设计最佳实践：小接口，行为定义
- Channel 用于流式数据传递的 Go 惯用模式
- Anthropic SDK 的 ContentBlock 模式：`anthropic.NewTextBlock("...")`
- OpenAI SDK 的 ContentPart 模式：后续映射到内部 TextBlock

---

## Task 4: Anthropic 适配器实现

**状态**：已完成

**目标**：基于 Anthropic Go SDK 实现 `Provider` 接口，支持与 Claude 模型的流式对话，含消息格式转换、超时与重试机制。

**影响文件**：
- `llm/anthropic.go` — 新建，Anthropic 适配器

**依赖**：Task 2, Task 3

**具体内容**：
1. 引入 Anthropic Go SDK：`go get github.com/anthropics/anthropic-sdk-go`
2. 定义 `AnthropicProvider` 结构体，持有 `*anthropic.Client` 实例、模型名称、超时和重试配置
3. 实现构造函数 `NewAnthropicProvider(cfg config.Config) *AnthropicProvider`：
   - 使用 `anthropic.NewClient(option.WithAPIKey(...))` 初始化客户端
   - 如果 `BaseURL` 非空，使用 `option.WithBaseURL(...)` 设置自定义地址
4. 实现内部消息格式转换函数 `convertMessages(messages []llm.Message) []anthropic.MessageParam`：
   - 遍历内部 `Message`，按 Role 转换为 `anthropic.NewUserMessage` / `anthropic.NewAssistantMessage`
   - 遍历 `Message.Content`（`[]ContentBlock`），将 `TextBlock` 转换为 `anthropic.NewTextBlock(...)`
   - System Prompt 单独通过 `MessageNewParams.System` 传入，不混入 messages 数组
   - 附带注释说明后续 ImageBlock 将映射为 `anthropic.NewImageBlock`
5. 实现 `StreamChat` 方法：
   - 使用 `context.WithTimeout` 包装传入的 ctx，超时时间取配置值
   - 将通用 `Message` 通过转换函数转为 Anthropic SDK 格式
   - 调用 SDK 的流式 API 发起请求
   - 启动 goroutine 从 SDK 流中读取事件，将 `ContentBlockDelta` 中的 `TextDelta` 转换为通用 `StreamChunk` 写入 channel
   - 流结束时发送 `Done: true` 的 chunk 并关闭 channel
   - ctx 取消时（用户 Esc 中断），立即终止流读取、发送 `Done: true` 并关闭 channel
6. 实现重试逻辑（封装在内部方法 `streamWithRetry` 中）：
   - 仅对网络错误（net.Error）和 HTTP 5xx 错误重试，最多 `MaxRetries` 次
   - 重试间隔采用指数退避：首次 1s，后续 2s、4s
   - HTTP 401（认证错误）和 429（限流）不重试，直接通过 channel 返回错误
   - 每次重试记录日志
7. 编写单元测试验证消息格式转换逻辑（不调用真实 API）

**参考资料**：
- Anthropic Go SDK：`github.com/anthropics/anthropic-sdk-go`
- 客户端初始化：`anthropic.NewClient(option.WithAPIKey("..."))`
- 消息构造：`anthropic.NewUserMessage(anthropic.NewTextBlock("..."))`
- 流式事件类型：`anthropic.ContentBlockDeltaEvent`，Delta 类型 `anthropic.TextDelta`
- Go 标准库 `context.WithTimeout` 设置超时
- Go 标准库 `errors.Is(err, context.DeadlineExceeded)` 判断超时

---

## Task 5: OpenAI 适配器实现

**状态**：已完成

**目标**：基于 OpenAI Go SDK 实现 `Provider` 接口，支持与 GPT 系列模型的流式对话，含消息格式转换、超时与重试机制。

**影响文件**：
- `llm/openai.go` — 新建，OpenAI 适配器

**依赖**：Task 2, Task 3

**具体内容**：
1. 引入 OpenAI Go SDK：`go get github.com/openai/openai-go`
2. 定义 `OpenAIProvider` 结构体，持有 `*openai.Client` 实例、模型名称、超时和重试配置
3. 实现构造函数 `NewOpenAIProvider(cfg config.Config) *OpenAIProvider`：
   - 使用 `openai.NewClient()` 初始化客户端，通过 option 注入 API Key
   - 如果 `BaseURL` 非空，通过 `option.WithBaseURL(...)` 设置自定义地址
4. 实现内部消息格式转换函数 `convertMessages(messages []llm.Message) []openai.ChatCompletionMessageParamUnion`：
   - 遍历内部 `Message`，按 Role 转换为 `openai.UserMessage` / `openai.AssistantMessage` / `openai.SystemMessage`
   - 当前 Content 仅有 TextBlock 时，直接传 string 给 `openai.UserMessage(text)`
   - 附带注释说明后续 ImageBlock 将映射为 `openai.ImagePart(...)`
5. 实现 `StreamChat` 方法：
   - 使用 `context.WithTimeout` 包装传入的 ctx
   - 使用 `client.Chat.Completions.NewStreaming(ctx, params)` 发起流式请求
   - 启动 goroutine 遍历 stream，将 `stream.Current().Choices[0].Delta.Content` 转换为通用 `StreamChunk` 写入 channel
   - 流结束时发送 `Done: true` 的 chunk 并关闭 channel
   - ctx 取消时立即终止流读取
6. 实现重试逻辑（与 Task 4 相同策略）：
   - 网络错误和 5xx 重试，指数退避，401/429 不重试
7. 编写单元测试验证消息格式转换逻辑

**参考资料**：
- OpenAI Go SDK：`github.com/openai/openai-go`
- 流式调用：`client.Chat.Completions.NewStreaming(ctx, openai.ChatCompletionNewParams{...})`
- 遍历流：`for stream.Next() { evt := stream.Current(); evt.Choices[0].Delta.Content }`
- 消息构造：`openai.UserMessage("...")`、`openai.SystemMessage("...")`、`openai.AssistantMessage("...")`

---

## Task 6: 上下文滑窗与对话管理

**状态**：已完成

**目标**：实现简单的滑动窗口上下文策略和对话历史管理，支持多轮对话的消息维护。

**影响文件**：
- `internal/memory/context/window.go` — 新建，滑动窗口策略
- `internal/engine/conversation/manager.go` — 新建，对话历史管理器

**依赖**：Task 3

**具体内容**：
1. 在 `window.go` 中实现 `SlidingWindow` 结构体：
   - 持有当前消息列表和窗口大小配置（最大保留轮数）
   - 提供 `AddMessage(msg llm.Message)` 方法：添加消息到列表
   - 提供 `GetMessages(systemPrompt string) []llm.Message` 方法：返回 [SystemPrompt消息, ...最近的N轮消息]
   - 策略：System Prompt 固定保留，剩余空间按消息对（User+Assistant）为单位，超出时丢弃最早的消息对
   - 预留 System Prompt 空间约 20%，剩余给对话历史
   - 附带注释说明此为简化策略，后续 Step 7 将实现高级上下文管理（摘要压缩等）
   - 注意：消息使用 `llm.Message{Content: []ContentBlock}` 类型，不是 string
2. 在 `manager.go` 中实现 `ConversationManager` 结构体：
   - 组合 `SlidingWindow` 实例
   - 提供 `AddUserMessage(content string)` 方法：构造 `Message{Role: RoleUser, Content: []ContentBlock{NewTextBlock(content)}}` 并添加到窗口
   - 提供 `AddAssistantMessage(content string)` 方法：同上，Role 为 RoleAssistant
   - 提供 `GetContext(systemPrompt string) []llm.Message` 方法：返回当前窗口内的完整消息列表
   - 提供 `TokenEstimate() int` 方法：基于消息内容的简单 token 估算（中文按 2 字符/token，英文按 4 字符/token 粗估）
   - 提供 `RemainingTokens(maxTokens int) int` 方法：返回剩余可用 token 数

**参考资料**：
- 滑动窗口策略：FIFO 队列，按消息对（一轮对话）为单位淘汰
- Token 估算参考：GPT-4 Tokenizer 约每 4 个英文字符 ≈ 1 token

---

## Task 7: 会话持久化

**状态**：已完成

**目标**：实现会话的本地持久化，支持退出后恢复上次对话。

**影响文件**：
- `internal/memory/session/session.go` — 新建，会话管理

**依赖**：Task 6

**具体内容**：
1. 定义会话数据结构 `Session`：
   - `ID`（string）：会话唯一标识（UUID）
   - `CreatedAt`（time.Time）：创建时间
   - `UpdatedAt`（time.Time）：最后更新时间
   - `Messages`（[]llm.Message）：对话消息列表（使用 ContentBlock 数组的 Message）
2. 实现 `SessionManager` 持久化存储：
   - 会话文件路径：`~/.codepilot/sessions/{session_id}.json`
   - `Save(session *Session) error`：将会话序列化为 JSON 写入文件
   - `Load(sessionID string) (*Session, error)`：从文件加载会话
   - `LoadLatest() (*Session, error)`：遍历 sessions 目录，按 UpdatedAt 排序加载最近的会话
   - `CreateNew() *Session`：创建新会话（生成 UUID）
3. JSON 序列化注意：
   - `llm.Message` 中 `Content []ContentBlock` 是接口类型，需自定义 JSON marshal/unmarshal（通过 `ContentBlockType` 鉴别具体类型反序列化）
   - 本步骤只有 TextBlock，反序列化时根据 type 字段创建 `TextBlock` 即可
4. 处理异常场景：
   - 会话文件损坏时：记录日志并创建新会话
   - 目录不存在时：自动创建 `~/.codepilot/sessions/` 目录

**参考资料**：
- Go 标准库 `os.UserHomeDir()` 获取主目录
- Go 标准库 `encoding/json` 做序列化/反序列化
- Go 标准库 `os.MkdirAll()` 创建嵌套目录
- Go 自定义 JSON 序列化：实现 `json.Marshaler` / `json.Unmarshaler` 接口

---

## Task 8: 日志系统

**状态**：已完成

**目标**：实现文件日志系统，为开发调试和问题排查提供基础，日志不展示在 TUI 界面中。

**影响文件**：
- `internal/logger/logger.go` — 新建，日志初始化与写入

**依赖**：Task 1

**具体内容**：
1. 引入日志库：`go get go.uber.org/zap`（高性能结构化日志）
2. 定义日志级别常量：Debug、Info、Warn、Error
3. 实现 `Init() error` 初始化函数：
   - 日志文件路径：`~/.codepilot/logs/codepilot.log`
   - 目录不存在时自动创建 `~/.codepilot/logs/`
   - 使用 zap 的文件输出模式（不输出到 stdout，避免干扰 TUI）
   - 日志格式：JSON 结构化，包含时间戳、级别、调用位置、消息
   - 日志轮转：单个文件最大 10MB，最多保留 5 个备份
4. 提供包级便捷函数：
   - `logger.Info(msg, fields...)`、`logger.Error(msg, fields...)`、`logger.Warn(msg, fields...)`、`logger.Debug(msg, fields...)`
   - `logger.Sync()` 刷新缓冲区（在程序退出前调用）
5. 提供全局 logger 实例，其他包通过 `logger.Info(...)` 直接调用

**参考资料**：
- Zap 日志库：`go.uber.org/zap`
- Zap 文件输出：`zapcore.NewCore(encoder, writer, level)`
- 日志轮转：`natefinch/lumberjack` 库配合 zap 使用

---

## Task 9: TUI 界面实现（Bubble Tea）

**状态**：已完成

**目标**：基于 Bubble Tea 实现完整的 TUI 界面，包含 Logo 展示、对话区域、输入框、底部状态栏，以及流式中断和优雅退出。

**影响文件**：
- `internal/interaction/tui/app.go` — 新建，Bubble Tea 主模型
- `internal/interaction/tui/logo.go` — 新建，猫头鹰 Logo ASCII Art
- `internal/interaction/tui/statusbar.go` — 新建，底部状态栏
- `internal/interaction/tui/message.go` — 新建，自定义消息类型

**依赖**：Task 6, Task 7, Task 8

**具体内容**：
1. 引入 Bubble Tea 及相关依赖：
   - `go get github.com/charmbracelet/bubbletea`
   - `go get github.com/charmbracelet/bubbles/textarea`（多行输入组件，支持 Shift+Enter 换行）
   - `go get github.com/charmbracelet/bubbles/viewport`（对话区域滚动）
   - `go get github.com/charmbracelet/glamour`（Markdown 渲染）
   - `go get github.com/charmbracelet/lipgloss`（样式）
2. 在 `logo.go` 中设计 ASCII 猫头鹰 Logo：
   - 设计一个字符版猫头鹰图案，附带 "CodePilot" 名称和版本号
   - 使用 Lip Gloss 添加颜色样式
3. 在 `message.go` 中定义 Bubble Tea 自定义消息类型：
   - `StreamChunkMsg`：携带流式 chunk 数据，触发 View 更新
   - `StreamDoneMsg`：流式响应结束信号
   - `StreamErrorMsg`：流式响应错误信号（携带 error）
4. 在 `statusbar.go` 中实现底部状态栏组件：
   - 展示当前模型名称（从配置读取）
   - 展示上下文窗口剩余 token 额度（从 ConversationManager 获取估算值）
   - 使用 Lip Gloss 添加样式
5. 在 `app.go` 中实现 Bubble Tea 主模型 `AppModel`：
   - 持有 Provider、ConversationManager、SessionManager、Config 等依赖引用
   - 持有 `cancelFunc context.CancelFunc` 用于中断当前流式请求
   - `Init()`：展示 Logo，尝试恢复上次会话，初始化 textarea 组件
   - `Update(msg)`：
     - 处理 `tea.KeyMsg`：
       - Enter 发送消息（textarea 非空时）
       - Esc 中断当前流式响应（调用 cancelFunc，已输出部分保留）
       - Ctrl+C 触发优雅退出（保存会话后退出）
     - 处理 `StreamChunkMsg`：将 chunk 追加到当前助手回复缓冲区，触发 View 重绘
     - 处理 `StreamDoneMsg`：将完整回复（ContentBlock 数组形式）加入对话历史，持久化会话，刷新状态栏 token 额度
     - 处理 `StreamErrorMsg`：在对话区域展示红色错误提示，用户可继续输入
   - `View()`：拼接 Logo + 对话区域（Viewport）+ 输入框 + 状态栏，渲染完整 UI

**参考资料**：
- Bubble Tea 核心接口：`Init() Cmd`、`Update(Msg) (Model, Cmd)`、`View() string`
- 程序创建：`tea.NewProgram(model, tea.WithAltScreen())`
- Bubbles 组件：`textarea.New()`, `viewport.New(width, height)`
- Glamour 渲染：`glamour.Render(markdown, "dark")`
- Bubble Tea 优雅退出：`tea.Quit` 命令

---

## Task 10: 接入主流程

**状态**：已完成

**目标**：将日志初始化、配置加载、Provider 初始化、对话管理、会话持久化和 TUI 界面串联到 `main.go`，形成完整的启动与退出链路。

**影响文件**：
- `cmd/codepilot/main.go` — 修改，替换 Hello World 为完整启动逻辑

**依赖**：Task 2, Task 3, Task 4, Task 5, Task 7, Task 8, Task 9

**具体内容**：
1. 在 `main()` 中实现启动流程（按顺序）：
   - 初始化日志系统：`logger.Init()`，失败时 fallback 到 stdout 日志并继续运行
   - 加载配置文件：`config.Load()`
   - 根据配置创建 Provider 实例：`llm.NewProvider(cfg)`
   - 创建 ConversationManager 实例
   - 创建 SessionManager，尝试恢复上次会话
   - 初始化 Bubble Tea 主模型，注入所有依赖
   - 使用 `defer logger.Sync()` 确保程序退出前刷新日志
   - 启动 Bubble Tea 程序：`tea.NewProgram(model, tea.WithAltScreen()).Run()`
2. 在 TUI 模型中实现用户消息发送流程：
   - 用户按 Enter 后，将输入文本通过 ConversationManager 构造为 User Message（TextBlock 形式）
   - 创建带 cancel 的 context，保存 cancelFunc 到 AppModel
   - 调用 Provider 的 `StreamChat(ctx, ...)` 方法，获取 chunk channel
   - 启动 goroutine 从 channel 读取 chunk，转为 `tea.Msg` 通过 `p.Send()` 发送给 Bubble Tea Update 循环
   - 流式响应结束后（正常完成或中断），通过 SessionManager 持久化对话历史
3. 实现优雅退出：
   - Ctrl+C 时先调用 SessionManager.Save() 保存当前会话
   - 调用 logger.Sync() 刷新日志
   - 然后执行 tea.Quit
4. 处理启动异常：
   - 日志初始化失败：记录到 stdout，继续运行（不阻塞启动）
   - 配置文件不存在或格式错误：友好提示后退出
   - Provider 初始化失败（如 API Key 无效格式）：提示后退出

**参考资料**：
- Bubble Tea 程序运行：`program.Run()`
- Bubble Tea 发送自定义消息：`program.Send(msg)`
- Go `defer` 用于确保清理逻辑执行

---

## Task 11: 端到端验证

**状态**：已完成（自动化部分全部通过，交互部分需手动验证）

**目标**：验证 Anthropic 和 OpenAI 两个供应商的完整对话链路，确保流式输出、中断、会话恢复、重试、日志等全部功能正常。

**影响文件**：
- 无新文件，验证已有功能

**依赖**：Task 10

**具体内容**：
1. 使用 Anthropic 配置启动，验证：
   - Logo 正常展示
   - 输入消息后流式响应逐字输出
   - Markdown 渲染正常（代码块、粗体、列表）
   - 多轮对话上下文保持（第 2 轮能引用第 1 轮内容）
   - 底部状态栏显示模型名和剩余 token
   - Esc 中断正在进行的响应，已输出部分保留
   - Ctrl+C 退出后重启，上次对话历史恢复
   - 日志文件 `~/.codepilot/logs/codepilot.log` 中有请求/响应记录
2. 切换为 OpenAI 配置，重复上述验证
3. 验证异常场景：
   - 配置文件不存在时的提示
   - API Key 错误时的错误提示（不崩溃，用户可继续输入）
   - 断网时的错误提示（不崩溃）
4. 对照 checklist.md 逐项验证并记录结果

**参考资料**：
- Anthropic 测试模型：`claude-sonnet-4-20250514`
- OpenAI 测试模型：`gpt-4o`

---

## Task 12: 多会话管理

**状态**：已完成（SessionManager 层已实现；TUI 命令解析与处理函数此前缺失，现已补全）

**目标**：实现多会话管理，支持创建新会话、列出历史会话、切换到指定会话恢复对话，用户通过简单的文本命令（`/sessions`、`/new`、`/resume <id>`）触发操作，类似 Claude Code 的 `/resume` 体验。

**影响文件**：
- `src/internal/memory/session/session.go` — 修改，新增 `ListSessions()`、`Delete()`、`SessionSummary` 结构体
- `src/internal/interaction/tui/app.go` — 修改，新增会话命令解析与切换逻辑
- `src/internal/interaction/tui/message.go` — 修改，新增会话列表展示相关的消息类型

**依赖**：Task 7（会话持久化）、Task 9（TUI 界面）

**具体内容**：

### 1. 扩展 SessionManager（session.go）

1. 新增 `SessionSummary` 结构体，用于会话列表展示（避免加载完整消息）：
   - `ID`（string）：会话 ID
   - `CreatedAt`（time.Time）：创建时间
   - `UpdatedAt`（time.Time）：最后更新时间
   - `MessageCount`（int）：消息数量
   - `Preview`（string）：首条用户消息的前 80 字符预览（无用户消息时显示 "(空会话)"）
2. 实现 `ListSessions() ([]SessionSummary, error)` 方法：
   - 遍历 sessions 目录下所有 JSON 文件
   - 对每个文件做轻量解析（仅读取 id、created_at、updated_at、messages 数量和首条用户消息），避免反序列化完整消息列表
   - 按 UpdatedAt 降序排列返回
   - 损坏的文件跳过并记录日志
3. 实现 `Delete(id string) error` 方法：
   - 删除指定 ID 的会话文件
   - 文件不存在时返回明确错误

### 2. 实现会话命令解析（app.go）

1. 在 `handleKeyMsg` 的 `enter` 分支中，发送消息前增加命令检测逻辑：
   - 输入 `/sessions` → 调用 `handleSessionsCommand()`，在对话区域展示会话列表
   - 输入 `/new` → 调用 `handleNewSessionCommand()`，保存当前会话，创建新会话，清空 TUI 对话窗口并重置状态
   - 输入 `/resume <id>` → 调用 `handleResumeCommand(id)`，切换到指定会话
   - 输入 `/` 开头但不匹配上述命令 → 显示"未知命令"提示
   - 非命令输入 → 走原有的消息发送逻辑
2. 注意：此处的命令解析是简化版内联实现，后续 Step 9（快捷命令系统）会将其重构为正式的命令框架

### 3. 实现会话列表展示（app.go）

1. 实现 `handleSessionsCommand()`：
   - 调用 `sessMgr.ListSessions()` 获取会话摘要列表
   - 格式化为 Markdown 表格展示在对话区域：
     ```
     **历史会话：**
     | # | ID (前8位) | 更新时间 | 消息数 | 预览 |
     |---|---|---|---|---|
     | 1 | a1b2c3d4 | 2026-05-31 14:30 | 12 | 帮我写一个排序算法... |
     | 2 | e5f6g7h8 | 2026-05-30 09:15 | 5 | 解释一下这段代码... |
     
     输入 `/resume <id>` 恢复指定会话，输入 `/new` 创建新会话
     ```
   - 列表为空时显示"暂无历史会话"
   - 标记当前所在会话（在 ID 前加 `*` 标记）

### 4. 实现会话切换逻辑（app.go）

1. 实现 `handleNewSessionCommand()`：
   - 持久化当前会话（如有）
   - 创建新会话：`sessMgr.CreateNew()`
   - 重新构造一个新的 `ConversationManager` 实例（与 `main.go` 中初始化方式一致，50 轮窗口），替换当前的 convMgr
   - 清空 `messages` 显示列表和 `streamingText`
   - 更新 viewport 展示欢迎信息（复用 `buildConversationContent()` 无消息时的欢迎文案）
   - 在对话区域提示"已创建新会话"
2. 实现 `handleResumeCommand(id string)`：
   - 支持前缀匹配：用户只需输入 ID 的前几位（至少 4 位），自动匹配唯一会话
   - 无匹配或多个匹配时给出明确提示
   - 持久化当前会话（如有）
   - 调用 `sessMgr.Load(id)` 加载目标会话
   - 重置 ConversationManager，将目标会话的消息重新注入 convMgr（复用 `NewAppModel` 中已有的恢复逻辑）
   - 清空并重建 `messages` 显示列表
   - 更新 viewport 展示恢复后的对话历史
   - 在对话区域底部提示"已恢复会话 {id}"
3. 切换过程中遇到加载错误时，显示错误提示但不影响当前会话（切换失败留在原会话）

**参考资料**：
- Claude Code `/resume` 交互模式：列出历史会话供用户选择恢复
- 现有 `SessionManager.LoadLatest()` 中的会话遍历和排序逻辑（`session.go:108-139`）
- 现有 `NewAppModel` 中的会话恢复逻辑（`app.go:134-151`），切换会话时复用相同的消息注入模式
- Go 标准库 `strings.HasPrefix` 用于 ID 前缀匹配
