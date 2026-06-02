package web

import (
	"errors"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// mockLauncher 记录 OpenURL 构造的命令而不实际执行；可注入 err 模拟启动失败。
type mockLauncher struct {
	calls []mockCall
	err   error
}

type mockCall struct {
	name string
	args []string
}

func (m *mockLauncher) run(name string, args ...string) error {
	m.calls = append(m.calls, mockCall{name: name, args: append([]string(nil), args...)})
	return m.err
}

// withMockLauncher 临时把 openURLFunc 替换为 mock，完成后恢复；返回 mock 指针。
func withMockLauncher(t *testing.T) *mockLauncher {
	t.Helper()
	mock := &mockLauncher{}
	orig := openURLFunc
	openURLFunc = mock.run
	t.Cleanup(func() { openURLFunc = orig })
	return mock
}

// TestOpenURLDispatchesByPlatform 验证不同平台下命令构造符合预期。
// 不实际打开浏览器（通过 mockLauncher 拦截）。
func TestOpenURLDispatchesByPlatform(t *testing.T) {
	mock := withMockLauncher(t)

	const url = "http://localhost:8969"
	if err := OpenURL(url); err != nil {
		t.Fatalf("OpenURL returned error: %v", err)
	}
	if len(mock.calls) != 1 {
		t.Fatalf("expected 1 launcher call, got %d", len(mock.calls))
	}
	call := mock.calls[0]

	var wantName string
	var wantArgs []string
	switch runtime.GOOS {
	case "windows":
		wantName, wantArgs = "cmd", []string{"/c", "start", "", url}
	case "darwin":
		wantName, wantArgs = "open", []string{url}
	case "linux":
		wantName, wantArgs = "xdg-open", []string{url}
	default:
		t.Skipf("unsupported platform: %s", runtime.GOOS)
	}

	if call.name != wantName {
		t.Errorf("command name: got %q, want %q", call.name, wantName)
	}
	if !reflect.DeepEqual(call.args, wantArgs) {
		t.Errorf("command args: got %v, want %v", call.args, wantArgs)
	}
}

// TestOpenURLRejectsEmpty 验证空字符串被拒绝且不调用 launcher。
func TestOpenURLRejectsEmpty(t *testing.T) {
	mock := withMockLauncher(t)

	err := OpenURL("")
	if err == nil {
		t.Fatal("expected error for empty url")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error should mention 'empty', got: %v", err)
	}
	if len(mock.calls) != 0 {
		t.Errorf("launcher should not be called for empty url, got %d calls", len(mock.calls))
	}
}

// TestOpenURLRejectsUnsupportedScheme 验证 file / javascript / ftp 等
// 危险或非 http(s) 协议被拒绝。
func TestOpenURLRejectsUnsupportedScheme(t *testing.T) {
	cases := []string{
		"file:///etc/passwd",
		"javascript:alert(1)",
		"ftp://example.com",
		"ssh://user@host",
		"about:blank",
	}
	for _, target := range cases {
		t.Run(target, func(t *testing.T) {
			mock := withMockLauncher(t)

			err := OpenURL(target)
			if err == nil {
				t.Fatalf("expected error for %q", target)
			}
			if !strings.Contains(err.Error(), "unsupported scheme") {
				t.Errorf("error should mention 'unsupported scheme', got: %v", err)
			}
			if len(mock.calls) != 0 {
				t.Errorf("launcher should not be called for unsupported scheme, got %d calls", len(mock.calls))
			}
		})
	}
}

// TestOpenURLAcceptsHTTPS 验证 https 协议被接受。
func TestOpenURLAcceptsHTTPS(t *testing.T) {
	mock := withMockLauncher(t)

	if err := OpenURL("https://example.com/path?q=1"); err != nil {
		t.Fatalf("https should be accepted, got: %v", err)
	}
	if len(mock.calls) != 1 {
		t.Fatalf("expected 1 launcher call, got %d", len(mock.calls))
	}
	// https URL 应当完整传递给 launcher，包括 path 与 query。
	if got := mock.calls[0].args[len(mock.calls[0].args)-1]; got != "https://example.com/path?q=1" {
		t.Errorf("launcher got URL %q, want full https URL", got)
	}
}

// TestOpenURLPropagatesLauncherError 验证底层启动失败时错误被透传给调用方。
func TestOpenURLPropagatesLauncherError(t *testing.T) {
	mock := withMockLauncher(t)
	sentinel := errors.New("executable file not found")
	mock.err = sentinel

	err := OpenURL("http://localhost:8969")
	if err == nil {
		t.Fatal("expected error to be propagated from launcher")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel error, got: %v", err)
	}
}
