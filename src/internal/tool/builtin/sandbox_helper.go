package builtin

import (
	"context"
	"fmt"

	"github.com/MeiCorl/CodePilot/src/internal/security"
)

// resolvePathFromContext 从 ctx 取出 SandboxMiddleware 注入的 PathResolver，
// 按 paramKey（如 "file_path" / "path"）取出已校验的绝对路径。
//
// 错误语义：
//   - ctx 中无 PathResolver → 视为内部错误（工具未走 ToolHandler）
//   - PathResolver 中缺 paramKey 键 → 视为内部错误（Middleware 行为异常）
//
// 两类错误都返回 error，由 ToolHandler 统一封装为 ToolResultBlock{IsError: true}。
// 此处不返回 security.ErrPathOutsideSandbox，因为越界拦截在 Middleware 层
// 已经处理，工具 Execute 不会被越界调用。
func resolvePathFromContext(ctx context.Context, paramKey string) (string, error) {
	resolver, ok := security.PathResolverFromContext(ctx)
	if !ok {
		return "", fmt.Errorf("沙箱未生效：ctx 中无 PathResolver（工具可能未走 ToolHandler）")
	}
	absPath, ok := resolver.Get(paramKey)
	if !ok {
		return "", fmt.Errorf("沙箱未生效：PathResolver 缺失 %q 键", paramKey)
	}
	return absPath, nil
}
