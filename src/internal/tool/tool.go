// Package tool 提供 CodePilot 的工具系统基石——Tool 接口、全局 Registry
// 以及统一的权限分级。所有内置工具（MCP 工具、Skill 工具、SubAgent 工具）
// 均遵循 Tool 接口契约，实现"新增工具不改系统代码"。
package tool

import (
	"context"
	"encoding/json"
)

// ToolPermission 标识工具对宿主系统的影响范围，供权限系统（Step 5）做安全分级。
// 本步骤仅作信息标注，不做强制拦截；强制拦截由工具自身 + safety 包负责。
type ToolPermission int

const (
	// PermRead 只读操作，不修改任何外部状态（读文件、搜索内容、查找文件）。
	PermRead ToolPermission = iota
	// PermWrite 写操作，会修改文件系统（创建/覆盖文件）。
	PermWrite
	// PermExec 执行操作，会启动子进程或执行 Shell 命令。
	PermExec
)

// String 返回权限的可读名称，便于日志与 UI 展示。
func (p ToolPermission) String() string {
	switch p {
	case PermRead:
		return "read"
	case PermWrite:
		return "write"
	case PermExec:
		return "exec"
	default:
		return "unknown"
	}
}

// Tool 是所有工具必须实现的统一接口。
//
// 上层（conversation manager、Registry）只通过此接口与工具交互；
// 工具的注册、查找、执行均基于 Name()，因此 Name 必须全局唯一且 snake_case。
//
// 工具实现应满足：
//   - Name() 返回稳定的 snake_case 标识，跨会话持久化需保持一致
//   - Description() 给 LLM 阅读，应清晰说明使用场景与限制
//   - InputSchema() 返回标准 JSON Schema（含 type/properties/required），用于 LLM 理解参数结构
//   - Execute() 必须响应 ctx.Done()，被取消时尽快返回
type Tool interface {
	// Name 返回工具名（snake_case，全局唯一）。
	Name() string
	// Description 返回工具描述，会被发给 LLM 帮助其理解工具用途。
	Description() string
	// InputSchema 返回工具输入参数的 JSON Schema。
	// 推荐使用 struct tag + jsonschema 反射生成，确保与 Execute 的入参严格一致。
	InputSchema() json.RawMessage
	// Permission 返回工具的权限分级。
	Permission() ToolPermission
	// Execute 执行工具逻辑。
	//
	// 参数：
	//   - ctx: 支持通过 cancel 终止工具执行（用户中止 / 超时 / 进程退出）
	//   - input: LLM 传入的参数，原始 JSON 字节；工具自行 Unmarshal 到内部结构
	//
	// 返回值：
	//   - output: 执行成功时的文本结果，会被回传给 LLM
	//   - err: 执行失败；非 nil 时 ToolHandler 会封装为 is_error=true 的 ToolResultBlock
	//
	// Execute 必须：
	//   - 响应 ctx.Done()，被取消时返回 ctx.Err()
	//   - 不向 panic 逃逸；如确需捕获内部 panic 后转为 error
	//   - 输出尽量结构化（文件名、行号、错误描述），便于 LLM 二次决策
	Execute(ctx context.Context, input json.RawMessage) (output string, err error)
}

// BaseTool 提供 Tool 接口的元数据公共字段，内置到具体工具结构体中即可减少样板。
// 工具只需实现 Execute() 与构造时填入的元数据，其他方法由 BaseTool 提供默认实现。
type BaseTool struct {
	ToolName        string
	ToolDescription string
	ToolInputSchema json.RawMessage
	ToolPermission  ToolPermission
}

// Name 返回工具名。
func (b *BaseTool) Name() string { return b.ToolName }

// Description 返回工具描述。
func (b *BaseTool) Description() string { return b.ToolDescription }

// InputSchema 返回工具输入参数的 JSON Schema。
func (b *BaseTool) InputSchema() json.RawMessage { return b.ToolInputSchema }

// Permission 返回工具的权限分级。
func (b *BaseTool) Permission() ToolPermission { return b.ToolPermission }
