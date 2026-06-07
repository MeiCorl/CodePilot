# Step 1 — LLM 接入与 TUI 交互(TUI功能在step1.1中国已重构为WebUI）

## 背景

CodePilot 是一个终端 AI Coding Agent（类似 Claude Code），需要从零构建。第一步的核心目标是**打通 LLM 通信链路**，让用户能在终端中与 LLM 进行多轮对话，验证"输入 → LLM → 流式输出"这条核心链路跑通，为后续工具调用、Agent Loop 等高级功能奠定基础。

## 目标用户

开发者，在终端环境中使用 CodePilot 进行 AI 辅助编程。

## 能力清单

1. **TUI 界面启动**：用户在终端执行 `codepilot` 命令，启动 Bubble Tea TUI 界面
2. **品牌展示**：启动时展示 CodePilot 的 ASCII 猫头鹰 Logo、系统名称和版本号
3. **底部状态栏**：实时展示当前使用的模型名称、会话上下文窗口剩余额度（token 估算）
4. **用户输入**：支持在 TUI 中输入多行文本消息，Enter 发送，Shift+Enter 换行
5. **流式响应**：LLM 回复以流式方式逐字输出，带打字机效果
6. **流式中断**：LLM 回复过程中，用户按 Esc 可中断当前响应；已输出部分保留为一条完整的助手消息，用户可继续下一轮输入
7. **Markdown 渲染**：LLM 回复内容使用 Glamour 做终端 Markdown 渲染（代码块高亮、粗体、列表缩进等）
8. **统一消息格式**：内部采用 ContentBlock 数组表示消息内容（本步骤仅实现 TextBlock），各 Provider 适配器负责将内部格式转换为对应 SDK 消息格式
9. **多模型供应商支持**：支持 Anthropic（Claude 系列）和 OpenAI（GPT 系列），通过 Provider 抽象接口实现，后续可扩展
10. **配置文件驱动**：通过 `~/.codepilot/setting.json` 配置模型供应商、模型名称、API 地址、密钥等，支持切换供应商
11. **会话持久化**：对话历史自动保存到本地文件，每个会话对应一个 JSON 文件，存储在 `~/.codepilot/sessions/` 目录下
12. **多会话管理**：支持创建新会话、列出历史会话、切换到指定会话恢复对话。用户通过输入 `/sessions`（列出会话）、`/new`（新建会话并清空当前对话窗口）、`/resume <id>`（恢复会话）进行操作，类似 Claude Code 的 `/resume`
13. **上下文滑窗**：采用最简单的滑动窗口策略管理上下文，预留 System Prompt 空间（约 20%），超出时丢弃最早的消息对
14. **API 超时与重试**：请求超时默认 60 秒（可配置）；网络错误和 5xx 服务端错误自动重试最多 2 次；认证错误（401）和限流错误（429）不重试、直接提示用户
15. **优雅退出**：Ctrl+C 退出时，确保当前会话已持久化到磁盘后再退出，避免对话丢失
16. **日志系统**：文件日志写入 `~/.codepilot/logs/`，记录 API 请求/响应摘要、错误信息、关键状态变化，TUI 界面不展示日志

## 非功能要求

1. **性能**：流式响应首 token 延迟取决于 LLM 供应商网络，本地处理不引入额外感知延迟
2. **可用性**：LLM API 调用失败时，在 TUI 中展示友好错误提示，不崩溃退出，用户可继续输入
3. **安全性**：API Key 等敏感信息禁止明文打印到终端输出，配置文件权限建议仅用户可读
4. **兼容性**：支持 Windows 10+、macOS、Linux 主流终端
5. **可扩展性**：Provider 接口设计需预留工具调用（tool_use / function_calling）扩展点；ContentBlock 设计需预留图片/文件等输入类型扩展点；本步骤仅实现文本对话
6. **可靠性**：日志系统不阻塞主流程，异步写入；重试机制带退避策略避免雪崩

## 设计骨架

```
codepilot/
├── src/
│   ├── main.go                  # 程序入口
│   ├── internal/
│   │   ├── interaction/         # 第1层：交互层
│   │   │   └── tui/             # TUI 实现
│   │   │       ├── app.go       # Bubble Tea 主模型（Model/Update/View）
│   │   │       ├── logo.go      # 猫头鹰 Logo ASCII Art
│   │   │       ├── statusbar.go # 底部状态栏组件
│   │   │       └── message.go   # 自定义 Bubble Tea 消息类型
│   │   ├── engine/              # 第2层：引擎层
│   │   │   ├── conversation/    # 对话管理
│   │   │   │   └── manager.go   # 对话历史管理、消息构造
│   │   │   └── prompt/          # 提示词管理
│   │   │       └── system.go    # System Prompt 模板
│   │   ├── memory/              # 第4层：记忆层
│   │   │   ├── context/         # 上下文管理
│   │   │   │   └── window.go    # 滑动窗口策略
│   │   │   └── session/         # 会话管理
│   │   │       └── session.go   # 会话持久化、恢复
│   │   ├── config/              # 配置加载
│   │   │   └── config.go        # 配置文件读取与解析
│   │   └── logger/              # 日志系统
│   │       └── logger.go        # 文件日志初始化与写入
│   └── llm/                     # LLM 供应商抽象
│       ├── provider.go          # Provider 统一接口定义
│       ├── anthropic.go         # Anthropic 适配器（含消息格式转换）
│       ├── openai.go            # OpenAI 适配器（含消息格式转换）
│       └── types.go             # 通用消息类型（ContentBlock 体系）
├── config/
│   └── setting.example.json      # 配置文件示例
├── docs/
│   └── step1-LLM打通/           # 本步骤设计文档
├── go.mod
└── go.sum
```

## Out of Scope（本步骤不做）

1. 工具调用（tool_use / function_calling）—— Step 2 实现
2. Agent Loop（ReAct 推理循环）—— Step 3 实现
3. 完整 System Prompt 设计 —— Step 4 实现
4. 权限系统 —— Step 5 实现
5. MCP 协议 —— Step 6 实现
6. 高级上下文管理（摘要压缩、缓存策略）—— Step 7 实现
7. 自动记忆系统 —— Step 8 实现
8. 快捷命令系统（/help、/clear 等斜杠命令）—— Step 9 实现
9. Skill 系统 —— Step 10 实现
10. Hook 系统 —— Step 11 实现
11. SubAgent —— Step 12 实现
12. 多模态输入（图片输入、文件引用等）—— 记入 Todo List，ContentBlock 预留扩展位，后续实现
13. 沙箱隔离 —— Step 5 安全层实现
14. 多语言国际化

## Todo List（后续待实现）

1. 多模态输入：支持图片输入、文件引用等非文本输入形式（ContentBlock 扩展 ImageBlock 等）

