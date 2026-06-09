// Package jsonrpc 的编解码实现。
//
// 设计要点：
//   - 序列化为单行 JSON（不含换行），传输层负责追加 '\n' 适配 JSONL 协议
//   - UnmarshalMessage 用「轻量 peek + 完整反序列化」两阶段避免反射开销放大
//   - ID 统一为 string 类型：JSON-RPC 允许 string / number / null，
//     CodePilot 内部全部用 string，避免 JS number 精度问题与类型不一致
package jsonrpc

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sync/atomic"
)

// ErrInvalidJSON 当消息不是合法 JSON 时返回。
type ErrInvalidJSON struct {
	Raw string
}

// Error 实现 error 接口。
func (e *ErrInvalidJSON) Error() string {
	return fmt.Sprintf("jsonrpc: 无效的 JSON: %q", e.Raw)
}

// ErrInvalidMessage 当消息缺少 jsonrpc 字段或必要的 method/id/result 字段时返回。
type ErrInvalidMessage struct {
	Reason string
}

// Error 实现 error 接口。
func (e *ErrInvalidMessage) Error() string {
	return fmt.Sprintf("jsonrpc: 无效的消息结构: %s", e.Reason)
}

// MarshalRequest 把 Request 序列化为单行 JSON（不含末尾换行）。
// 序列化失败时返回错误。
func MarshalRequest(req *Request) ([]byte, error) {
	if req == nil {
		return nil, fmt.Errorf("jsonrpc: Request 不能为 nil")
	}
	if req.JSONRPC == "" {
		req.JSONRPC = Version
	}
	return json.Marshal(req)
}

// MarshalNotification 把 Notification 序列化为单行 JSON。
func MarshalNotification(n *Notification) ([]byte, error) {
	if n == nil {
		return nil, fmt.Errorf("jsonrpc: Notification 不能为 nil")
	}
	if n.JSONRPC == "" {
		n.JSONRPC = Version
	}
	return json.Marshal(n)
}

// MarshalResponse 把 Response 序列化为单行 JSON（CodePilot 作为 client 不会主动发 Response，
// 仅在单元测试与可能的 mock server 场景使用）。
func MarshalResponse(resp *Response) ([]byte, error) {
	if resp == nil {
		return nil, fmt.Errorf("jsonrpc: Response 不能为 nil")
	}
	if resp.JSONRPC == "" {
		resp.JSONRPC = Version
	}
	return json.Marshal(resp)
}

// UnmarshalMessage 解析单条 JSON 消息并按字段组合返回 Request / Notification / Response。
//
// 识别规则（按 JSON-RPC 2.0 规范）：
//   - 有 "method" 字段：Request（带 id）或 Notification（无 id）
//   - 有 "result" 或 "error" 字段：Response
//   - 其他：返回 ErrInvalidMessage
//
// 返回值类型可能是 *Request / *Notification / *Response，调用方用类型断言区分。
// 该函数对入参做完整校验：jsonrpc 字段必须为 "2.0"，否则视为非法消息。
func UnmarshalMessage(data []byte) (any, error) {
	if len(data) == 0 {
		return nil, &ErrInvalidJSON{Raw: string(data)}
	}
	// 阶段一：轻量 peek，仅解析用于判别的字段
	var peek struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id,omitempty"`
		Method  string          `json:"method,omitempty"`
		Result  json.RawMessage `json:"result,omitempty"`
		Error   *Error          `json:"error,omitempty"`
	}
	if err := json.Unmarshal(data, &peek); err != nil {
		return nil, &ErrInvalidJSON{Raw: string(data)}
	}
	if peek.JSONRPC != Version {
		return nil, &ErrInvalidMessage{Reason: fmt.Sprintf("jsonrpc 字段应为 %q, 实际 %q", Version, peek.JSONRPC)}
	}
	switch {
	case peek.Method != "":
		// 阶段二：完整反序列化
		// 注意：id 字段类型不确定（string / number），先用 json.RawMessage 中转再 normalizeID，
		// 避免直接 unmarshal 到 string 字段时遇到 number 类型报错。
		if isNullID(peek.ID) {
			// Notification：method 有但 id 为 null / 缺失
			var n Notification
			if err := json.Unmarshal(data, &n); err != nil {
				return nil, &ErrInvalidMessage{Reason: "反序列化 Notification 失败: " + err.Error()}
			}
			return &n, nil
		}
		// Request：带 id
		var raw struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params,omitempty"`
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, &ErrInvalidMessage{Reason: "反序列化 Request 失败: " + err.Error()}
		}
		return &Request{
			JSONRPC: raw.JSONRPC,
			ID:      normalizeID(raw.ID),
			Method:  raw.Method,
			Params:  raw.Params,
		}, nil
	case len(peek.Result) > 0 || peek.Error != nil:
		var raw struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Result  json.RawMessage `json:"result,omitempty"`
			Error   *Error          `json:"error,omitempty"`
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, &ErrInvalidMessage{Reason: "反序列化 Response 失败: " + err.Error()}
		}
		return &Response{
			JSONRPC: raw.JSONRPC,
			ID:      normalizeID(raw.ID),
			Result:  raw.Result,
			Error:   raw.Error,
		}, nil
	default:
		return nil, &ErrInvalidMessage{Reason: "消息既无 method 也无 result/error 字段"}
	}
}

// isNullID 判断原始 id 字段是否为 null 或缺失。
func isNullID(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	s := string(raw)
	return s == "null" || s == ""
}

// normalizeID 把 id 字段统一为 string。
// JSON-RPC 2.0 允许 id 为 string / number / null，CodePilot 内部统一用 string
// 避免 number 精度问题与类型断言时的分歧。
func normalizeID(raw json.RawMessage) string {
	if isNullID(raw) {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// 兜底：number 类型直接保留原始字符串（含可能的浮点表示）
	return string(raw)
}

// IDGenerator 生成 JSON-RPC 请求 id。
//
// 格式：`req_<seq>_<hex>`，
// - seq  进程内 atomic counter，保证同进程内单调递增
// - hex  crypto/rand 8 字节，跨进程也几乎不会冲突
//
// 单进程内只需保证唯一性即可，无需全局有序。
type IDGenerator struct {
	counter atomic.Uint64
}

// NewIDGenerator 构造一个新的 id 生成器。
func NewIDGenerator() *IDGenerator {
	return &IDGenerator{}
}

// Next 返回一个新 id。
func (g *IDGenerator) Next() string {
	seq := g.counter.Add(1)
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand 在 Linux 上读 /dev/urandom 几乎不会失败
		// 兜底：仅用 counter
		return fmt.Sprintf("req_%d", seq)
	}
	return fmt.Sprintf("req_%d_%x", seq, binary.BigEndian.Uint64(buf[:]))
}
