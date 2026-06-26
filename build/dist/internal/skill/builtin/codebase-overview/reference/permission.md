# 权限管理 — CodePilot 实现原理

> 隶属 Step 5（权限系统设计）| 架构层:第 5 层 安全层 | 核心入口:`src/internal/security/checker.go`

## §1 模块定位

权限管理位于第 5 层 安全层(横切层),是 CodePilot 所有高风险操作的「守门员」,在工具执行前按模式 / 规则 / HITL 决定 Allow / Ask / Deny,与路径沙箱形成纵深防御。

- **三层模式**:`ModeStrict / ModeDefault / ModePermissive`,严格 / 默认 / 宽松
- **可配置规则**:`Rule{Tool, Pattern, Action, Reason}`,支持路径级 Glob 匹配
- **多层配置合并**:全局 `~/.codepilot/setting.json` + 项目 `<cwd>/.codepilot/setting.json`,项目级 mode 覆盖全局 mode,项目级 rules 排在全局 rules 前
- **HITL 人在回路**:`HITLCallback` + 三种授权范围(本次 / 本会话 / 永久)
- **黑名单**:`CheckBashCommand`(Bash 危险命令硬编码拦截)
- **路径沙箱**:`ResolveInSandboxWithRoots` + `SandboxMiddleware`(详见 sandbox.go)
- **WebUI 确认对话框**:拦截器返回 ActionAsk 时,前端弹对话框,用户选择授权范围

## §2 核心数据结构

- `Mode`(`src/internal/security/config.go`)— 枚举 `ModeStrict / ModeDefault / ModePermissive`
- `Action`(`src/internal/security/`)— 枚举 `ActionAllow / ActionAsk / ActionDeny`
- `Rule`(security)— 单条权限规则,字段 `Tool / Pattern / Action / Reason`
- `PermissionPolicy`(`src/internal/security/config.go:14`)— 多层合并后的最终策略,字段 `Mode / Rules / HasProjectConfig`
- `Checker`(`src/internal/security/checker.go:29`)— 权限检查器,字段 `mode / rules / workdir / sessionRules / oneTimePathRules / mu`
- `Decision`(checker.go)— 单次检查决策,字段 `Action / Reason / Pattern / Rule`
- `Interceptor`(`src/internal/security/interceptor.go:36`)— 拦截器,字段 `checker / hitlCallback / mu`
- `InterceptorResult`(interceptor.go:18)— 拦截结果,字段 `Decision / PermanentRule`
- `HITLCallback`(interceptor.go)— 人在回路回调函数,签名 `func(ctx, tool, input, Decision) (HITLResponse, error)`
- `HITLResponse`(security)— 授权范围响应,字段 `Scope (one-time / session / permanent) / ApprovedRule`
- 拦截器校验流程总览(见下方 §3.7):`src/internal/security/interceptor.go:67`(Check 入口)+ `src/internal/security/checker.go:60`(Decide 单次检查)+ `src/internal/security/sandbox_middleware.go:183`(SandboxMiddleware)
- `SandboxMiddleware`(`src/internal/security/sandbox_middleware.go:183`)— 路径沙箱中间件
- `ResolveInSandboxWithRoots`(`src/internal/security/sandbox.go:64`)— 附加只读根机制
- `CheckBashCommand`(`src/internal/security/blacklist.go`)— Bash 危险命令黑名单

## §3 关键流程

### 3.1 多层配置合并(`LoadPermissions`)

`LoadPermissions(globalConf, projectConf) *PermissionPolicy`(`src/internal/security/config.go:35`)流程:

1. 初始化 `policy{Mode: ModeDefault, Rules: []}`
2. 解析全局 `globalRules := parseRules(globalConf)`
3. 处理项目级配置:
   - `policy.HasProjectConfig = true`
   - `projectRules := parseRules(projectConf)`
   - **mode 覆盖**:`projectConf.Permissions.Mode != ""` 时取项目级,否则取全局
   - **rules 排序**:项目级 rules 排在全局 rules 前面(优先匹配)
4. 校验 mode 合法性,非法时降级为 `ModeDefault`

[Why] 项目级 mode 覆盖全局而非合并:**Why** mode 是「档位」语义,无法合并;项目级需求通常比全局更严格(如生产环境强制 strict),故项目级优先。

### 3.2 工具执行前的拦截链(`ToolHandler.doExecute`)

`ToolHandler.doExecute`(tool_handler.go 内部)流程:

1. **Middlewares 链**:`SandboxMiddleware`(tool_handler.go:124 注释)做路径类工具的硬兜底沙箱校验
2. **Interceptor.Check**(interceptor.go:67)— `Checker.Decide` + 可选 HITL
3. **执行工具**:`tool.Execute(ctx, input)`
4. **执行后事件**:fire `OnEnd` 回调(包含执行结果 / 错误)

`ToolHandler.ExecuteBatch` 按权限分级调度(read 并行 / write 串行 / exec 串行,详见 tool-system.md)。

### 3.3 `Checker.Decide` 决策流程

`Checker.Decide(ctx, toolName, input) Decision`(`src/internal/security/checker.go:60+`)流程:

1. **路径工具路径提取**:`extractStringParam(params, paramKey)` 拿 `file_path` / `command` 等参数
2. **规则匹配**(顺序):
   - 遍历 `sessionRules`(会话级临时规则,优先)
   - 遍历 `rules`(配置级规则,项目级在前)
   - **路径类规则**:`buildPathAwarePattern(tool, path, workdir)` 构造规范化 pattern,`MatchPathRule` 匹配
3. **mode 兜底**:
   - `ModeStrict`:任何未匹配规则的工具 → ActionAsk(询问用户)
   - `ModeDefault`:PermWrite → ActionAsk;PermExec(Bash) → ActionAsk;PermRead → ActionAllow
   - `ModePermissive`:所有未匹配规则的工具 → ActionAllow
4. **返回 Decision{Action, Reason, Pattern, Rule}**

[Why] 严格 / 默认 / 宽松三档:**Why** 工业惯例(GitHub Copilot / Cursor 等都采用);严格档适合生产环境(CI / 服务器),默认档适合日常开发,宽松档适合 demo / 实验。

### 3.4 HITL 人在回路

`Interceptor.Check`(interceptor.go:67)在决策为 ActionAsk 时:

1. 调 `hitlCallback(ctx, toolName, input, decision)`(interceptor.go 注释)
2. callback 返回 `HITLResponse{Scope, ApprovedRule}`
3. **Scope = one-time**:`AddOneTimePathRule(rule)`,规则进 `oneTimePathRules`(仅 SandboxMiddleware 消费一次后删除)
4. **Scope = session**:`AddSessionRule(rule)`,规则进 `sessionRules`(本次会话有效)
5. **Scope = permanent**:返回 `PermanentRule` 给 handler,由 handler 写入 `setting.json` 持久化

WebUI 的权限对话框(`src/internal/interaction/web/`)在收到 `permission_ask` 事件后弹出,展示工具名 + 参数摘要 + 三个选项按钮(本次允许 / 本会话允许 / 始终允许)。

[Why] 三种授权范围:**Why** 用户对同一工具的信任度随场景变化:偶尔一次试探(one-time)、某个工作流反复用(session)、跨工作流通用工具(permanent)。三档语义清晰,避免「一次性 allow 但实际每次都生效」的过度授权。

### 3.5 路径沙箱(SandboxMiddleware)

`SandboxMiddleware(workdir, ruleProvider, opts...) MiddlewareFunc`(`src/internal/security/sandbox_middleware.go:183`)流程:

1. `IsPathTool(toolName)` 判断是否路径类工具
2. 提取 `params[paramKey]`,空时 `Glob / Grep` 默认走 workdir,其他工具透传
3. **PermRead 时启用附加根**:`ResolveInSandboxWithRoots(path, workdir, readRoots)`
4. **PermWrite / PermExec 仅认 workdir**:`ResolveInSandbox(path, workdir)`
5. 越界时 `ruleProvider.MatchPathRule(tool, absForRule)` 查询路径级 allow 规则
   - **命中** → 放行,`WithPathResolver(ctx, resolver)` 注入 ctx
   - **未命中** → 拦截,返回 `ErrPathOutsideSandbox`
6. 合法路径同样 `WithPathResolver(ctx, resolver)` 注入 ctx(供工具 Execute 用 `resolvePathFromContext` 直接拿 absPath)

[Why] PermWrite 不启用附加根:**Why** 「能读 memory」不等于「能写 memory」;纵深防御防止 memory 目录被 WriteFile 直接篡改。

### 3.6 黑名单(`CheckBashCommand`)

`BashTool.Execute` 不再做黑名单检查(已提升至拦截器层,`bash.go:70` 注释)—— 由 `security.Checker.Decide` 在拦截器层做硬安全预检。`blacklist.go` 列出 rm -rf / 、 mkfs 、 shutdown 等危险命令模式。

[Why] 黑名单提到拦截器层:**Why** 黑名单是权限决策而非工具实现细节;拦截器层对所有工具统一拦截,工具自身无需关心黑名单规则。

### 3.7 校验流程全链(总结)

```
Tool call from LLM
  ↓
SandboxMiddleware (路径类工具)
  ├─ IsPathTool? → false: skip
  ├─ PermRead? → ResolveInSandboxWithRoots (workdir + memory + skill roots)
  ├─ PermWrite/Exec? → ResolveInSandbox (仅 workdir)
  ├─ 越界 → MatchPathRule → allow / deny
  └─ 合法 → WithPathResolver(ctx) 注入 absPath
  ↓
Interceptor.Check
  ├─ Checker.Decide
  │   ├─ sessionRules 匹配
  │   ├─ rules 匹配 (项目级在前)
  │   ├─ mode 兜底 (Strict/Default/Permissive)
  │   └─ 返回 Decision
  ├─ ActionAllow → 放行
  ├─ ActionAsk → hitlCallback → Scope (one-time/session/permanent)
  │   ├─ one-time → AddOneTimePathRule → 下次 SandboxMiddleware 命中即消费
  │   ├─ session → AddSessionRule → 本会话 Checker.Decide 优先匹配
  │   └─ permanent → handler 写入 setting.json → 下次启动生效
  └─ ActionDeny → 拦截,返回 error
  ↓
tool.Execute(ctx, input) — absPath 从 ctx 拿
  ↓
fire OnEnd 回调 → WebUI 工具徽标更新
```

## §4 与其他模块的依赖

- **上游**(权限模块依赖):
  - `internal/config.Config`(`src/internal/config/`)— 加载 `setting.json` 多层配置
  - `internal/tool.Tool`(permission 字段)— 提供 `PermRead / PermWrite / PermExec`
  - `internal/interaction/web/handler`(HITL callback)— WebUI 弹对话框收集用户选择
- **下游被依赖**:
  - `internal/tool/builtin/Bash` — 黑名单拦截
  - `internal/memory/autolearn/Store`(`Store.RootFor` 注入 `WithReadRoots`)
  - `internal/skill/scanner`(`builtin/user/project` skill 根注入 `WithReadRoots`)
  - `main.go:547-557`(装配 SandboxMiddleware + ReadFile 附加根)

## §5 设计决策

### 决策 1:三层模式(Strict/Default/Permissive)

- **问题**:权限严格度如何兼顾安全与体验
- **方案**:`ModeStrict`(严格,任何未匹配规则的工具都需 Ask)+ `ModeDefault`(默认,PermWrite/Exec 需 Ask)+ `ModePermissive`(宽松,所有工具自动 Allow)
- **理由**:**Why** 工业惯例;用户能根据场景(生产 / 开发 / 实验)选择合适档位;默认 Default 是「多数场景下的合理起点」

### 决策 2:会话级临时规则与一次性路径规则分离

- **问题**:用户选「本会话允许」与「本次允许」应有什么不同效果?
- **方案**:`sessionRules []Rule`(本会话所有 Decide 优先匹配)+ `oneTimePathRules []Rule`(仅 SandboxMiddleware 消费一次后删除)
- **理由**:**Why** 「本会话允许」是「我对这个工作流放心」,跨工具调用生效;「本次允许」是「这次特殊情况放行」,仅一次避免退化为会话级授权

### 决策 3:HITL 三种授权范围(one-time/session/permanent)

- **问题**:用户在权限对话框的三个选项应有什么语义
- **方案**:Scope 字段枚举,one-time 进 oneTimePathRules / session 进 sessionRules / permanent 持久化到 setting.json
- **理由**:**Why** 三种范围覆盖「试探 / 工作流 / 通用工具」三种使用模式;持久化路径清晰,handler 在 permanent 时拿到 `*Rule` 写入 setting.json

### 决策 4:沙箱与权限层职责分离

- **问题**:路径越界检查与权限决策应放在哪一层
- **方案**:`SandboxMiddleware` 做「路径是否合法可碰」硬兜底(无法关闭)+ `Checker.Decide` 做「碰之前要不要问」决策(mode + allow/deny/ask 规则)
- **理由**:**Why** 两层职责分离形成纵深防御——即便权限层配错,沙箱层仍能拦截路径越界;权限层即便放行,沙箱层仍能拒绝 workdir 外

### 决策 5:WebUI 确认对话框

- **问题**:ActionAsk 决策如何在 UI 上让用户做选择
- **方案**:WebUI handler 收到 `permission_ask` 事件后弹对话框,展示工具名 + 参数摘要 + 三个选项按钮
- **理由**:**Why** 「可视化确认」是工业惯例;参数摘要(`buildParamsSummary` interceptor.go:261)让用户能基于实际参数决策而非盲选

## §6 关键文件索引

| 路径 | 角色 |
|------|------|
| `src/internal/security/config.go:14` | `PermissionPolicy` 多层合并策略 |
| `src/internal/security/config.go:35` | `LoadPermissions` 多层合并 |
| `src/internal/security/checker.go:29` | `Checker` 权限检查器 |
| `src/internal/security/checker.go:60+` | `Decide` 单次检查决策 |
| `src/internal/security/checker.go:220` | `SetMode` 运行时切换档位 |
| `src/internal/security/interceptor.go:36` | `Interceptor` 拦截器 |
| `src/internal/security/interceptor.go:67` | `Check` 拦截入口 |
| `src/internal/security/interceptor.go:261` | `buildParamsSummary` HITL 参数摘要 |
| `src/internal/security/sandbox_middleware.go:183` | `SandboxMiddleware` 路径沙箱中间件 |
| `src/internal/security/sandbox.go:64` | `ResolveInSandboxWithRoots` 附加只读根 |
| `src/internal/security/sandbox.go:139` | `IsPathOutsideSandbox` 权限层路径判定 |
| `src/internal/security/blacklist.go` | `CheckBashCommand` Bash 危险命令黑名单 |
| `src/internal/security/policy.go` | 策略相关 helpers |
| `src/internal/security/hitl.go` | HITL 响应结构定义 |
| `src/internal/interaction/web/protocol.go:470` | `PermissionModePayload` 权限模式 WS 推送 |
| `src/main.go:547-557` | `buildMemoryReadRoots / buildSkillReadRoots` 沙箱附加根装配 |