// Package builtin 提供 CodePilot 的内置工具集。
//
// 本文件实现 EditFile 工具：按精确字符串匹配替换文件中的内容片段。
// 适用于对已有文件进行局部修改，而非全量覆盖（WriteFile 负责全量写入）。
// old_string 必须在文件中唯一匹配；若不唯一则报错，要求用户缩小范围。
package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap"

	"github.com/MeiCorl/CodePilot/src/internal/logger"
	"github.com/MeiCorl/CodePilot/src/internal/tool"
)

// EditFileName 是 EditFile 工具的唯一标识（大驼峰格式）。
const EditFileName = "EditFile"

// editFileInput 是 EditFile 工具的入参结构。
type editFileInput struct {
	FilePath  string `json:"file_path" jsonschema:"required,description=要编辑的文件路径（相对工作目录或绝对路径）"`
	OldString string `json:"old_string" jsonschema:"required,description=要被替换的原文本（必须精确匹配，包含缩进）"`
	NewString string `json:"new_string" jsonschema:"required,description=替换后的新文本（设为空字符串可删除 old_string）"`
}

// editFileSchema 见 schema.go。
var _ = editFileInput{}

// EditFileTool 是 EditFile 工具的实现。
//
// 沙箱解析由 ToolHandler.SandboxMiddleware 统一处理；absPath 来自 ctx。
type EditFileTool struct {
	tool.BaseTool
	// DiffSink 用于在执行成功后把 before/after 推送给 WebUI 用于 diff 弹窗。
	// 可为 nil（主流程未注入或单测场景），nil 时跳过写入不 panic。
	// 类型为 tool.FileDiffSink（定义在 tool 包，避免 builtin 反向依赖 web）。
	DiffSink tool.FileDiffSink
}

// NewEditFileTool 构造 EditFile 工具实例。
//
// workingDir 参数保留签名以兼容 RegisterWithOptions 调用点（main.go），
// 内部不使用——沙箱配置由 ToolHandler.RegisterMiddleware 注入。
func NewEditFileTool(workingDir string) *EditFileTool {
	_ = workingDir
	return &EditFileTool{
		BaseTool: tool.BaseTool{
			ToolName:        EditFileName,
			ToolDescription: "对已有文件进行精确字符串替换编辑。old_string 必须与文件中的内容精确匹配（包括缩进和空行），且在文件中唯一出现。若不唯一会报错，需缩小范围。设 new_string 为空字符串可删除指定内容。适用于局部修改，优于 WriteFile 的全量覆盖。",
			ToolInputSchema: editFileSchema,
			ToolPermission:  tool.PermWrite,
		},
	}
}

// SetDiffSink 注入 diff 接收器。主流程在 RegisterWithOptions 之后调一次。
// 设计动机与 WriteFileTool.SetDiffSink 相同。
func (t *EditFileTool) SetDiffSink(sink tool.FileDiffSink) {
	t.DiffSink = sink
}

// Execute 实现 tool.Tool.Execute。
func (t *EditFileTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in editFileInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}
	if strings.TrimSpace(in.FilePath) == "" {
		return "", errors.New("file_path 不能为空")
	}
	if in.OldString == "" {
		return "", errors.New("old_string 不能为空")
	}

	// 沙箱解析：由 ToolHandler.SandboxMiddleware 完成；absPath 来自 ctx。
	absPath, err := resolvePathFromContext(ctx, "file_path")
	if err != nil {
		return "", err
	}

	if err := ctx.Err(); err != nil {
		return "", err
	}

	// 读取原文件内容
	content, err := os.ReadFile(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("文件不存在: %s", absPath)
		}
		return "", fmt.Errorf("读取文件失败: %w", err)
	}

	original := string(content)

	// 检查 old_string 是否存在
	count := strings.Count(original, in.OldString)
	if count == 0 {
		return "", fmt.Errorf("未在文件中找到 old_string 指定的内容。请确认 old_string 与文件中的实际内容完全一致（包括缩进、空行等）")
	}
	if count > 1 {
		return "", fmt.Errorf("old_string 在文件中出现了 %d 次，无法唯一定位。请扩大 old_string 的上下文范围使其唯一匹配", count)
	}

	// 执行替换
	newContent := strings.Replace(original, in.OldString, in.NewString, 1)

	// 写回文件
	if err := os.WriteFile(absPath, []byte(newContent), 0644); err != nil {
		return "", fmt.Errorf("写入文件失败: %w", err)
	}

	// 计算替换的行数变化信息
	oldLines := strings.Count(in.OldString, "\n") + 1
	newLines := strings.Count(in.NewString, "\n") + 1
	if in.NewString == "" {
		newLines = 0
	}

	// 写入成功后推 diff。失败不影响主返回值。
	t.recordDiff(ctx, absPath, original, newContent)

	return fmt.Sprintf("已编辑 %s（替换了 %d 行 → %d 行）", absPath, oldLines, newLines), nil
}

// recordDiff 与 WriteFileTool 同名方法语义一致：ctx 缺 id 或 sink 为 nil 时安全跳过。
func (t *EditFileTool) recordDiff(ctx context.Context, absPath, before, after string) {
	if t.DiffSink == nil {
		return
	}
	id, ok := tool.ToolUseIDFromContext(ctx)
	if !ok {
		return
	}
	if !t.DiffSink.Set(id, tool.FileDiffEntry{
		FilePath: absPath,
		Before:   before,
		After:    after,
	}) {
		logger.Warn("EditFile diff 被 DiffSink 拒绝",
			zap.String("tool_use_id", id),
			zap.String("file_path", absPath),
		)
	}
}
