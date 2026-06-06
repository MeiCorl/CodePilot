package sources

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MeiCorl/CodePilot/src/internal/engine/prompt/template"
)

// TestEnvironmentSource_Name 验证 Name 返回固定值 "environment"。
func TestEnvironmentSource_Name(t *testing.T) {
	s := NewEnvironmentSource()
	if got := s.Name(); got != "environment" {
		t.Errorf("Name() 应返回 'environment'，得到 %q", got)
	}
}

// TestEnvironmentSource_OSFromEnv 验证 Env.OS 优先于 runtime.GOOS。
func TestEnvironmentSource_OSFromEnv(t *testing.T) {
	s := NewEnvironmentSource()
	section, err := s.Assemble(context.Background(), Env{OS: "linux", CWD: "/tmp"})
	if err != nil {
		t.Fatalf("Assemble 失败: %v", err)
	}
	if !strings.Contains(section.Content, "OS: linux") {
		t.Errorf("Content 应包含 'OS: linux'，实际:\n%s", section.Content)
	}
}

// TestEnvironmentSource_OSFromRuntimeFallback 验证 Env.OS 为空时降级到 runtime.GOOS。
func TestEnvironmentSource_OSFromRuntimeFallback(t *testing.T) {
	s := NewEnvironmentSource()
	section, err := s.Assemble(context.Background(), Env{CWD: "/tmp"})
	if err != nil {
		t.Fatalf("Assemble 失败: %v", err)
	}
	expectedOS := runtime.GOOS
	if !strings.Contains(section.Content, "OS: "+expectedOS) {
		t.Errorf("Content 应包含 'OS: %s'（runtime.GOOS），实际:\n%s", expectedOS, section.Content)
	}
}

// TestEnvironmentSource_CWDFromEnv 验证 Env.CWD 正确写入 Content。
func TestEnvironmentSource_CWDFromEnv(t *testing.T) {
	s := NewEnvironmentSource()
	section, err := s.Assemble(context.Background(), Env{OS: "linux", CWD: "/home/user/proj"})
	if err != nil {
		t.Fatalf("Assemble 失败: %v", err)
	}
	if !strings.Contains(section.Content, "CWD: /home/user/proj") {
		t.Errorf("Content 应包含 'CWD: /home/user/proj'，实际:\n%s", section.Content)
	}
}

// TestEnvironmentSource_ResolveCWDFromRealPath 验证 Content 中的 CWD
// 是经过 filepath.EvalSymlinks resolve 后的真实路径。
// 模拟方法：创建临时软链 → 把它作为 Env.CWD 传入 → 验证 Content 中仍是真实路径
// （实际是验证 builder 路径，但 Assemble 只读 Env，所以这里验证 Env.CWD 透传后
//  不会被二次 EvalSymlinks 改变——降级行为由 collectCWD 负责）。
func TestEnvironmentSource_ResolveCWDFromRealPath(t *testing.T) {
	realDir := t.TempDir()
	linkDir := t.TempDir()
	link := filepath.Join(linkDir, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("本平台不支持软链或权限不足: %v", err)
	}

	s := NewEnvironmentSource()
	// 把软链路径作为 Env.CWD 传入，Assemble 应原样透传
	section, err := s.Assemble(context.Background(), Env{OS: "linux", CWD: link})
	if err != nil {
		t.Fatalf("Assemble 失败: %v", err)
	}
	// 这里验证的是透传行为：Env.CWD 是什么，Content 中就是什么
	// 真实路径 resolve 由 collectCWD 完成
	if !strings.Contains(section.Content, "CWD: "+link) {
		t.Errorf("Content 应保留 Env.CWD 原始值 %q，实际:\n%s", link, section.Content)
	}
}

// TestEnvironmentSource_GitInTempRepo 验证在临时 git 仓库中能正确采集 branch 与 dirty 状态。
// 这是 checklist.md 中「环境采集在临时 git 仓库中跑」的验收项。
func TestEnvironmentSource_GitInTempRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git 命令不可用，跳过本测试")
	}

	repo := initTempGitRepo(t, "feature/test-branch", true /*withUntracked*/)
	defer os.RemoveAll(repo)

	// 直接调用 collectGitStatus 而不是通过 Assemble，避免 Env.CWD 二次采集覆盖
	git := collectGitStatus(repo)
	if git.Branch != "feature/test-branch" {
		t.Errorf("Branch 应为 'feature/test-branch'，得到 %q", git.Branch)
	}
	if !git.Dirty {
		t.Errorf("Dirty 应为 true（存在 untracked 文件），得到 false")
	}
	if git.LastCommit == "" {
		t.Errorf("LastCommit 应非空（应包含 'initial commit'），得到空")
	}
}

// TestEnvironmentSource_GitCleanRepo 验证在干净的 git 仓库中 Dirty=false。
func TestEnvironmentSource_GitCleanRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git 命令不可用，跳过本测试")
	}

	repo := initTempGitRepo(t, "main", false /*noUntracked*/)
	defer os.RemoveAll(repo)

	git := collectGitStatus(repo)
	if git.Branch != "main" {
		t.Errorf("Branch 应为 'main'，得到 %q", git.Branch)
	}
	if git.Dirty {
		t.Errorf("Dirty 应为 false（仓库干净），得到 true")
	}
}

// TestEnvironmentSource_NonGitDir 验证非 git 目录中降级为「not a git repository」。
func TestEnvironmentSource_NonGitDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git 命令不可用，跳过本测试")
	}

	nonGitDir := t.TempDir()

	s := NewEnvironmentSource()
	// Env 不预填 GitStatus，让 Source 触发现场采集
	section, err := s.Assemble(context.Background(), Env{OS: "linux", CWD: nonGitDir})
	if err != nil {
		t.Fatalf("Assemble 不应返回错误（非 git 不应阻塞），得到: %v", err)
	}
	if !strings.Contains(section.Content, "not a git repository") {
		t.Errorf("非 git 目录 Content 应含 'not a git repository'，实际:\n%s", section.Content)
	}
	// 验证 Content 中**不**包含 Git branch / Last commit 字段
	if strings.Contains(section.Content, "Git branch:") {
		t.Errorf("非 git 目录不应有 Git branch 字段，实际:\n%s", section.Content)
	}
}

// TestEnvironmentSource_PrefilledGitStatus 验证 Env.GitStatus 已预填时不会触发 git 命令。
// 通过 Branch 非空 + LastCommit 为空的组合触发 Assemble 的「无需二次采集」路径。
func TestEnvironmentSource_PrefilledGitStatus(t *testing.T) {
	s := NewEnvironmentSource()
	section, err := s.Assemble(context.Background(), Env{
		OS:      "linux",
		CWD:     "/tmp",
		Date:    "2026-06-06",
		Version: "1.0.5",
		GitStatus: template.GitStatus{
			Branch:     "dev",
			Dirty:      true,
			LastCommit: "abc1234 test",
		},
	})
	if err != nil {
		t.Fatalf("Assemble 失败: %v", err)
	}
	if !strings.Contains(section.Content, "Git branch: dev") {
		t.Errorf("应使用预填的 branch")
	}
	if !strings.Contains(section.Content, "dirty (has uncommitted changes)") {
		t.Errorf("应显示 dirty 状态")
	}
	if !strings.Contains(section.Content, "Last commit: abc1234 test") {
		t.Errorf("应包含预填的 Last commit")
	}
}

// TestEnvironmentSource_TemplateVarsSubstituted 验证 Content 中 date 与 version 正确显示。
func TestEnvironmentSource_TemplateVarsSubstituted(t *testing.T) {
	s := NewEnvironmentSource()
	section, err := s.Assemble(context.Background(), Env{
		OS:      "linux",
		CWD:     "/tmp",
		Date:    "2026-06-06",
		Version: "1.0.5",
	})
	if err != nil {
		t.Fatalf("Assemble 失败: %v", err)
	}
	if !strings.Contains(section.Content, "Date: 2026-06-06") {
		t.Errorf("Content 应包含 'Date: 2026-06-06'，实际:\n%s", section.Content)
	}
	if !strings.Contains(section.Content, "CodePilot version: 1.0.5") {
		t.Errorf("Content 应包含 'CodePilot version: 1.0.5'，实际:\n%s", section.Content)
	}
}

// TestEnvironmentSource_TokensPositive 验证 Tokens > 0。
func TestEnvironmentSource_TokensPositive(t *testing.T) {
	s := NewEnvironmentSource()
	section, err := s.Assemble(context.Background(), Env{OS: "linux", CWD: "/tmp"})
	if err != nil {
		t.Fatalf("Assemble 失败: %v", err)
	}
	if section.Tokens <= 0 {
		t.Errorf("Tokens 应 > 0，得到 %d", section.Tokens)
	}
}

// TestEnvironmentSource_XMLStructure 验证 Content 包含 <environment> 包裹。
func TestEnvironmentSource_XMLStructure(t *testing.T) {
	s := NewEnvironmentSource()
	section, err := s.Assemble(context.Background(), Env{OS: "linux", CWD: "/tmp"})
	if err != nil {
		t.Fatalf("Assemble 失败: %v", err)
	}
	if !strings.HasPrefix(section.Content, "<environment>") {
		t.Errorf("Content 应以 <environment> 开头")
	}
	if !strings.HasSuffix(section.Content, "</environment>") {
		t.Errorf("Content 应以 </environment> 结尾")
	}
}

// initTempGitRepo 在 t.TempDir() 基础上初始化一个 git 仓库，切换到指定 branch，
// 可选地创建一个 untracked 文件。
// 返回仓库路径。
func initTempGitRepo(t *testing.T, branch string, withUntracked bool) string {
	t.Helper()
	repo := t.TempDir()

	run := func(name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = repo
		// 在 Windows 上避免 git 弹密码输入
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=echo")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v 失败: %v\n输出: %s", args, err, out)
		}
	}

	run("git", "init", "-q", "-b", branch)
	run("git", "config", "user.email", "test@example.com")
	run("git", "config", "user.name", "Test")
	run("git", "config", "commit.gpgsign", "false")

	// 必须有至少一次 commit 才能让 rev-parse 拿到 branch
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("init"), 0644); err != nil {
		t.Fatalf("写 README.md 失败: %v", err)
	}
	run("git", "add", "README.md")
	run("git", "commit", "-q", "-m", "initial commit")

	if withUntracked {
		if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("dirty"), 0644); err != nil {
			t.Fatalf("写 untracked.txt 失败: %v", err)
		}
	}
	return repo
}
