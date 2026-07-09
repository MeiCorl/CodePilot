---
name: auto-specs
description: Auto Spec 工作流技能 —— 需求澄清、任务拆解、功能验证三文档生成（spec/tasks/checklist）；文档确认一次后自动按依赖并行派发 Agent 完成所有 task，适用于 /auto-specs 新需求或断点续做。
---
# Auto Spec 工作流技能
`/auto-specs` 是 `specs` 的全自动版本：文档生成阶段与 `specs` 相同；生成 `spec.md`、`tasks.md`、`checklist.md` 后只等用户做一次整体确认；确认后主会话只负责编排，不亲自写代码，按 `tasks.md` 依赖关系自动派发 `general-purpose` Agent，一口气跑完所有 task。
- `specs`：每完成一个 task 都停下等用户确认。
- `auto-specs`：三份文档确认一次后，后续 task 自动推进；仅 Agent `blocked` 时停下。
- 无前置依赖或依赖已完成的 task 可并行派发多个 Agent；每个 Agent 独立上下文，靠三份 md 文档获取任务信息。
## 适用场景
| 场景 | 判定 | 动作 |
| --- | --- | --- |
| 新需求 | 用户提出新功能且无对应 spec 文档 | 需求澄清 → 生成三份文档 → 阶段 A 确认 → 并行编排 |
| 断点续做 | 用户说“第 N 步/继续上次/调整之前工作” | 读三份文档 → 汇报状态 → 阶段 A 确认 → 并行编排 |

step 目录规则：扫描 `docs/step*`；优先匹配项目预定义计划名（见 `.harness/PROJECT.md`、`.harness/PROGRESS.md`）；已存在则复用，否则按编号创建。计划外新功能由技能拟定编号，子任务可用 `stepX.Y`。
## 需求澄清
每轮最多问 3 个具体问题，不假设答案；关键点足够清晰后告知“需求已足够清晰，准备生成文档”。优先澄清：
1. 核心问题：痛点、目标用户。
2. 能力边界：做什么、不做什么。
3. 交互方式：如何触发、如何反馈。
4. 边缘场景：异常输入、失败处理。
5. 现有关系：依赖、影响、兼容。
6. 非功能要求：性能、安全、兼容性。
## 三份文档规范
文档落盘到 `docs/{step_n-idea_name}/`。所有文档、提问、汇报默认中文。
### spec.md
说明“做什么/不做什么/做到什么程度算完成”。必须包含：
- `# Step N — {功能名称}`
- `## 背景`
- `## 目标用户`
- `## 能力清单`
- `## 非功能要求`
- `## 设计骨架`（目录结构示意）
- `## Out of Scope`
红线：不要写具体函数名、参数名、默认值、错误文本、行号、SDK 类型名等易过期实现细节。
### tasks.md
说明“怎么做/按什么顺序/动什么文件”。要求：
- 3~10 个 task，每个能一次专注会话完成。
- 每个 task 标注：状态（`待完成`/`进行中`/`已完成`）、目标、影响文件、依赖任务、具体内容、参考资料。
- 依赖必须显式：无依赖写 `无`；多个依赖写 `Task X, Task Y` 并说明原因。并行编排以此判断 ready tasks。
- 能独立落地的能力尽量拆成无共享前置依赖的小 task；主流程接入、端到端验证、跨模块收口、公共接口定型应声明依赖。
- 最后两个 task 固定为：`接入主流程`、`端到端验证`。
- 状态管理：开始前改 `进行中`；通过验证后改 `已完成`；实际编辑 `tasks.md`。
Task 模板：
```markdown
## Task K: {任务名称}
**状态**: 待完成
**目标**: {一句话描述}
**影响文件**:
- `path/to/file.go` — 新建/修改，{说明}
**依赖**: 无 / Task X ({原因})
**具体内容**:
1. {步骤1}
2. {步骤2}
**参考资料**:
- {SDK 文档 / 函数签名 / 行号引用}
```
### checklist.md
说明“怎么验证/验收项是什么”。要求：
- 每项可勾选、可观测，避免“实现完整”等模糊表述。
- 包含「预期」「实际」「结论」。
- spec 中不宜出现的具体值（错误文本、默认值、阈值）放到验收项。
- 至少一条端到端验收。
```markdown
- [ ] {功能点描述}
  - 预期: {具体行为或结果}
  - 实际: {验证时填写}
  - 结论: {通过/不通过}
```
## 断点续做
1. 定位目录：用户说“第 N 步”则找 `docs/step{N}-*/`；说功能名则模糊匹配；找不到列出已有步骤让用户选。
2. 顺序阅读 `spec.md`、`tasks.md`、`checklist.md`。
3. 汇报当前步骤、已完成 task、下一批可执行 task、checklist 通过情况。
4. 直接进入阶段 A，让用户做一次整体确认后自动跑。
## 自动派发编排
### 阶段 A：唯一确认点
1. 输出“文档已就绪”小结：task 总数、入口目录、三份文档覆盖范围。
2. 明确提示：回复“确认 / OK / 继续 / go”即按依赖并行派发 Agent，一口气跑完所有 task；如需调整，指出章节/task/验收项。
3. 等待用户回复，这是流程中唯一固定等待点。
4. 收到确认后进入编排模式，不再询问。
5. 若用户要求改文档，修改后回到本阶段第 1 步重新确认。
### 阶段 B：并行主循环
```text
loop:
  1. 扫描 tasks.md，解析每个 task 的状态、依赖、影响文件
  2. 全部已完成 → 阶段 C
  3. 计算 ready tasks：状态为「待完成」；依赖为「无」或依赖 task 均「已完成」；不在 running 集合
  4. 没有 ready tasks：若有「进行中」则等当前批次返回；否则按 blocked 处理
  5. 按 B.1 选择一批 ready tasks 并行派发
  6. 等待本批 Agent 返回，按 D.3 处理
```
#### B.1 并行批次选择
- 默认并行窗口 `3`；任务明显独立且影响文件无重叠可到 `4`；共享核心文件、公共接口或同一 checklist 区块时降为 `1~2`。
- 同批优先选择编号较小、依赖层级相同、影响文件不重叠的 ready tasks。
- `接入主流程`、`端到端验证` 默认依赖前面所有实现类 task，除非 `tasks.md` 明确说明可提前执行。
- 派发前记录本批编号，必要时先统一标记为「进行中」，避免重复派发。
- 简洁日志示例：`[Auto-Specs] → 并行派发 3 个 Agent: Task 1, Task 2, Task 3`；`[Auto-Specs] ← Task 2 completed (checklist X/Y 通过)`。
### 阶段 C：整步收尾
1. 同步 `.harness/PROGRESS.md`：更新总览、追加已完成步骤、从待完成步骤删行、调整架构层覆盖度，并在最终汇报说明已更新。
2. 输出整步总结：task 总数、最终 commit hash、能力清单摘要。
3. 自然终止。
## Agent 派发规范
### D.1 Agent 类型
使用 `general-purpose` Agent（全工具）；不要用 `Explore` 或 `Plan`。
### D.2 Prompt 模板
```text
你是 Auto-Specs 工作流中的 Task Worker。主会话已生成三份 md 文档，派发你独立完成 Task {K}（{task_name}）。本工作流可能同时派发多个 Worker。
任务坐标：
- 工作目录: docs/{step_n-idea_name}/
- 主项目根: {项目根绝对路径}
必读：
1. {项目根}/docs/{step_n-idea_name}/spec.md
2. {项目根}/docs/{step_n-idea_name}/tasks.md（只执行 Task {K}）
3. {项目根}/docs/{step_n-idea_name}/checklist.md（只更新 Task {K} 对应项）
流程：
1. 校验 Task {K} 状态仅为「待完成」或「进行中」，且依赖均「已完成」；否则返回 blocked。
2. 将 Task {K} 标为「进行中」（若主会话已标记则确认即可）。
3. 按 Task {K} 具体内容实现；SDK 使用必须查最新文档，不凭记忆。
4. 按 checklist 验证并填写「实际」「结论」。
5. 全部验证通过才将 Task {K} 标为「已完成」；否则保持「进行中」。
6. 可在主项目根提交：git add -A && git commit -m "Step {N} Task {K}: {task_name}"；无 git 可跳过。
返回严格 JSON：
{
  "task": "{K}",
  "name": "{task_name}",
  "status": "completed" | "blocked",
  "files_changed": ["path/relative/to/repo/root"],
  "checklist": {"passed": 0, "failed": 0, "items": [{"desc": "", "result": "pass" | "fail", "note": ""}]},
  "commit_hash": "<无 git 时为 null>",
  "notes": "实现要点 / 遗留问题 / blocked 原因"
}
硬约束：
- 使用简体中文。
- 遵循 .harness/PROGRESS.md、架构分层与系统设计规则。
- 不修改 Task {K} 范围外内容，不修改 .harness/PROGRESS.md。
- 并行执行时只改自己负责的代码、Task 状态段、对应 checklist 项；写入前重读相关 md，避免覆盖其他 Worker。
- 发现并发冲突或影响正确性时，不重写他人内容，返回 blocked 并说明冲突文件/原因。
```
### D.3 返回处理
| Agent 状态 | checklist | 主会话动作 |
| --- | --- | --- |
| `completed` | 全部通过 | 确认对应 task 为「已完成」，重算 ready tasks 并继续派发 |
| `completed` | 有失败项 | 回退为「进行中」，重派同 task 修复 Agent |
| `blocked` | — | 停止派发新 Agent；等待已派发同批返回后汇报阻塞，等用户决策 |
| 调用失败 | — | 重试一次；仍失败按 blocked 处理 |
## 异常与通用规则
- Agent blocked：停止新派发；等待同批已派发 Agent 返回并持久化状态后，询问用户接受/调整/终止。
- 并行写入冲突：保留已完成结果，冲突 task 维持「进行中」或回退「待完成」，作为 blocked 汇报。
- 用户中途叫停：依赖 `tasks.md` 状态持久化；下次 `/auto-specs continue` 断点续做。
- 用户要求改 spec/tasks/checklist：停止编排，修改后回到阶段 A 重新确认。
- Agent 谎报通过：主会话回退并重派。
- Agent `completed` 是唯一推进条件；`blocked` 必须停下等用户决策。
