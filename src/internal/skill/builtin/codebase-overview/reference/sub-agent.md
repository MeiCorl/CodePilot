# SubAgent — CodePilot 实现原理(STUB)

> 状态:**规划中,尚未实现** | 目标 Step:12 | 预计架构层:第 3 层 工具层

## §1 规划背景

SubAgent 是 CodePilot 5 层架构「工具层」规划中的子代理系统,由主 Agent 派生出独立子代理执行子任务,支持并行调度、上下文隔离与结果回传,用于处理复杂的多步骤工作。

按 `.harness/PROJECT.md` 的步骤计划,Step 12 作为整条流水线的最后一步,在 Hook 系统(Step 11)之后实施,本质是主 Agent 可调用的特殊「工具」,由引擎层编排调度。

## §2 规划目标

- 由主 Agent 派生独立子 Agent 执行子任务
- 子 Agent 支持并行调度与上下文隔离
- 任务结果回传主 Agent,纳入 ReAct 决策循环

(完整能力清单与一句话描述待 Step 12 启动后填入。)

## §3 当前状态

- 状态:**待开始**(`docs/step12-SubAgent/` 目录目前尚未创建,无 spec/tasks/checklist)
- 预计实施时间:见 `.harness/PROGRESS.md`「🕓 待完成步骤」表格
- 本步骤只占位说明,不涉及任何 SubAgent 实际实现

## §4 详细设计

详细设计待 Step 12 启动 `/specs` 流程后产出,届时见:

- `docs/step12-SubAgent/spec.md` — 能力清单与非功能要求
- `docs/step12-SubAgent/tasks.md` — 任务拆分
- `docs/step12-SubAgent/checklist.md` — 验收项

## §5 用户如何应对当前不可用

- 当用户问「SubAgent 怎么调用」时,告知:**CodePilot 目前未实现 SubAgent,规划在 Step 12**
- 现有「主 Agent 串行工具调用 + ReAct 循环」已覆盖大部分场景,无需 SubAgent 也能完成多步骤任务
- 若用户希望参与设计,可在 `docs/step12-SubAgent/` 下创建初始 spec 草稿