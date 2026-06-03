package builtin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/MeiCorl/CodePilot/src/tool"
	"github.com/MeiCorl/CodePilot/src/tool/safety"
	"github.com/bmatcuk/doublestar/v4"
)

// GrepName 是 Grep 工具的 snake_case 唯一标识。
const GrepName = "grep"

const (
	grepMaxResults   = 100
	grepMaxLineBytes = 4 * 1024 // 单行最大字节数
	grepMaxFileBytes = 10 << 20 // 单文件扫描上限 10MB
	grepBinarySniff  = 512      // 二进制判定字节数
)

// grepInput 是 Grep 工具的入参结构。
type grepInput struct {
	Pattern string `json:"pattern" jsonschema:"required,description=正则表达式（Go RE2 语法）"`
	Path    string `json:"path" jsonschema:"description=搜索根目录，相对工作目录解析，默认工作目录根"`
	Include string `json:"include" jsonschema:"description=文件名 glob 过滤（如 *.go），仅匹配此 glob 的文件会被扫描"`
}

var _ = grepInput{} // 见 schema.go

// GrepTool 是 Grep 工具的实现。
type GrepTool struct {
	tool.BaseTool
	WorkingDirectory string
}

// NewGrepTool 构造 Grep 工具实例。
func NewGrepTool(workingDir string) *GrepTool {
	return &GrepTool{
		BaseTool: tool.BaseTool{
			ToolName:        GrepName,
			ToolDescription: "在指定目录下按正则搜索文件内容，输出 路径:L<行号>:<行内容>。支持 include glob 过滤（如 *.go）。最多返回 100 条匹配。基准目录、include 路径、文件均需落在工作目录之内。",
			ToolInputSchema: grepSchema,
			ToolPermission:  tool.PermRead,
		},
		WorkingDirectory: workingDir,
	}
}

// grepMatch 单条匹配结果。
type grepMatch struct {
	Path string
	Line int
	Text string
}

// Execute 实现 tool.Tool.Execute。
func (t *GrepTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in grepInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}
	if strings.TrimSpace(in.Pattern) == "" {
		return "", errors.New("pattern 不能为空")
	}
	re, err := regexp.Compile(in.Pattern)
	if err != nil {
		return "", fmt.Errorf("正则编译失败: %w", err)
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

	truncated := false
	var matches []grepMatch
	walkErr := filepath.WalkDir(absBase, func(p string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		// include 过滤（glob 匹配文件名）
		if in.Include != "" {
			matched, _ := doublestar.PathMatch(in.Include, filepath.Base(p))
			if !matched {
				return nil
			}
		}
		// 沙箱二次校验
		if _, err := safety.ResolveInSandbox(p, t.WorkingDirectory); err != nil {
			return nil
		}
		// 文件大小过滤
		info, err := d.Info()
		if err != nil || info.Size() > grepMaxFileBytes {
			return nil
		}
		// 二进制嗅探
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		var sniff [grepBinarySniff]byte
		n, _ := f.Read(sniff[:])
		f.Close()
		if n > 0 && isBinary(sniff[:n]) {
			return nil
		}
		// 逐行扫描
		f2, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f2.Close()
		scanner := bufio.NewScanner(f2)
		scanner.Buffer(make([]byte, 0, 64*1024), grepMaxLineBytes)
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			line := scanner.Text()
			if re.MatchString(line) {
				matches = append(matches, grepMatch{Path: p, Line: lineNo, Text: line})
				if len(matches) >= grepMaxResults {
					truncated = true
					return fs.SkipAll
				}
			}
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
		// ctx 取消
		if errors.Is(walkErr, context.Canceled) || errors.Is(walkErr, context.DeadlineExceeded) {
			return "", walkErr
		}
		// 其他遍历错误（部分文件无权限），忽略
	}

	if len(matches) == 0 {
		return "（无匹配）", nil
	}

	var b strings.Builder
	for _, m := range matches {
		fmt.Fprintf(&b, "%s:L%d:%s\n", m.Path, m.Line, m.Text)
	}
	if truncated {
		fmt.Fprintf(&b, "（结果截断：超过 %d 条上限）\n", grepMaxResults)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// isBinary 简单二进制判定：包含 NUL 字节。
func isBinary(sniff []byte) bool {
	for _, b := range sniff {
		if b == 0 {
			return true
		}
	}
	return false
}
