// mcp-mock-stdio 入口：实现一个最小的 stdio MCP server。
//
// 设计要点：
//   - 单文件实现：~250 行覆盖 JSON-RPC 2.0 收发 + 2 个工具 + 基本错误码
//   - 标准库 only：bufio 读 stdin、json 序列化、fmt.Fprintln 写 stdout
//   - 与 CodePilot 解耦：独立 module，不依赖任何 CodePilot 包
//   - 可观测：每次请求打印到 stderr（不污染 stdout JSONL 流）
//
// 工具契约：
//   - echo(text: string) -> {content: [{type:"text", text:<input>}]}
//   - add(a: number, b: number) -> {content: [{type:"text", text: <a+b as string>}]}
//
// 退出：
//   - stdin EOF（CodePilot 关闭 Transport）→ 优雅退出 0
//   - JSON 解析失败 → 继续读下一行（防御性：单条畸形消息不影响整体）
//   - method not found → 返回 -32601 错误码
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// 工具描述常量：与 mock server 的 tools/list 响应保持一致。
const (
	serverName    = "mock-stdio"
	serverVersion = "1.0.0"
	protocolVer   = "2025-03-26"
)

// JSONRPCRequest 是入站 JSON-RPC 2.0 消息的最小表示。
//
// 字段选择：
//   - ID 用 json.RawMessage 因为可能为 string / number / null
//   - Params 用 json.RawMessage 因为是任意 JSON 结构
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response 是出站 JSON-RPC 2.0 Response。
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// Notification 是出站 Notification（server 主动发起的通知，mock 暂未使用）。
type Notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// RPCError 与 JSON-RPC 2.0 错误对象对齐。
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// JSON-RPC 2.0 标准错误码。
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// Tool 是工具描述的本地表示，输出给 client 时再序列化为 MCP 标准结构。
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// tools 是该 mock server 暴露的 2 个测试工具。
var tools = []Tool{
	{
		Name:        "echo",
		Description: "回显输入文本",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string","description":"要回显的文本"}},"required":["text"]}`),
	},
	{
		Name:        "add",
		Description: "两数相加",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"number"},"b":{"type":"number"}},"required":["a","b"]}`),
	},
}

func main() {
	// 用 bufio.Scanner 逐行读取 stdin（JSONL 协议）
	reader := bufio.NewScanner(os.Stdin)
	// 增大 buffer：tool 描述 / 复杂 args 可能有较长行
	reader.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	// stdout 用 jsonEncoder 不带末尾换行 → 手动 Write 字节并 WriteByte('\n')
	// 这种方式比 encoding/json + Fprintln 略快，且精确控制输出格式
	writer := bufio.NewWriterSize(os.Stdout, 64*1024)
	defer writer.Flush()

	fmt.Fprintf(os.Stderr, "[mock-stdio] server started (name=%s, version=%s)\n", serverName, serverVersion)

	for reader.Scan() {
		line := bytesTrimSpace(reader.Bytes())
		fmt.Fprintf(os.Stderr, "[mock-stdio] read line (%d bytes)\n", len(line))
		if len(line) == 0 {
			// 空行：跳过（防御性：避免噪声）
			continue
		}

		// 先按"是否带 id 字段"判别 Request vs Notification
		var peek struct {
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			JSONRPC string          `json:"jsonrpc"`
		}
		if err := json.Unmarshal(line, &peek); err != nil {
			fmt.Fprintf(os.Stderr, "[mock-stdio] parse error: %v\n", err)
			// 协议层错误：无法关联到具体 request id，按 ParseError 响应（id=null）
			writeError(writer, nil, codeParseError, "Parse error", err.Error())
			continue
		}
		if peek.JSONRPC != "2.0" {
			writeError(writer, peek.ID, codeInvalidRequest, "Invalid Request", "jsonrpc 字段必须为 2.0")
			continue
		}

		// 无 id → Notification（不需要回应）
		if len(peek.ID) == 0 || string(peek.ID) == "null" {
			fmt.Fprintf(os.Stderr, "[mock-stdio] notification: %s\n", peek.Method)
			continue
		}

		// 带 id → Request
		var req JSONRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			writeError(writer, peek.ID, codeInvalidRequest, "Invalid Request", err.Error())
			continue
		}

		// 完整解析后再分发
		handleRequest(writer, &req)
	}

	if err := reader.Err(); err != nil && err != io.EOF {
		fmt.Fprintf(os.Stderr, "[mock-stdio] stdin read error: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "[mock-stdio] stdin closed, exit\n")
}

// handleRequest 按 method 名称分发处理。
func handleRequest(w *bufio.Writer, req *JSONRPCRequest) {
	fmt.Fprintf(os.Stderr, "[mock-stdio] request: method=%s\n", req.Method)
	switch req.Method {
	case "initialize":
		// MCP 规范：client 握手 → server 返回 capabilities + serverInfo
		result := map[string]any{
			"protocolVersion": protocolVer,
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    serverName,
				"version": serverVersion,
			},
		}
		writeResult(w, req.ID, result)

	case "tools/list":
		// MCP 规范：tools/list 返回 { tools: [...] }
		result := map[string]any{"tools": tools}
		writeResult(w, req.ID, result)

	case "tools/call":
		// 业务执行：分发到具体工具
		handleToolCall(w, req)

	case "ping":
		// MCP 规范：ping 返回 {} 作为心跳
		writeResult(w, req.ID, map[string]any{})

	default:
		writeError(w, req.ID, codeMethodNotFound, "Method not found", req.Method)
	}
}

// handleToolCall 解析 tools/call params → 调具体工具 → 回写 MCP 格式 result。
func handleToolCall(w *bufio.Writer, req *JSONRPCRequest) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeError(w, req.ID, codeInvalidParams, "Invalid params", err.Error())
		return
	}
	if params.Name == "" {
		writeError(w, req.ID, codeInvalidParams, "Invalid params", "name 字段不能为空")
		return
	}

	switch params.Name {
	case "echo":
		result, rpcErr := toolEcho(params.Arguments)
		if rpcErr != nil {
			writeRPCError(w, req.ID, rpcErr)
			return
		}
		writeResult(w, req.ID, result)

	case "add":
		result, rpcErr := toolAdd(params.Arguments)
		if rpcErr != nil {
			writeRPCError(w, req.ID, rpcErr)
			return
		}
		writeResult(w, req.ID, result)

	default:
		// 工具不存在也走 -32601（与 method not found 同码）
		writeError(w, req.ID, codeMethodNotFound, "Method not found: tool "+params.Name, params.Name)
	}
}

// toolEcho 工具实现：text -> text。
func toolEcho(args json.RawMessage) (map[string]any, *RPCError) {
	var p struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return nil, &RPCError{Code: codeInvalidParams, Message: "Invalid params", Data: err.Error()}
	}
	// MCP 规范：result.content 数组每个元素含 type + 该类型字段
	return map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": p.Text},
		},
	}, nil
}

// toolAdd 工具实现：a + b（数值）。
func toolAdd(args json.RawMessage) (map[string]any, *RPCError) {
	// 兼容 int / float：用 json.Number 解析为字符串再转 float
	var p struct {
		A json.Number `json:"a"`
		B json.Number `json:"b"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return nil, &RPCError{Code: codeInvalidParams, Message: "Invalid params", Data: err.Error()}
	}
	a, errA := strconv.ParseFloat(p.A.String(), 64)
	b, errB := strconv.ParseFloat(p.B.String(), 64)
	if errA != nil || errB != nil {
		return nil, &RPCError{Code: codeInvalidParams, Message: "Invalid params", Data: "a/b 必须是数字"}
	}
	sum := a + b
	// 整数结果用整数字符串（避免 3.0000000000000004 这类浮点伪影）
	var sumStr string
	if sum == float64(int64(sum)) {
		sumStr = strconv.FormatInt(int64(sum), 10)
	} else {
		sumStr = strconv.FormatFloat(sum, 'g', -1, 64)
	}
	return map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": sumStr},
		},
	}, nil
}

// writeResult 序列化成功响应并写出。
func writeResult(w *bufio.Writer, id json.RawMessage, result any) {
	resp := Response{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	if err := writeJSON(w, resp); err != nil {
		fmt.Fprintf(os.Stderr, "[mock-stdio] write result failed: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "[mock-stdio] sent response id=%s\n", string(id))
	}
}

// writeError 序列化错误响应并写出（data 是 string，data=="" 时省略 Data 字段）。
func writeError(w *bufio.Writer, id json.RawMessage, code int, msg, data string) {
	e := &RPCError{Code: code, Message: msg}
	if data != "" {
		e.Data = data
	}
	writeRPCError(w, id, e)
}

// writeRPCError 直接用 *RPCError 写出（支持 Data 字段为任意类型）。
func writeRPCError(w *bufio.Writer, id json.RawMessage, e *RPCError) {
	resp := Response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   e,
	}
	if err := writeJSON(w, resp); err != nil {
		fmt.Fprintf(os.Stderr, "[mock-stdio] write error failed: %v\n", err)
	}
}

// writeJSON 把 v 序列化为单行 JSON 写入 w 并追加 '\n'。
//
// 内部用 json.Marshal 然后追加换行，确保一行一条消息（JSONL 协议）。
func writeJSON(w *bufio.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	if err := w.WriteByte('\n'); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	// 调试：每次成功写出后打印行长度（验证 write 真的发出去了）
	fmt.Fprintf(os.Stderr, "[mock-stdio] wrote %d bytes\n", len(b)+1)
	return nil
}

// bytesTrimSpace 去除 bytes 前后空白，避免 ` ` / `\t` 干扰。
func bytesTrimSpace(b []byte) []byte {
	// 简化实现：仅 strip 前后 ASCII 空白
	start := 0
	for start < len(b) && (b[start] == ' ' || b[start] == '\t' || b[start] == '\r' || b[start] == '\n') {
		start++
	}
	end := len(b)
	for end > start && (b[end-1] == ' ' || b[end-1] == '\t' || b[end-1] == '\r' || b[end-1] == '\n') {
		end--
	}
	return b[start:end]
}

// 工具库 _ = strings.TrimSpace (避免编译期 unused import 警告,实际未用)
var _ = strings.TrimSpace
