# Step 5 — 权限系统设计

## 背景

Step 2 工具系统集成阶段，我们在 `internal/tool/safety/` 包中实现了两层基础安全机制：Bash 命令黑名单（8 条正则规则拦截已知高危命令）和路径沙箱（`ResolveInSandbox` 限制文件操作在 `WorkingDirectory` 内）。这两层机制内嵌在各工具的 `Execute()` 方法中，不可被配置关闭，提供了硬兜底。

然而当前体系存在明显不足：
1. **权限分级（`ToolPermission`）仅用于调度策略**（读并行 / 写串行），未做强制拦截
2. **无用户可控的配置机制**——用户无法声明哪些操作放行、哪些需要确认
3. **无人在回路（HITL）**——当安全策略无法自动决策时，没有渠道把决定权交回用户
4. **无权限模式切换**——无法根据场景在严格/默认/放行之间快速切换
5. **权限拒绝直接中断流程**——无法作为错误反馈给 LLM 让其自主调整策略

本步骤在现有 `safety` 包的硬兜底之上，构建一套**可配置、可交互、纵深防御**的权限系统，归属于架构第 5 层（安全层），作为横切关注点为所有工具调用提供安全保障。

## 目标用户

- **终端开发者**：使用 CodePilot 进行日常编码，希望 Agent 能自主完成大部分操作，仅在敏感操作时请求确认
- **安全敏感团队**：需要严格管控 Agent 行为，限制文件访问范围和命令执行权限
- **快速体验用户**：信任 Agent 能力，希望最小化确认弹窗干扰

## 能力清单

### 核心能力

1. **三层权限模式**：提供 `strict`（严格）/ `default`（默认）/ `permissive`（放行）三档切换，每档定义不同级别的自动放行与拦截策略
2. **可配置的允许/拒绝/询问规则**：在配置文件中按「工具名 + 参数模式」声明 `allow` / `deny` / `ask` 动作，覆盖档位默认行为
3. **多层配置合并**：支持三层规则源——用户全局（`~/.codepilot/setting.json`）、项目级（`<cwd>/.codepilot/setting.json`）、会话级临时规则（内存）——按「会话级 > 项目级 > 全局」优先级合并
4. **人在回路（HITL）确认**：当规则未明确命中或命中 `ask` 动作时，暂停 Agent Loop 并通过 WebSocket 向用户请求确认
5. **三种授权范围**：用户确认时可选择"本次允许"（OneTime）、"本会话允许"（Session）、"永久允许"（Permanent），其中 Permanent 自动写入对应层级的配置文件
6. **权限拒绝优雅降级**：权限拒绝作为 `ToolResultBlock{IsError: true}` 返回给 LLM，区分"安全策略拦截"与"用户主动拒绝"两种错误信息，LLM 可自主调整策略继续工作

### 安全能力（保留 + 增强）

7. **危险命令黑名单增强**：保留现有 8 条正则规则作为硬拦截底层，扩展覆盖远程脚本下载执行（`curl | sh` / `wget -O- | bash`）等模式
8. **路径沙箱策略化**：保留现有 `ResolveInSandbox` 硬兜底，在其之上增加可配置的路径模式规则——越界路径不再一律拒绝，而是根据档位和规则决定是否允许（放行档自动放行 / 默认档询问 / 严格档拒绝）
9. **未知工具默认策略**：对于未来 MCP 注册的外部工具或未声明权限级别的工具，按当前档位的默认策略处理

### 交互能力

10. **WebUI 权限确认对话框**：展示工具名、参数摘要、触发原因，提供"拒绝 / 本次允许 / 本会话允许 / 永久允许"四个操作按钮
11. **状态栏权限模式展示**：WebUI 状态栏显示当前权限模式标识，鼠标悬停可查看生效规则概要

## 非功能要求

1. **性能**：权限检查为同步操作，单次检查延迟不超过 1ms，不得成为 Agent Loop 的性能瓶颈
2. **安全性**：硬兜底层（黑名单 + 路径沙箱）永远不可被配置关闭或绕过，权限系统的规则只能在此基础上"加严"或"加问"，不能"放宽"硬安全限制
3. **可扩展性**：权限检查器通过接口暴露，未来 MCP 工具注册时可复用同一套权限机制
4. **向后兼容**：无 `permissions` 配置时等效于 `default` 模式且无自定义规则，现有用户无需修改配置即可升级
5. **并发安全**：会话级临时规则的读写需加锁保护，避免并发工具执行的竞态条件
6. **持久化安全**：自动写入 `setting.json` 时保留用户已有的其他配置字段，采用"读取-合并-写回"策略，不覆盖无关配置

## 设计骨架

### 代码目录结构

```
src/internal/security/              # 安全层统一包（整合原 tool/safety + 新增权限系统）
├── policy.go                       # 策略模型：Mode / Action / Rule / Decision / Scope
├── checker.go                      # 权限检查器：规则匹配 + 档位覆盖 + 优先级合并
├── config.go                       # 多层配置加载：全局 + 项目级 + 会话级
├── interceptor.go                  # 工具执行拦截器：集成到 ToolHandler
├── hitl.go                         # HITL 回调接口：PermissionCallback 定义
├── blacklist.go                    # 危险命令黑名单（迁移自 tool/safety/bash_blacklist.go，含扩展规则）
└── sandbox.go                      # 路径沙箱（迁移自 tool/safety/path.go，含新增查询函数）

src/internal/tool/safety/           # [删除] 整包迁移至 internal/security/

src/internal/config/
└── config.go                       # [修改] 增加 Permissions 配置字段 + 项目级配置加载

src/internal/engine/conversation/
├── tool_handler.go                 # [修改] doExecute() 中插入拦截器调用
├── agent_loop.go                   # [修改] 支持 HITL 暂停/恢复
└── manager.go                      # [修改] 构造时注入权限组件

src/internal/interaction/web/
├── handler.go                      # [修改] 实现 HITL WebSocket 交互
└── static/                         # [修改] 前端权限确认对话框

src/internal/tool/builtin/
├── *.go                            # [修改] import 从 safety 改为 security

src/main.go                         # [修改] 顶层构造权限系统组件
```

### 配置结构

`permissions` 字段将加入 `setting.json`（全局或项目级均可）：

```json
{
  "permissions": {
    "mode": "default",
    "rules": [
      {
        "tool": "Bash",
        "pattern": "git *",
        "action": "allow",
        "reason": "允许所有 git 操作"
      },
      {
        "tool": "WriteFile",
        "pattern": "/etc/**",
        "action": "deny",
        "reason": "禁止写入系统目录"
      }
    ]
  }
}
```

### 核心调用链

```
ToolHandler.doExecute()
  → Interceptor.Check(ctx, toolName, params)
    → Checker.Decide(ctx, toolName, params)
      → 合并三层规则（会话 > 项目 > 全局）
      → 规则匹配（glob / 前缀）
      → 档位默认策略覆盖
      → 返回 Decision{Action, Reason}
    → 硬安全检查（security 包内的黑名单 + 路径沙箱）
    → Action=Ask → HITLCallback(ctx, request) → 等待用户响应
  → Decision=Allow → 继续执行
  → Decision=Deny → 返回 ToolResultBlock error
```

## Out of Scope（本步骤不做）

1. **MCP 工具的权限注册机制**——Step 6 MCP 协议实现时再定义外部工具如何声明权限级别，本步骤仅确保接口可扩展
2. **沙箱隔离执行**——Step 5 仅做权限拦截，不提供独立进程/容器级别的沙箱隔离
3. **命令行参数控制权限模式**——当前仅通过配置文件和 HITL 切换，不新增 CLI flag
4. **权限审计日志**——不在本步骤实现独立的权限审计日志系统，复用现有日志
5. **角色/用户体系**——不做多用户、角色的权限管理
6. **加密或签名配置文件**——不校验 `setting.json` 的完整性
