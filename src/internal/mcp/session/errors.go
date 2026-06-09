// Package session 的错误定义。
//
// 设计原则：
//   - 错误分为「协议层」（server 返回 error 字段）与「客户端层」（连接断开 / 超时 / 关闭）
//   - 所有错误用 sentinel error（var ErrXxx = errors.New(...)）以支持 errors.Is 判断
//   - 必要时附加错误类型（如 *RPCError）以保留详细上下文（code / message）
package session

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ErrSessionClosed 在已 Close 的 Session 上调用任何 RPC 方法时返回。
//
// 与 transport.ErrClosed 区分：本错误专指 Session 整体关闭（包括
// Pool.CloseAll 触发的优雅关闭），不限于 Transport 自身的关闭。
var ErrSessionClosed = errors.New("mcp: session 已关闭")

// ErrRPCTimeout 当 ctx 携带 deadline 或调用方显式指定 timeout 时
// request 超过时限未收到响应时返回。
//
// 与 ctx 取消区分：ctx 取消返回 ctx.Err()；本错误仅在「ctx 仍有效
// 但已超过 timeout」时返回，便于上层决定是否重试。
var ErrRPCTimeout = errors.New("mcp: RPC 调用超时")

// ErrServerUnhealthy Session 进入永久 unhealthy 状态后调用任何方法时返回。
//
// 触发条件：重连策略（Task 7）连续 3 次重连失败。当前 Task 4 暂不实现
// 重连，但保留该 sentinel 以便 Task 7 直接复用。
var ErrServerUnhealthy = errors.New("mcp: server 已永久 unhealthy，请重启 CodePilot")

// ErrNoResponse 当 server 返回的 Response 缺失 result 与 error 字段时返回。
//
// 属于协议违规：正常 server 不会出现此情况，但防御性处理避免空指针。
var ErrNoResponse = errors.New("mcp: server 返回的响应既无 result 也无 error")

// RPCError 表示 server 通过 JSON-RPC error 字段返回的业务/协议错误。
//
// 与 transport 错误区分：transport 错误是连接/IO 层面问题，本错误是
// server 显式拒绝（如 method not found / invalid params / 工具执行失败）。
type RPCError struct {
	// Code JSON-RPC 错误码。
	Code int
	// Message 错误描述。
	Message string
	// Data server 自定义错误数据（可能为 nil）。
	Data json.RawMessage
}

// Error 实现 error 接口。
func (e *RPCError) Error() string {
	if len(e.Data) == 0 {
		return fmt.Sprintf("mcp: RPC error %d: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("mcp: RPC error %d: %s (data=%s)", e.Code, e.Message, string(e.Data))
}

// Unwrap 兼容 errors.Is 链。
func (e *RPCError) Unwrap() error {
	return nil
}

// newRPCError 从 server 返回的 error 字段构造 RPCError。
func newRPCError(code int, message string, data json.RawMessage) *RPCError {
	return &RPCError{Code: code, Message: message, Data: data}
}
