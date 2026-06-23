// Package slash 提供 CodePilot 的 slash 命令抽象层。
//
// 背景：Step 9 把 /new、/sessions、/resume、/clear、/compact、/dump
// 这 6 条斜杠命令以前端硬编码数组（src/internal/interaction/web/static/app.js
// 中的 SLASH_COMMANDS）+ 后端独立 handler 的方式接入。每新增一条命令都
// 需要同步修改前后端两处，存在易遗漏的双写风险。
//
// 关键约束：本包不 import web 包（即 src/internal/interaction/web）。
// 这样 Step 10 Skill 系统可以在零依赖 web 层的前提下注册自己的 slash 命令。
// web 层依赖本包（按从下到上的依赖方向），形成单向引用链。
//
// 设计要点：
//   - SlashCommand 接口风格与 tool.Tool 对齐（同样的元数据 + Execute 模式）。
//   - Registry 负责命令的注册、查找、列表与变化通知，与 tool.Registry 同构。
//   - 业务逻辑（handleNewSession / handleClearSession 等）由 builtin 命令通过
//     Execute 方法直接复用，handler 函数体零改动。
//   - 变化通知（OnChange）在本步骤仅注册回调，Skill 动态注册留到 Step 10
//     触发 slash_commands_updated 推送。
//
// 接入方式：
//   - 内置命令：main.go 调用 slash.RegisterBuiltin(registry, handler) 一行注册。
//   - 第三方（如 Skill）：实现 SlashCommand 接口后调 registry.Register(cmd) 即可。
//
// 与 tool 包的对比：
//   - tool.Tool 由 LLM 通过 tool_use 协议调用；SlashCommand 由用户通过
//     前端 UI（输入 / 弹出下拉）触发，不经过 LLM。
//   - tool.Tool 的 Execute 入参是 JSON 字节；SlashCommand 的 Execute 入参
//     是 WebSocket 连接 + 可选 arg 字符串。
package slash
