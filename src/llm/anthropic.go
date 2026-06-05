package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/MeiCorl/CodePilot/src/internal/config"
	"github.com/MeiCorl/CodePilot/src/internal/tool"
	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// AnthropicProvider 是 Anthropic（Claude 系列）的 LLM 适配器。
// 实现 Provider 接口，将内部通用消息格式转换为 Anthropic SDK 格式，
// 支持流式输出、超时控制、重试机制、工具调用（tool_use / tool_result）。
type AnthropicProvider struct {
	client     *anthropic.Client
	model      string
	maxTokens  int
	timeout    int
	maxRetries int
}

// NewAnthropicProvider 根据 Config 创建 Anthropic 适配器实例。
// 如果 BaseURL 非空，使用自定义 API 地址（支持代理）。
func NewAnthropicProvider(cfg *config.Config) *AnthropicProvider {
	opts := []option.RequestOption{
		option.WithAPIKey(cfg.APIKey),
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}

	client := anthropic.NewClient(opts...)

	return &AnthropicProvider{
		client:     &client,
		model:      cfg.Model,
		maxTokens:  cfg.MaxTokens,
		timeout:    cfg.Timeout,
		maxRetries: cfg.MaxRetries,
	}
}

// convertTools 把内部 ToolSpec 列表转换为 Anthropic SDK 的 ToolUnionParam 列表。
//
// InputSchema 是完整的 JSON Schema（含 type/properties/required）；
// Anthropic SDK 的 ToolInputSchemaParam 限定 type=object，因此只挑出
// properties / required 字段透传，其余字段（如 description）由 LLM 端按规范自动处理。
func (p *AnthropicProvider) convertTools(specs []tool.ToolSpec) []anthropic.ToolUnionParam {
	out := make([]anthropic.ToolUnionParam, 0, len(specs))
	for _, s := range specs {
		schema := anthropic.ToolInputSchemaParam{
			Properties: map[string]any{},
		}
		if len(s.InputSchema) > 0 {
			// 从完整 JSON Schema 中挑出 properties / required；
			// 解析失败时退化为空 schema（仍能注册工具，但 LLM 看不到参数约束）
			var raw struct {
				Properties map[string]any `json:"properties"`
				Required   []string       `json:"required"`
			}
			if err := json.Unmarshal(s.InputSchema, &raw); err == nil {
				if raw.Properties != nil {
					schema.Properties = raw.Properties
				}
				schema.Required = raw.Required
			}
		}
		out = append(out, anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				Name:        s.Name,
				Description: anthropic.String(s.Description),
				InputSchema: schema,
			},
		})
	}
	return out
}

// convertMessages 把内部 Message 数组转换为 Anthropic SDK 的 MessageParam 数组。
// 支持 text / tool_use / tool_result 三种 ContentBlock。
//
// tool_use 块出现在 assistant 消息中（LLM 历史决定要调的工具）；
// tool_result 块出现在 user 消息中（系统回传的执行结果）。
func (p *AnthropicProvider) convertMessages(messages []Message) []anthropic.MessageParam {
	params := make([]anthropic.MessageParam, 0, len(messages))
	for _, msg := range messages {
		blocks := make([]anthropic.ContentBlockParamUnion, 0, len(msg.Content))
		for _, block := range msg.Content {
			switch b := block.(type) {
			case *TextBlock:
				blocks = append(blocks, anthropic.NewTextBlock(b.Text))
			case *ToolUseBlock:
				// 解析 Input 原始 JSON 为 any（SDK NewToolUseBlock 接受 any）
				var input any
				if len(b.Input) > 0 {
					input = json.RawMessage(b.Input)
				}
				blocks = append(blocks, anthropic.NewToolUseBlock(b.ID, input, b.Name))
			case *ToolResultBlock:
				blocks = append(blocks, anthropic.NewToolResultBlock(b.ToolUseID, b.Content, b.IsError))
			}
		}

		switch msg.Role {
		case RoleUser:
			params = append(params, anthropic.NewUserMessage(blocks...))
		case RoleAssistant:
			params = append(params, anthropic.NewAssistantMessage(blocks...))
		}
	}
	return params
}

// StreamChat 发起一次 Anthropic 流式对话请求。
// toolSpecs 为空时按"无工具"模式请求；非空时随请求发送 tools 数组，
// LLM 可在响应中返回 tool_use 内容块，doStream 会在流结束时通过
// StreamChunk.ToolUse 字段捎带出来。
func (p *AnthropicProvider) StreamChat(ctx context.Context, systemPrompt string, messages []Message, toolSpecs []tool.ToolSpec) (<-chan StreamChunk, error) {
	ch := make(chan StreamChunk, 64)

	go func() {
		defer close(ch)
		p.streamWithRetry(ctx, systemPrompt, messages, toolSpecs, ch)
	}()

	return ch, nil
}

// streamWithRetry 实现带重试的流式请求。
// 仅对网络错误和 HTTP 5xx 重试，采用指数退避策略。
// HTTP 401（认证错误）和 429（限流）不重试，直接通过 channel 返回错误。
func (p *AnthropicProvider) streamWithRetry(ctx context.Context, systemPrompt string, messages []Message, toolSpecs []tool.ToolSpec, ch chan<- StreamChunk) {
	msgParams := p.convertMessages(messages)

	params := anthropic.MessageNewParams{
		MaxTokens: int64(p.maxTokens),
		Messages:  msgParams,
		Model:     p.model,
	}
	if systemPrompt != "" {
		params.System = []anthropic.TextBlockParam{
			{Text: systemPrompt},
		}
	}
	if len(toolSpecs) > 0 {
		params.Tools = p.convertTools(toolSpecs)
	}

	var lastErr error
	for attempt := 0; attempt <= p.maxRetries; attempt++ {
		if attempt > 0 {
			// 指数退避：1s, 2s, 4s
			delay := time.Duration(math.Pow(2, float64(attempt-1))) * time.Second
			select {
			case <-ctx.Done():
				ch <- StreamChunk{Done: true}
				return
			case <-time.After(delay):
			}
		}

		lastErr = p.doStream(ctx, params, ch)
		if lastErr == nil {
			// 流正常结束
			return
		}

		// 判断是否需要重试
		if !p.shouldRetry(lastErr) {
			ch <- StreamChunk{Err: lastErr, Done: true}
			return
		}
		// 可重试错误，继续循环
	}

	// 重试耗尽，返回最后一次错误
	ch <- StreamChunk{Err: fmt.Errorf("重试 %d 次后仍失败: %w", p.maxRetries, lastErr), Done: true}
}

// doStream 执行一次流式请求，将响应 chunk 写入 channel。
// 返回 nil 表示流正常结束，非 nil 表示遇到错误。
//
// 流式 tool_use 解析策略：每个 content block 用 block index 标识，
// 遇到 ContentBlockStartEvent 且 Type=tool_use 时为该 index 创建一个
// 累积器（保存 ID/Name/partial input），后续 ContentBlockDeltaEvent
// 中 Type=input_json_delta 的事件把 partial_json 追加到累积器，
// ContentBlockStopEvent 触发后 parse 完整 input 构造 ToolUseBlock，
// 在下一个 StreamChunk（Done=true）上捎带 ToolUse 字段。
func (p *AnthropicProvider) doStream(ctx context.Context, params anthropic.MessageNewParams, ch chan<- StreamChunk) error {
	// 包装超时 context
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.timeout)*time.Second)
	defer cancel()

	stream := p.client.Messages.NewStreaming(ctx, params)

	// 累积每个 content block 的 tool_use 增量输入（key 为 block index）
	type toolUseAccum struct {
		id          string
		name        string
		partialJSON strings.Builder
	}
	pendingToolUses := make(map[int64]*toolUseAccum)
	// 已完成的 tool_use 块，按 block index 存储，最后按 index 升序排列
	completedToolUses := make(map[int64]*ToolUseBlock)

	// Token 用量：从 MessageStartEvent 获取 input_tokens，从 MessageDeltaEvent 获取 output_tokens
	var inputTokens, outputTokens int64

	for stream.Next() {
		event := stream.Current()

		switch evt := event.AsAny().(type) {
		case anthropic.MessageStartEvent:
			// 流开始时获取 input_tokens（本轮发送给模型的总输入 token 数）
			inputTokens = evt.Message.Usage.InputTokens
		case anthropic.MessageDeltaEvent:
			// 流结束时获取累计的 output_tokens
			outputTokens = evt.Usage.OutputTokens
		case anthropic.ContentBlockStartEvent:
			// ContentBlockStartEvent.ContentBlock 可能是 text 或 tool_use，
			// 通过 Type 字段判别
			if evt.ContentBlock.Type == "tool_use" {
				pendingToolUses[evt.Index] = &toolUseAccum{
					id:   evt.ContentBlock.ID,
					name: evt.ContentBlock.Name,
				}
			}
		case anthropic.ContentBlockDeltaEvent:
			switch delta := evt.Delta.AsAny().(type) {
			case anthropic.TextDelta:
				if delta.Text != "" {
					select {
					case ch <- StreamChunk{Content: delta.Text}:
					case <-ctx.Done():
						// 区分用户主动取消和超时
						if ctx.Err() == context.Canceled {
							ch <- StreamChunk{Done: true}
							return nil
						}
						// DeadlineExceeded：超时，返回错误让上层感知
						return fmt.Errorf("LLM 流式传输超时（%d秒）: %w", p.timeout, ctx.Err())
					}
				}
			case anthropic.InputJSONDelta:
				if acc, ok := pendingToolUses[evt.Index]; ok {
					acc.partialJSON.WriteString(delta.PartialJSON)
				}
			}
		case anthropic.ContentBlockStopEvent:
			// tool_use 块结束时把累积的 partial JSON 解析为完整 input
			if acc, ok := pendingToolUses[evt.Index]; ok {
				input := json.RawMessage("{}")
				if acc.partialJSON.Len() > 0 {
					// 验证是否为合法 JSON，非法时退化为空对象并附 error 到 ToolUse 不必要
					// —— 上层 conversation manager 会拿到空 input 后调工具，
					// 工具自身的参数校验会给出明确错误
					if json.Valid([]byte(acc.partialJSON.String())) {
						input = json.RawMessage(acc.partialJSON.String())
					}
				}
				completedToolUses[evt.Index] = NewToolUseBlock(acc.id, acc.name, input).(*ToolUseBlock)
				delete(pendingToolUses, evt.Index)
			}
		}
	}

	// 按 block index 升序排列所有已完成的 tool_use 块
	toolUses := sortToolUsesByIndex(completedToolUses)

	// 构造 token 用量
	usage := &TokenUsage{
		InputTokens:  int(inputTokens),
		OutputTokens: int(outputTokens),
	}

	// 检查流错误
	if err := stream.Err(); err != nil {
		// 用户主动取消（点击停止按钮等）视为正常中断，不视为错误
		if errors.Is(err, context.Canceled) {
			ch <- StreamChunk{Done: true, ToolUses: toolUses, Usage: usage}
			return nil
		}
		// 超时视为错误，不应静默丢弃——用户看到的是"Thinking 后无输出"
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("LLM 请求超时（%d秒），可能原因：上下文过长或模型推理耗时超出限制: %w", p.timeout, err)
		}
		return err
	}

	// 流正常结束，携带所有 tool_use 块和 token 用量
	ch <- StreamChunk{Done: true, ToolUses: toolUses, Usage: usage}
	return nil
}

// shouldRetry 判断错误是否值得重试。
// 网络错误和 5xx 服务端错误可重试，401/429 不重试。
func (p *AnthropicProvider) shouldRetry(err error) bool {
	// context 取消/超时不重试
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	// 检查 Anthropic API 错误状态码
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.StatusCode == http.StatusUnauthorized: // 401
			return false
		case apiErr.StatusCode == http.StatusTooManyRequests: // 429
			return false
		case apiErr.StatusCode >= 500: // 5xx
			return true
		default:
			return false
		}
	}

	// 其他网络错误可重试
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	// 默认不重试
	return false
}

// sortToolUsesByIndex 按 block index 升序排列已完成的 tool_use 块，
// 返回有序的切片。用于在流结束时把并行累积的多个 tool_use
// 按 Anthropic 协议的 content block 顺序输出。
func sortToolUsesByIndex(completed map[int64]*ToolUseBlock) []ToolUseBlock {
	if len(completed) == 0 {
		return nil
	}
	// 收集所有 index 并排序
	indices := make([]int64, 0, len(completed))
	for idx := range completed {
		indices = append(indices, idx)
	}
	sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })

	result := make([]ToolUseBlock, 0, len(indices))
	for _, idx := range indices {
		if block, ok := completed[idx]; ok {
			result = append(result, *block)
		}
	}
	return result
}
