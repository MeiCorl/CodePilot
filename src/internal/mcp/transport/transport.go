// Package transport 提供 MCP 客户端的传输层抽象。
//
// 设计原则：
//   - Transport 是面向「连接」的双向字节流抽象
//   - 消息是单行 JSON（不含换行），传输层负责自动追加 '\n' 以适配 JSONL 协议
//   - Recv 在对端正常关闭时返回 io.EOF，其他错误代表异常断开
//   - Close 是幂等的，多次调用不报错
//
// 两种实现：
//   - stdio: 本地子进程，stdin/stdout 双向 JSONL
//   - http:  远程 HTTP，POST 单次请求-响应（MCP Streamable HTTP 同步模式）
package transport

import (
	"context"
	"errors"
)

// Message 是单条 JSON-RPC 消息的字节序列（不含末尾换行）。
//
// 别名而非新类型：方便调用方用 bytes / string 直接赋值。
type Message = []byte

// ErrClosed 在 Transport 已 Close 后再调用 Send/Recv 时返回。
//
// 不复用 io.ErrClosed 以避免不同 Go 标准库版本下的可见性差异。
var ErrClosed = errors.New("transport: 已关闭")

// ErrNotConnected 在未 Connect 的 Transport 上调用 Send/Recv 时返回。
var ErrNotConnected = errors.New("transport: 未连接")

// Transport 是 MCP 消息传输的抽象接口。
//
// 实现约束：
//   - Connect 失败时返回的 Transport 仍处于未连接状态，可重试 Connect
//   - Recv 在对端正常关闭时返回 io.EOF
//   - Recv 在 ctx 取消时返回 ctx.Err()
//   - Send/Recv 应该是线程安全的：允许单 Transport 上多 goroutine 并发
type Transport interface {
	// Connect 建立底层连接（启动子进程 / 探测 HTTP endpoint）。
	// 重复 Connect 应该是幂等的：已连接时直接返回 nil。
	Connect(ctx context.Context) error

	// Send 发送单条消息（自动追加 '\n'）。调用方需保证 msg 不含换行。
	// Transport 已关闭时返回 ErrClosed；底层 IO 失败返回包装错误。
	Send(msg []byte) error

	// Recv 阻塞读取下一条消息（自动去除末尾 '\n'）。
	// 返回 io.EOF 代表对端正常关闭；ctx 取消时返回 ctx.Err()。
	Recv() ([]byte, error)

	// Close 关闭连接。多次调用幂等，返回第一次的错误（若有）。
	Close() error

	// IsAlive 查询当前连接状态。仅反映内部状态，不进行实际探测。
	IsAlive() bool
}
