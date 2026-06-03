// Package builtin 提供 CodePilot 的内置工具集。
// 本文件实现 init() 集中注册入口，所有内置工具在包加载时
// 自动注册到 tool.DefaultRegistry()。
//
// 调用方（main.go）只需：
//
//	import _ "src/tool/builtin"  // 触发 init()
//
// 即可使用 Registry.List() 拿到 5 个内置工具。
// 后续新增工具只需在本包添加实现，并在 Register() 函数中追加一行。
package builtin

import (
	"os"
	"time"

	"github.com/MeiCorl/CodePilot/src/tool"
)

// DefaultWorkingDirectory 是注册时使用的默认工作目录。
// 在 init 时通过 os.Getwd() 获取；主流程可通过显式 New*Tool
// 覆盖（Task 8 在 conversation manager 中传入配置 working_directory）。
var DefaultWorkingDirectory string

// DefaultBashTimeout 是 Bash 工具的默认执行超时。
// 同样在 init 时设置；Task 8 会从 config 读取覆盖。
var DefaultBashTimeout = 30 * time.Second

// init 在 builtin 包加载时完成：
//  1. 设置默认工作目录
//  2. 批量注册所有内置工具
func init() {
	if wd, err := os.Getwd(); err == nil {
		DefaultWorkingDirectory = wd
	}
	Register(tool.DefaultRegistry())
}

// Register 把所有内置工具注册到指定 Registry。
// 测试可通过传入独立的 Registry 隔离环境。
func Register(r *tool.Registry) {
	RegisterWithOptions(r, DefaultWorkingDirectory, DefaultBashTimeout)
}

// RegisterWithOptions 用显式的工作目录与 Bash 超时注册所有内置工具。
//
// 适用于主流程在加载 config 后调用：用 cfg.ToolWorkingDirectory /
// cfg.ToolExecutionTimeoutSeconds 重新构造 5 个工具实例并覆盖
// init() 时按 cwd 兜底注册的实例，保证配置字段真正生效。
func RegisterWithOptions(r *tool.Registry, workdir string, bashTimeout time.Duration) {
	r.MustReplace(
		NewReadFileTool(workdir),
		NewWriteFileTool(workdir),
		NewBashTool(bashTimeout),
		NewGlobTool(workdir),
		NewGrepTool(workdir),
	)
}
