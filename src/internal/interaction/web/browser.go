package web

import (
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
)

// browserLauncher 抽象出可被替换的命令执行入口；测试中可注入 mock 验证
// 命令构造而不实际启动浏览器。
type browserLauncher func(name string, args ...string) error

// defaultLauncher 通过 os/exec 实际启动命令；不等待命令结束，进程退出码
// 由浏览器自身决定，本函数仅在启动阶段返回错误（如可执行文件不存在）。
func defaultLauncher(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	return cmd.Start()
}

// openURLFunc 指向 OpenURL 内部使用的命令构造器。默认使用 defaultLauncher。
var openURLFunc browserLauncher = defaultLauncher

// OpenURL 在系统默认浏览器中打开 target。
//
// 仅支持 http 与 https 协议，避免误触发 file://、javascript: 等危险 scheme。
// 打开失败时仅返回错误，调用方决定如何处理（打印警告 vs 中断），
// 不在本函数内 panic。
func OpenURL(target string) error {
	if target == "" {
		return fmt.Errorf("web: open url: target is empty")
	}
	u, err := url.Parse(target)
	if err != nil {
		return fmt.Errorf("web: open url: parse %q: %w", target, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("web: open url: unsupported scheme %q", u.Scheme)
	}
	switch runtime.GOOS {
	case "windows":
		// start 是 cmd 内置命令，必须通过 cmd /c 启动；
		// 第一个 "" 是 start 的窗口标题占位，避免目标 URL 被误识别为标题。
		return openURLFunc("cmd", "/c", "start", "", target)
	case "darwin":
		return openURLFunc("open", target)
	case "linux":
		return openURLFunc("xdg-open", target)
	default:
		return fmt.Errorf("web: open url: unsupported platform %q", runtime.GOOS)
	}
}
