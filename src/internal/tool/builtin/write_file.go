package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MeiCorl/CodePilot/src/internal/tool"
	"github.com/MeiCorl/CodePilot/src/internal/tool/safety"
)

// WriteFileName 是 WriteFile 工具的 snake_case 唯一标识。
const WriteFileName = "write_file"

// writeFileInput 是 WriteFile 工具的入参结构。
type writeFileInput struct {
	FilePath string `json:"file_path" jsonschema:"required,description=要写入的文件路径（相对工作目录或绝对路径）"`
	Content  string `json:"content" jsonschema:"required,description=写入文件的完整内容（覆盖式写入）"`
}

var _ = writeFileInput{} // 见 schema.go

// WriteFileTool 是 WriteFile 工具的实现。
type WriteFileTool struct {
	tool.BaseTool
	WorkingDirectory string
}

// NewWriteFileTool 构造 WriteFile 工具实例。
func NewWriteFileTool(workingDir string) *WriteFileTool {
	return &WriteFileTool{
		BaseTool: tool.BaseTool{
			ToolName:        WriteFileName,
			ToolDescription: "创建或覆盖写入文件。若父目录不存在会自动创建（mkdir -p 语义）。",
			ToolInputSchema: writeFileSchema,
			ToolPermission:  tool.PermWrite,
		},
		WorkingDirectory: workingDir,
	}
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

	// 路径沙箱校验（WriteFile 的目标文件可尚不存在，沙箱仅校验 parent dir）
	absPath, err := safety.ResolveInSandbox(in.FilePath, t.WorkingDirectory)
	if err != nil {
		return "", err
	}

	if err := ctx.Err(); err != nil {
		return "", err
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

	return fmt.Sprintf("已写入 %d 字节到 %s", len(in.Content), absPath), nil
}
