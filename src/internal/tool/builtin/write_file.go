// Package builtin 提供 CodePilot 的内置工具集。
//
// 本文件实现 WriteFile 工具：覆盖式写入文件（不存在则创建）；
// 若父目录不存在会自动创建（mkdir -p 语义）。
package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"

	"github.com/MeiCorl/CodePilot/src/internal/logger"
	"github.com/MeiCorl/CodePilot/src/internal/tool"
)

// WriteFileName 是 WriteFile 工具的唯一标识（大驼峰格式）。
const WriteFileName = "WriteFile"

// writeFileInput 是 WriteFile 工具的入参结构。
type writeFileInput struct {
	FilePath string `json:"file_path" jsonschema:"required,description=要写入的文件路径（相对工作目录或绝对路径）"`
	Content  string `json:"content" jsonschema:"required,description=写入文件的完整内容（覆盖式写入）"`
}

// writeFileSchema 见 schema.go。
var _ = writeFileInput{}

// WriteFileTool 是 WriteFile 工具的实现。
//
// 沙箱解析由 ToolHandler.SandboxMiddleware 统一处理；absPath 来自 ctx。
type WriteFileTool struct {
	tool.BaseTool
	// DiffSink 用于在执行成功后把 before/after 推送给 WebUI 用于 diff 弹窗。
	// 可为 nil（主流程未注入或单测场景），nil 时跳过写入不 panic。
	// 类型为 tool.FileDiffSink（定义在 tool 包，避免 builtin 反向依赖 web）。
	DiffSink tool.FileDiffSink
}

// NewWriteFileTool 构造 WriteFile 工具实例。
//
// workingDir 参数保留签名以兼容 RegisterWithOptions 调用点（main.go），
// 内部不使用——沙箱配置由 ToolHandler.RegisterMiddleware 注入。
func NewWriteFileTool(workingDir string) *WriteFileTool {
	_ = workingDir
	return &WriteFileTool{
		BaseTool: tool.BaseTool{
			ToolName:        WriteFileName,
			ToolDescription: "创建或覆盖写入文件。若父目录不存在会自动创建（mkdir -p 语义）。优先使用此内置工具而非 Bash 命令来创建/写入文件。仅创建新文件或需全量覆盖时使用；局部修改请使用 EditFile。",
			ToolInputSchema: writeFileSchema,
			ToolPermission:  tool.PermWrite,
		},
	}
}

// SetDiffSink 注入 diff 接收器。主流程在 RegisterWithOptions 之后调一次。
// 设计为 setter 而非构造器参数：避免改动 RegisterWithOptions 签名波及所有测试；
// nil 参数表示显式关闭 diff 采集。
func (t *WriteFileTool) SetDiffSink(sink tool.FileDiffSink) {
	t.DiffSink = sink
}

// Execute 实现 tool.Tool.Execute。
func (t *WriteFileTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in writeFileInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}
	if strings.TrimSpace(in.FilePath) == "" {
		return "", errors.New("file_path 不能为空")
	}

	// 沙箱解析：由 ToolHandler.SandboxMiddleware 完成；absPath 来自 ctx。
	// absPath 已在 sandbox 内（含 symlink 解析），其父目录天然在 sandbox 内，
	// os.MkdirAll(dir) 不会越界创建——无需再做 sandbox 校验。
	absPath, err := resolvePathFromContext(ctx, "file_path")
	if err != nil {
		return "", err
	}

	if err := ctx.Err(); err != nil {
		return "", err
	}

	// 读取旧内容用作 diff.before。
	// 文件不存在时（新文件场景）before 为空字符串，符合 FileDiffStore 约定。
	var before string
	if data, err := os.ReadFile(absPath); err == nil {
		before = string(data)
	} else if !os.IsNotExist(err) {
		// 读取失败（权限等）不影响写入主流程，仅记录后继续
		logger.Warn("WriteFile 读取旧内容失败，diff.before 留空",
			zap.String("file_path", absPath), zap.Error(err))
	}

	// 自动创建父目录
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("创建父目录失败: %w", err)
	}

	// 覆盖写入
	if err := os.WriteFile(absPath, []byte(in.Content), 0644); err != nil {
		return "", fmt.Errorf("写入文件失败: %w", err)
	}

	// 写入成功后推 diff。Sink 为 nil 或 ctx 缺 toolUseID 时安全跳过。
	// 任何分支的失败都不影响主流程返回值（写入已成功，diff 是辅助功能）。
	t.recordDiff(ctx, absPath, before, in.Content)

	return fmt.Sprintf("已写入 %d 字节到 %s", len(in.Content), absPath), nil
}

// recordDiff 尝试把本次改动推给 DiffSink。
// 失败不返回 error：主流程语义是"写入文件"，diff 仅供 UI 二次展示。
func (t *WriteFileTool) recordDiff(ctx context.Context, absPath, before, after string) {
	if t.DiffSink == nil {
		return
	}
	id, ok := tool.ToolUseIDFromContext(ctx)
	if !ok {
		// ctx 中无 toolUseID 时（单测 / 无 engine 包装）跳过，
		// 避免写入一条没有 id 的 diff 记录（Store 也会拒收）。
		return
	}
	if !t.DiffSink.Set(id, tool.FileDiffEntry{
		FilePath: absPath,
		Before:   before,
		After:    after,
	}) {
		logger.Warn("WriteFile diff 被 DiffSink 拒绝",
			zap.String("tool_use_id", id),
			zap.String("file_path", absPath),
		)
	}
}
