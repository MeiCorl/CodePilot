// mcp-mock-http 入口：实现一个最小的 HTTP MCP server。
//
// 设计要点：
//   - 单文件实现：~250 行覆盖 HTTP server + JSON-RPC 分发 + 2 个工具
//   - 标准库 only：net/http + encoding/json
//   - 与 CodePilot 解耦：独立 module，不依赖任何 CodePilot 包
//   - 可观测：监听地址 stdout 打印 + 每次请求 stderr 日志（不污染响应）
//   - 端口可选：-port 0 让系统分配，集成测试可读 stdout 获取实际端口
//
// 工具契约（与 stdio mock 一致）：
//   - echo(text: string) -> {content: [{type:"text", text:<input>}]}
//   - add(a: number, b: number) -> {content: [{type:"text", text: <a+b as string>}]}
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
)

const (
	serverName    = "mock-http"
	serverVersion = "1.0.0"
	protocolVer   = "2025-03-26"
)

// JSONRPCRequest 与 JSON-RPC 2.0 消息对齐。
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response 与 JSON-RPC 2.0 Response 对齐。
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
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

// tools 与 stdio mock 完全一致，方便跨传输对比测试。
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
	port := flag.Int("port", 0, "监听端口（0 = 系统自动分配）")
	flag.Parse()

	// 用自定义 listener 拿到实际绑定端口（特别是 -port=0 时）
	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("[mock-http] listen failed: %v", err)
	}
	actualPort := listener.Addr().(*net.TCPAddr).Port

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", mcpHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})

	server := &http.Server{
		Handler: mux,
	}

	// 关键：把监听地址 stdout 打印，让集成测试能 parse
	// 格式：LISTENING: http://127.0.0.1:<port>/mcp
	fmt.Printf("LISTENING: http://127.0.0.1:%d/mcp\n", actualPort)
	os.Stdout.Sync() // 强制刷盘，防止测试侧读不到

	log.SetOutput(os.Stderr)
	log.Printf("[mock-http] server started (name=%s, version=%s, url=http://127.0.0.1:%d/mcp)",
		serverName, serverVersion, actualPort)

	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Fatalf("[mock-http] serve failed: %v", err)
	}
}

// mcpHandler 是 /mcp 端点的入口。
//
// 协议要点：
//   - 仅支持 POST（Streamable HTTP 同步模式；MCP 也允许 GET 用于 SSE 流，本 mock 暂不实现）
//   - 请求 Content-Type 必须是 application/json
//   - 响应 Content-Type 固定为 application/json（不实现 SSE 模式）
//   - 首次响应携带 Mcp-Session-Id header，后续请求回传（httpTransport 会自动处理）
func mcpHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 限制 body 大小：避免恶意/异常 client 发巨大请求
	body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024*1024))
	if err != nil {
		writeHTTPError(w, http.StatusBadRequest, "读请求体失败: "+err.Error())
		return
	}
	defer r.Body.Close()

	// 设置 Mcp-Session-Id：每次新连接生成一个
	sessionID := r.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		sessionID = generateSessionID()
	}
	w.Header().Set("Mcp-Session-Id", sessionID)
	w.Header().Set("Content-Type", "application/json")

	// 按 id 字段存在性区分 Request vs Notification
	var peek struct {
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		JSONRPC string          `json:"jsonrpc"`
	}
	if err := json.Unmarshal(body, &peek); err != nil {
		writeJSONRPCError(w, nil, codeParseError, "Parse error", err.Error())
		return
	}
	if peek.JSONRPC != "2.0" {
		writeJSONRPCError(w, peek.ID, codeInvalidRequest, "Invalid Request", "jsonrpc 字段必须为 2.0")
		return
	}

	// 无 id → Notification
	if len(peek.ID) == 0 || string(peek.ID) == "null" {
		log.Printf("[mock-http] notification: %s", peek.Method)
		// Notification 不需要响应：返回 204
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// 带 id → Request
	var req JSONRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONRPCError(w, peek.ID, codeInvalidRequest, "Invalid Request", err.Error())
		return
	}

	log.Printf("[mock-http] request: method=%s id=%s", req.Method, string(req.ID))
	handleRequest(w, &req)
}

// handleRequest 按 method 名称分发处理（与 stdio mock 保持一致）。
func handleRequest(w http.ResponseWriter, req *JSONRPCRequest) {
	switch req.Method {
	case "initialize":
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
		writeJSONRPCResult(w, req.ID, result)

	case "tools/list":
		result := map[string]any{"tools": tools}
		writeJSONRPCResult(w, req.ID, result)

	case "tools/call":
		handleToolCall(w, req)

	case "ping":
		writeJSONRPCResult(w, req.ID, map[string]any{})

	default:
		writeJSONRPCError(w, req.ID, codeMethodNotFound, "Method not found", req.Method)
	}
}

// handleToolCall 解析 tools/call params → 调具体工具 → 回写 MCP 格式 result。
func handleToolCall(w http.ResponseWriter, req *JSONRPCRequest) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeJSONRPCError(w, req.ID, codeInvalidParams, "Invalid params", err.Error())
		return
	}
	if params.Name == "" {
		writeJSONRPCError(w, req.ID, codeInvalidParams, "Invalid params", "name 字段不能为空")
		return
	}

	switch params.Name {
	case "echo":
		result, rpcErr := toolEcho(params.Arguments)
		if rpcErr != nil {
			writeJSONRPCErrorObj(w, req.ID, rpcErr)
			return
		}
		writeJSONRPCResult(w, req.ID, result)

	case "add":
		result, rpcErr := toolAdd(params.Arguments)
		if rpcErr != nil {
			writeJSONRPCErrorObj(w, req.ID, rpcErr)
			return
		}
		writeJSONRPCResult(w, req.ID, result)

	default:
		writeJSONRPCError(w, req.ID, codeMethodNotFound, "Method not found: tool "+params.Name, params.Name)
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
	return map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": p.Text},
		},
	}, nil
}

// toolAdd 工具实现：a + b（数值）。
func toolAdd(args json.RawMessage) (map[string]any, *RPCError) {
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

// writeJSONRPCResult 写出成功响应。
func writeJSONRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	resp := Response{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	writeJSON(w, resp)
}

// writeJSONRPCError 写出错误响应（data 是 string）。
func writeJSONRPCError(w http.ResponseWriter, id json.RawMessage, code int, msg, data string) {
	e := &RPCError{Code: code, Message: msg}
	if data != "" {
		e.Data = data
	}
	writeJSONRPCErrorObj(w, id, e)
}

// writeJSONRPCErrorObj 写出错误响应（直接用 *RPCError）。
func writeJSONRPCErrorObj(w http.ResponseWriter, id json.RawMessage, e *RPCError) {
	resp := Response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   e,
	}
	writeJSON(w, resp)
}

// writeHTTPError 输出 HTTP 层面的错误（非 JSON-RPC 错误，状态码非 200）。
func writeHTTPError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, msg)
}

// writeJSON 序列化为单行 JSON 并写出（带 newline）。
func writeJSON(w http.ResponseWriter, v any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		log.Printf("[mock-http] encode failed: %v", err)
	}
}

// generateSessionID 生成一个 16 字节 hex 编码的 session id。
func generateSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 兜底：用时间戳
		return fmt.Sprintf("session-%d", os.Getpid())
	}
	return hex.EncodeToString(b[:])
}
