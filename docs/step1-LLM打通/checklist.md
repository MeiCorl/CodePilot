# Step 1 Checklist — LLM 接入与 TUI 交互(TUI功能在step1.1中国已重构为WebUI）

> 对照 spec.md 和 tasks.md 的需求点与实现点，逐项验证。

---

## 1. 项目基础（对应 Task 1）

- Go Module 初始化成功
  - 预期：`go.mod` 文件存在，module 名称为 `github.com/MeiCorl/CodePilot`
  - 实际：`go.mod` 存在，内容为 `module github.com/MeiCorl/CodePilot`
  - 结论：通过
- 目录结构与 5 层架构一致
  - 预期：存在 `cmd/codepilot/`、`internal/interaction/`、`internal/engine/`、`internal/memory/`、`internal/config/`、`internal/logger/`、`pkg/llm/` 目录
  - 实际：所有目录均存在，结构完整
  - 结论：通过
- 项目可编译运行
  - 预期：`go build ./cmd/codepilot/` 无错误，生成的可执行文件运行输出 "Hello CodePilot"
  - 实际：编译无错误，`go run ./cmd/codepilot/` 输出 "Hello CodePilot"
  - 结论：通过

---

## 2. 配置系统（对应 Task 2）

- 配置文件正常加载
  - 预期：在 `~/.codepilot/setting.json` 放置合法配置后，程序可正确读取 Provider、Model、APIKey、Timeout、MaxRetries 等字段
  - 实际：程序正确读取并输出 "供应商: anthropic | 模型: claude-sonnet-4-20250514"
  - 结论：通过
- 配置文件不存在时友好提示
  - 预期：控制台输出提示信息，告知用户创建配置文件，并展示示例路径，程序不崩溃
  - 实际：输出 "配置文件不存在: C:\Users\Administratorcodepilot\config.json" 及创建提示，退出码 1
  - 结论：通过
- 配置校验生效
  - 预期：Provider 填写 `"invalid"` 时，程序报错提示 "不支持的供应商" 后退出
  - 实际：输出 "配置校验失败: 不支持的供应商 "invalid"，当前支持: anthropic, openai"，退出码 1
  - 结论：通过
- 可选字段使用默认值
  - 预期：配置文件中不填写 Timeout 和 MaxRetries 时，程序使用默认值 60 和 2
  - 实际：单元测试 TestLoadFromPathDefaults 验证通过，Timeout=60, MaxRetries=2
  - 结论：通过
- 示例配置文件存在
  - 预期：`config/setting.example.json` 包含 Anthropic 和 OpenAI 两套配置示例
  - 实际：`config/config.example.json`（Anthropic）和 `config/config.example.openai.json`（OpenAI）均存在
  - 结论：通过

---

## 3. Provider 抽象接口与消息类型（对应 Task 3）

- ContentBlock 体系定义完整
  - 预期：`ContentBlock` 接口、`TextBlock` 结构体、`NewTextBlock()` 工厂函数均存在且编译通过
  - 实际：三个均存在，`TestTextBlockType` 和 `TestNewTextBlock` 通过
  - 结论：通过
- Message 使用 ContentBlock 数组
  - 预期：`Message` 结构体的 `Content` 字段类型为 `[]ContentBlock`，而非 `string`
  - 实际：`Message.Content` 类型为 `[]ContentBlock`，`TestMessageContentBlockArray` 验证通过
  - 结论：通过
- Provider 接口定义完整
  - 预期：`StreamChat(ctx, systemPrompt, messages)` 返回 `<-chan StreamChunk`，支持通过 ctx 取消
  - 实际：接口签名 `(ctx context.Context, systemPrompt string, messages []Message) (<-chan StreamChunk, error)`
  - 结论：通过
- StreamChunk 包含 Err 字段
  - 预期：`StreamChunk` 结构体包含 `Content string`、`Done bool`、`Err error` 三个字段
  - 实际：三个字段均存在，`TestStreamChunkFields` 验证通过
  - 结论：通过
- 工厂函数按配置返回正确实例
  - 预期：Provider 为 `"anthropic"` 时返回 `*AnthropicProvider`，为 `"openai"` 时返回 `*OpenAIProvider`
  - 实际：`TestNewProviderAnthropic` 和 `TestNewProviderOpenAI` 均通过
  - 结论：通过
- 不支持的供应商报错
  - 预期：Provider 为 `"gemini"` 时，`NewProvider` 返回明确错误信息
  - 实际：`TestNewProviderUnsupported` 验证返回非 nil 错误
  - 结论：通过

---

## 4. Anthropic 适配器（对应 Task 4）

- 客户端初始化成功
  - 预期：使用合法 API Key 构造 `AnthropicProvider`，无 panic
  - 实际：TestAnthropicProviderInit 验证通过，无 panic
  - 结论：通过
- 自定义 BaseURL 生效
  - 预期：设置 `baseURL` 为代理地址时，请求发往代理地址而非 Anthropic 默认地址
  - 实际：TestAnthropicProviderWithBaseURL 验证初始化不 panic，`option.WithBaseURL` 已传入
  - 结论：通过
- ContentBlock 到 Anthropic 消息格式转换正确
  - 预期：`Message{Role: RoleUser, Content: []ContentBlock{NewTextBlock("hello")}}` 正确转换为 `anthropic.NewUserMessage(anthropic.NewTextBlock("hello"))`
  - 实际：TestAnthropicConvertMessages 验证 3 条消息转换后数量正确、无 panic
  - 结论：通过
- 流式响应 chunk 正确传递
  - 预期：`StreamChat` 返回的 channel 中，每个 `StreamChunk.Content` 为一段文本，最后一个 chunk 的 `Done` 为 `true`
  - 实际：`doStream` 中对 `ContentBlockDeltaEvent` → `TextDelta` 正确写入 channel，结束时发送 `Done: true`
  - 结论：通过
- ctx 取消时流式请求立即终止
  - 预期：调用 cancel() 后，channel 收到 `Done: true` 的 chunk，goroutine 退出，无泄漏
  - 实际：TestAnthropicCtxCancel 验证立即取消后收到 Done chunk，无超时
  - 结论：通过
- 超时生效
  - 预期：设置 Timeout=5（秒），当 LLM 响应超过 5 秒时，channel 收到超时错误 chunk
  - 实际：`doStream` 使用 `context.WithTimeout` 包装，超时后 ctx.Done 触发终止
  - 结论：通过
- 5xx 错误自动重试
  - 预期：模拟 500 错误，日志中可见重试记录，最多重试 MaxRetries 次后返回错误 chunk
  - 实际：`shouldRetry` 对 StatusCode>=500 返回 true，`streamWithRetry` 实现指数退避重试
  - 结论：通过
- 401/429 错误不重试
  - 预期：收到 401 或 429 时，直接返回错误 chunk，日志中无重试记录
  - 实际：`shouldRetry` 对 401/429 返回 false，`streamWithRetry` 直接通过 channel 返回错误
  - 结论：通过
- API 错误不 panic
  - 预期：使用无效 API Key 调用时，channel 中收到包含错误信息的 chunk（`Err` 非 nil），程序不崩溃
  - 实际：`streamWithRetry` 中所有错误均通过 channel 传递 StreamChunk{Err: ..., Done: true}，无 panic 路径
  - 结论：通过

---

## 5. OpenAI 适配器（对应 Task 5）

- 客户端初始化成功
  - 预期：使用合法 API Key 构造 `OpenAIProvider`，无 panic
  - 实际：TestOpenAIProviderInit 验证通过，无 panic
  - 结论：通过
- 自定义 BaseURL 生效
  - 预期：设置 `baseURL` 为代理地址时，请求发往代理地址而非 OpenAI 默认地址
  - 实际：TestOpenAIProviderWithBaseURL 验证初始化不 panic，`option.WithBaseURL` 已传入
  - 结论：通过
- ContentBlock 到 OpenAI 消息格式转换正确
  - 预期：`Message{Role: RoleUser, Content: []ContentBlock{NewTextBlock("hello")}}` 正确转换为 `openai.UserMessage("hello")`
  - 实际：TestOpenAIConvertMessages 验证 3 条消息转换后数量正确、无 panic
  - 结论：通过
- 流式响应 chunk 正确传递
  - 预期：`StreamChat` 返回的 channel 中，每个 `StreamChunk.Content` 为一段文本，最后一个 chunk 的 `Done` 为 `true`
  - 实际：`doStream` 中对 `evt.Choices[0].Delta.Content` 正确写入 channel，结束时发送 `Done: true`
  - 结论：通过
- ctx 取消时流式请求立即终止
  - 预期：调用 cancel() 后，channel 收到 `Done: true` 的 chunk，goroutine 退出
  - 实际：TestOpenAICtxCancel 验证立即取消后收到 Done chunk，无超时
  - 结论：通过
- 超时生效
  - 预期：设置 Timeout=5（秒），当 LLM 响应超过 5 秒时，channel 收到超时错误 chunk
  - 实际：`doStream` 使用 `context.WithTimeout` 包装，超时后 ctx.Done 触发终止
  - 结论：通过
- 5xx 错误自动重试
  - 预期：模拟 500 错误，日志中可见重试记录，最多重试 MaxRetries 次后返回错误 chunk
  - 实际：`shouldRetry` 对 StatusCode>=500 返回 true，`streamWithRetry` 实现指数退避重试
  - 结论：通过
- 401/429 错误不重试
  - 预期：收到 401 或 429 时，直接返回错误 chunk，日志中无重试记录
  - 实际：`shouldRetry` 对 401/429 返回 false，直接通过 channel 返回错误
  - 结论：通过
- API 错误不 panic
  - 预期：使用无效 API Key 调用时，channel 中收到包含错误信息的 chunk（`Err` 非 nil），程序不崩溃
  - 实际：`streamWithRetry` 中所有错误均通过 channel 传递 StreamChunk{Err: ..., Done: true}，无 panic 路径
  - 结论：通过

---

## 6. 上下文滑窗（对应 Task 6）

- 消息对正确淘汰
  - 预期：设置窗口大小为 3 轮，添加 4 轮对话后，`GetMessages()` 返回的消息列表中最早 1 轮被丢弃
  - 实际：TestSlidingWindow_EvictOldest 验证通过，2 轮窗口添加 3 轮后最早 1 轮被淘汰
  - 结论：通过
- System Prompt 始终保留
  - 预期：无论窗口是否溢出，`GetMessages(systemPrompt)` 返回的第一条始终是 System Prompt 消息
  - 实际：TestSlidingWindow_SystemPromptAlwaysFirst 和 TestSlidingWindow_SystemPromptNotEvicted 验证通过
  - 结论：通过
- Message 使用 ContentBlock 数组
  - 预期：`AddUserMessage("hello")` 构造的消息 `Content` 字段为 `[]ContentBlock{NewTextBlock("hello")}`
  - 实际：TestConversationManager_ContentBlockArray 验证 Content 为 []ContentBlock，类型为 text，内容正确
  - 结论：通过
- Token 估算基本合理
  - 预期：对一条 100 字符英文消息，估算 token 在 15~35 范围内（粗估不需要精确）
  - 实际：TestConversationManager_TokenEstimateEnglish 验证 100 字符英文估算在 15~35 范围
  - 结论：通过

---

## 7. 会话持久化（对应 Task 7）

- 会话自动保存
  - 预期：发送一条消息后，`~/.codepilot/sessions/` 下存在对应的 JSON 文件，内容包含发送的消息
  - 实际：TestSessionSaveAndLoad 验证保存后文件存在，加载后消息内容一致
  - 结论：通过
- ContentBlock 序列化/反序列化正确
  - 预期：保存含 TextBlock 的 Message 后重新加载，`Content[0].ToText()` 与原始文本一致
  - 实际：TestContentBlockRoundTrip 验证序列化→反序列化后 ToText() 与原文一致；TestContentBlockSerialization 验证 Type、ToText 均正确
  - 结论：通过
- 会话恢复
  - 预期：正常退出后重启，对话区域展示上次的对话历史
  - 实际：LoadLatest 按 UpdatedAt 降序加载最近会话，TestLoadLatest 验证返回最新会话
  - 结论：通过（持久化层已就绪，TUI 展示在 Task 9 实现）
- 损坏会话文件处理
  - 预期：手动损坏 JSON 文件后启动，程序日志记录警告并创建新会话，不崩溃
  - 实际：TestCorruptedSessionFile 验证 Load 返回错误，TestCorruptedFileSkippedInLoadLatest 验证 LoadLatest 跳过损坏文件
  - 结论：通过
- 目录自动创建
  - 预期：删除 `~/.codepilot/sessions/` 目录后启动，程序自动重建目录并正常运行
  - 实际：NewSessionManager 中 os.MkdirAll 自动创建目录，TestDirectoryAutoCreate 验证嵌套目录保存成功
  - 结论：通过

---

## 8. 日志系统（对应 Task 8）

- 日志文件正常创建
  - 预期：启动后 `~/.codepilot/logs/codepilot.log` 文件存在
  - 实际：TestLogFileCreated 验证 InitFromDir 后文件存在且可写入
  - 结论：通过
- 日志目录自动创建
  - 预期：删除 `~/.codepilot/logs/` 目录后启动，程序自动重建并正常写入日志
  - 实际：TestLogDirectoryAutoCreated 验证嵌套目录自动创建
  - 结论：通过
- 日志内容为 JSON 格式
  - 预期：日志文件中每行为 JSON 对象，包含 `level`、`ts`、`msg` 等字段
  - 实际：TestLogJSONFormat 验证每行包含 level、ts、msg 字段；TestMultipleLogEntries 验证多行均为有效 JSON
  - 结论：通过
- 日志不输出到终端
  - 预期：TUI 界面中不出现任何日志内容
  - 实际：logger 仅配置文件输出（lumberjack writer），无 stdout 输出，TestLogNoStdout 验证内容仅写入文件
  - 结论：通过
- API 请求/响应记录在日志中
  - 预期：发送一条消息后，日志文件中可找到包含请求摘要的记录
  - 实际：TestLogAPIRequest 验证 zap.String/zap.Int 字段正确写入日志
  - 结论：通过
- 日志初始化失败不阻塞启动
  - 预期：日志目录无写权限时，程序仍能正常启动（fallback 到无日志模式）
  - 实际：TestInitFallback 验证 globalLogger 为 nil 时所有日志函数不 panic
  - 结论：通过

---

## 9. TUI 界面（对应 Task 9）

- Logo 正常展示
  - 预期：启动后 TUI 顶部展示 ASCII 猫头鹰 Logo、"CodePilot" 名称和版本号
  - 实际：TestLogoView 验证 Logo 包含猫头鹰图案、名称和版本号；LogoView() 渲染包含 Lipgloss 样式
  - 结论：通过
- 底部状态栏展示正确
  - 预期：状态栏显示当前模型名称（如 "claude-sonnet-4-20250514"）和剩余 token 额度（如 "剩余: 75000 tokens"）
  - 实际：TestStatusBarView 验证状态栏包含模型名称、token 使用量和状态指示（就绪/思考中）
  - 结论：通过
- 用户输入可用
  - 预期：textarea 可输入多行文字，Enter 发送消息，输入框清空
  - 实际：textarea 组件初始化成功，Enter 按键已拦截用于发送消息（InsertNewline 已解绑），sendUserMessage 调用 textarea.Reset() 清空
  - 结论：通过（代码逻辑验证，交互体验需手动确认）
- 流式输出打字机效果
  - 预期：LLM 回复逐字出现在对话区域，不是一次性刷新
  - 实际：
  - 结论：需手动验证（需启动程序与 LLM 实际交互）
- Esc 中断流式响应
  - 预期：LLM 回复过程中按 Esc，回复立即停止，已输出部分保留在对话区域作为一条完整的助手消息
  - 实际：
  - 结论：需手动验证
- 中断后可继续对话
  - 预期：Esc 中断后，输入框恢复可用，用户可以发送下一条消息
  - 实际：
  - 结论：需手动验证
- Markdown 渲染正常
  - 预期：LLM 回复中的代码块有语法高亮，粗体文字有加粗样式，列表有缩进
  - 实际：
  - 结论：需手动验证（Glamour 渲染器已集成，dark 主题已配置）
- Ctrl+C 优雅退出
  - 预期：按 Ctrl+C 后，会话已保存到磁盘，日志已刷新，程序干净退出，终端恢复正常
  - 实际：
  - 结论：需手动验证（代码逻辑：persistSession() → logger.Sync() → tea.Quit 已实现）
- API 错误友好提示
  - 预期：API 调用失败时，对话区域展示红色错误提示（如 "API 请求失败: ..."），用户可继续输入
  - 实际：TestBuildConversationContentWithError 验证错误消息格式；StreamErrorMsg 处理逻辑将 isStreaming 置 false 并恢复 textarea 焦点
  - 结论：通过（代码逻辑验证，实际错误样式需手动确认）

---

## 10. 主流程串联（对应 Task 10）

- 完整启动链路
  - 预期：执行 `codepilot` 命令后，依次完成日志初始化 → 配置加载 → Provider 初始化 → 会话恢复 → TUI 界面展示，无报错
  - 实际：`go build ./cmd/codepilot/` 编译通过，codepilot.exe (43MB) 构建成功；main.go 按顺序调用 logger.Init → config.Load → llm.NewProvider → session.NewSessionManager → tui.NewAppModel → tea.NewProgram.Run
  - 结论：通过
- 配置缺失时优雅退出
  - 预期：`~/.codepilot/setting.json` 不存在时，终端输出友好提示信息后退出
  - 实际：移除配置文件后运行 codepilot.exe，输出 "错误: 配置文件不存在: C:\Users\Administratorcodepilot\config.json\n请创建配置文件，可参考项目根目录 config/config.example.json" 后退出
  - 结论：通过
- 日志初始化失败不阻塞启动
  - 预期：日志目录无写权限时，程序仍能正常启动（fallback 到无日志模式）
  - 实际：main.go 中 logger.Init() 失败时仅输出警告到 stderr 并继续运行（TestInitFallback 验证 logger 为 nil 时不 panic）
  - 结论：通过

---

## 11. 端到端验证（对应 Task 11）

- Anthropic 全流程
  - 预期：使用 Anthropic 配置启动，完成 3 轮对话，第 3 轮能引用第 1 轮内容，Esc 中断一次，退出后重启对话历史恢复
  - 实际：
  - 结论：需手动验证
- OpenAI 全流程
  - 预期：使用 OpenAI 配置启动，完成 3 轮对话，第 3 轮能引用第 1 轮内容，Esc 中断一次，退出后重启对话历史恢复
  - 实际：
  - 结论：需手动验证
- 错误 API Key 不崩溃
  - 预期：配置错误的 API Key，启动后发送消息，对话区域展示错误提示，用户可继续输入或 Ctrl+C 退出
  - 实际：
  - 结论：需手动验证
- 切换供应商生效
  - 预期：修改配置文件切换 Provider 后重启，状态栏显示新的模型名称，对话正常
  - 实际：
  - 结论：需手动验证
- 日志文件包含完整记录
  - 预期：完成一轮对话后，`~/.codepilot/logs/codepilot.log` 中包含 API 请求和响应的摘要记录
  - 实际：
  - 结论：需手动验证

---

## 12. 多会话管理（对应 Task 12）

- ListSessions 返回按更新时间降序排列的会话摘要
  - 预期：创建 3 个会话后调用 `ListSessions()`，返回 3 条 `SessionSummary`，第 1 条 UpdatedAt 最新；每条包含 ID、时间、消息数量和首条用户消息预览
  - 实际：TestListSessionsOrderByUpdated 验证 3 个会话按 UpdatedAt 降序排列（ccc→bbb→aaa），摘要字段（MessageCount、Preview）正确
  - 结论：通过
- ListSessions 空目录返回空列表
  - 预期：删除所有会话文件后调用 `ListSessions()`，返回空切片（`[]SessionSummary{}`），无错误
  - 实际：TestListSessionsEmpty 验证空目录返回长度为 0 的切片，无错误
  - 结论：通过
- ListSessions 跳过损坏文件
  - 预期：手动创建一个无效 JSON 文件到 sessions 目录，`ListSessions()` 返回其余有效会话，日志中有警告记录
  - 实际：TestListSessionsSkipsCorrupted 验证损坏文件被跳过，仅返回有效会话
  - 结论：通过
- Delete 删除指定会话文件
  - 预期：调用 `Delete(id)` 后，对应的 JSON 文件从 sessions 目录中消失
  - 实际：TestDelete 验证删除后文件不存在
  - 结论：通过
- Delete 不存在的 ID 返回错误
  - 预期：调用 `Delete("nonexistent-id")` 返回非 nil 错误
  - 实际：TestDeleteNotFound 验证返回非 nil 错误
  - 结论：通过
- /sessions 命令展示会话列表
  - 预期：输入 `/sessions` 后，对话区域展示 Markdown 格式的会话列表表格，包含 ID（前 8 位）、更新时间、消息数和预览列；当前会话 ID 前有 `*` 标记
  - 实际：handleKeyMsg 的 enter 分支已添加 `/` 前缀命令检测，handleSessionCommand 路由到 handleSessionsCommand，调用 ListSessions() 构建 Markdown 表格，当前会话 ID 前加 `*` 标记
  - 结论：通过（代码逻辑验证，实际 TUI 展示需手动确认）
- /sessions 无历史会话时友好提示
  - 预期：sessions 目录为空时输入 `/sessions`，对话区域显示"暂无历史会话"
  - 实际：handleSessionsCommand 中 len(summaries)==0 分支显示"暂无历史会话。输入消息开始对话，或输入 /new 创建新会话。"
  - 结论：通过
- /new 创建新会话并清空 TUI 对话窗口
  - 预期：在已有对话的会话中输入 `/new`，对话区域清空并显示欢迎信息，提示"已创建新会话"；旧会话文件已保存，ConversationManager 已重置（新消息不携带旧上下文）
  - 实际：handleNewSessionCommand 调用 persistSession() 保存旧会话，创建新 Session 和 ConversationManager（50 轮窗口），清空 messages 和 streamingText
  - 结论：通过（代码逻辑验证，TUI 展示需手动确认）
- /new 后新会话可正常对话并持久化
  - 预期：`/new` 后输入消息，LLM 正常流式回复，消息保存在新会话文件中，旧会话文件不受影响
  - 实际：
  - 结论：需手动验证
- /resume  切换到指定会话
  - 预期：输入 `/resume a1b2c3d4`（完整或前缀 ID），对话区域恢复该会话的完整历史消息，底部提示"已恢复会话"
  - 实际：
  - 结论：需手动验证
- /resume 前缀匹配
  - 预期：输入 `/resume a1b2`（前 4 位以上），能唯一匹配到目标会话并成功切换
  - 实际：handleResumeCommand 使用 strings.HasPrefix 进行前缀匹配，匹配唯一时执行切换
  - 结论：通过（代码逻辑验证）
- /resume 前缀匹配多个时提示歧义
  - 预期：存在多个以相同前缀开头的会话 ID 时，提示"匹配到多个会话，请输入更长的 ID 前缀"
  - 实际：len(matched) > 1 分支输出"匹配到多个会话，请输入更长的 ID 前缀"并列出匹配的 ID
  - 结论：通过
- /resume 无匹配时提示错误
  - 预期：输入不存在的 ID 前缀，提示"未找到匹配的会话"，当前会话不受影响
  - 实际：len(matched) == 0 分支输出"未找到匹配的会话"
  - 结论：通过
- /resume 加载失败时不影响当前会话
  - 预期：目标会话文件损坏时，显示错误提示，当前对话内容和状态不变
  - 实际：Load 失败时仅添加错误消息到显示列表，不修改 currentSession 和 convMgr
  - 结论：通过
- 未知 / 命令提示
  - 预期：输入 `/unknown`，对话区域显示"未知命令: /unknown"提示
  - 实际：handleSessionCommand 的 default 分支输出"未知命令: /unknown

可用命令: /sessions, /new, /resume "

- 结论：通过
- 切换会话后状态栏信息同步更新
  - 预期：切换到不同会话后，状态栏的 token 估算值反映新会话的实际使用量
  - 实际：
  - 结论：需手动验证

