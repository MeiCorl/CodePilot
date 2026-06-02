//go:build windows

package console

import (
	"syscall"
)

var (
	kernel32                  = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleWindow      = kernel32.NewProc("GetConsoleWindow")
	user32                    = syscall.NewLazyDLL("user32.dll")
	procShowWindow            = user32.NewProc("ShowWindow")
	procIsWindowVisible       = user32.NewProc("IsWindowVisible")
)

// ShowWindow 第二个参数的取值。
const (
	swHide         = 0
	swShowNoActive = 4 // 兼容备用：若 ShowWindow 调用失效可用此值重新显示
)

// hide 隐藏当前进程附属的控制台窗口。
//
// 实现说明：
//   - GetConsoleWindow 失败（返回 0）通常意味着进程没有控制台
//     （例如从 Windows 服务启动、或编译为 -H windowsgui 的 GUI 程序），
//     此时直接 no-op，不算错误。
//   - ShowWindow 是非阻塞调用，立即返回，调用结果一般不关心。
//   - 不捕获 lastErr：失败时（理论上几乎不可能）保留默认行为，
//     进程继续运行即可。
func hide() {
	hwnd, _, _ := procGetConsoleWindow.Call()
	if hwnd == 0 {
		return
	}
	_, _, _ = procShowWindow.Call(hwnd, uintptr(swHide))
}

// visible 判断当前进程的控制台窗口当前是否可见。
//   - 没有控制台（hwnd == 0）→ false
//   - 有控制台但已隐藏 → false
//   - 有控制台且可见 → true
func visible() bool {
	hwnd, _, _ := procGetConsoleWindow.Call()
	if hwnd == 0 {
		return false
	}
	// IsWindowVisible 返回 BOOL（非 0 表示可见）
	ret, _, _ := procIsWindowVisible.Call(hwnd)
	return ret != 0
}

