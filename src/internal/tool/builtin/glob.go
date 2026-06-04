package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MeiCorl/CodePilot/src/internal/tool"
	"github.com/MeiCorl/CodePilot/src/internal/tool/safety"
	"github.com/bmatcuk/doublestar/v4"
)

// GlobName 是 Glob 工具的唯一标识（大驼峰格式）。
const GlobName = "Glob"

const (
	globMaxResults = 100
)

// globInput 是 Glob 工具的入参结构。
type globInput struct {
	Pattern string `json:"pattern" jsonschema:"required,description=glob 模式，支持 ** 递归（如 src/**/*.go）"`
	Path    string `json:"path" jsonschema:"description=基准目录，相对工作目录解析，默认工作目录根"`
}

var _ = globInput{} // 见 schema.go

// GlobTool 是 Glob 工具的实现。
type GlobTool struct {
	tool.BaseTool
	WorkingDirectory string
}

// NewGlobTool 构造 Glob 工具实例。
func NewGlobTool(workingDir string) *GlobTool {
	return &GlobTool{
		BaseTool: tool.BaseTool{
			ToolName:        GlobName,
			ToolDescription: "按 glob 模式查找匹配的文件路径，支持 ** 递归（如 src/**/*.go）。结果按绝对路径排序，最多返回 100 条。基准目录与匹配结果均需落在工作目录之内。优先使用此内置工具而非 Bash 命令（如 find/ls）来搜索文件。",
			ToolInputSchema: globSchema,
			ToolPermission:  tool.PermRead,
		},
		WorkingDirectory: workingDir,
	}
}

// Execute 实现 tool.Tool.Execute。
func (t *GlobTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in globInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}
	if strings.TrimSpace(in.Pattern) == "" {
		return "", errors.New("pattern 不能为空")
	}

	if err := ctx.Err(); err != nil {
		return "", err
	}

	// 基准目录沙箱校验
	base := in.Path
	if base == "" {
		base = t.WorkingDirectory
	}
	absBase, err := safety.ResolveInSandbox(base, t.WorkingDirectory)
	if err != nil {
		return "", err
	}
	if !dirExists(absBase) {
		return "", fmt.Errorf("基准目录不存在: %s", absBase)
	}

	// doublestar.GlobWalk 在 fs.FS 上工作，pattern 总是相对 FS 根。
	// 我们的语义：fsys 始终指向 absBase，pattern 若为绝对路径则转为相对。
	pattern := filepath.ToSlash(in.Pattern)
	if strings.HasPrefix(pattern, "/") {
		// 绝对路径：去掉前导 / 转为相对（保留中间子路径）
		pattern = strings.TrimPrefix(pattern, "/")
	}

	fsys := os.DirFS(absBase)

	var matches []string
	walkErr := doublestar.GlobWalk(fsys, pattern, func(p string, d fs.DirEntry) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if d != nil && d.IsDir() {
			return nil
		}
		abs, err := filepath.Abs(filepath.Join(absBase, p))
		if err != nil {
			return nil
		}
		// 结果二次沙箱校验
		if _, err := safety.ResolveInSandbox(abs, t.WorkingDirectory); err != nil {
			return nil
		}
		matches = append(matches, abs)
		return nil
	})
	if walkErr != nil {
		if errors.Is(walkErr, context.Canceled) || errors.Is(walkErr, context.DeadlineExceeded) {
			return "", walkErr
		}
		// 模式不匹配时 doublestar 返回 ErrBadPattern，走 filepath.ErrBadPattern
		// 我们视为无匹配
		if errors.Is(walkErr, doublestar.ErrBadPattern) || strings.Contains(walkErr.Error(), "syntax error") {
			return "", fmt.Errorf("glob 模式语法错误: %w", walkErr)
		}
	}

	// 排序 + 截断
	sort.Strings(matches)
	truncated := false
	if len(matches) > globMaxResults {
		matches = matches[:globMaxResults]
		truncated = true
	}

	if len(matches) == 0 {
		return "（无匹配文件）", nil
	}

	var b strings.Builder
	for _, m := range matches {
		b.WriteString(m)
		b.WriteString("\n")
	}
	if truncated {
		fmt.Fprintf(&b, "（结果截断：超过 %d 条上限，仅返回前 %d 条）\n", globMaxResults, globMaxResults)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// dirExists 判断目录是否存在。
func dirExists(p string) bool {
	info, err := os.Stat(p)
	if err != nil {
		return false
	}
	return info.IsDir()
}
