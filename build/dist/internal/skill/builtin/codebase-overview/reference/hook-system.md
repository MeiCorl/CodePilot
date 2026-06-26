# Hook 系统 — CodePilot 实现原理(STUB)

> 状态:**规划中,尚未实现** | 目标 Step:11 | 预计架构层:第 3 层 工具层

## §1 规划背景

Hook 系统是 CodePilot 5 层架构中「工具层」最后一个待落地的横切扩展能力,目标在工具调用与 Skill 执行等关键事件前后插入用户自定义逻辑,实现日志记录、权限拦截、结果过滤、审计等增强。

按 `.harness/PROJECT.md` 的步骤计划,Step 11 在 Step 10(Skill 系统)之后实施,接入既有工具执行链路,作为事件驱动的扩展机制。

## §2 规划目标

- 在工具 / Skill 调用前后插入自定义钩子
- 支持日志记录、权限拦截、结果过滤等场景
- 与现有 5 层架构兼容,不破坏 Step 2 工具系统与 Step 5 权限链路

(完整能力清单与一句话描述待 Step 11 启动后填入。)

## §3 当前状态

- 状态:**待开始**(`docs/step11-Hook系统/` 目录目前尚未创建,无 spec/tasks/checklist)
- 预计实施时间:见 `.harness/PROGRESS.md`「🕓 待完成步骤」表格
- 本步骤只占位说明,不涉及任何 Hook 实际实现

## §4 详细设计

详细设计待 Step 11 启动 `/specs` 流程后产出,届时见:

- `docs/step11-Hook系统/spec.md` — 能力清单与非功能要求
- `docs/step11-Hook系统/tasks.md` — 任务拆分
- `docs/step11-Hook系统/checklist.md` — 验收项

## §5 用户如何应对当前不可用

- 当用户问「Hook 系统怎么用」时,告知:**CodePilot 目前未实现 Hook 系统,规划在 Step 11**
- 若用户希望参与设计,可在 `docs/step11-Hook系统/` 下创建初始 spec 草稿
- 当前阶段的「工具前后拦截」可临时借助 Step 5 权限系统的 HITL + 黑名单实现