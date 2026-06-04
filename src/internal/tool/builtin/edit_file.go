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

	"github.com/MeiCorl/CodePilot/src/internal/tool"
	"github.com/MeiCorl/CodePilot/src/internal/tool/safety"
)

// EditFileName 是 EditFile 工具的唯一标识（大驼峰格式）。
const EditFileName = "EditFile"

// editFileInput 是 EditFile 工具的入参结构。
type editFileInput struct {
	FilePath  string `json:"file_path" jsonschema:"required,description=要编辑的文件路径（相对工作目录或绝对路径）"`
	OldString string `json:"old_string" jsonschema:"required,description=要被替换的原文本（必须精确匹配，包含缩进）"`
	NewString string `json:"new_string" jsonschema:"required,description=替换后的新文本（设为空字符串可删除 old_string）"`
}

var _ = editFileInput{} // 见 schema.go

// EditFileTool 是 EditFile 工具的实现。
type EditFileTool struct {
	tool.BaseTool
	// WorkingDirectory 是路径沙箱根目录，所有 file_path 必须落在其内。
	WorkingDirectory string
}

// NewEditFileTool 构造 EditFile 工具实例。
func NewEditFileTool(workingDir string) *EditFileTool {
	return &EditFileTool{
		BaseTool: tool.BaseTool{
			ToolName:        EditFileName,
			ToolDescription: "对已有文件进行精确字符串替换编辑。old_string 必须与文件中的内容精确匹配（包括缩进和空行），且在文件中唯一出现。若不唯一会报错，需缩小范围。设 new_string 为空字符串可删除指定内容。适用于局部修改，优于 WriteFile 的全量覆盖。",
			ToolInputSchema: editFileSchema,
			ToolPermission:  tool.PermWrite,
		},
		WorkingDirectory: workingDir,
	}
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

	// 路径沙箱校验
	absPath, err := safety.ResolveInSandbox(in.FilePath, t.WorkingDirectory)
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

	return fmt.Sprintf("已编辑 %s（替换了 %d 行 → %d 行）", absPath, oldLines, newLines), nil
}
