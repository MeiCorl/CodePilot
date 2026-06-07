# Step 2 — 工具系统集成：实现任务拆分

> 任务状态：`待完成` / `进行中` / `已完成`
> 实现原则：按依赖顺序串行；每完成一项立即更新状态；每完成一个子任务等用户确认再开始下一个

---

## Task 1: 工具核心抽象 + ContentBlock 扩展

**状态**：已完成

**目标**：建立工具系统的基石——`Tool` 接口、全局 `Registry`、`ToolPermission` 枚举、参数校验框架，并扩展 step1 留下的 `ContentBlock` 扩展位为 `ToolUseBlock`/`ToolResultBlock`，使 LLM 消息流可承载工具调用与结果。

**影响文件**：
- `src/tool/tool.go` — 新建，Tool 接口、ToolInfo、ToolPermission 枚举、参数校验辅助函数
- `src/tool/registry.go` — 新建，Registry（线程安全的 map + 全局默认实例 + Register/Get/List 函数）
- `src/llm/types.go` — 修改，扩展 `ContentBlockType` 枚举，新增 `ToolUseBlock`/`ToolResultBlock` 结构体
- `src/llm/types_json.go` — 修改，新增上述类型的 JSON 序列化

**依赖**：无

**具体内容**：
1. 在 `src/tool/tool.go` 定义：
   - `type Tool interface { Name() string; Description() string; InputSchema() json.RawMessage; Permission() ToolPermission; Execute(ctx context.Context, input json.RawMessage) (output string, err error) }`
   - `type ToolPermission int` 枚举：`PermRead`（只读）、`PermWrite`（写文件）、`PermExec`（执行命令）
   - 提供 `BaseTool` 嵌入结构，封装 Name/Description/Schema/Permission 公共字段
2. 在 `src/tool/registry.go` 定义：
   - `var defaultRegistry = NewRegistry()`
   - `type Registry struct { mu sync.RWMutex; tools map[string]Tool }`
   - 方法：`Register(tool Tool) error`（Name 重复返回错误）、`Get(name string) (Tool, bool)`、`List() []Tool`、`EnabledNames(cfg *config.Config) []string`（按配置过滤）
3. 在 `src/llm/types.go`：
   - `ContentBlockType` 新增 `ContentBlockTypeToolUse = "tool_use"`、`ContentBlockTypeToolResult = "tool_result"`
   - 新增 `ToolUseBlock{ ID, Name string; Input json.RawMessage }` 实现 `ContentBlock` 接口
   - 新增 `ToolResultBlock{ ToolUseID string; Content string; IsError bool }` 实现 `ContentBlock` 接口
4. 在 `src/llm/types_json.go`：补充 `ToolUseBlock`/`ToolResultBlock` 的 MarshalJSON/UnmarshalJSON（与 step1 现有 TextBlock 风格保持一致，使用 `type` 字段区分）
5. 写 `src/tool/registry_test.go`：覆盖 Register/Get/List/重复 Name 报错等基本场景

**参考资料**：
- Anthropic SDK `ToolUseBlock` 字段：见 [Anthropic API 文档](https://docs.anthropic.com/en/docs/build-with-claude/tool-use)
- OpenAI function calling 协议：见 [OpenAI Function Calling 文档](https://platform.openai.com/docs/guides/function-calling)
- step1 已有的 `ContentBlock` 体系：参考 [src/llm/types.go](../../src/llm/types.go) 的 `TextBlock` 实现风格

---

## Task 2: 5 个基础工具实现 + 集中注册入口

**状态**：已完成

**目标**：实现 ReadFile/WriteFile/Bash/Glob/Grep 5 个内置工具，所有工具实现统一 Tool 接口；通过 `register.go` 的 init() 函数集中注册到默认 Registry。

**影响文件**：
- `src/tool/builtin/read_file.go` — 新建，ReadFile 工具
- `src/tool/builtin/write_file.go` — 新建，WriteFile 工具
- `src/tool/builtin/bash.go` — 新建，Bash 工具
- `src/tool/builtin/glob.go` — 新建，Glob 工具
- `src/tool/builtin/grep.go` — 新建，Grep 工具
- `src/tool/builtin/register.go` — 新建，init() 集中注册
- `src/tool/builtin/read_file_test.go` 等 — 新建，5 个工具的 happy path 单元测试

**依赖**：Task 1

**具体内容**：
1. **ReadFile 工具**：
   - 输入参数：`file_path` (string, 必填)、`offset` (int, 可选, 默认 0)、`limit` (int, 可选, 默认 2000 行)
   - 输出：行号 + 内容（格式 `L<行号>: <内容>`），文末附 "（共 N 行，本次返回 M 行 [offset=X, limit=Y]）"
   - 异常处理：文件不存在返回明确错误；非文本文件（用 `http.DetectContentType` 判定 binary）返回错误；无权限返回错误
   - 路径必须先经 `safety.ResolveInSandbox` 校验（Task 3）
2. **WriteFile 工具**：
   - 输入参数：`file_path` (string, 必填)、`content` (string, 必填)
   - 行为：覆盖写入（不存在则创建；目录不存在则 `os.MkdirAll`）
   - 输出：成功写入字节数与文件绝对路径
   - 路径沙箱校验
3. **Bash 工具**：
   - 输入参数：`command` (string, 必填)、`timeout` (int, 可选, 覆盖全局默认)
   - 行为：通过 `os/exec` + `context.WithTimeout` 执行（**不**用 `sh -c`，exec.Command 直接传命令与参数，shell 元字符不会被解释；如确实需要 shell 能力再走 sh -c 但必须先过黑名单）
   - 输出：stdout + stderr + exit code（如有 stderr 或非零 exit code 则 `IsError=true`）
   - 命令字符串先经 `safety.CheckBashCommand` 黑名单拦截（Task 3）
4. **Glob 工具**：
   - 输入参数：`pattern` (string, 必填)、`path` (string, 可选, 默认 cwd)
   - 行为：使用 `filepath.Glob` 或 `doublestar` 库（评估后选）支持 `**` 递归
   - 输出：匹配到的文件绝对路径列表（限制最多 100 条，超出截断并提示）
   - 基准路径与结果路径均需沙箱校验
5. **Grep 工具**：
   - 输入参数：`pattern` (string, 必填)、`path` (string, 可选, 默认 cwd)、`include` (string, 可选, 文件 glob 过滤)
   - 行为：使用 Go 标准库 `regexp` 或 ripgrep 子进程（评估后选）
   - 输出：每条匹配一行，格式 `文件路径:L<行号>:<内容>`，最多返回 100 条
   - 路径沙箱校验
6. **`register.go`**：`func init() { registry := tool.DefaultRegistry(); registry.MustRegister(ReadFileTool{}, WriteFileTool{}, BashTool{}, GlobTool{}, GrepTool{}) }`
7. 单元测试：每个工具至少 1 个 happy path（正常输入有预期输出）+ 1 个 error case（参数错误/路径越界/命令非法）

**参考资料**：
- Anthropic SDK 的 `Tool` schema 参考：见 context7 查询 `anthropic-sdk-go` 的 tool_use 示例
- 路径 resolve：`filepath.Abs` + `filepath.Clean`，参考 Go 标准库
- glob 库选型：评估 `github.com/bmatcuk/doublestar/v4`（轻量、纯 Go、支持 `**`）
- ripgrep 子进程 vs Go regexp：优先 Go regexp（避免外部依赖）

---

## Task 3: 安全兜底 - 路径沙箱 + Bash 危险命令黑名单

**状态**：已完成

**目标**：实现工具层最小化安全兜底：(1) 路径沙箱——所有路径工具必须 resolve 为绝对路径后落在配置的 working_directory 内；(2) Bash 危险命令黑名单——拦截明显的破坏性命令。两道兜底**不可被工具配置关闭**。

**影响文件**：
- `src/tool/safety/path.go` — 新建，路径 resolve + 范围校验
- `src/tool/safety/bash_blacklist.go` — 新建，Bash 危险命令黑名单
- `src/tool/safety/path_test.go` — 新建，路径校验单测
- `src/tool/safety/bash_blacklist_test.go` — 新建，黑名单单测

**依赖**：Task 2（在工具里被调用；本任务先实现安全包供 Task 2 集成）

**具体内容**：
1. **路径沙箱**：
   - `func ResolveInSandbox(path, sandboxDir string) (string, error)`
   - 行为：
     a. 若是相对路径，相对于 sandboxDir resolve
     b. 调用 `filepath.Abs` + `filepath.Clean` 得到规范绝对路径
     c. 校验规范化后路径必须以 sandboxDir 开头（注意 Windows 路径分隔符与大小写不敏感）
     d. 不通过则返回 `ErrPathOutsideSandbox` 明确错误
   - 单测覆盖：`./file` 解析为 sandbox 内 ✓、`../escape` 被拒 ✓、绝对路径在 sandbox 内 ✓、绝对路径在 sandbox 外被拒 ✓、symlink 指向 sandbox 外被拒（用 `filepath.EvalSymlinks`）
2. **Bash 黑名单**：
   - `func CheckBashCommand(cmd string) error`
   - 黑名单规则（用正则/字符串匹配，覆盖以下模式）：
     - `rm\s+(-[a-zA-Z]*[rRfF][a-zA-Z]*\s+)*(/|/\*|~|\$HOME)` （递归删根目录/家目录）
     - `mkfs`、`shutdown`、`reboot`、`halt`、`poweroff`、`init\s+[0-6]`
     - `dd\s+.*of=/dev/`（任意设备写入）
     - `:\(\)\{\s*:\|:&\s*\};:` （fork bomb）
     - `chmod\s+(-[a-zA-Z]*\s+)*777\s+/` （根目录全开权限）
     - `>\s*/dev/sd[a-z]` （直接写磁盘设备）
   - 不通过则返回 `ErrDangerousCommand` 明确错误
   - 单测覆盖：每条规则至少 1 个命中用例 + 2-3 个正常命令（`ls`、`cat`、`go build`）放行

**参考资料**：
- 路径安全参考：OWASP Path Traversal
- Symlink 处理：`os.Lstat` + `filepath.EvalSymlinks`，注意 EvalSymlinks 对不存在的路径会失败，需要在 resolve 前先确认存在或允许文件尚不存在的场景（WriteFile 创建文件时路径不存在）

---

## Task 4: Provider 接口扩展 + Anthropic 协议 tools 适配

**状态**：已完成

**目标**：扩展 `StreamChat` 接口增加 `tools []Tool` 参数；Anthropic 适配器实现 tools 数组转换与 tool_use/tool_result 内容块的协议适配。

**影响文件**：
- `src/llm/provider.go` — 修改，`StreamChat` 签名增加 `tools []tool.Tool` 参数（注意循环依赖：llm 包 import tool 包可能造成 tool → llm → tool，需评估；备选方案：把 tools 的 `Name/Description/InputSchema` 抽取为 llm 包内独立类型 `ToolSpec`）
- `src/llm/anthropic.go` — 修改，组装 `anthropic SDK` 的 `Tools` 字段；解析响应中的 `tool_use` 内容块转换为内部 `ToolUseBlock`；发送 tool_result 时组装为 SDK 的 `ToolResultBlock` 并以 `RoleUser` 发送
- `src/llm/anthropic_test.go` — 补充/修改，工具调用场景的单测

**依赖**：Task 1（ToolUseBlock、ToolResultBlock 已定义）

**具体内容**：
1. 解决循环依赖问题：
   - 方案 A：在 `src/llm` 包内定义 `type ToolSpec struct { Name, Description string; InputSchema json.RawMessage }`（不含 Permission/Execute），Provider 接口使用 `[]ToolSpec`；Registry 提供 `ToSpecs() []ToolSpec` 转换方法
   - 方案 B：把 Tool 接口下移到 `src/llm` 包（破坏分层）
   - 推荐方案 A，保持 tool 在自己的包，llm 只关心协议层的描述
2. `provider.go` 修改：
   - `StreamChat(ctx, systemPrompt, messages, toolSpecs []ToolSpec) (<-chan StreamChunk, error)`
3. `anthropic.go` 修改：
   - 从 toolSpecs 组装 `[]anthropic.Tool`，其中 `InputSchema` 直接用 `InputSchema`（anthropic SDK 接受 `any`）
   - 解析 `content_block_start` 事件：`type=tool_use` 时构造内部 `ToolUseBlock` 通过 StreamChunk 发出
   - 流结束事件处理：把 `tool_use` 块（可能与 text 块混合）打包成 `[]ContentBlock` 消息（这里需要扩展 StreamChunk 或新增 ToolUse 事件类型，方案见下）
4. StreamChunk 扩展（评估）：
   - 方案：在 StreamChunk 中增加 `ToolUse *ToolUseBlock` 字段，LLM 流结束时把 ToolUse 块一起送回 conversation manager
   - 备选：增加独立事件类型 `EventTypeToolUse`，但 StreamChunk 已是专用结构
5. Anthropic SDK 调用查询：使用 context7 查询最新 `anthropic-sdk-go` 的 `MessageNew` / `Tools` / `ToolUseBlock` / `ToolResultBlock` API 签名

**参考资料**：
- Anthropic tool_use 协议：https://docs.anthropic.com/en/docs/build-with-claude/tool-use
- Anthropic SDK：使用 context7 查询 `anthropic-sdk-go` 最新 API
- 现有 `anthropic.go` 实现：[src/llm/anthropic.go](../../src/llm/anthropic.go)

---

## Task 5: OpenAI 协议 tools 适配

**状态**：已完成

**目标**：OpenAI 适配器实现 function_calling 协议——tools 数组转 OpenAI `Tool` 格式（含 `Type: "function"`）、解析流式响应中的 `tool_calls` 增量参数拼接、非流式响应中的 `tool_calls` 字段转换为内部 `ToolUseBlock`、回传时以 `Role: "tool"` + `tool_call_id` 发送。

**影响文件**：
- `src/llm/openai.go` — 修改，添加 tools 转换
- `src/llm/openai_test.go` — 补充/修改，工具调用场景单测

**依赖**：Task 4（接口扩展已落地）

**具体内容**：
1. `openai.go` 修改：
   - tools 组装：`[]openai.Tool{ {Type: "function", Function: openai.FunctionDefinition{ Name, Description, Parameters: InputSchema } } }`
   - 请求参数加入 `Tools`
   - 响应解析：非流式 `Choice.Message.ToolCalls` 转 `[]ToolUseBlock`；流式增量 `Choice.Delta.ToolCalls` 中 `function.arguments` 是字符串片段，需要按 index 累加，结束时 parse JSON
2. 回传：tool_result 在 OpenAI 协议中是独立 `role: tool` 消息，每条 tool_call 对应一条 `Message{ Role: "tool", ToolCallID, Content }`
3. StreamChunk 扩展：与 Anthropic 一致，使用相同的 `ToolUse` 字段
4. OpenAI SDK 查询：使用 context7 查询 `openai-go` 最新 API 签名（特别注意 `openai.ChatCompletionMessageToolCall` 字段）

**参考资料**：
- OpenAI function calling 协议：https://platform.openai.com/docs/guides/function-calling
- OpenAI SDK：使用 context7 查询 `sashabaranov/go-openai` 最新 API
- 现有 `openai.go` 实现：[src/llm/openai.go](../../src/llm/openai.go)

---

## Task 6: conversation manager 集成单轮工具执行

**状态**：已完成

**目标**：在 conversation manager 中实现单轮工具执行闭环：流式读取 LLM 响应 → 检测到 `tool_use` → 查 Registry 取工具 → 解析参数 → 执行（带超时与 context 取消） → 构造 `ToolResultBlock` → 把历史消息（含 tool_use 与 tool_result）二次发给 LLM → 把 LLM 的最终回复展示给用户。

**影响文件**：
- `src/internal/engine/conversation/manager.go` — 修改，新增 `StreamChatWithToolExecution` 入口或扩展现有方法
- `src/internal/engine/conversation/tool_handler.go` — 新建，工具分发执行器（查 Registry + Execute + 错误封装）
- `src/internal/engine/conversation/manager_test.go` — 补充，端到端单测

**依赖**：Task 1, Task 2, Task 4, Task 5

**具体内容**：
1. `tool_handler.go`：
   - `type ToolHandler struct { registry *tool.Registry; timeout time.Duration; workingDir string }`
   - 方法 `Execute(ctx, toolUse ToolUseBlock) ToolResultBlock`：查 Registry → 校验权限标记 → 调 `tool.Execute(ctx, toolUse.Input)` → 成功封装为 `Content=output, IsError=false`；失败封装为 `Content=err.Error(), IsError=true`
   - 超时：包装 `context.WithTimeout(ctx, h.timeout)` 传给工具
2. `manager.go` 修改：
   - 在发送消息前注入 `tools`（从 `tool.DefaultRegistry().EnabledNames(cfg)` + `ToSpecs()` 转换）
   - 接收 StreamChunk 流时累积 `[]ContentBlock`，流结束后检查是否含 `ToolUseBlock`
   - 若含：把 tool_use 追加到 messages → 调 `ToolHandler.Execute` → 追加 `ToolResultBlock` → 二次调 `StreamChat` 拿 LLM 最终回复 → 二次流也展示给用户
   - 若不含：维持现有行为（仅展示 text）
3. 关键交互点：把 `tool_use` 与 `tool_result` 的中间事件通过 WebSocket 推送给前端（Task 7 用）—— 工具开始前 push `tool_call_start`，工具结束后 push `tool_call_end`，状态切换走 `status_update`

**参考资料**：
- 现有 manager：[src/internal/engine/conversation/manager.go](../../src/internal/engine/conversation/manager.go)
- 现有 manager 单测：[src/internal/engine/conversation/manager_test.go](../../src/internal/engine/conversation/manager_test.go)

---

## Task 7: WebUI 工具执行展示

**状态**：已完成

**目标**：在 WebUI（浏览器中间会话栏）以独立消息条目展示工具调用——开始（"🔧 正在调用工具: ReadFile, 参数: {...}"）、完成（"✓ 工具执行完成 (耗时 0.2s)"）或失败（"✗ 工具执行失败 (耗时 0.1s)"）；复用现有 `abort_stream` 消息 + `streamState` 机制支持用户在工具执行中点击"停止"按钮中断（通过 context cancel）。

**影响文件**：
- `src/internal/interaction/web/protocol.go` — 修改，新增 `tool_call_start` / `tool_call_end` 消息常量与 Payload 类型；扩展 `Status` 枚举为 `idle | thinking | tool_running | error`；扩展 `ChatMessage` 增加 `ToolCall *ToolCallDisplay` 字段
- `src/internal/interaction/web/handler.go` — 修改，工具执行前 push `tool_call_start`、执行后 push `tool_call_end`；状态切到 `tool_running`；`abort_stream` 复用现有 `streamState.abort()` 中断工具 ctx
- `src/internal/interaction/web/tool_msg.go` — 新建，工具消息渲染辅助（参数摘要截前 200 字符、结果摘要截前 500 字符）
- `src/internal/interaction/web/static/style.css` — 补充工具消息块样式（左侧图标栏 + 折叠/展开区域 + 状态徽章）
- `src/internal/interaction/web/static/app.js` — 补充工具消息渲染逻辑（收到 `tool_call_start` 插入"正在执行"占位；收到 `tool_call_end` 替换为完成态；点击展开/折叠参数与结果）

**依赖**：Task 6

**具体内容**：
1. `protocol.go` 新增：
   ```go
   // 服务端 → 客户端
   const (
       MsgTypeToolCallStart = "tool_call_start"
       MsgTypeToolCallEnd   = "tool_call_end"
   )
   // 状态
   const StatusToolRunning = "tool_running"

   type ToolCallStartPayload struct {
       ToolUseID string          `json:"tool_use_id"`
       Name      string          `json:"name"`
       Input     json.RawMessage `json:"input"`
       StartedAt time.Time       `json:"started_at"`
   }

   type ToolCallEndPayload struct {
       ToolUseID  string `json:"tool_use_id"`
       Name       string `json:"name"`
       Output     string `json:"output"`     // 结果摘要，截前 500 字符
       IsError    bool   `json:"is_error"`
       DurationMs int64  `json:"duration_ms"`
       Status     string `json:"status"`     // completed / error / aborted / timeout
   }

   type ToolCallDisplay struct {
       ID         string `json:"id"`
       Name       string `json:"name"`
       Input      string `json:"input"`       // JSON 字符串
       Output     string `json:"output"`      // 摘要
       IsError    bool   `json:"is_error"`
       DurationMs int64  `json:"duration_ms"`
       Status     string `json:"status"`
   }

   // 扩展现有 ChatMessage
   type ChatMessage struct {
       Role     string           `json:"role"`
       Content  string           `json:"content"`
       ToolCall *ToolCallDisplay `json:"tool_call,omitempty"`
   }
   ```
2. `handler.go` 修改：
   - 工具执行 goroutine 中：开始时 sendStatusUpdate(StatusToolRunning) + sendMessage(MsgTypeToolCallStart, ...)，结束时 sendStatusUpdate(StatusThinking) + sendMessage(MsgTypeToolCallEnd, ...)
   - 复用 `streamState`：工具 goroutine 也通过 `streamState.tryAcquire()` 获取 ctx，状态机保证工具执行期间不可发起新请求；用户点击停止按钮触发 `abort_stream` → `streamState.abort()` → 工具 ctx 取消
   - `sendSessionLoaded` 扩展：遍历 messages 时把 ToolUseBlock 转为 ToolCallDisplay
3. `tool_msg.go`：辅助函数 `summarizeOutput(s string) string`（截前 500 字符 + "..."）、`summarizeInput(input json.RawMessage) string`（compact JSON）
4. 前端样式：工具消息块采用左竖线 + 图标 + 折叠区，点击展开查看参数与结果
5. 工具结果回传给 LLM 后，**不**在 WebUI 重复展示原始 tool_result（避免视觉冗余），仅在 LLM 最终回复中体现

**参考资料**：
- 现有 web 层：[src/internal/interaction/web/](../../src/internal/interaction/web/)
- 现有 WebSocket 协议：[src/internal/interaction/web/protocol.go](../../src/internal/interaction/web/protocol.go)
- 现有流式状态机：[src/internal/interaction/web/handler.go](../../src/internal/interaction/web/handler.go) 的 `streamState` 段落
- step1.1 spec：[docs/step1.1-UI界面重构/spec.md](../../step1.1-UI界面重构/spec.md)

---

## Task 8: 配置扩展 + main.go 接入

**状态**：已完成

**目标**：扩展 `setting.json` 字段承载工具配置；`main.go` 新增 import 触发 `builtin` 包的 init() 注册所有工具；启动日志打印已注册工具列表与安全兜底状态。

**影响文件**：
- `src/internal/config/config.go` — 修改，新增 `ToolsConfig` 结构、`ToolExecutionTimeout`、`ToolWorkingDirectory` 字段
- `config/setting.example.json` — 修改，补充 tools 段示例
- `src/main.go` — 修改，import `_ "src/tool/builtin"` 触发注册

**依赖**：Task 1, Task 2, Task 6

**具体内容**：
1. `config.go`：
   - `type ToolsConfig struct { Enabled []string \`json:"enabled,omitempty"\` }`
   - `type Config struct { ...; Tools ToolsConfig \`json:"tools"\`; ToolExecutionTimeoutSeconds int \`json:"tool_execution_timeout_seconds"\`; ToolWorkingDirectory string \`json:"tool_working_directory,omitempty"\` }`
   - 默认值：`ToolExecutionTimeoutSeconds = 30`；`ToolWorkingDirectory = ""` 视为 cwd
2. `setting.example.json`：
   ```json
   {
     "tools": { "enabled": ["read_file", "write_file", "bash", "glob", "grep"] },
     "tool_execution_timeout_seconds": 30,
     "tool_working_directory": ""
   }
   ```
3. `main.go`：在 import 区添加 `_ "src/tool/builtin"`，启动时打印日志 `logger.Info("已注册工具", "count", len(registry.List()))`
4. 工具启用过滤：conversation manager 调 `registry.EnabledNames(cfg.Tools.Enabled)` 取实际启用的工具描述发给 LLM

**参考资料**：
- 现有 config：[src/internal/config/config.go](../../src/internal/config/config.go)
- 现有 main.go：[src/main.go](../../src/main.go)

---

## Task 9: 会话持久化兼容 + 端到端验证

**状态**：已完成

**目标**：(1) 验证 step1 的 session 持久化能正确序列化/反序列化 `ToolUseBlock`/`ToolResultBlock`；(2) 端到端跑通"用户说'读 main.go' → LLM 调 ReadFile → 工具执行 → tool_result 回传 → LLM 基于真实代码回答"；(3) 验证停止按钮中断、危险命令拦截、路径越界拦截、超时触发等边界场景。

**影响文件**：
- `src/internal/memory/session/session.go` — 修改/确认 ContentBlock 序列化兼容性
- 各 task 留下的 `*_test.go` — 补全验证
- `docs/step2-工具系统集成/checklist.md` — 逐项验证并填结果

**依赖**：Task 1-8

**具体内容**：
1. **会话持久化兼容**：
   - 验证 `ToolUseBlock` / `ToolResultBlock` 序列化到 session JSON 后能正确反序列化
   - 启动 codepilot → 触发一次工具调用 → 退出 → 恢复会话 → 历史记录中能看到 tool_use 与 tool_result
2. **Anthropic 协议端到端**：
   - 配置 anthropic provider → 启动 → 输入"请读一下 src/main.go 然后告诉我前 20 行是什么" → 观察 LLM 是否自主调用 ReadFile → 验证工具执行 → 验证最终回复
3. **OpenAI 协议端到端**：
   - 配置 openai provider → 重复上述场景
4. **边界场景**：
   - 输入"执行 rm -rf /" → 验证 Bash 黑名单拦截
   - 输入"读 /etc/passwd" → 验证路径沙箱拦截
   - 输入"执行一个死循环 `sleep 99999`" → 验证 30s 超时（可临时改 timeout 为 5s 加快测试）
   - 工具执行中点击停止按钮 → 验证工具 goroutine 被取消
5. **逐项填写 checklist.md**，所有项必须通过

**参考资料**：
- 现有 session：[src/internal/memory/session/session.go](../../src/internal/memory/session/session.go)
- checklist 验证项：[docs/step2-工具系统集成/checklist.md](./checklist.md)
