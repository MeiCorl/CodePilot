# Step 5 — 权限系统设计：任务清单

---

## Task 1: 策略模型与配置结构定义

**状态**：已完成

**目标**：定义权限系统的核心类型（Mode / Action / Rule / Decision / Scope）和配置结构，为后续所有任务提供类型基础。

**影响文件**：
- `src/internal/security/policy.go` — 新建，策略模型全部类型定义
- `src/internal/security/config.go` — 新建，多层配置加载结构
- `src/internal/config/config.go` — 修改，Config 结构体增加 Permissions 字段

**依赖**：无

**具体内容**：

1. **新建 `src/internal/security/policy.go`**，定义以下核心类型：
   - `Mode` 类型（`int` 或 `string`）：`ModeStrict` / `ModeDefault` / `ModePermissive`，带 `String()` 方法
   - `Action` 类型：`ActionAllow` / `ActionDeny` / `ActionAsk`，带 `String()` 方法
   - `Scope` 类型：`ScopeOneTime` / `ScopeSession` / `ScopePermanent`
   - `Rule` 结构体：`Tool`（工具名，大驼峰如 `Bash`、`WriteFile`）/ `Pattern`（路径 glob 或命令前缀）/ `Action`（allow/deny/ask）/ `Reason`（可选说明）
   - `Decision` 结构体：`Action` / `Reason` / `MatchedRule`（命中的规则引用，可能为 nil）
   - `PermissionRequest` 结构体：`ToolName` / `Params`（`map[string]interface{}`）/ `Reason` / `MatchedRule`
   - `PermissionResponse` 结构体：`Allowed`（bool）/ `Scope`（Scope 枚举）
   - 档位默认行为映射函数 `ModeDefaultAction(mode Mode, perm tool.ToolPermission) Action`——Strict 档所有 Write/Exec 走 Ask；Default 档 Write 走 Allow 但 Exec 走 Ask；Permissive 档全部走 Allow

2. **修改 `src/internal/config/config.go`**，在 `Config` 结构体中增加：
   ```go
   Permissions PermissionsConfig `json:"permissions,omitempty"`
   ```
   其中 `PermissionsConfig` 结构体包含：
   - `Mode string` — 权限模式，空字符串等效于 `"default"`
   - `Rules []RuleConfig` — 自定义规则列表
   - `RuleConfig` 结构体：`Tool` / `Pattern` / `Action` / `Reason`

3. **新建 `src/internal/security/config.go`**，实现多层配置加载：
   - `LoadPermissions(globalConf, projectConf config.Config) *PermissionPolicy`——合并全局和项目级配置
   - 项目级配置文件路径：`<cwd>/.codepilot/setting.json`，使用 `config.LoadFromPath()` 加载
   - 合并逻辑：项目级 `mode` 覆盖全局 `mode`；项目级 `rules` 和全局 `rules` 合并，项目级在前（优先匹配）
   - 无 `permissions` 字段时返回 `ModeDefault` + 空规则列表
   - `PermissionPolicy` 结构体：`Mode` / `Rules []Rule` / `Source`（标记来源层级）

4. **编写单元测试**：
   - `TestModeDefaultAction`——覆盖三档 × 三权限级别共 9 种组合
   - `TestLoadPermissions_GlobalOnly`——仅全局配置
   - `TestLoadPermissions_ProjectOverride`——项目级覆盖全局
   - `TestLoadPermissions_EmptyConfig`——无 permissions 字段时默认值

**参考资料**：
- 现有 Config 结构体：`src/internal/config/config.go` 第 18-30 行
- 现有 ToolPermission 定义：`src/internal/tool/tool.go` 中 `ToolPermission` 枚举
- 档位行为定义见 spec.md 能力清单第 1 条

---

## Task 2: 权限检查器核心逻辑

**状态**：已完成

**目标**：实现权限检查器 `Checker`，完成规则匹配（glob 路径 + Bash 命令前缀）、档位覆盖、优先级合并的核心逻辑。

**影响文件**：
- `src/internal/security/checker.go` — 新建，Checker 核心实现
- `src/internal/security/checker_test.go` — 新建，检查器单元测试

**依赖**：Task 1（策略模型和配置结构）

**具体内容**：

1. **新建 `src/internal/security/checker.go`**，实现 `Checker` 结构体：
   ```go
   type Checker struct {
       mode      Mode
       rules     []Rule          // 合并后的规则列表（会话级 > 项目级 > 全局）
       mu        sync.RWMutex    // 保护 sessionRules 的并发读写
       sessionRules []Rule       // 会话级临时规则（内存）
   }
   ```

2. **核心方法 `Decide(ctx, toolName string, params map[string]interface{}, perm tool.ToolPermission) Decision`**：
   - **Step 1 — 硬安全预检**：如果工具名是 `Bash`，先调用 `security.CheckBashCommand()`（同包调用，下同），命中直接返回 `ActionDeny`，Reason 为 "安全策略拦截：{黑名单原因}"。如果路径类工具越界，先通过 `security.IsPathOutsideSandbox()` 检查——但这里不直接拒绝，而是继续走规则匹配，让档位决定是 ask 还是 deny
   - **Step 2 — 会话级规则匹配**：遍历 `sessionRules`（RWMutex 读锁），找到第一条匹配的规则
   - **Step 3 — 配置级规则匹配**：遍历 `rules`（合并后的项目+全局），找到第一条匹配的规则
   - **Step 4 — 档位默认策略**：无规则命中时，根据 `mode` 和 `perm` 调用 `ModeDefaultAction()` 获取默认动作
   - 返回 `Decision{Action, Reason, MatchedRule}`

3. **规则匹配函数 `matchRule(rule Rule, toolName string, params map[string]interface{}) bool`**：
   - `rule.Tool` 匹配：`"*"` 匹配所有工具，否则精确匹配（大驼峰）
   - `rule.Pattern` 匹配：
     - `"*"` 匹配所有参数
     - 路径类工具（ReadFile / WriteFile / EditFile / Glob / Grep）：对 `file_path` / `path` / `base_dir` 参数使用 `path.Match` 进行 glob 匹配
     - Bash 工具：对 `command` 参数做**命令前缀匹配**——将 pattern 与 command 的首个 token（命令名）及整体做 `strings.HasPrefix` 匹配
   - Pattern 为空时等效于 `"*"`

4. **会话规则管理方法**：
   - `AddSessionRule(rule Rule)` — 追加会话级临时规则（写锁）
   - `ClearSessionRules()` — 清空会话级规则（写锁）

5. **编写单元测试**：
   - `TestChecker_RuleMatch_AllTool`——`"*"` 匹配所有工具
   - `TestChecker_RuleMatch_PathGlob`——路径 glob 匹配（`src/**/*.go`、`/etc/**`）
   - `TestChecker_RuleMatch_BashPrefix`——Bash 命令前缀匹配（`git *`、`curl *`）
   - `TestChecker_Decide_StrictMode`——严格档：Write/Exec 走 Ask
   - `TestChecker_Decide_DefaultMode`——默认档：Write 走 Allow、Exec 走 Ask
   - `TestChecker_Decide_PermmissiveMode`——放行档：全部 Allow
   - `TestChecker_Decide_SessionRulePriority`——会话级规则优先于配置级
   - `TestChecker_Decide_BashBlacklistDeny`——黑名单命中直接拒绝，不受档位影响
   - `TestChecker_AddSessionRule_ConcurrentSafe`——并发安全测试

**参考资料**：
- 原有 Bash 黑名单（将在 Task 6 迁移至 security 包）：`src/internal/tool/safety/bash_blacklist.go` 的 `CheckBashCommand()` 函数
- 原有路径沙箱（将在 Task 6 迁移至 security 包）：`src/internal/tool/safety/path.go` 的 `ResolveInSandbox()` 函数
- Go `path.Match` 文档：https://pkg.go.dev/path#Match
- 工具参数字段名参考各 builtin 工具的 InputSchema（如 `bash.go` 的 `command`、`write_file.go` 的 `file_path`）

---

## Task 3: 工具执行拦截器

**状态**：已完成

**目标**：实现 `Interceptor`，将其集成到 `ToolHandler.doExecute()` 中，在工具执行前完成权限检查并处理拦截/放行/HITL 委托。

**影响文件**：
- `src/internal/security/interceptor.go` — 新建，拦截器接口与实现
- `src/internal/security/hitl.go` — 新建，HITL 回调类型定义
- `src/internal/engine/conversation/tool_handler.go` — 修改，`doExecute()` 中插入拦截器调用
- `src/internal/security/blacklist.go` — 新建，危险命令黑名单（迁移并扩展自原 `tool/safety/bash_blacklist.go`）

**依赖**：Task 2（Checker 核心逻辑）

**具体内容**：

1. **新建 `src/internal/security/hitl.go`**，定义 HITL 回调类型：
   ```go
   // HITLCallback 是人在回路确认的回调函数类型。
   // 当权限检查结果为 ask 时，由调用方（如 WebUI）实现此回调，
   // 向用户请求确认并返回用户的选择。
   // ctx 可用于超时控制，返回的 PermissionResponse 中 Scope 标识用户选择的授权范围。
   type HITLCallback func(ctx context.Context, req PermissionRequest) (PermissionResponse, error)
   ```

2. **新建 `src/internal/security/interceptor.go`**，实现 `Interceptor`：
   ```go
   type Interceptor struct {
       checker      *Checker
       hitlCallback HITLCallback   // 可为 nil，nil 时 ask 等同于 deny
   }
   ```
   核心方法 `Check(ctx context.Context, toolName string, params map[string]interface{}, perm tool.ToolPermission) (*Decision, error)`：
   - 调用 `checker.Decide()` 获取 Decision
   - `ActionAllow` → 直接返回 nil（放行）
   - `ActionDeny` → 返回 Decision，调用方生成错误 ToolResultBlock
   - `ActionAsk` → 如果 `hitlCallback != nil`，构造 `PermissionRequest` 调用回调：
     - 用户允许 → 根据 Scope 处理：`ScopeSession` 调用 `checker.AddSessionRule()`；`ScopePermanent` 返回特殊标记让调用方写配置文件；`ScopeOneTime` 不做额外处理
     - 用户拒绝 → 返回 Decision（Deny），Reason 为 "用户拒绝本次操作"
     - 回调超时/错误 → 视为 Deny，Reason 为 "权限确认超时"
   - `ActionAsk` + `hitlCallback == nil` → 视为 Deny，Reason 为 "权限系统要求确认但无可用的确认通道"

3. **新建 `src/internal/security/blacklist.go`**（迁移并扩展自原 `tool/safety/bash_blacklist.go`）：
   - 将原有 8 条正则黑名单规则完整迁移过来，保持 `CheckBashCommand()` 函数签名和 `DangerousCommandError` 类型不变
   - 新增远程脚本下载执行规则：匹配 `curl ... | sh` / `curl ... | bash` / `wget ... -O- | sh` / `wget ... -O- | bash` 等管道模式
   - 新增 `curl ... | sudo sh` 等 sudo 组合变体
   - 包名改为 `security`

4. **修改 `src/internal/engine/conversation/tool_handler.go`**：
   - `ToolHandler` 结构体增加 `interceptor *security.Interceptor` 字段
   - 新增 `SetInterceptor(i *security.Interceptor)` setter 方法
   - 在 `doExecute()` 方法中，在查找工具之后、调用 `t.Execute()` 之前，插入拦截器调用：
     ```go
     if h.interceptor != nil {
         decision, err := h.interceptor.Check(ctx, toolUse.Name, params, t.Permission())
         if err != nil || decision != nil {
             // decision != nil 表示被拦截
             // 生成 ToolResultBlock{IsError: true, Content: decision.Reason}
             return result
         }
     }
     ```
   - 从 `toolUse.Input`（JSON）解析出 `map[string]interface{}` 传给拦截器

5. **编写单元测试**：
   - `TestInterceptor_Allow`——放行场景
   - `TestInterceptor_Deny_PolicyBlock`——策略拦截，错误信息包含"安全策略拦截"
   - `TestInterceptor_Deny_UserReject`——用户拒绝，错误信息包含"用户拒绝"
   - `TestInterceptor_Ask_WithCallback`——HITL 回调正常工作
   - `TestInterceptor_Ask_NoCallback`——无回调时 ask 视为 deny
   - `TestInterceptor_Ask_Timeout`——回调超时视为 deny
   - `TestInterceptor_SessionScope`——Session scope 自动添加会话规则
   - `TestInterceptor_PermanentScope`——Permanent scope 返回标记让调用方写配置

**参考资料**：
- ToolHandler.doExecute() 当前逻辑：`src/internal/engine/conversation/tool_handler.go` 第 168-217 行
- ToolHandler 结构体定义：同文件第 30-45 行
- ToolResultBlock 定义：`src/internal/tool/tool.go` 中 `ToolResultBlock` 结构体
- 工具 Input JSON 格式：参考各 builtin 工具的 InputSchema

---

## Task 4: HITL 后端交互机制

**状态**：已完成

**目标**：在 WebUI Handler 中实现 HITL 的 WebSocket 交互协议，包括权限确认请求发送、用户响应接收、Agent Loop 暂停/恢复机制，以及"永久允许"的配置文件写入。

**影响文件**：
- `src/internal/interaction/web/handler.go` — 修改，实现 HITLCallback、处理 permission_request/response WebSocket 消息
- `src/internal/engine/conversation/agent_loop.go` — 修改，支持 HITL 等待
- `src/internal/interaction/web/protocol.go` — 修改（如存在），增加 HITL 消息类型常量

**依赖**：Task 3（Interceptor 和 HITL 回调类型）

**具体内容**：

1. **WebSocket 协议定义**：
   - 新增消息类型 `permission_request`：后端 → 前端
     ```json
     {
       "type": "permission_request",
       "id": "perm_abc123",
       "tool_name": "Bash",
       "params_summary": "command: git push origin main",
       "reason": "默认模式下执行类操作需要确认",
       "rule": { "tool": "Bash", "pattern": "*", "action": "ask" }
     }
     ```
   - 新增消息类型 `permission_response`：前端 → 后端
     ```json
     {
       "type": "permission_response",
       "id": "perm_abc123",
       "allowed": false,
       "scope": "once"
     }
     ```
   - 超时默认 60 秒，超时后后端自动按"拒绝"处理

2. **实现 HITLCallback 函数**（在 `web.Handler` 中）：
   - 构造 `permission_request` 消息并发送到 WebSocket
   - 使用 channel + select 等待用户响应或超时
   - 响应通过 WebSocket `permission_response` 消息路由到对应 channel
   - `Handler` 结构体增加 `pendingPermissions map[string]chan PermissionResponse` + `mu sync.Mutex` 管理等待中的确认请求
   - 处理 `scope=permanent` 时：将新规则写入对应的 `setting.json`（项目级规则写项目配置，否则写全局配置）

3. **修改 `src/internal/engine/conversation/agent_loop.go`**：
   - `AgentLoop()` 本身无需大改——因为 HITLCallback 是同步阻塞调用，在 `ExecuteBatch()` → `Execute()` → `doExecute()` → `interceptor.Check()` 的调用链中自然阻塞当前 goroutine，Agent Loop 自动暂停等待
   - 但需确保 `ctx` 超时不受影响——HITL 等待使用独立超时（60s），不占用工具执行超时

4. **WebSocket 消息路由**：
   - 在 `handleWebSocket()` 的消息分发 switch 中增加 `permission_response` case
   - 根据 `id` 查找 `pendingPermissions` 中的 channel，将响应发送到 channel

5. **"永久允许"配置写入**：
   - 新增 `writeRuleToConfig(filePath string, rule RuleConfig) error` 辅助函数
   - 逻辑：读取现有 JSON → 解析为 `map[string]interface{}` → 在 `permissions.rules` 数组中追加规则 → 写回文件
   - 使用文件锁或原子写入避免并发冲突
   - 写入失败时日志警告但不阻断流程（降级为 Session scope）

6. **编写测试**：
   - `TestHITLCallback_AllowOnce`——本次允许，不追加规则
   - `TestHITLCallback_AllowSession`——本会话允许，追加会话级规则
   - `TestHITLCallback_AllowPermanent`——永久允许，写入配置文件
   - `TestHITLCallback_Reject`——用户拒绝
   - `TestHITLCallback_Timeout`——超时自动拒绝
   - `TestHITLCallback_Concurrent`——多个并发确认请求互不干扰

**参考资料**：
- WebSocket 消息处理：`src/internal/interaction/web/handler.go` 的 `handleWebSocket()` 方法
- Agent Loop 调用链：`src/internal/engine/conversation/agent_loop.go` 第 94-276 行
- 配置文件加载：`src/internal/config/config.go` 的 `LoadFromPath()` 函数
- 现有 WebSocket 消息类型：参考 `handler.go` 中的 switch 分支

---

## Task 5: WebUI 权限确认对话框

**状态**：已完成

**目标**：在前端实现权限确认对话框 UI，展示工具名、参数摘要、触发原因，提供四个操作按钮，并处理 WebSocket 消息的收发。

**影响文件**：
- `src/internal/interaction/web/static/js/app.js` — 修改，增加权限确认对话框逻辑
- `src/internal/interaction/web/static/css/style.css` — 修改，增加对话框样式

**依赖**：Task 4（后端 HITL WebSocket 协议）

**具体内容**：

1. **CSS 样式**（权限确认对话框）：
   - 半透明遮罩层覆盖全屏
   - 居中对话框卡片，深色背景 + 琥珀金强调色（与现有设计系统一致）
   - 顶部标题区：显示权限图标 + "权限确认" 文字
   - 中部内容区：工具名（大驼峰，如 `Bash`）、参数摘要（格式化显示关键参数）、触发原因
   - 底部按钮区：四个按钮横排
     - "拒绝"：红色系
     - "本次允许"：中性色
     - "本会话允许"：蓝色系
     - "永久允许"：琥珀金色
   - 底部倒计时文字：显示剩余确认时间（60s）

2. **JavaScript 逻辑**：
   - 监听 `permission_request` WebSocket 消息
   - 收到消息后弹出对话框，填充工具名/参数/原因
   - 四个按钮点击处理：
     - "拒绝"：发送 `{type: "permission_response", id, allowed: false, scope: "once"}`
     - "本次允许"：发送 `{type: "permission_response", id, allowed: true, scope: "once"}`
     - "本会话允许"：发送 `{type: "permission_response", id, allowed: true, scope: "session"}`
     - "永久允许"：发送 `{type: "permission_response", id, allowed: true, scope: "permanent"}`
   - 倒计时显示：60s 倒计时，到期自动发送"拒绝"
   - 同时只允许一个确认对话框，新请求排队等待（前端 FIFO 队列）

3. **Agent Loop 暂停状态展示**：
   - 对话框弹出期间，状态栏显示"等待用户确认..."文字
   - 消息流区域不展示新的中间状态，保持对话框关闭前的最后内容

**参考资料**：
- 现有 WebSocket 消息处理逻辑：`static/js/app.js` 中 WebSocket onmessage 处理
- 现有 CSS 设计系统：`static/css/style.css` 中的颜色变量和组件样式
- 参考现有工具执行 UI 的样式（`tool_call_start` / `tool_call_end` 事件的处理方式）

---

## Task 6: 安全代码迁移整合

**状态**：已完成

**目标**：将原有 `internal/tool/safety/` 包整体迁移至 `internal/security/` 包下统一管理，更新所有引用路径，并完成路径沙箱策略化升级。

**影响文件**：
- `src/internal/security/sandbox.go` — 新建，路径沙箱（迁移自 `tool/safety/path.go`，新增 `IsPathOutsideSandbox()` 查询函数）
- `src/internal/tool/safety/bash_blacklist.go` — 删除（已迁移至 `security/blacklist.go`）
- `src/internal/tool/safety/path.go` — 删除（已迁移至 `security/sandbox.go`）
- `src/internal/tool/safety/` — 删除整个包目录
- `src/internal/tool/builtin/bash.go` — 修改，import 从 `safety` 改为 `security`，移除 `Execute()` 内部黑名单调用
- `src/internal/tool/builtin/read_file.go` — 修改，import 从 `safety` 改为 `security`
- `src/internal/tool/builtin/write_file.go` — 修改，import 从 `safety` 改为 `security`
- `src/internal/tool/builtin/edit_file.go` — 修改，import 从 `safety` 改为 `security`
- `src/internal/tool/builtin/glob.go` — 修改，import 从 `safety` 改为 `security`
- `src/internal/tool/builtin/grep.go` — 修改，import 从 `safety` 改为 `security`
- `src/internal/security/interceptor.go` — 修改，补充路径越界检查逻辑

**依赖**：Task 3（拦截器集成 + blacklist.go 已在 Task 3 中新建）

**具体内容**：

1. **新建 `src/internal/security/sandbox.go`**（迁移自 `tool/safety/path.go`）：
   - 将原有 `ResolveInSandbox()` 函数、`ErrPathOutsideSandbox` 错误完整迁移过来，包名改为 `security`
   - 保持 `ResolveInSandbox()` 函数签名和返回值不变，所有调用方只需改 import 路径
   - 新增 `IsPathOutsideSandbox(path, sandboxDir string) (bool, error)` 查询函数：
     - 与 `ResolveInSandbox` 逻辑相同但不拒绝，仅返回 `bool` 表示是否越界
     - 供拦截器在权限检查时判断路径是否在工作目录范围外

2. **更新所有 builtin 工具的 import 路径**：
   - `bash.go`：import 从 `internal/tool/safety` 改为 `internal/security`
   - `read_file.go` / `write_file.go` / `edit_file.go` / `glob.go` / `grep.go`：同上
   - 调用点无需修改函数名（`ResolveInSandbox` / `CheckBashCommand` 保持原名）

3. **移除 `bash.go` 中的 `Execute()` 内部黑名单调用**：
   - 删除 `safety.CheckBashCommand(in.Command)` 调用（已在 Task 3 中迁移至拦截器层的 `Checker.Decide()` 硬安全预检）
   - 黑名单检查从工具内部提升到拦截器层，仍然是不可绕过的硬安全层

4. **修改 Interceptor.Check() 中的路径越界检查**（在 Task 3 基础上补充）：
   - 对路径类工具（ReadFile / WriteFile / EditFile / Glob / Grep），调用 `security.IsPathOutsideSandbox()` 检查
   - 越界路径根据档位处理：
     - `Permissive`：自动放行（仍保留 `ResolveInSandbox` 硬兜底在工具内部执行，防止逃逸）
     - `Default`：走 `Ask`（HITL 确认）
     - `Strict`：走 `Deny`

5. **删除 `src/internal/tool/safety/` 整个包**：
   - 确认所有引用已迁移后，删除 `bash_blacklist.go` 和 `path.go`
   - 删除空的 `safety/` 目录
   - 运行全量编译确认无遗漏引用

6. **确保路径沙箱硬兜底仍保留**：
   - 各工具内部的 `security.ResolveInSandbox()` 调用保留不动（仅改了 import 路径）
   - 即使权限系统放行了越界路径，工具执行时仍需通过 `ResolveInSandbox` 的硬检查
   - 这形成**双层防护**：权限系统做策略决策 + security 包做硬兜底

7. **编写测试**：
   - `TestSandbox_Migrate_ResolveInSandbox`——迁移后 `ResolveInSandbox` 功能不变
   - `TestSandbox_IsPathOutsideSandbox`——查询函数正确返回越界状态
   - `TestPathSandbox_StrictMode_OutsideDeny`——严格档越界路径拒绝
   - `TestPathSandbox_DefaultMode_OutsideAsk`——默认档越界路径需确认
   - `TestPathSandbox_PermissiveMode_OutsideAllow`——放行档越界路径放行（但硬兜底仍生效）
   - `TestBashBlacklist_MovedToInterceptor`——黑名单从工具内移至拦截器后仍生效
   - `TestDoubleLayer_PolicyAllowButSandboxBlock`——策略放行但硬兜底拒绝的场景
   - `TestAllBuiltinTools_ImportUpdated`——全量编译通过，无残留 safety 引用

**参考资料**：
- 原有路径沙箱（待迁移）：`src/internal/tool/safety/path.go`
- 原有 Bash 黑名单（已在 Task 3 迁移）：`src/internal/security/blacklist.go`
- 各 builtin 工具的 `Execute()` 方法中的安全检查调用点：`bash.go:72`、`read_file.go:71`、`write_file.go:71`、`edit_file.go:79`、`glob.go:72+103`、`grep.go:88+117`

---

## Task 7: 接入主流程 + 状态栏展示

**状态**：已完成

**目标**：在 `main.go` 顶层构造权限系统组件并注入到依赖链中，WebUI 状态栏增加权限模式展示。

**影响文件**：
- `src/main.go` — 修改，构造 Checker → Interceptor → 注入 ToolHandler
- `src/internal/interaction/web/handler.go` — 修改，注入 Interceptor 到 ToolHandler，状态栏事件
- `src/internal/interaction/web/static/js/app.js` — 修改，状态栏权限模式展示
- `src/internal/interaction/web/static/css/style.css` — 修改，权限模式标识样式

**依赖**：Task 4（HITL 后端）+ Task 5（前端对话框）+ Task 6（安全代码迁移整合）

**具体内容**：

1. **修改 `src/main.go`**：
   - 在工具系统初始化之后、Handler 构造之前，增加权限系统构造：
     ```go
     // 构造权限检查器
     checker := security.NewChecker(policy)
     // 构造拦截器（HITL callback 在 Handler 层注入）
     interceptor := security.NewInterceptor(checker, nil) // callback 后续设置
     // 注入到 ToolHandler
     toolHandler.SetInterceptor(interceptor)
     ```
   - 将 `interceptor` 传递给 `web.Handler` 构造函数，由 Handler 设置 HITL callback

2. **修改 `src/internal/interaction/web/handler.go`**：
   - `Handler` 构造时接收 `interceptor *security.Interceptor`
   - 实现 HITLCallback 函数并调用 `interceptor.SetHITLCallback(callback)`
   - 会话启动时发送 `permission_mode` WebSocket 消息，包含当前 mode 和规则概要

3. **状态栏权限模式展示**：
   - 在现有状态栏区域增加权限模式标识：
     - `strict` 模式：红色锁图标 + "严格"
     - `default` 模式：蓝色盾牌图标 + "默认"
     - `permissive` 模式：绿色解锁图标 + "放行"
   - 鼠标悬停显示 tooltip：当前模式名称 + 生效规则数 + 会话临时规则数
   - 收到 `permission_mode` WebSocket 消息时更新显示

4. **项目级配置加载集成**：
   - 在 Handler 初始化时，检测 `<cwd>/.codepilot/setting.json` 是否存在
   - 若存在，加载项目级配置并与全局配置合并
   - 项目级配置不存在时仅使用全局配置

5. **验证完整的注入链路**：
   - `main.go` → `Checker` → `Interceptor` → `ToolHandler.SetInterceptor()` → HITL 在 `web.Handler` 中设置
   - 确认各层依赖方向正确（安全层不依赖引擎层/Web 层，通过回调函数反向注入）

**参考资料**：
- main.go 当前初始化流程：`src/main.go` 第 80-160 行
- Handler 构造：`src/internal/interaction/web/handler.go` 的 `NewHandler()` 函数
- 状态栏现有结构：`static/js/app.js` 中的状态栏渲染逻辑

---

## Task 8: 端到端验证

**状态**：已完成

**目标**：验证权限系统从配置加载到工具执行拦截再到 HITL 交互的完整链路，确保新旧功能均表现正常。

**影响文件**：
- `src/internal/security/integration_test.go` — 新建，端到端集成测试
- 无新增生产代码

**依赖**：Task 7（主流程接入）

**具体内容**：

1. **集成测试场景**：

   **场景 A：默认模式无配置启动**
   - 无 `permissions` 配置时，等效于 `default` 模式
   - ReadFile / Glob / Grep 自动放行
   - WriteFile 自动放行
   - Bash 执行类命令走 HITL 确认

   **场景 B：严格模式下的文件写入**
   - 配置 `mode: "strict"`
   - WriteFile 触发权限确认
   - 用户选择"本次允许" → 本次执行成功，下次仍需确认
   - 用户选择"本会话允许" → 后续同工具写操作自动放行
   - 用户拒绝 → 返回错误给 LLM，LLM 可尝试其他方案

   **场景 C：自定义规则覆盖档位**
   - 配置 `mode: "default"` + 规则 `{tool: "Bash", pattern: "git *", action: "allow"}`
   - Bash 执行 `git status` 自动放行（命中规则）
   - Bash 执行 `npm install` 走 HITL 确认（未命中规则，档位默认为 ask）

   **场景 D：多层配置合并**
   - 全局配置 `mode: "permissive"` + 规则 `{tool: "WriteFile", pattern: "/etc/**", action: "deny"}`
   - 项目级配置 `mode: "default"`（无额外规则）
   - 最终 mode 为 `default`（项目级覆盖全局）
   - WriteFile 写 `/etc/hosts` 仍然 deny（全局规则保留）

   **场景 E：永久允许写配置**
   - 用户选择"永久允许"后，检查对应 `setting.json` 中新增了规则
   - 重启后规则生效

   **场景 F：Bash 黑名单不可绕过**
   - 即使 `mode: "permissive"`，`rm -rf /` 仍被黑名单拦截
   - 错误信息明确标识"安全策略拦截"

   **场景 G：路径越界双层防护**
   - 放行档下越界路径被权限系统放行
   - 但工具内部 `ResolveInSandbox` 硬兜底仍然拒绝（双层防护）

   **场景 H：向后兼容——无 permissions 配置**
   - 使用现有 `setting.json`（无 `permissions` 字段）启动
   - 所有功能正常，等效于 `default` 模式

2. **冒烟测试**：
   - 启动 CodePilot（`go run main.go`）
   - 通过 WebUI 发送测试消息触发工具调用
   - 验证状态栏权限模式标识正确显示
   - 验证权限确认对话框弹出和交互

**参考资料**：
- 现有 e2e 测试模式：参考 `step1.4` 中 e2e 测试的写法
- 冒烟测试方式：参考各步骤的冒烟测试执行方式
