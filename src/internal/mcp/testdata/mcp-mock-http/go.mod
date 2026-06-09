// mcp-mock-http 是 CodePilot Step 6 端到端测试专用的 HTTP mock MCP server。
//
// 职责：
//   - 监听 127.0.0.1:<port>（端口由 -port 参数指定，0 表示系统自动分配）
//   - POST /mcp 端点实现 MCP Streamable HTTP 协议（application/json 响应）
//   - 启动时把实际监听地址 stdout 打印 `LISTENING: <url>` 让测试侧可读
//   - 暴露 2 个测试工具：echo / add
//
// 与 CodePilot 主 module 解耦：独立 go.mod，避免测试时把 CodePilot 内部包
// 拉入 mock server 的依赖图。
module github.com/MeiCorl/CodePilot/src/internal/mcp/testdata/mcp-mock-http

go 1.26.1
