// Package executor 实现 CodePilot Hook 系统的 4 种 Action 执行器（spec §D）。
//
// 4 个执行器共享同一接口 Executor,使得 HookEngine 能以「事件 → 数组 → 顺序执行」
// 方式串起任意组合。错误隔离（panic recover / 上抛 error → 记 warn 不传播）由
// RunSafe 包装函数统一处理,各 executor 内不再显式 recover。
//
// 设计要点:
//   - 单一职责:每个执行器只负责「执行单一动作」,变量插值 / 超时控制 / 日志埋点
//     由公共层 + Engine 协作完成;
//   - 同步语义为主,async 由 Engine 在 goroutine 包装,executor 不感知并发;
//   - 返回 error 仅用于日志诊断,Engine 总是 warn 记录后继续下一条 entry,
//     绝不会把 executor 的 error 冒泡到主 Agent Loop。
package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/MeiCorl/CodePilot/src/internal/hookcontext"
	"go.uber.org/zap"
)

// Executor 是 4 种 action 的统一抽象接口。
//
// [Why interface 而非 func 字段] 4 种 action 的入参/返回值差异大:CommandExecutor
// 不需要 LLM provider / tool registry,HttpExecutor 不需要 LLM,只有 AgentExecutor
// 需要完整 Provider + Registry。interface 把「构造期依赖」与「Execute 调用」分离,
// Engine 在 LoadEntries 阶段一次性实例化好,运行时按 type 调度无需再判分支。
//
// Type() 用于 Engine 在错误日志 / Stats 中标识调用方（"command" / "http" / ...）,
// Execute 是唯一可调用方法,语义与 spec §D 各小节一一对应。
type Executor interface {
	// Type 返回 action 类型字符串，与 config.HookActionConfig.Type 同值。
	Type() string

	// Execute 执行单次 action。Engine 负责：
	//   - 命中条件后调用 Execute；
	//   - 传入执行上下文 ctx（已带超时）；
	//   - 传入 HookContext（事件上下文，Executor 可读但不应修改）；
	//   - 传入 vars（HookContext.Vars 产出，供 $VAR 插值）；
	//   - 接收返回 error 用于日志诊断（不向上抛）。
	Execute(ctx context.Context, hookCtx *hookcontext.HookContext, vars map[string]string) error
}

// Factory 是 action 反序列化工厂签名。Engine 在 LoadEntries 阶段为每条 entry
// 选取对应 Executor。
//
// [Why Factory 形式而非纯 type switch] 把 action 反序列化（HookActionConfig.Raw
// → 具体 executor config）与 executor 构造合并到一个工厂,Engine 只做 type
// 分发,不暴露内部 executor 类型;后续新增 action 类型只需追加工厂实现。
type Factory func(action json.RawMessage) (Executor, error)

// ----- 公共错误类型 -----

// CommandError 标记 command action 退出码非 0。
//
// [Why 独立错误类型] Engine 只需 warn 记录(不传播),但用户日志需要明确区分
// 「超时」/「命令失败」/「环境错误」三种来源以便排错。errors.Is 配合 wrap
// 链路可让上层日志携带具体 ExitCode + stderr 片段。
type CommandError struct {
	// ExitCode 为子进程退出码（非 0 表示失败）。
	ExitCode int
	// Stderr 为子进程 stderr 截断输出（最多保留 stderrSnippetLimit 字节）。
	Stderr string
	// Command 为出问题的命令原文，便于日志。
	Command string
}

// Error 实现 error 接口。
func (e *CommandError) Error() string {
	if e.Stderr == "" {
		return fmt.Sprintf("hook command exit %d: %s", e.ExitCode, e.Command)
	}
	// stderr 片段按单行/截断显示,避免超长日志
	snippet := e.Stderr
	if len(snippet) > 200 {
		snippet = snippet[:200] + "..."
	}
	return fmt.Sprintf("hook command exit %d (stderr=%q): %s", e.ExitCode, snippet, e.Command)
}

// HttpError 标记 http action 收到非 2xx 响应。
//
// [Why 保留 body 片段] 4xx/5xx body 通常含错误描述（如 Slack webhook 返回
// "channel_not_found"），截断保留前 512 字节足够排错又不撑爆日志。
type HttpError struct {
	// StatusCode 为响应状态码（如 404 / 500）。
	StatusCode int
	// Body 为响应 body 截断片段（前 512 字节）。
	Body string
	// Method + URL 方便日志定位。
	Method string
	URL    string
}

// Error 实现 error 接口。
func (e *HttpError) Error() string {
	body := e.Body
	if len(body) > 200 {
		body = body[:200] + "..."
	}
	return fmt.Sprintf("hook http %s %s status=%d body=%q", e.Method, e.URL, e.StatusCode, body)
}

// ErrCommandTimeout 标记 command action 超时（ctx 被取消）。
//
// [Why sentinel error] 调用方可用 errors.Is(err, ErrCommandTimeout) 区分
// 超时与一般命令失败;Engine 仍走 warn 记录不传播,但业务日志可按此判断
// 用户是否需要延长 timeout 字段。
var ErrCommandTimeout = fmt.Errorf("hook command timeout")

// ErrEmptyPrompt 标记 prompt action 文本为空（template 替换后为空 + as 必填）。
var ErrEmptyPrompt = fmt.Errorf("hook prompt text empty")

// ErrInvalidPromptAs 标记 prompt action 的 as 字段非合法值（本期仅允许 system_reminder）。
type ErrInvalidPromptAs struct{ As string }

// Error 实现 error 接口。
func (e *ErrInvalidPromptAs) Error() string {
	return fmt.Sprintf("hook prompt as=%q not supported, only %q in current step", e.As, promptAsSystemReminder)
}

// ErrEmptyAgentPrompt 标记 agent action 的 prompt 模板为空。
var ErrEmptyAgentPrompt = fmt.Errorf("hook agent prompt empty")

// ErrNoLLMProvider 标记 agent action 缺少可用的 LLM provider。
type ErrNoLLMProvider struct{}

// Error 实现 error 接口。
func (e *ErrNoLLMProvider) Error() string {
	return "hook agent action: no LLM provider configured"
}

// ErrInvalidURLScheme 标记 http action 的 URL 非 http/https。
type ErrInvalidURLScheme struct{ URL string }

// Error 实现 error 接口。
func (e *ErrInvalidURLScheme) Error() string {
	return fmt.Sprintf("hook http url %q must use http or https scheme", e.URL)
}

// stderrSnippetLimit 是 CommandError 保留 stderr 的最大字节数。
const stderrSnippetLimit = 512

// httpBodySnippetLimit 是 HttpError 保留 body 的最大字节数。
const httpBodySnippetLimit = 512

// promptAsSystemReminder 是 prompt action 本期唯一合法的 as 取值。
//
// [Why 硬编码白名单而非允许任意字符串] spec §D.2 明确「当前仅支持这一种」,
// 后续增加新形态（user_role_inject / assistant_role_hint 等）时,只需在此
// 常量旁加分支 + 更新 ValidateHookConfig,业务调用点零改动。
const promptAsSystemReminder = "system_reminder"

// PromptResult 是 PromptExecutor 的「待注入文本」取值辅助载体（在 prompt.go
// 实现的具体取值走 PromptExecutor.Last() 字段,本类型保留兼容的常量定义,以
// 备未来 prompt action 变体（如多段注入）扩展需要返回更丰富结构）。
//
// 兼容性保留:Engine 在 hook Step 6 用类型断言读取 last 文本,本类型未实际使
// 用,仅为公共契约点保留演进空间。后续 prompt action 支持多段注入时,可改为
// PromptExecutor.LastN() 返回 []PromptResult,本类型承载多段场景。
type PromptResult struct {
	// Text 为变量替换后的最终文本，已含 <system-reminder>...</system-reminder> 包裹。
	Text string
}

// ActionType 常量集合，与 config.HookActionConfig.Type 同值，便于代码内复用。
const (
	ActionTypeCommand = "command"
	ActionTypeHTTP    = "http"
	ActionTypePrompt  = "prompt"
	ActionTypeAgent   = "agent"
)

// ParseActionType 从 action 的 raw JSON 中提取 type 字段。type 缺失或为非
// 字符串时返回错误。Engine 在 LoadEntries 之前用它做早期校验。
func ParseActionType(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", fmt.Errorf("hook action: empty raw json")
	}
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "", fmt.Errorf("hook action: parse type: %w", err)
	}
	if probe.Type == "" {
		return "", fmt.Errorf("hook action: missing type field")
	}
	return probe.Type, nil
}

// NewExecutorByType 按 action.type 分发到对应 Executor 工厂。
//
// 输入 raw 是 HookActionConfig.Raw（已含 type 字段的完整 JSON 对象）。
//
// 返回 error 时意味着 type 非法或该 type 的反序列化失败,Engine 应整体跳过
// 该 entry 并记 error 日志。
func NewExecutorByType(action json.RawMessage) (Executor, error) {
	t, err := ParseActionType(action)
	if err != nil {
		return nil, err
	}
	switch t {
	case ActionTypeCommand:
		return NewCommandExecutor(action)
	case ActionTypeHTTP:
		return NewHttpExecutor(action)
	case ActionTypePrompt:
		return NewPromptExecutor(action)
	case ActionTypeAgent:
		// AgentExecutor 只需构造「配置 + 模板」部分,LLMProvider / ToolRegistry
		// 由 Engine 在 wire 时通过 SetProvider / SetRegistry 注入。
		return NewAgentExecutor(action)
	default:
		return nil, fmt.Errorf("hook action type %q not supported", t)
	}
}

// ----- panic recover 包装 -----

// RunSafe 用 defer recover 把 executor.Execute 内部的 panic 转换为 error,
//
// Engine 收到 error 后只 warn 记录,绝不会让 panic 杀死主 Agent Loop。
//
// logger 非 nil 时 panic 会以 error 级别记录堆栈片段(event + action type +
// executor type + panic value);logger 为 nil 时静默吞掉,适合测试场景。
//
// [Why 单独抽函数而非 Engine 端 recover] Engine 还要承担 once/async/Stats/
// timeout/context 等多重职责;把 panic isolation 收敛到 executor 包,Engine
// 只调 RunSafe,不重复写 defer recover。
func RunSafe(logger *zap.Logger, event, actionType string, exec Executor, ctx context.Context, hookCtx *hookcontext.HookContext, vars map[string]string) (err error) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		err = fmt.Errorf("hook %s action %s panicked: %v", event, actionType, r)
		if logger != nil {
			logger.Error("hook action panicked",
				zap.String("event", event),
				zap.String("action_type", actionType),
				zap.Any("panic", r),
			)
		}
	}()
	return exec.Execute(ctx, hookCtx, vars)
}

// ----- 通用工具 -----

// ParseDuration 支持的格式：
//   - 标准 time.ParseDuration（"10s" / "500ms" / "2m"）;
//   - 空字符串 → 返回 def（默认 0 由调用方自行解释）；
//
// [Why 自己包装而非 time.ParseDuration 直传] 空字符串与非法字符串时希望
// 显式降级到默认值,而 time.ParseDuration 对空字符串返回 error,
// 需要调用方写额外的 if 分支。集中到这里省 4 个调用点的重复代码。
func ParseDuration(raw string, def time.Duration) (time.Duration, error) {
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", raw, err)
	}
	return d, nil
}

// DefaultCommandTimeout 是 command action 未配置 timeout 时的回退值。
//
// [Why 30s] 与 spec §H「同步 hook 默认超时 30s」一致;单条 shell 命令超过 30s
// 已经属于异常(用户应该用 async),Engine 同时按此值截断。
const DefaultCommandTimeout = 30 * time.Second

// DefaultHTTPTimeout 是 http action 未配置 timeout 时的回退值。
const DefaultHTTPTimeout = 30 * time.Second

// DefaultAgentTimeout 是 agent action 未配置 timeout 时的回退值。
//
// [Why 60s] spec §D.4 显式规定。
const DefaultAgentTimeout = 60 * time.Second
