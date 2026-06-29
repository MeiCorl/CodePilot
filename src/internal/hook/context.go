// Package hook — HookContext + 构造工厂 + 变量映射的对外别名层。
//
// 实际实现已迁出到 internal/hookcontext 子包(为避免 hook ↔ executor 双向
// 依赖产生的 import cycle)。本文件通过 Go type alias 重新暴露给 hook 包
// 调用方,保持以下 API 兼容:
//   - hook.HookContext == hookcontext.HookContext(同类型,字段可读)
//   - hook.NewPreToolUseContext(...) == hookcontext.NewPreToolUseContext(...)
//   - hook.ExtractToolInputFilePath(...) == hookcontext.ExtractToolInputFilePath(...)
//   - hook.Vars() 调用形式保留(由 HookContext 自身方法承载)
//
// 历史背景:Task 2 首次实现时,HookContext / Interpolate 直接放在 hook 父包;
// Task 5 引入 Engine 时发现 executor 子包已 import hook.HookContext,导致
// hook → executor 引入 cycle。本次重构最小化破坏面,只把实现迁出,保留
// 所有 public API(测试代码 + 集成点代码无需改 package import)。
package hook

import "github.com/MeiCorl/CodePilot/src/internal/hookcontext"

// HookContext 是 Hook 触发时的上下文载体。
//
// type alias 而非 named type:调用方拿到的仍是 hookcontext.HookContext 同一类型,
// 字段读写 / 类型断言 / 反序列化都透明。spec §E 字段含义详见 hookcontext.HookContext。
type HookContext = hookcontext.HookContext

// 构造工厂函数(对 hookcontext 工厂的透传)。
var (
	NewProgramContext     = hookcontext.NewProgramContext
	NewErrorContext       = hookcontext.NewErrorContext
	NewCompactContext     = hookcontext.NewCompactContext
	NewSessionContext     = hookcontext.NewSessionContext
	NewIterationContext   = hookcontext.NewIterationContext
	NewPreToolUseContext  = hookcontext.NewPreToolUseContext
	NewPostToolUseContext = hookcontext.NewPostToolUseContext
	NewMessageContext     = hookcontext.NewMessageContext
)

// ExtractToolInputFilePath 透传到 hookcontext 版本(spec §E 路径提取)。
var ExtractToolInputFilePath = hookcontext.ExtractToolInputFilePath
