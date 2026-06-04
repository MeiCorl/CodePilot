package safety

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestResolveInSandboxRelativePath 验证相对路径被正确解析到 sandbox 内。
func TestResolveInSandboxRelativePath(t *testing.T) {
	sandbox := t.TempDir()
	got, err := ResolveInSandbox("./foo.txt", sandbox)
	if err != nil {
		t.Fatalf("预期放行, 实际错误: %v", err)
	}
	if !strings.HasPrefix(got, sandbox) {
		t.Errorf("结果不在 sandbox 内: %s", got)
	}
}

// TestResolveInSandboxAbsoluteInside 验证 sandbox 内的绝对路径放行。
func TestResolveInSandboxAbsoluteInside(t *testing.T) {
	sandbox := t.TempDir()
	target := filepath.Join(sandbox, "sub", "file.go")
	got, err := ResolveInSandbox(target, sandbox)
	if err != nil {
		t.Fatalf("预期放行, 实际错误: %v", err)
	}
	if got != target {
		t.Errorf("路径不一致: %s vs %s", got, target)
	}
}

// TestResolveInSandboxParentTraversal 验证 `..` 越界被拦截。
func TestResolveInSandboxParentTraversal(t *testing.T) {
	sandbox := t.TempDir()
	_, err := ResolveInSandbox("../../../etc/passwd", sandbox)
	if err == nil {
		t.Fatal("预期被拦截, 实际放行")
	}
	if !errors.Is(err, ErrPathOutsideSandbox) {
		t.Errorf("错误类型错误: %v", err)
	}
}

// TestResolveInSandboxAbsoluteOutside 验证 sandbox 外的绝对路径被拦截。
func TestResolveInSandboxAbsoluteOutside(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上无 etc/passwd 概念, 跳过")
	}
	sandbox := t.TempDir()
	_, err := ResolveInSandbox("/etc/passwd", sandbox)
	if err == nil {
		t.Fatal("预期被拦截, 实际放行")
	}
	if !errors.Is(err, ErrPathOutsideSandbox) {
		t.Errorf("错误类型错误: %v", err)
	}
}

// TestResolveInSandboxEmptyPath 验证空路径被拒。
func TestResolveInSandboxEmptyPath(t *testing.T) {
	_, err := ResolveInSandbox("", t.TempDir())
	if err == nil {
		t.Fatal("空路径应被拦截")
	}
}

// TestResolveInSandboxSymlinkOutside 验证 symlink 指向 sandbox 外被拦截。
// Windows 上创建 symlink 需要管理员权限，CI 环境不一定可用，跳过。
func TestResolveInSandboxSymlinkOutside(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上创建 symlink 需要特权, 跳过")
	}
	sandbox := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0644); err != nil {
		t.Fatalf("准备 sandbox 外文件失败: %v", err)
	}
	link := filepath.Join(sandbox, "escape")
	if err := os.Symlink(outsideFile, link); err != nil {
		t.Fatalf("创建 symlink 失败: %v", err)
	}
	_, err := ResolveInSandbox(link, sandbox)
	if err == nil {
		t.Fatal("symlink 越界应被拦截")
	}
	if !errors.Is(err, ErrPathOutsideSandbox) {
		t.Errorf("错误类型错误: %v", err)
	}
}

// TestResolveInSandboxSymlinkInside 验证 symlink 指向 sandbox 内放行。
func TestResolveInSandboxSymlinkInside(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上创建 symlink 需要特权, 跳过")
	}
	sandbox := t.TempDir()
	real := filepath.Join(sandbox, "real.txt")
	if err := os.WriteFile(real, []byte("ok"), 0644); err != nil {
		t.Fatalf("准备 sandbox 内文件失败: %v", err)
	}
	link := filepath.Join(sandbox, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("创建 symlink 失败: %v", err)
	}
	got, err := ResolveInSandbox(link, sandbox)
	if err != nil {
		t.Fatalf("sandbox 内 symlink 应放行: %v", err)
	}
	// 返回的应是 symlink 解析后的真实路径
	if !strings.HasPrefix(got, sandbox) {
		t.Errorf("结果不在 sandbox 内: %s", got)
	}
}
