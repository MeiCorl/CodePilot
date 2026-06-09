// Package jsonrpc 实现 JSON-RPC 2.0 消息的数据结构与标准错误码。
// 规范参考：https://www.jsonrpc.org/specification
//
// 消息分三类：
//   - Request       有 id，server 必须返回 Response
//   - Notification  有 method 但无 id，server 不回应
//   - Response      对 Request 的回应，Result 与 Error 互斥
//
// 编码约定：
//   - 所有消息 JSON 字段名严格小写
//   - ID 类型统一为 string（CodePilot 内部统一字符串 id，避免 number 精度问题）
//   - 序列化产物为单行 JSON（不含末尾换行），由传输层负责追加 '\n'
package jsonrpc

import "encoding/json"

// Version 是 JSON-RPC 协议版本号，所有消息必须固定为 "2.0"。
const Version = "2.0"

// MessageKind 区分三种消息类型，供调用方在 UnmarshalMessage 后做强类型断言。
type MessageKind int

const (
	// KindUnknown 未识别类型。
	KindUnknown MessageKind = iota
	// KindRequest 带 id 的方法调用。
	KindRequest
	// KindNotification 无 id 的单向消息。
	KindNotification
	// KindResponse 对 Request 的回应。
	KindResponse
)

// Request 是带 id 的方法调用，server 必须返回 Response。
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Notification 是不需要回应的单向消息。
type Notification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Error 是 Response 失败时的错误结构。
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Response 是 Request 的回应，Result 与 Error 互斥（不会同时存在）。
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// 标准错误码（JSON-RPC 2.0 规范第 5.1 节）。
// 错误码是有符号整数，范围约定：
//   - -32768 ~ -32000：预定义错误
//   - -32000 ~ -32099：服务端实现自定义错误
//   - 其他正/负数：完全自定义
const (
	// CodeParseError JSON 解析失败。
	CodeParseError = -32700
	// CodeInvalidRequest 请求结构不合法（如缺少 jsonrpc 字段）。
	CodeInvalidRequest = -32600
	// CodeMethodNotFound 方法不存在。
	CodeMethodNotFound = -32601
	// CodeInvalidParams 方法参数不合法。
	CodeInvalidParams = -32602
	// CodeInternalError 服务端内部错误。
	CodeInternalError = -32603

	// CodeServerErrorMin / CodeServerErrorMax 预留给实现自定义服务端错误。
	CodeServerErrorMin = -32099
	CodeServerErrorMax = -32000
)
