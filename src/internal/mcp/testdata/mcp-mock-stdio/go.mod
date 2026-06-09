// mcp-mock-stdio 是 CodePilot Step 6 端到端测试专用的 stdio mock MCP server。
//
// 职责：
//   - 读 stdin JSON-RPC 2.0 消息
//   - 识别 initialize / notifications/initialized / tools/list / tools/call / ping
//   - 写出符合 MCP 2025-03-26 规范的 Response（stdout JSONL）
//   - 暴露 2 个测试工具：echo / add
//
// 与 CodePilot 主 module 解耦：独立 go.mod，避免测试时把 CodePilot 内部包
// 拉入 mock server 的依赖图。
module github.com/MeiCorl/CodePilot/src/internal/mcp/testdata/mcp-mock-stdio

go 1.26.1
