// Package template 提供 System Prompt 的输入参数与模板渲染能力。
//
// 之所以独立成子包：
//   - sources 子包需要使用 Env 类型 + Render 函数来产出 Section
//   - prompt（Builder）子包需要使用 Env 类型调用 Source.Assemble
//   - 如果 Env/Render 定义在 sources 包，static/environment 会反向依赖 sources（循环）
//   - 如果定义在 prompt 包，sources 也会反向依赖 prompt（循环）
//   - 提取到独立的 template 子包后，sources 与 prompt 都能单向依赖 template
//
// 本包是整个 prompt 模块的「叶子节点」，不应 import 任何 prompt 内部子包。
package template

// GitStatus 描述当前工作目录的 Git 状态，由 environment Source 采集。
// 任一字段采集失败时降级为 "unknown"（branch/lastCommit）或 false（dirty），
// 永不上抛错误——环境信息的缺失不应阻塞会话启动。
type GitStatus struct {
	// Branch 为当前分支名（如 "master"、"main"），非 git 仓库时为 ""。
	Branch string
	// Dirty 表示工作区是否有未提交变更（含 untracked）。
	Dirty bool
	// LastCommit 为最近一次 commit 的简短描述（git log -1 --oneline）。
	LastCommit string
}

// Env 是 Source.Assemble 接收的输入环境参数。
//
// Env 在一次 Assemble 调用内只读；所有字段由调用方（handler）
// 在会话启动时一次性采集并传入，避免 Source 内出现 os 调用
// 散落导致难以测试。
type Env struct {
	// OS 为当前操作系统标识（runtime.GOOS），如 "windows"/"linux"/"darwin"。
	OS string
	// CWD 为当前工作目录的绝对路径，且已通过 filepath.EvalSymlinks
	// resolve 为真实路径。
	CWD string
	// GitStatus 为当前工作目录的 Git 状态，非 git 仓库时为零值。
	GitStatus GitStatus
	// Date 为当前日期字符串，格式 "YYYY-MM-DD"。
	Date string
	// Version 为 CodePilot 程序自身的版本号（来自 build flag）。
	Version string
	// StaticOverrides 允许按子模块名覆盖静态 SP 的内容，
	// key 为子模块名（如 "system_role"），value 为替换内容。
	// 留空时使用硬编码默认内容；该机制主要给开发者模式做实验用。
	StaticOverrides map[string]string
}
