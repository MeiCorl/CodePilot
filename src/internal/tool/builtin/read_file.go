package builtin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	skillbuiltin "github.com/MeiCorl/CodePilot/src/internal/skill/builtin"
	"github.com/MeiCorl/CodePilot/src/internal/tool"
)

const ReadFileName = "ReadFile"

const (
	readFileDefaultLimit = 2000
	readFileBinarySniff  = 512
)

type readFileInput struct {
	FilePath string `json:"file_path" jsonschema:"required,description=要读取的文件路径（相对工作目录、绝对路径或 embedded:// 内置 Skill 路径）"`
	Offset   int    `json:"offset" jsonschema:"description=起始行号（0-based），默认 0"`
	Limit    int    `json:"limit" jsonschema:"description=最多返回的行数，默认 2000"`
}

var _ = readFileInput{}

type ReadFileTool struct {
	tool.BaseTool
}

func NewReadFileTool(workingDir string) *ReadFileTool {
	_ = workingDir
	return &ReadFileTool{
		BaseTool: tool.BaseTool{
			ToolName:        ReadFileName,
			ToolDescription: "读取文件内容并按行返回（每行带行号 L<n>:）。支持 offset/limit 分页读取大文件。无法读取二进制文件、不存在的文件或沙箱外路径；也支持 embedded://internal/skill/builtin/... 内置 Skill 只读资源。优先使用此内置工具而非 Bash 命令（如 cat/head/tail）来读取文件。",
			ToolInputSchema: json.RawMessage(readFileSchema),
			ToolPermission:  tool.PermRead,
		},
	}
}

func (t *ReadFileTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in readFileInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}
	if strings.TrimSpace(in.FilePath) == "" {
		return "", errors.New("file_path 不能为空")
	}

	absPath, err := resolvePathFromContext(ctx, "file_path")
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	if skillbuiltin.IsEmbeddedPath(absPath) {
		data, err := skillbuiltin.ReadEmbeddedFile(absPath)
		if err != nil {
			return "", fmt.Errorf("读取内置 Skill 文件失败: %w", err)
		}
		return readFileFromReadSeeker(bytes.NewReader(data), in.Offset, in.Limit)
	}

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

	return readFileFromReadSeeker(f, in.Offset, in.Limit)
}

func readFileFromReadSeeker(r io.ReadSeeker, offset, limit int) (string, error) {
	var sniff [readFileBinarySniff]byte
	n, _ := r.Read(sniff[:])
	if n > 0 {
		ct := http.DetectContentType(sniff[:n])
		if !strings.HasPrefix(ct, "text/") && ct != "application/json" && ct != "application/xml" {
			return "", fmt.Errorf("非文本文件（%s），拒绝读取", ct)
		}
	}
	if _, err := r.Seek(0, 0); err != nil {
		return "", fmt.Errorf("重置文件指针失败: %w", err)
	}

	if limit <= 0 {
		limit = readFileDefaultLimit
	}
	if offset < 0 {
		offset = 0
	}

	var (
		out        strings.Builder
		totalLines int
		returned   int
		startLine  = offset + 1
	)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

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

	fmt.Fprintf(&out, "（共 %d 行，本次返回 %d 行 [offset=%d, limit=%d]）", totalLines, returned, offset, limit)
	return out.String(), nil
}
