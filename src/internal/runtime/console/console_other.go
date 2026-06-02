//go:build !windows

package console

// hide 在非 Windows 平台是 no-op：Linux/macOS 进程通常在终端里
// 被启动（开发场景）或被 nohup/daemon 化（生产场景），没有等价于
// Windows 控制台窗口的"附属 UI"概念，无需隐藏。
func hide() {}

// visible 在非 Windows 平台始终返回 true。语义上"当前进程关联了
// 一个终端"这件事一般成立，方便上层代码统一处理。
func visible() bool { return true }
