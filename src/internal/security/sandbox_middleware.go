package security

import (
	"context"
	"encoding/json"

	"go.uber.org/zap"

	"github.com/MeiCorl/CodePilot/src/internal/logger"
	"github.com/MeiCorl/CodePilot/src/internal/tool"
)

// ---------------------------------------------------------------------------
// 路径类工具注册表（PathTools）
// ---------------------------------------------------------------------------
//
// PathTools 描述路径类工具的元数据（toolName → paramKey）。
//
// **维护者注意**：新增路径类工具（含 Step 6 的 MCP 文件系统工具）必须在此注册。
// 注册后 SandboxMiddleware 会自动在 ToolHandler 层拦截该工具的路径参数，
// 越界路径在 Handler 入口就被拒绝，工具 Execute 不会被调用。
//
// key 格式：本表沿用大驼峰工具名（与 builtin 包的 ToolName 常量值一致），
// 保持与现有 pathTools 概念零 breaking change。
var PathTools = map[string]string{
	"ReadFile":  "file_path",
	"WriteFile": "file_path",
	"EditFile":  "file_path",
	"Glob":      "path",
	"Grep":      "path",
}

// PathToolPermissions 描述路径类工具的读写权限，用于沙箱外路径在
// SandboxMiddleware 兜底阶段复用权限模式矩阵。
var PathToolPermissions = map[string]tool.ToolPermission{
	"ReadFile":  tool.PermRead,
	"Glob":      tool.PermRead,
	"Grep":      tool.PermRead,
	"WriteFile": tool.PermWrite,
	"EditFile":  tool.PermWrite,
}

// IsPathTool 判断 toolName 是否为路径类工具；ok=true 时 paramKey 为
// 该工具路径参数在 params map 中的键名（如 "file_path" / "path"）。
func IsPathTool(toolName string) (paramKey string, ok bool) {
	paramKey, ok = PathTools[toolName]
	return
}

// PathToolPermission 返回路径类工具的权限分级。
func PathToolPermission(toolName string) (tool.ToolPermission, bool) {
	perm, ok := PathToolPermissions[toolName]
	return perm, ok
}

// ---------------------------------------------------------------------------
// PathResolver：Middleware 注入 ctx 的"已解析绝对路径"表
// ---------------------------------------------------------------------------

// PathResolver 持有 SandboxMiddleware 解析后的绝对路径，按 paramKey 索引。
//
// 工具侧通过 PathResolverFromContext(ctx) 取出后，按 key 拿绝对路径使用，
// 不再自行调用 ResolveInSandbox——保证沙箱校验只跑一次且不漏防。
type PathResolver struct {
	abs map[string]string
}

// NewPathResolver 构造一个空的 PathResolver。
func NewPathResolver() *PathResolver {
	return &PathResolver{abs: make(map[string]string)}
}

// Set 写入 key→absPath 映射。同 key 覆盖式写入（同一工具多次 Middleware 调用场景）。
func (r *PathResolver) Set(key, absPath string) {
	r.abs[key] = absPath
}

// Get 按 key 取出绝对路径；不存在时返回 ("", false)。
func (r *PathResolver) Get(key string) (string, bool) {
	v, ok := r.abs[key]
	return v, ok
}

// All 返回内部 map 的浅拷贝，供调试/日志使用。
func (r *PathResolver) All() map[string]string {
	out := make(map[string]string, len(r.abs))
	for k, v := range r.abs {
		out[k] = v
	}
	return out
}

// ---------------------------------------------------------------------------
// PathRuleProvider：路径级 allow 规则查询接口
// ---------------------------------------------------------------------------

// PathRuleProvider 是 SandboxMiddleware 越界查询的抽象接口。
//
// 为什么需要：用户通过"永久允许"或"本会话允许"生成的路径级规则（如
// `{ReadFile, /tmp/*, allow}`）既要在 Checker 层被识别（避免每次都弹窗），
// 也要在 Middleware 层被识别（避免硬兜底拦截已授权路径）。
//
// 实现方：*Checker（见 Checker.MatchPathRule）。
//
// 语义：给定工具名 + 规范化后的绝对路径，查询是否有匹配的 allow 规则。
// 命中时 Middleware 应把该路径注入 PathResolver 并放行。
type PathRuleProvider interface {
	MatchPathRule(toolName, absPath string) (matched bool, reason string)
}

// ---------------------------------------------------------------------------
// MiddlewareFunc：Middleware 链上的单个处理函数
// ---------------------------------------------------------------------------

// MiddlewareFunc 是 Middleware 链上的单个处理函数签名。
//
// 语义：
//   - 返回 (outCtx, nil) → 放行，outCtx 注入到后续中间件与工具 Execute 的 ctx
//   - 返回 (_, err)      → 拦截，ToolHandler 立即返回 err，不调工具 Execute
//
// 参数：
//   - ctx:     来自 ToolHandler.doExecute 的 execCtx
//   - toolName: 当前工具名（大驼峰）
//   - input:   LLM 传入的原始 JSON 参数
//   - perm:    工具的权限分级（PermRead / PermWrite / PermExec）
type MiddlewareFunc func(
	ctx context.Context,
	toolName string,
	input json.RawMessage,
	perm tool.ToolPermission,
) (outCtx context.Context, err error)

// ---------------------------------------------------------------------------
// SandboxMiddleware：路径类工具的硬兜底中间件
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// MiddlewareOption：SandboxMiddleware 的可选能力（functional options）
// ---------------------------------------------------------------------------

// middlewareConfig 承载 SandboxMiddleware 的可选项，由 functional option 填充。
// 采用 option 模式而非改 SandboxMiddleware 签名，目的是对既有调用点
// （main.go 与既有单测的 SandboxMiddleware(workdir, provider) 形式）零侵入。
type middlewareConfig struct {
	// readRoots 附加只读根（绝对路径），仅对读取类工具（perm==PermRead）放宽沙箱。
	// Step 8 用于放行 ~/.codepilot/memory 与 <cwd>/.codepilot/memory，使 LLM
	// 能 ReadFile/Glob/Grep 读取记忆文件。写入/执行类工具忽略此项（纵深防御）。
	readRoots []string
}

// MiddlewareOption 配置 SandboxMiddleware 的可选能力。
type MiddlewareOption func(*middlewareConfig)

// WithReadRoots 注入「附加只读根」——仅对读取类工具（perm==PermRead）放宽沙箱，
// 使其可读取工作目录之外的白名单目录（Step 8 用于放行 memory 目录）。
// 写入/执行类工具不受影响：memory 目录对 WriteFile/EditFile 仍被沙箱拦截，
// 记忆只能经 autolearn.Store 受控写入，避免 LLM 绕过索引维护直接改文件。
func WithReadRoots(roots []string) MiddlewareOption {
	return func(c *middlewareConfig) {
		c.readRoots = roots
	}
}

// SandboxMiddleware 返回一个 MiddlewareFunc，对路径类工具执行沙箱解析。
//
// 行为：
//  1. toolName 不在 PathTools → 透传 ctx（无 op）
//  2. 从 input 解析出 params，提取 paramKey 对应的 path 字符串
//  3. path 为空时：
//     - Glob / Grep 默认 workdir（与原工具行为一致）
//     - 其它工具透传（由工具自身报"path 不能为空"）
//  4. 沙箱校验：读取类工具（perm==PermRead）附带附加只读根调
//     ResolveInSandboxWithRoots，其余工具调 ResolveInSandbox（仅 workdir）
//  5. 失败 → 查 ruleProvider → 命中则放行；未命中则返回 ErrPathOutsideSandbox
//  6. 成功 → 在 ctx 注入 PathResolver{abs: {paramKey: absPath}} 后放行
//
// ruleProvider 为 nil 时走旧硬兜底逻辑（用于向后兼容与单测）。
// opts 为空时（默认）不附带任何附加根，行为与改造前完全一致。
//
// 注意：Middleware 持有的 workdir / ruleProvider / readRoots 在闭包内捕获；
// 并发 Execute 场景下多个 goroutine 会各自拿到独立的 PathResolver 实例，
// 无共享状态（readRoots 切片构建后只读共享，安全）。
func SandboxMiddleware(workdir string, ruleProvider PathRuleProvider, opts ...MiddlewareOption) MiddlewareFunc {
	cfg := middlewareConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	// 闭包捕获 cfg.readRoots（构建后只读），供每次工具调用按 perm 决定是否启用。
	readRoots := cfg.readRoots

	return func(ctx context.Context, toolName string, input json.RawMessage, perm tool.ToolPermission) (context.Context, error) {
		paramKey, isPath := IsPathTool(toolName)
		if !isPath {
			return ctx, nil
		}

		// 解析 input params（解析失败按空 params 处理，与 interceptor 一致）
		params, err := parseInputParams(input)
		if err != nil {
			logger.WarnCtx(ctx, "SandboxMiddleware 解析工具输入失败，按空参数处理",
				zap.String("tool", toolName),
				zap.Error(err),
			)
			params = nil
		}

		pathStr := extractStringParam(params, paramKey)
		if pathStr == "" {
			// Glob / Grep 工具允许 path 为空，默搜 workdir
			if toolName == "Glob" || toolName == "Grep" {
				pathStr = workdir
			} else {
				// 其它工具透传，由工具自身在 Execute 内报"file_path 不能为空"
				return ctx, nil
			}
		}

		// 沙箱校验：仅读取类工具（PermRead）启用附加只读根（WithReadRoots），
		// 放行 ~/.codepilot/memory 等白名单目录；写入/执行类工具仅认 workdir，
		// 防止 memory 目录被 WriteFile/EditFile 直接写入（纵深防御）。
		// err 复用上方 parseInputParams 已声明的同名变量（同作用域，不可重新 :=）。
		var absPath string
		if perm == tool.PermRead && len(readRoots) > 0 {
			absPath, err = ResolveInSandboxWithRoots(pathStr, workdir, readRoots)
		} else {
			absPath, err = ResolveInSandbox(pathStr, workdir)
		}
		if err != nil {
			// 越界：若 ruleProvider 配置了路径级 allow 规则，查询是否命中；
			// 命中则放行（注入 PathResolver 携带规范化路径），未命中则保持硬兜底。
			if ruleProvider != nil {
				absForRule := normalizeForRule(pathStr, workdir)
				if matched, reason := ruleProvider.MatchPathRule(toolName, absForRule); matched {
					logger.InfoCtx(ctx, "SandboxMiddleware 越界路径被路径级规则放行",
						zap.String("tool", toolName),
						zap.String("param_key", paramKey),
						zap.String("raw_path", pathStr),
						zap.String("abs_path", absForRule),
						zap.String("reason", reason),
					)
					resolver := NewPathResolver()
					resolver.Set(paramKey, absForRule)
					return WithPathResolver(ctx, resolver), nil
				}
			}
			logger.InfoCtx(ctx, "SandboxMiddleware 拦截越界路径",
				zap.String("tool", toolName),
				zap.String("param_key", paramKey),
				zap.String("raw_path", pathStr),
				zap.String("workdir", workdir),
				zap.Error(err),
			)
			return ctx, err
		}

		resolver := NewPathResolver()
		resolver.Set(paramKey, absPath)
		return WithPathResolver(ctx, resolver), nil
	}
}

// ---------------------------------------------------------------------------
// ctx 工具函数（参考 tool.WithToolUseID 模式）
// ---------------------------------------------------------------------------

// pathResolverKey 是 context 中携带 PathResolver 的私有 key。
// 使用空 struct 类型避免与其他包的 ctx value key 冲突。
type pathResolverKey struct{}

// WithPathResolver 返回一个新的 context，携带指定的 PathResolver。
// resolver 为 nil 时返回原 ctx（不注入）。
func WithPathResolver(ctx context.Context, resolver *PathResolver) context.Context {
	if resolver == nil {
		return ctx
	}
	return context.WithValue(ctx, pathResolverKey{}, resolver)
}

// PathResolverFromContext 从 ctx 中取出由 WithPathResolver 注入的 PathResolver。
//
// 返回值：存在时 ok=true 且 resolver 非 nil；不存在或 resolver 为 nil 时 ok=false。
//
// 工具侧应把"未拿到 PathResolver"视为内部错误（沙箱未生效）并返回 error，
// 借此强制"任何路径类工具必须经 ToolHandler 调用"。
func PathResolverFromContext(ctx context.Context) (*PathResolver, bool) {
	if ctx == nil {
		return nil, false
	}
	v := ctx.Value(pathResolverKey{})
	if v == nil {
		return nil, false
	}
	r, ok := v.(*PathResolver)
	if !ok || r == nil {
		return nil, false
	}
	return r, true
}
