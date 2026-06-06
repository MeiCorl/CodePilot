# Step 4 — System Prompt 设计

> 本文档回答「要解决什么问题、做哪些能力、不做哪些、做到什么程度算完成」。具体函数名、参数、错误文本、SDK 类型名均不在此规定，留给 tasks.md。

---

## 背景

经过 Step 1～3，CodePilot 已具备：LLM 双向打通、工具系统（5 个内置工具）、ReAct Agent Loop 多轮迭代能力。但 LLM 在多轮任务中**行为风格飘忽不定**：

- 同一类问题有时洋洋洒洒解释一大段、有时惜字如金给一行代码
- 经常"一声不吭直接动手"，用户失去预期控制权
- 探索性问题直接动手改代码，而不是给 2~3 个方案让用户挑
- 越权做"顺手优化"（用户只让修一个 bug，Agent 把整片代码重构了）
- 引用代码位置没有稳定格式，前端无法跳转
- 工具选择不当（如用 `Bash cat` 代替 `ReadFile`，绕过了路径沙箱）
- 安全边界不清晰（自动决定 `rm -rf`、跳过 git hook 等）

**根因**：当前 LLM 收到的「角色与行为约束」是 Step 1 临时塞进 `system` 字段的一段简短字符串，内容粗糙、无结构、不可演进。前几步在 `src/internal/engine/prompt/` 留下了空目录作为扩展点。

本步骤要建设一套**分层、可组合、可观测、可演进**的 System Prompt 体系，让 Agent 行为有稳定预期，同时为后续 Step 5（权限）、Step 7（上下文）、Step 8（自动记忆）预留接入位。

---

## 目标用户

- **终端用户（直接使用者）**：希望 Agent 行为稳定、可预测、可中断、可追问
- **二次开发者 / 项目维护者**：希望能在 `AGENTS.md` 中定制本项目专属规则
- **CodePilot 自身维护者**：希望 System Prompt 易于调试、易于扩展

---

## 能力清单

1. **静态 System Prompt 体系**：固定不变、内容稳定、可缓存；包含 5 个子模块——角色设定、行为准则、代码质量规范、工具使用原则、安全边界
2. **环境上下文自动注入**：会话启动时一次性采集操作系统、工作目录（绝对路径）、Git 状态（branch / 是否有未提交变更 / 最近 commit），拼成结构化文本注入到 `system` 字段尾部
3. **AGENTS.md 加载机制**：支持全局 `~/.codepilot/AGENTS.md` 与项目级 `<cwd>/AGENTS.md` 两级加载，按**项目级优先**策略合并同名段落
4. **项目指令作为 user 消息**：AGENTS.md 合并内容以独立的 user-role 消息插入到对话流的最前部（紧跟 system 之后），避免污染 system 字段造成注意力稀释
5. **自动记忆扩展点**：在 System Prompt 组装管线中预留 `Memories` 槽位（可注入到 messages 区），本步骤仅实现空实现 + 接入位，**真实数据由 Step 8 提供**
6. **模板变量插值**：静态 SP 与环境上下文中支持少量预定义变量（`{{OS}}`、`{{CWD}}`、`{{DATE}}`、`{{VERSION}}`），避免硬编码过期信息
7. **Anthropic Prompt Caching**：在 Anthropic Provider 协议层对静态 SP + 环境上下文打 `cache_control` 标记，多轮迭代命中缓存降低成本与延迟
8. **可观测性**：在 WebUI 状态栏与日志中显示当前 System Prompt 的总 token 数（估算）+ 各层小计；提供开发者模式开关可一键导出当前完整组装结果
9. **配置可关闭**：在 `config.json` 中提供 `system_prompt.enabled` 开关，关闭后回退到空 system 字段（保持与早期会话的兼容）
10. **会话恢复兼容**：System Prompt 本身**不**持久化到 session JSON（每次启动重新组装），但组装结果应在同一会话内保持稳定，确保恢复会话后 LLM 看到的 system 仍与首次一致

---

## 非功能要求

- **正确性**：相同输入（cfg / cwd / git / AGENTS.md）下多次组装结果完全一致（确定性）
- **性能**：System Prompt 组装耗时 < 5ms（典型配置）；Anthropic 缓存命中时第二轮起延迟应明显下降
- **可扩展性**：新增第 5、6 层 SP（如 Step 8 记忆、Step 5 权限提示）只需注册一个 Source Provider，无需改动主流程
- **可测试性**：每层 SP 都有可单元测试的纯函数入口；环境采集、AGENTS.md 解析、模板渲染均不依赖全局状态
- **安全性**：AGENTS.md 加载严格限制在配置的 working_directory 范围内，禁止符号链接逃逸；模板变量不接受外部输入，仅内部已知值
- **兼容性**：旧 session 加载后正常运转（System Prompt 重新组装，旧消息流不变）

---

## 设计骨架

> 仅目录结构示意，不规定具体函数签名。

```
src/internal/engine/prompt/
├── README.md                          # 本模块设计说明（架构、扩展指南）
├── builder.go                         # 顶层 Builder：按层组装最终 SystemPrompt 结构体
├── template.go                        # 模板变量 {{OS}}/{{CWD}}/{{DATE}}/{{VERSION}} 渲染
├── sources/
│   ├── source.go                      # Source 接口定义（Name + Assemble(ctx) (string, error)）
│   ├── static.go                      # 静态 SP：硬编码 5 个子模块（角色/行为/代码质量/工具/安全）
│   ├── environment.go                 # 环境上下文：OS / CWD / Git 状态采集
│   ├── agents_md.go                   # AGENTS.md 加载：全局 + 项目级合并
│   └── memory.go                      # 自动记忆占位（Step 8 接入，暂返回空）
├── render/
│   ├── split_system.go                # 把组装结果拆为 system 字段 + 起始 user 消息
│   └── cache_mark.go                  # Anthropic 缓存标记（输出 cache_control 区间信息）
└── tokens/
    └── estimate.go                    # 简单 token 估算（字符数 / 4 近似，足够用于状态栏展示）

src/llm/
├── types.go                           # 扩展：SystemPrompt 结构（system 字符串 + 首条 user 消息 + cache 区间）
├── anthropic.go                       # 改造：system 拆为多段 cache_control 块；首条 user 消息正常追加
└── openai.go                          # 改造：system 字符串 + 首条 user 消息正常追加

src/internal/memory/context/
└── window.go                          # 改造：把首条 user 消息视为"不可裁剪"（SP 注入区）

src/internal/config/
└── config.go                          # 扩展：SystemPromptConfig { Enabled, StaticOverrides, AGENTSGlobals }

src/internal/interaction/web/
├── handler.go                         # 改造：会话启动时调用 prompt.Builder 一次并缓存
└── status.go                          # 改造：状态栏推送 SP token 数估算
```

**关键设计点**：

1. **Builder 模式**：每个 Source 独立可测试，Builder 顺序调用并拼装；新增 SP 层只需注册 Source
2. **system 字段 vs user 消息**：静态 SP + 环境上下文进 `system`；AGENTS.md + 记忆进 `messages[0]`（role=user）
3. **缓存切片**：Anthropic 协议下，system 字段会被进一步切片为「静态 SP」+「环境上下文」两段独立 cache 块，最大化缓存复用
4. **可观测注入位**：在 Builder 内埋点统计每层 token 数，暴露给 WebUI 状态栏

---

## Out of Scope（本步骤不做）

| 不做的事 | 原因 / 留给哪一步 |
|---------|------------------|
| 自动记忆的真实数据采集、存储、检索 | 留给 Step 8（记忆系统） |
| 权限确认相关的 SP 注入（如"危险工具需用户确认"） | 留给 Step 5（权限系统） |
| 上下文压缩 / 摘要策略对 SP 的影响 | 留给 Step 7（上下文管理） |
| MCP 工具列表动态注入到 SP | 留给 Step 6（MCP 协议） |
| SubAgent 的 SP 差异化 | 留给 Step 12（SubAgent） |
| 多语言 SP（i18n） | 暂不需要，按需扩展 |
| WebUI 上可视化编辑 AGENTS.md | 不在 MVP 范围，可由用户用编辑器维护 |
| SP A/B 实验框架 | 超出当前需求 |

---

## 验收口径

满足以下全部条件视为本步骤完成：

- 6 个 Task 全部状态为「已完成」
- checklist.md 中所有验证项已勾选且结论为「通过」
- `go build ./...` 无错误
- `go test ./internal/engine/prompt/...` 全部通过
- 端到端：启动 WebUI，发送一条用户消息，能在 WebUI 状态栏看到 SP token 数；Anthropic 模型下第二轮起 `cache_read_input_tokens > 0`
- 项目进度文档 `PROGRESS.md` 已同步更新
