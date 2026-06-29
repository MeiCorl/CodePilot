// Package executor — AgentExecutor 实现 (spec §D.4,本期 stub)。
//
// agent action 让用户能在一个 Hook 触发点调一次 LLM 子任务(比如「请评论刚才
// 写入的文件是否有安全漏洞」)。本期实现是「瘦客户端」:
//   - 复用主 Agent 的 LLM Provider;
//   - 把 prompt 作为独立 user 消息、单次 LLM 调用、无 tool_use 链路;
//   - max_iterations 字段本期固定忽略(Step 12 升级时实现);
//   - allow_tools 字段本期固定忽略(Step 12 升级时按需实现);
//   - 响应只走日志,绝不写回主会话 history(spec §D.4「LLM 响应不写回主会话」)。
//
// 与完整 SubAgent 的区别(spec §D.4 + Out of Scope §5):
//   - 本期没有独立 conversation;
//   - 没有 history(连一条 user 都没有后续);
//   - 没有 stream 透传给主 Agent;
//   - 没有 result 回调主 Agent;
//   - 只能用于「让 LLM 一次性评注/检测」式用例,而非 SubAgent 真正的多轮独立推理。
//
// TODO(Step 12): Step 12 SubAgent 系统上线后,本执行器应升级为启动独立 SubAgent:
//   - 独立 conversation 与独立 history;
//   - 允许最多 max_iterations 轮迭代(替换本期固定 1 轮);
//   - 允许 allow_tools 列出的工具描述传入 SubAgent;
//   - 最终结果可选回传主会话(由调用方决定,默认不回传);
//   - 复用 SubAgent 包提供的 session/conversation interface;
//
// 升级时不应破坏现有 setting.json schema;允许 hooks 保留就字段继续生效。
package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MeiCorl/CodePilot/src/internal/hookcontext"
	"github.com/MeiCorl/CodePilot/src/internal/tool"
	"github.com/MeiCorl/CodePilot/src/llm"
	"go.uber.org/zap"
)

// AgentConfig 是 agent action 的 type-specific 配置,对应 setting.json:
//   {
//     "type": "agent",
//     "prompt": "请检查 $TOOL_INPUT_FILE_PATH 是否有安全漏洞",
//     "max_iterations": 1,           // 本期固定忽略
//     "allow_tools": ["ReadFile"],   // 本期固定忽略,Step 12 实现
//     "timeout": "60s"               // 默认 60s
//   }
type AgentConfig struct {
	// Prompt 为必填的 LLM 提示词,支持 $VAR 插值。
	Prompt string `json:"prompt"`
	// MaxIterations 本期固定忽略(spec §D.4「本步骤固定 1,忽略配置」)。
	MaxIterations int `json:"max_iterations,omitempty"`
	// AllowTools 本期固定忽略(spec §D.4「可选用,缺省为空」,
	// Step 12 升级时作为 agent SubAgent 的可用工具白名单)。
	AllowTools []string `json:"allow_tools,omitempty"`
	// Timeout 字符串(默认 60s,spec §D.4)。
	Timeout string `json:"timeout,omitempty"`
}

// AgentExecutor 是 agent action 的执行器实现(本期 stub)。
type AgentExecutor struct {
	cfg AgentConfig

	// provider / registry / timeout 由 Engine 在 wire 阶段注入;
	// 也可在 NewAgentExecutor 阶段直接传入(便于测试)。
	provider llm.Provider
	registry *tool.Registry

	// timeout 是单次 Execute 整体超时(spec §D.4「timeout 60s」)。
	timeout time.Duration
}

// NewAgentExecutor 解析 raw action JSON 并构造 AgentExecutor(不注入 provider / registry)。
//
// provider / registry 缺省为空(nil),Execute 时若检测到 nil 则报 ErrNoLLMProvider;
// 注入责任由 Engine 在 Wire 阶段调 SetProvider / SetRegistry 完成。
func NewAgentExecutor(raw json.RawMessage) (*AgentExecutor, error) {
	var cfg AgentConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("hook agent action: parse: %w", err)
	}
	timeout, err := ParseDuration(cfg.Timeout, DefaultAgentTimeout)
	if err != nil {
		return nil, fmt.Errorf("hook agent action: timeout: %w", err)
	}
	if timeout <= 0 {
		timeout = DefaultAgentTimeout
	}
	return &AgentExecutor{
		cfg:     cfg,
		timeout: timeout,
	}, nil
}

// SetProvider 注入 LLM provider,Engine 在 Wire 阶段调用一次。
//
// [Why Setter 而非构造时传入] executor 包不应反向依赖 main.go 加载逻辑;
// Engine 持有 provider,executor 通过 setter 注入,降低耦合。
func (e *AgentExecutor) SetProvider(p llm.Provider) { e.provider = p }

// SetRegistry 注入 tool registry,Engine 在 Wire 阶段调用一次。
func (e *AgentExecutor) SetRegistry(r *tool.Registry) { e.registry = r }

// Timeout 返回本执行器计算后的 timeout 值(供 Engine/统计使用)。
func (e *AgentExecutor) Timeout() time.Duration { return e.timeout }

// Type 返回 "agent"。
func (e *AgentExecutor) Type() string { return ActionTypeAgent }

// Execute 触发一次 LLM 子任务流式调用,等流结束后返回 nil / err。
//
// 流程:
//  1. Interpolate(prompt, vars);
//  2. prompt 为空 → ErrEmptyAgentPrompt;
//  3. context.WithTimeout(timeout) 包整个流;
//  4. 构造 messages: [{role: user, content: TextBlock{prompt}}];
//     [Why 不带 system 字段] spec §D.4「无 system,单一 user 消息」;
//     [Why 单 user 不带其它轮次]本期 stub,不进入 SubAgent 的多轮迭代;
//  5. provider.StreamChat(ctx, SystemPrompt{}, messages, nil);toolSpecs 传 nil
//     (本期固定不带工具,Step 12 升级);
//  6. 消费 channel 累积 Content 到 aggregator;
//  7. 收到 Done=true 的 chunk 时检查 Err;无 Err → debug 日志打印 LLM 文本;
//  8. **绝不**把响应写回主会话 history(spec §D.4「不写回主会话 history」);
//
// 返回:
//   - nil 视为成功(完成调用,LLM 响应已走日志);
//   - 非 nil 视为失败,Engine 走 warn 记录。
//
// 关键防御:
//   - 不论 LLM 流中断 / 超时 / 上游 err,Execute 都不抛 panic,只 log + 透传;
//   - aggregator 拒绝在响应中看到 tool_use(spec §D.4「max_iterations 本期固定 1」,
//     出现 tool_use 就视作 anomaly,记 warn 后继续不报错)。
func (e *AgentExecutor) Execute(ctx context.Context, hookCtx *hookcontext.HookContext, vars map[string]string) error {
	_ = hookCtx
	if e.provider == nil {
		return &ErrNoLLMProvider{}
	}

	rendered := hookcontext.Interpolate(e.cfg.Prompt, vars)
	if trimSpaces(rendered) == "" {
		return ErrEmptyAgentPrompt
	}

	// 整次调用包一个超时,与 e.timeout 一致(spec §D.4「timeout 60s」)。
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	messages := []llm.Message{
		{
			Role: llm.RoleUser,
			Content: []llm.ContentBlock{
				llm.NewTextBlock(rendered),
			},
		},
	}

	// [Why 不构造 systemPrompt] spec §D.4「无 system」;传 SystemPrompt{} 即
	// 让 Provider 跳过 system 字段构造(参考 llm.SystemPrompt.IsEmpty())。
	sp := llm.SystemPrompt{}

	ch, err := e.provider.StreamChat(ctx, sp, messages, nil)
	if err != nil {
		return fmt.Errorf("hook agent: stream init: %w", err)
	}

	// 消费 stream:累积 Content,等 Done=true 才返回。
	var (
		aggregator strings.Builder
		streamErr  error
		hasDone    bool
		sawToolUse bool
	)
	for chunk := range ch {
		if chunk.Content != "" {
			aggregator.WriteString(chunk.Content)
		}
		if len(chunk.ToolUses) > 0 {
			// Step 12 升级后应把 tool_use 串到 SubAgent 的工具执行器;
			// 本期作为 anomaly 记 warn,不抛错(spec §D.4「本步骤固定 1 轮」)。
			sawToolUse = true
		}
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
		if chunk.Done {
			hasDone = true
			break
		}
	}

	// 防御:channel 未正常 close Done,ctx 已超时。
	if !hasDone {
		if streamErr == nil {
			streamErr = context.Cause(ctx)
			if streamErr == nil {
				streamErr = errors.New("hook agent: stream closed without done chunk")
			}
		}
	}

	finalText := aggregator.String()
	logAgentResponse(ctx, e.cfg, finalText, sawToolUse, streamErr)

	if streamErr != nil {
		return fmt.Errorf("hook agent: stream: %w", streamErr)
	}
	return nil
}

// logAgentResponse 把 LLM 响应文本 + 出错信息写到 zap 日志。
//
// [Why 通过 logger.Debug 而非 DebugCtx] Execute 内的 logger 是 executor 的
// 隐式依赖,Engine 在 SetProvider 之外另提供 logger;通过参数 ctx 路由到
// 会话日志器(若已 OpenSession),由 zap 提供默认 sink。
func logAgentResponse(ctx context.Context, cfg AgentConfig, finalText string, sawToolUse bool, err error) {
	// 不直接 import logger 包可能产生循环依赖;此处走 zap.L() 获取底层 logger。
	// [Why 用 zap 而非 logger.InfoCtx] executor 包只 import zap 与标准库,
	// 不依赖 logger 包;Engine 在 agent 类型 entry 触发时由 Engine 本身记录
	// success/fail 统计,executor 只负责把响应原文写到合适的通道。
	//
	// 注意:本函数被 zero value logger 调用时(zap.NewNop)是安全的。
	logger := zap.L()
	if err != nil {
		logger.Debug("hook agent: stream returned error",
			zap.Int("max_iterations", cfg.MaxIterations),
			zap.Strings("allow_tools", cfg.AllowTools),
			zap.String("text", truncate(finalText, 512)),
			zap.Bool("saw_tool_use", sawToolUse),
			zap.Error(err),
		)
		return
	}
	logger.Debug("hook agent: response received",
		zap.Int("max_iterations", cfg.MaxIterations),
		zap.Strings("allow_tools", cfg.AllowTools),
		zap.String("text", truncate(finalText, 512)),
		zap.Bool("saw_tool_use", sawToolUse),
	)
}
