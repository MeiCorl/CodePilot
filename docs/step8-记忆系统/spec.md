# Step 8 — 记忆系统（自动学习记忆）

## 背景

Agent 的记忆一般分为**短期记忆**与**长期记忆**：

- **短期记忆**：即会话上下文的历史消息。CodePilot 已在 Step 7（上下文管理）完成，具备滑动窗口、两层压缩、撞墙兜底等能力。
- **长期记忆**：又可分为静态记忆与动态记忆。
  - **静态记忆**：System Prompt、工具集描述等，Step 4 已落地。
  - **动态记忆（半静态）**：AGENTS.md、环境信息（自动采集），Step 4 已落地。
  - **自动学习记忆（缺失）**：Agent 在使用过程中，按一定规则自主总结、沉淀、跨会话召回的记忆。这是本步骤要补齐的最后一类长期记忆。

目前 CodePilot 每次**启动新会话时都是"失忆"的**——上一轮会话中用户反复强调的偏好（如缩进风格）、给出的纠正反馈、交代的部署运维知识、提供的外部参考链接，下一轮会话全部丢失，需要用户重新说明。本步骤通过引入"自动学习记忆"消除这一痛点：让 Agent 在每轮对话结束后**自主判断是否有值得长期记住的信息**，沉淀为分类记忆文件，并在后续会话启动时自动召回。

## 目标用户

CodePilot 的所有使用者（开发者），尤其是：

- 重视跨会话一致性的用户（不希望每次重复交代偏好与纠正）；
- 在固定项目中长期工作的用户（希望 Agent 记住项目架构、部署方式、关键文档位置）。

## 能力清单

1. **4 类自动学习记忆**：用户偏好（`user_preference`）、用户反馈（`user_feedback`）、项目知识（`project_knowledge`）、参考信息（`reference`）。
2. **分级存储**：
   - 用户级记忆（偏好 + 反馈）保存到 `~/.codepilot/memory/`，跨所有项目生效；
   - 项目级记忆（项目知识 + 参考信息）保存到 `<cwd>/.codepilot/memory/`，跟随项目。
3. **单条记忆独立 md 文件**：每条记忆一个文件，文件名为语义化 slug（如 `indent-style.md`）。
4. **MEMORY.md 目录索引**：用户级与项目级 memory 目录下各维护一个 `MEMORY.md`，按 4 类分块，每行格式 `- [type](file.md)——简介`。
5. **索引注入上下文**：新增 `memory` Source（复用 Step 4 的 prompt Source 体系，作为 LeadUserMessage 注入），会话启动时合并用户级 + 项目级两个索引注入，与 AGENTS.md 注入过程一致。
6. **索引体积上限**：注入内容限制 200 行或 25KB，超过截断并打日志提醒，防止撑爆上下文（阈值可在 `setting.json` 配置）。
7. **LLM 按需读取记忆详情**：放宽 ReadFile 路径沙箱，放行 `.codepilot/memory` 目录，LLM 可根据索引读取具体记忆文件全文；读取仍走 `permission.Decide` 权限链路。
8. **智能节流的后台异步回顾**：每轮 Agent Loop 结束（`StopReason=completed` 即 end_turn）且本轮用户输入有实质内容时，在后台**异步**回顾本轮对话，判断是否有值得总结的信息；`aborted` / `error` / `max_iterations` / `context_overflow` 等异常终止一律不触发。
9. **独立无状态回顾通道**：回顾走**独立的 LLM 调用**，自带本轮对话快照（用户输入 + 最终回复 + 工具调用名摘要）作为输入，**绝不回写主对话历史**，避免污染上下文、干扰后续轮次。
10. **LLM 比对索引去重/更新**：生成记忆前先读取已有 MEMORY.md 索引，由回顾 LLM 决定是**新建**独立文件还是**覆盖/合并**已有同主题文件，并同步刷新索引，消除冗余与自相矛盾。
11. **配置驱动**：`setting.json` 新增 `memory` 配置段，支持整体开关、索引阈值覆盖等。
12. **可观测性**：结构化日志记录 sessionID、触发原因、生成/更新了哪些记忆、失败原因；后台失败静默降级，不影响主流程。

## 非功能要求

- **高性能**：后台异步执行，绝不阻塞用户下一轮输入；回顾 LLM 调用与主 Agent Loop 完全解耦，回顾耗时不影响响应延迟。
- **高可用**：回顾全链路（LLM 调用 / JSON 解析 / 文件写入 / 索引刷新）任一环节失败均**静默降级 + 结构化日志**，不抛异常到主流程；goroutine 内 `panic recover` 兜底；同一会话的回顾请求串行化，避免并发互相覆盖索引。
- **高安全**：
  - 回顾 prompt 明确禁止记录密钥 / 密码 / token 等敏感凭证；
  - 记忆文件名做 `isSafeName` 路径逃逸防护（仅允许 `[a-z0-9-]`，限定长度）；
  - ReadFile 读取记忆详情仍走 `permission.Decide` 全链路，可被 allow/deny/ask 规则控制；
  - MEMORY.md / 记忆文件原子写入（临时文件 + rename），避免并发或崩溃损坏。
- **高扩展**：
  - 复用 Step 4 的 `prompt.Source` 接口，记忆注入与静态/环境/AGENTS.md 注入同构；
  - 记忆类型、回顾策略、存储路径均可通过配置扩展；
  - 回顾器与存储层解耦，后续可替换为向量检索等高级召回方式而不动核心。
- **兼容性**：
  - 无 memory 目录 / 关闭记忆开关时正常启动，降级为无记忆状态；
  - 与 Step 7 上下文管理、Step 4 SP 注入、Step 5 权限系统共存无冲突；
  - 旧会话恢复不受影响（记忆索引每次启动重新 assemble 注入，不持久化到 session JSON）。

## 设计骨架

新增 `src/internal/memory/` 包作为记忆层核心，归第 4 层（记忆层）。模块划分如下：

```
src/internal/memory/           # 新增：记忆系统核心包
├── types.go                   # 记忆类型枚举、记忆记录结构、索引行结构
├── store.go                   # 文件存储：记忆文件读写 + MEMORY.md 索引读写 + 原子写入 + 路径逃逸防护
├── source.go                  # memory Source（实现 prompt.sources.Source）：合并两级索引注入上下文
├── reviewer.go                # 后台异步回顾器：构造回顾输入、独立调 LLM、解析决策、写文件刷索引
├── prompt.go                  # 回顾专用 prompt 模板（含分类/去重比对/敏感约束指令）
└── sanitizer.go               # 敏感信息正则脱敏（可选兜底）

src/internal/engine/prompt/sources/   # 复用：memory Source 在此注册到 Builder
src/internal/security/sandbox.go      # 修改：放行 .codepilot/memory 路径
src/internal/engine/conversation/     # 修改：OnLoopDone 挂载回顾器，智能节流触发
src/main.go                           # 修改：构造 memory 依赖、注册 Source、挂钩子
```

各模块职责：

- **types**：定义 4 类记忆枚举、单条记忆记录（含 type/title/content/时间戳）、MEMORY.md 索引行解析结构。
- **store**：记忆文件与 MEMORY.md 索引的持久化抽象，提供"读索引 / 写记忆文件 / 刷新索引行 / 原子写 / 路径逃逸防护"等能力，是回顾器与 Source 的共同依赖。
- **source**：实现 `prompt.Source` 接口，会话启动时读取用户级 + 项目级两个 MEMORY.md，合并、截断、包标签后作为 LeadUserMessage 注入，与 AGENTS.md Source 同构。
- **reviewer**：后台异步回顾器。监听 `OnLoopDone`，满足节流条件时构造本轮快照 → 独立无状态调 LLM → 解析"新建/覆盖"决策 → 通过 store 写文件刷索引。per-session 串行 + panic recover + 静默降级。
- **prompt**：回顾专用 prompt 模板，指令 LLM 按四分类判断、比对待覆盖的已有索引项、输出结构化决策、禁止记录敏感凭证。
- **sanitizer**：对记忆正文做敏感信息正则脱敏的可选兜底（首版以 prompt 约束为主）。

## Out of Scope（本步骤不做）

- **语义检索 / 向量化召回**：本步骤仅做"索引注入 + 按需读取"的轻量召回，不做 RAG / 向量库 / 相关性评分排序。
- **跨用户共享 / 云端同步**：记忆纯本地，不涉及多用户共享或云端同步。
- **记忆的手动编辑 UI**：用户可直接手动编辑 md 文件与 MEMORY.md，不提供专门的可视化编辑界面。
- **记忆过期 / 淘汰策略**：不做 TTL / LRU 等自动淘汰（后续可选）。
- **MCP 协议暴露记忆能力**：不将记忆作为 MCP server 暴露给外部。
- **回顾模型独立配置的热切换**：`memory.review_model` 配置项预留字段，首版固定复用主 provider/主模型，不实现运行时切换。
