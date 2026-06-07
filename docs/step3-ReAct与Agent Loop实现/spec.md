# Step 3 — ReAct 与 Agent Loop 实现

## 背景

Step 2 中 CodePilot 实现了**单轮工具调用闭环**：LLM 发出一次 `tool_use` → 执行工具 → `tool_result` 回传 → 二次 LLM 拿到最终回复。但面对复杂任务（如「重构这个模块并补全测试」），单轮闭环远远不够——Agent 需要自主决定「读文件 → 分析 → 修改 → 再读验证 → 再修改」的多步迭代。

当前核心限制：
1. `RunTurn()` 硬编码为两轮 LLM 调用，无法循环
2. `StreamChunk` 只携带单个 `ToolUseBlock`，不支持模型一次返回多个工具调用
3. 无迭代上限保护，理论上可能无限循环
4. 无上下文 token 溢出检查，工具调用可能快速耗尽上下文窗口
5. 中断处理不完整，无法保留已完成迭代的进度

本步骤将实现 ReAct（Reasoning + Acting）模式的 Agent Loop，使 CodePilot 具备自主完成复杂任务的核心能力。

## 目标用户

CodePilot 的终端用户（开发者），通过 WebUI 输入复杂编码任务，期望 Agent 自主拆解、执行、验证，直到任务完成。

## 能力清单

### 核心能力

1. **ReAct 循环引擎**：将「LLM 推理 → 工具调用 → 结果反馈」升级为可循环迭代的 Agent Loop，直到 LLM 认为任务完成（无 `tool_use`）或触发终止条件
2. **多工具并行调用**：支持模型一次返回多个 `tool_use`，按工具权限分类执行——只读工具并行、写入/执行工具串行排队
3. **迭代上限保护**：默认最大 25 次迭代（一次 LLM 调用 + 工具执行 = 一次迭代），达到上限后注入提示让模型优雅收尾
4. **上下文 token 溢出保护**：每次迭代前检查剩余 token，空间不足时注入提示让模型总结当前进展并回复用户
5. **优雅中断与进度保留**：用户中断（Ctrl+C / 点击停止）时，保留已完成迭代的所有消息到会话历史，支持后续恢复
6. **工具错误智能反馈**：工具执行失败时，将错误信息作为 `ToolResult(IsError=true)` 反馈给 LLM，由 LLM 自主决定重试、换策略或向用户报告

### 防护能力

7. **死循环防护**：通过迭代上限 + 上下文溢出检查双重保障，避免 Agent 陷入无限循环
8. **LLM 生成异常处理**：流式响应中发生 API 错误时，通过错误回调通知上层，中断循环并保留已有进度
9. **工具执行异常处理**：工具超时、panic、被取消等异常均捕获并转为 `ToolResult(IsError=true)`，不中断循环

## 非功能要求

1. **性能**：只读工具并行执行，减少等待时间；迭代间避免不必要的内存分配
2. **可观测性**：每次迭代通过事件流向外推送迭代进度（当前第几轮），便于 WebUI 展示；日志记录每轮迭代的工具调用数、耗时等关键指标
3. **向后兼容**：新的 AgentLoop 接口完全替代旧的 `RunTurn`，调用方（handler.go）无感切换；会话持久化格式不变，已有的 `tool_use` / `tool_result` 消息结构完全复用
4. **可配置性**：最大迭代次数、上下文安全余量等关键参数通过 `setting.json` 可配置
5. **安全**：本步骤聚焦 Agent Loop 核心逻辑，权限拦截（HITL 确认）留到 Step 5

## 设计骨架

```
src/
├── llm/
│   ├── types.go                   # [修改] StreamChunk 支持多个 ToolUseBlock
│   ├── anthropic.go               # [修改] doStream 传递所有累积的 tool_use
│   └── openai.go                  # [修改] doStream 传递所有累积的 tool_use
├── internal/
│   ├── config/
│   │   └── config.go              # [修改] 新增 Agent Loop 配置字段
│   ├── engine/
│   │   └── conversation/
│   │       ├── manager.go         # [重构] RunTurn → AgentLoop，循环迭代逻辑
│   │       ├── tool_handler.go    # [修改] 新增 ExecuteBatch 批量执行方法
│   │       └── agent_loop.go      # [新建] Agent Loop 核心逻辑（循环控制、异常处理、策略）
│   ├── tool/
│   │   └── tool.go                # [不变] Permission 枚举已有，无需修改
│   └── interaction/
│       └── web/
│           ├── handler.go         # [修改] runStream 适配 AgentLoop 新接口
│           └── protocol.go        # [修改] 新增迭代进度事件类型
```

## Out of Scope（本步骤不做）

1. **完整上下文管理**（Step 7）：本步骤仅做简单的 token 估算 + 溢出检查，不做上下文摘要压缩、分层缓存等高级管理
2. **权限确认拦截（HITL）**（Step 5）：本步骤不实现工具执行前的用户确认弹窗
3. **System Prompt 设计**（Step 4）：本步骤复用现有 system prompt，不做专门的 Agent Loop 指令设计
4. **SubAgent 并行调度**（Step 12）：Agent Loop 中的并行仅限于同一轮的多个工具调用，不涉及子代理派生
5. **MCP 外部工具**（Step 6）：并行/串行策略仅针对内置工具，MCP 工具的执行策略留到 MCP 集成时处理
6. **WebUI 交互大改**：前端改动仅限于支持新的事件类型（迭代进度），不做整体 UI 重构
