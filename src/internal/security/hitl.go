package security

import "context"

// HITLCallback 是人在回路（Human-in-the-Loop）确认的回调函数类型。
//
// 当权限检查结果为 Ask 时，拦截器通过此回调向用户请求确认。
// 实现方（如 WebUI Handler）负责：
//   - 向前端推送 permission_request WebSocket 消息
//   - 等待用户操作（超时由调用方通过 ctx 控制）
//   - 返回用户的决策
//
// 参数：
//   - ctx: 支持超时控制，超时后回调应返回错误
//   - req: 权限确认请求详情（工具名、参数摘要、原因）
//
// 返回值：
//   - PermissionResponse: 用户的决策（允许/拒绝 + 授权范围）
//   - error: 回调执行失败时非 nil（如超时、连接断开）
type HITLCallback func(ctx context.Context, req PermissionRequest) (PermissionResponse, error)
