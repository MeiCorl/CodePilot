// Package sources（environment.go）实现「环境上下文」Source。
//
// 每次会话启动时由 WebUI handler 一次性采集以下信息：
//  1. OS 标识（runtime.GOOS）
//  2. CWD 绝对路径（已 resolve 真实路径）
//  3. Git 状态（branch / dirty / last commit）
//
// 输出为单条 Section（Placement=System），Content 为 XML 风格结构化文本，
// 让 LLM 能明确感知「我正在哪个环境运行」。
//
// 错误处理约定：环境信息采集是「尽力而为」——任何失败都降级为可读
// 字符串，绝不向上抛 error。环境信息缺失不应阻塞会话启动。
package sources

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/MeiCorl/CodePilot/src/internal/engine/prompt/template"
	"github.com/MeiCorl/CodePilot/src/internal/engine/prompt/tokens"
	"github.com/MeiCorl/CodePilot/src/internal/logger"
	"go.uber.org/zap"
)

// 环境采集超时：单条 git 命令最长 1s，避免阻塞会话启动
const envCommandTimeout = 1 * time.Second

// EnvironmentSource 实现 Source 接口，产出当前 OS + CWD + Git 状态的环境上下文。
type EnvironmentSource struct{}

// NewEnvironmentSource 构造一个环境上下文 Source 实例。
func NewEnvironmentSource() *EnvironmentSource { return &EnvironmentSource{} }

// Name 实现 Source 接口。
func (s *EnvironmentSource) Name() string { return "environment" }

// Assemble 把 OS / CWD / Git 状态格式化为 XML 风格文本。
//
// 行为：
//  1. OS / CWD 直接从 Env 读取（Env 由 handler 在调用 Assemble 前填好）
//  2. Git 状态：若 Env.GitStatus.Branch 非空，使用传入值；
//     否则本方法内调用 git 命令二次校验（适用于 handler 未预采样的场景）
//  3. 任何错误降级为可读字符串，不向上抛
//  4. 输出 Content 中已替换模板变量（{{OS}}/{{CWD}} 等）
func (s *EnvironmentSource) Assemble(_ context.Context, env Env) (Section, error) {
	// 优先用 Env 中已采集的 OS；若为空则用 runtime.GOOS 兜底
	osName := env.OS
	if osName == "" {
		osName = runtime.GOOS
	}

	// 优先用 Env 中已 resolve 的 CWD；若为空则现场采集
	cwd := env.CWD
	if cwd == "" {
		cwd = collectCWD()
	}

	// Git 状态：handler 可能已预采样（避免每个请求都跑 git 命令），
	// 但若 Branch 与 LastCommit 都为空，触发现场采集
	git := env.GitStatus
	if git.Branch == "" && git.LastCommit == "" {
		git = collectGitStatus(cwd)
	}

	// 组装结构化文本
	content := buildEnvironmentContent(osName, cwd, git, env.Date, env.Version)

	return Section{
		Name:      "environment",
		Content:   content,
		Placement: PlacementSystem,
		Tokens:    tokens.Estimate(content),
	}, nil
}

// buildEnvironmentContent 把环境字段拼成 XML 风格文本。
// 模板变量（{{DATE}}/{{VERSION}}）由 Render 统一替换。
func buildEnvironmentContent(osName, cwd string, git template.GitStatus, date, version string) string {
	var sb strings.Builder
	sb.WriteString("<environment>\n")
	sb.WriteString("OS: " + osName + "\n")
	sb.WriteString("CWD: " + cwd + "\n")
	if git.Branch != "" {
		sb.WriteString("Git branch: " + git.Branch + "\n")
		sb.WriteString("Git status: " + boolToGitStatus(git.Dirty) + "\n")
		if git.LastCommit != "" {
			sb.WriteString("Last commit: " + git.LastCommit + "\n")
		}
	} else {
		sb.WriteString("Git: not a git repository\n")
	}
	if date != "" {
		sb.WriteString("Date: " + date + "\n")
	}
	if version != "" {
		sb.WriteString("CodePilot version: " + version + "\n")
	}
	sb.WriteString("</environment>")
	return sb.String()
}

// boolToGitStatus 把 bool 转为可读字符串。
func boolToGitStatus(dirty bool) string {
	if dirty {
		return "dirty (has uncommitted changes)"
	}
	return "clean"
}

// collectCWD 采集当前工作目录的绝对路径（已 resolve 真实路径）。
// 任何错误降级为 "unknown"。
func collectCWD() string {
	wd, err := os.Getwd()
	if err != nil {
		logger.Warn("env: 获取工作目录失败", zap.Error(err))
		return "unknown"
	}
	// 软链解析：与 tool/safety/path.go 保持一致风格
	if resolved, err := filepath.EvalSymlinks(wd); err == nil {
		wd = resolved
	}
	return filepath.Clean(wd)
}

// collectGitStatus 在 cwd 中采集 Git 状态。
// 任何错误降级为零值 GitStatus，由调用方根据 Branch=="" 走「not a git repository」分支。
func collectGitStatus(cwd string) template.GitStatus {
	// 检查 cwd 是否在 git 仓库内
	if !isGitRepo(cwd) {
		return template.GitStatus{}
	}

	branch := runGitCommand(cwd, "rev-parse", "--abbrev-ref", "HEAD")
	if branch == "" {
		// detached HEAD 状态：rev-parse 会返回 "HEAD"，用 --short 形式无效
		// 这种情况下保持空，dirty 信息仍可采集
	}

	dirty := isWorkingTreeDirty(cwd)

	lastCommit := runGitCommand(cwd, "log", "-1", "--oneline")

	return template.GitStatus{
		Branch:     branch,
		Dirty:      dirty,
		LastCommit: lastCommit,
	}
}

// isGitRepo 通过 `git rev-parse --is-inside-work-tree` 判断是否在 git 仓库内。
func isGitRepo(cwd string) bool {
	out := runGitCommand(cwd, "rev-parse", "--is-inside-work-tree")
	return out == "true"
}

// isWorkingTreeDirty 通过 `git status --porcelain` 是否返回非空判断。
// 该命令同时覆盖 modified / untracked / staged 等所有变更。
func isWorkingTreeDirty(cwd string) bool {
	out := runGitCommand(cwd, "status", "--porcelain")
	return out != ""
}

// runGitCommand 在 cwd 中执行 git 命令并返回 stdout。
// 任何错误（命令不存在、超时、非零退出码）都降级为 ""，不返回 error。
func runGitCommand(cwd string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), envCommandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	// 关闭 stdin 避免 git 等待输入
	cmd.Stdin = nil

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// 记录但不抛错
		if ctx.Err() == context.DeadlineExceeded {
			logger.Warn("env: git 命令超时",
				zap.String("args", strings.Join(args, " ")),
				zap.String("cwd", cwd),
			)
		}
		return ""
	}
	return strings.TrimSpace(stdout.String())
}
