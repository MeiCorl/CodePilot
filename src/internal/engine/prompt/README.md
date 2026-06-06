# `engine/prompt` — System Prompt 组装模块

> 本模块属于架构第 2 层「引擎层」的「提示词」子模块，负责把多个来源（静态规则、环境上下文、AGENTS.md、自动记忆等）组合成最终发送给 LLM 的 System Prompt。

## 设计动机

经过 Step 1～3，LLM 的行为风格飘忽不定，根因是 System Prompt 只是一段临时塞进 `system` 字段的字符串。本模块用一套**分层、可组合、可扩展**的管线取代之：

- **稳定部分**（静态规则 + 环境上下文）进 `system` 字段 → 可被 Anthropic 缓存
- **可变部分**（AGENTS.md + 自动记忆）进首条 `user` 消息 → 避免注意力稀释
- **可观测**：每层 token 数有据可查，WebUI 状态栏可展示
- **可扩展**：新增 SP 来源只需实现 `Source` 接口并注册

## 数据流

```
        ┌──────────────────────────────────────────────────────┐
        │                  WebUI Handler                        │
        │  NewSession / ResumeSession 启动时采集 Env:            │
        │    OS, CWD(resolve), GitStatus, Date, Version         │
        └────────────────────────┬─────────────────────────────┘
                                 │
                                 ▼
        ┌──────────────────────────────────────────────────────┐
        │   prompt.NewBuilder(static, environment, agents_md,  │
        │                    memory).Assemble(ctx, env)         │
        │                                                      │
        │   Source order:                                       │
        │     1. static        → PlacementSystem                │
        │     2. environment   → PlacementSystem                │
        │     3. agents_md     → PlacementUserMessage           │
        │     4. memory        → PlacementUserMessage           │
        └────────────────────────┬─────────────────────────────┘
                                 │
                                 ▼
        ┌──────────────────────────────────────────────────────┐
        │             SystemPrompt                              │
        │   SystemBlocks:    []SystemBlock{Text, Cacheable}     │
        │   LeadUserMessage: string                             │
        │   Stats:           []SourceStat{Name, Tokens}         │
        │   TotalTokens:     int                                │
        └────────────────────────┬─────────────────────────────┘
                                 │
                                 ▼
        ┌──────────────────────────────────────────────────────┐
        │            LLM Provider                               │
        │   Anthropic: SystemBlocks → 多段 system + cache_control│
        │              LeadUserMessage → messages[0]            │
        │   OpenAI:    SystemBlocks → 拼为单个 system 字符串      │
        │              LeadUserMessage → messages[0]            │
        └──────────────────────────────────────────────────────┘
```

## 目录结构

```
src/internal/engine/prompt/
├── README.md            ← 本文件
├── builder.go           ← Builder 主流程（Assemble + 分组 + token 累计）
├── template.go          ← 模板变量 {{OS}}/{{CWD}}/... 渲染
├── sources/
│   ├── source.go        ← Source 接口 + Section/Env/SystemPrompt 结构
│   ├── static.go        ← 硬编码静态规则（角色/行为/代码/工具/安全）
│   ├── environment.go   ← OS / CWD / Git 状态采集
│   ├── agents_md.go     ← 全局 + 项目级 AGENTS.md 合并
│   └── memory.go        ← 自动记忆占位（Step 8 接入）
├── render/
│   ├── split_system.go  ← 拆分为 Anthropic 多段 / OpenAI 单段
│   └── cache_mark.go    ← cache_control 标记
└── tokens/
    └── estimate.go      ← token 估算
```

## 核心类型

| 类型 | 职责 |
|------|------|
| `Source` | 内容来源抽象接口；`Name() + Assemble(ctx, env) (Section, error)` |
| `Section` | 单个 Source 的一段产出；含 `Name/Content/Placement/Tokens` |
| `Placement` | `PlacementSystem`（进 system 字段）或 `PlacementUserMessage`（进首条 user 消息） |
| `Env` | Source 接收的输入；`OS/CWD/GitStatus/Date/Version/StaticOverrides` |
| `SystemPrompt` | Builder 产出；`SystemBlocks/LeadUserMessage/Stats/TotalTokens` |
| `Builder` | 串联多个 Source，按 Placement 分组，拼成 SystemPrompt |

详见 `sources/source.go` 顶部的类型注释。

## 扩展指南

### 新增一个 SP 来源

1. 在 `sources/` 下新建 `xxx.go`，实现 `Source` 接口：

   ```go
   type mySource struct{}
   func (s *mySource) Name() string { return "my_source" }
   func (s *mySource) Assemble(ctx context.Context, env sources.Env) (sources.Section, error) {
       return sources.Section{
           Name:      "my_source",
           Content:   sources.Render("current OS is {{OS}}", env),
           Placement: sources.PlacementSystem,  // 或 PlacementUserMessage
           Tokens:    tokens.Estimate("..."),
       }, nil
   }
   ```

2. 在 `cmd/codepilot` 或调用方注册：

   ```go
   builder := prompt.NewBuilder(
       &staticSource{},
       &environmentSource{},
       &agentsMDSource{},
       &memorySource{},
       &mySource{},  // ← 加这里
   )
   ```

3. 在 `PROGRESS.md` 与本 README 的「目录结构」同步登记。

### 关闭整个 System Prompt

`config.json` 中设置 `system_prompt.enabled = false`，Builder 会用空 Source 列表初始化，Assemble 直接返回零值 SystemPrompt（`IsEmpty() == true`）。Provider 收到空 SP 后跳过 system 字段构造。

## 缓存策略

Anthropic 协议下，`SystemBlock.Cacheable=true` 的段会被打 `cache_control` 标记，触发 server-side prompt cache。配置示意：

| 段 | Cacheable | 说明 |
|----|-----------|------|
| 静态 SP（static） | ✅ | 内容固定不变，命中率最高 |
| 环境上下文（environment） | ✅ | 同会话内不变；跨会话可能变（cwd 切换） |
| AGENTS.md | ❌ | 进 user 消息不进 system，不参与缓存 |
| 自动记忆 | ❌ | 同上 |

> 缓存 TTL 5min；多轮迭代中只要 content 不变就命中，显著降本降延迟。

## 测试

```bash
go test ./src/internal/engine/prompt/...
```

各 Task 的测试覆盖范围见 `docs/step4-System Prompt设计/checklist.md`。

## 相关文档

- [docs/step4-System Prompt设计/spec.md](../../../docs/step4-System Prompt设计/spec.md) — 设计规格
- [docs/step4-System Prompt设计/tasks.md](../../../docs/step4-System Prompt设计/tasks.md) — 任务拆分
- [docs/step4-System Prompt设计/checklist.md](../../../docs/step4-System Prompt设计/checklist.md) — 验收清单
- [HARNESS/PROJECT.md](../../PROJECT.md) — 整体架构
- Anthropic Prompt Caching 文档：https://docs.anthropic.com/en/docs/build-with-claude/prompt-caching
