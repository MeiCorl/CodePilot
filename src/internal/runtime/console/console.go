// Package console 提供跨平台的控制台窗口操作能力。
//
// 主要用途是支持「启动后台运行的 CodePilot」：在 Windows 上以
// console application 形式编译的 exe 会带一个黑色控制台窗口，
// 默认情况下用户希望浏览器被打开后该窗口被自动隐藏。其它平台
// 没有等价概念，相关函数被实现为 no-op。
//
// 当前提供两个能力：
//   - Hide: 隐藏进程附属的控制台窗口（Windows 有效，其他平台 no-op）
//   - Visible: 探测当前进程是否关联了可见控制台（Windows 有效，其他平台返回 true）
package console

// Hide 隐藏当前进程的控制台窗口。
//
// 仅当进程是 console application 且具有控制台时有效：会通过
// GetConsoleWindow 拿到窗口句柄，再调用 ShowWindow(SW_HIDE)。
// 如果进程没有控制台（例如 Linux daemon 或编译为 -H windowsgui
// 的 Windows GUI 程序），不会产生任何效果。
//
// 实现差异：
//   - Windows: 真正调用 user32.ShowWindow(kernel32.GetConsoleWindow(), SW_HIDE)
//   - 其它平台: no-op
func Hide() {
	hide()
}

// Visible 返回当前进程是否关联了可见的控制台窗口。
// 用于在 Hide 之前判断是否真的需要隐藏，避免无谓的 syscall 开销。
//
// 实现差异：
//   - Windows: 通过 GetConsoleWindow + IsWindowVisible 判定
//   - 其它平台: 始终返回 true（语义上等价于「有终端」）
func Visible() bool {
	return visible()
}
