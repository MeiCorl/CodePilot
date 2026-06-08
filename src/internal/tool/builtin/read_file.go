// Package builtin 提供 CodePilot 的内置工具集。
// 所有工具实现 tool.Tool 接口，通过 init() 注册到默认 Registry。
//
// 本文件实现 ReadFile 工具：按行读取文件内容，附带行号，
// 支持 offset/limit 分页模式；对二进制文件给出明确错误。
package builtin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/MeiCorl/CodePilot/src/internal/tool"
)

// ReadFileName 是 ReadFile 工具的唯一标识（大驼峰格式）。
const ReadFileName = "ReadFile"

const (
	readFileDefaultLimit = 2000
	readFileBinarySniff  = 512 // 二进制嗅探字节数
)

// readFileInput 是 ReadFile 工具的入参结构。
type readFileInput struct {
	FilePath string `json:"file_path" jsonschema:"required,description=要读取的文件路径（相对工作目录或绝对路径）"`
	Offset   int    `json:"offset" jsonschema:"description=起始行号（0-based），默认 0"`
	Limit    int    `json:"limit" jsonschema:"description=最多返回的行数，默认 2000"`
}

// readFileSchema 见 schema.go；input struct 仅为 JSON 解析用，类型定义保留便于 IDE 跳转。
var _ = readFileInput{}

// ReadFileTool 是 ReadFile 工具的实现。
//
// 沙箱解析由 ToolHandler.SandboxMiddleware 统一处理，工具侧不再调用
// security.ResolveInSandbox；Execute 入口从 ctx 拿已校验的 absPath。
type ReadFileTool struct {
	tool.BaseTool
}

// NewReadFileTool 构造 ReadFile 工具实例。
//
// workingDir 参数保留签名以兼容 RegisterWithOptions 调用点（main.go），
// 内部不使用——沙箱配置由 ToolHandler.RegisterMiddleware 注入。
func NewReadFileTool(workingDir string) *ReadFileTool {
	_ = workingDir
	return &ReadFileTool{
		BaseTool: tool.BaseTool{
			ToolName:        ReadFileName,
			ToolDescription: "读取文件内容并按行返回（每行带行号 L<n>:）。支持 offset/limit 分页读取大文件。无法读取二进制文件、不存在的文件或沙箱外的路径。优先使用此内置工具而非 Bash 命令（如 cat/head/tail）来读取文件。",
			ToolInputSchema: json.RawMessage(readFileSchema),
			ToolPermission:  tool.PermRead,
		},
	}
}

// Execute 实现 tool.Tool.Execute。
func (t *ReadFileTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in readFileInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}
	if strings.TrimSpace(in.FilePath) == "" {
		return "", errors.New("file_path 不能为空")
	}

	// 沙箱解析：由 ToolHandler.SandboxMiddleware 完成；absPath 来自 ctx。
	// 未拿到 PathResolver 视为内部错误（工具未走 ToolHandler）。
	absPath, err := resolvePathFromContext(ctx, "file_path")
	if err != nil {
		return "", err
	}

	if err := ctx.Err(); err != nil {
		return "", err
	}

	// 打开文件
	f, err := os.Open(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("文件不存在: %s", absPath)
		}
		if os.IsPermission(err) {
			return "", fmt.Errorf("无权限读取: %s", absPath)
		}
		return "", fmt.Errorf("打开文件失败: %w", err)
	}
	defer f.Close()

	// 二进制嗅探（前 512 字节）
	var sniff [readFileBinarySniff]byte
	n, _ := f.Read(sniff[:])
	if n > 0 {
		ct := http.DetectContentType(sniff[:n])
		if !strings.HasPrefix(ct, "text/") && ct != "application/json" && ct != "application/xml" {
			return "", fmt.Errorf("非文本文件（%s），拒绝读取", ct)
		}
	}
	if _, err := f.Seek(0, 0); err != nil {
		return "", fmt.Errorf("重置文件指针失败: %w", err)
	}

	// 按行扫描
	limit := in.Limit
	if limit <= 0 {
		limit = readFileDefaultLimit
	}
	offset := in.Offset
	if offset < 0 {
		offset = 0
	}

	var (
		out        strings.Builder
		totalLines int
		returned   int
		startLine  = offset + 1
	)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // 大文件行缓冲 16MB

	for scanner.Scan() {
		totalLines++
		if totalLines <= offset {
			continue
		}
		if returned >= limit {
			break
		}
		fmt.Fprintf(&out, "L%d: %s\n", startLine+returned, scanner.Text())
		returned++
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("读取文件失败: %w", err)
	}

	fmt.Fprintf(&out, "（共 %d 行, 本次返回 %d 行 [offset=%d, limit=%d]）", totalLines, returned, offset, limit)
	return out.String(), nil
}
