// Package executor — PromptExecutor 实现 (spec §D.2)。
//
// prompt action 让用户能把一段「规约性文本」注入到当前轮 user 消息尾部,LLM 在
// 下一轮调用时即可读取。例如「该项目 Go 文件使用 tabs 缩进,提交前请用
// gofmt 格式化」这类规约。
//
// 设计要点:
//   - 本执行器只负责「计算待注入文本」,并通过 *PromptExecutor.Last() 方法暴露;
//   - 实际注入由 Engine 调 PromptSink.AppendToCurrentMessage(spec §D.2)完成,
//     解耦 executor 与 session 状态,避免 executor ↔ engine 循环依赖;
//   - 本期 as 字段硬编码仅支持 "system_reminder"(spec §D.2),其它值 warn
//     + ErrInvalidPromptAs 拒绝;
//   - 文本用 <system-reminder>...</system-reminder> 包裹(spec §D.2「LLM 明确感
//     知到这是规约边界」),不进入 system 字段(不破坏 Anthropic prompt caching,
//     spec §D.2 末尾)。
//
// 调用流程:
//  1. Engine 命中条件;
//  2. Engine 调 Execute,把渲染结果暂存到 PromptExecutor.last 字段;
//  3. Engine 类型断言 PromptExecutor 后调 Last() 取文本;
//  4. Engine 调 sink.AppendToCurrentMessage(text) 完成注入。
//
// 留 hookCtx 包依赖只是为了一致性:四类 executor 签名一致,Engine 调 RunSafe
// 时不需要按类型分支判断入参。
package executor

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/MeiCorl/CodePilot/src/internal/hookcontext"
)

// PromptConfig 是 prompt action 的 type-specific 配置,对应 setting.json:
//   {
//     "type": "prompt",
//     "text": "<system-reminder>...</system-reminder>",
//     "as": "system_reminder"   // 本期唯一合法值
//   }
type PromptConfig struct {
	// Text 为模板文本,支持 $VAR 替换。
	Text string `json:"text"`
	// As 决定注入形态,本期仅支持 "system_reminder"。
	As string `json:"as,omitempty"`
}

// PromptExecutor 是 prompt action 的执行器实现。
//
// 不可变配置(cfg)+ 可变单次结果(last),last 在 Execute 后被外部读取,Engine
// 注入完成后一般不再复用,避免并发场景下错位读取(若同一 executor 同时被多个
// 事件触发,我们也不希望复用——Engine 在 EngineConfig 阶段每次新建 executor)。
type PromptExecutor struct {
	cfg  PromptConfig
	last string
}

// NewPromptExecutor 解析 raw action JSON 并构造 PromptExecutor。
//
// 校验:
//   - JSON 格式错误 → error;
//   - as 非空且非 "system_reminder" → error(本期拒绝,后续扩展时在此放行);
//
// 不要求 text 非空(允许用户临时禁用规约:配置保留但留空),Execute 阶段再判
// 纯空白并返回 ErrEmptyPrompt。
func NewPromptExecutor(raw json.RawMessage) (*PromptExecutor, error) {
	var cfg PromptConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("hook prompt action: parse: %w", err)
	}
	if cfg.As != "" && cfg.As != promptAsSystemReminder {
		return nil, &ErrInvalidPromptAs{As: cfg.As}
	}
	return &PromptExecutor{cfg: cfg}, nil
}

// Type 返回 "prompt"。
func (e *PromptExecutor) Type() string { return ActionTypePrompt }

// Execute 计算待注入文本,缓存到 last 字段供 Engine 类型断言取出。
//
// 流程:
//  1. Interpolate(text, vars) 替换 $VAR;
//  2. 纯空白(replace 后 trim 为空)→ return ErrEmptyPrompt,不写 last;
//  3. 渲染结果写入 e.last;
//
// [Why 通过字段带回而非直接返 *PromptResult] Execute 接口签名固定为
// (ctx, hookCtx, vars) error,无法携带额外结果。PromptExecutor 暴露
// Last() 作为「副产物」通道,Engine 类型断言后调用。这是 4 个 executor 中
// 唯一需要额外结果产物的设计;其它 3 个执行结果即 error nil。
func (e *PromptExecutor) Execute(ctx context.Context, hookCtx *hookcontext.HookContext, vars map[string]string) error {
	_ = ctx
	_ = hookCtx
	rendered := hookcontext.Interpolate(e.cfg.Text, vars)
	if trimSpaces(rendered) == "" {
		e.last = ""
		return ErrEmptyPrompt
	}
	e.last = rendered
	return nil
}

// Last 返回最近一次 Execute 渲染好的待注入文本。
//
// [Why 单独方法而非直接读 cfg.Text] cfg.Text 是配置原文,Execute 后我们不应
// 改写配置语义;Last() 给出「本次 Execute 渲染结果」,调用方(Engine)拿到
// 后应立刻 append 到 sink,不应再次执行第二次 Execute 后再读 Last。
func (e *PromptExecutor) Last() string {
	return e.last
}

// trimSpaces 剔除首尾 ASCII 空白字符。
//
// [Why 不引入 strings.TrimSpace] 实际功能完全一致,但 executor 包不依赖
// strings 的话编译产物更清晰。本实现只处理 ASCII 空白(spec §D.2 文本
// 多为英文规约);遇到 Unicode 空白不常见,如需扩展再换 strings.TrimSpace。
func trimSpaces(s string) string {
	start := 0
	for start < len(s) {
		c := s[start]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f' {
			start++
			continue
		}
		break
	}
	end := len(s)
	for end > start {
		c := s[end-1]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f' {
			end--
			continue
		}
		break
	}
	return s[start:end]
}
