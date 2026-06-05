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
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

// OpenAIProvider 是 OpenAI（GPT 系列）的 LLM 适配器。
// 实现 Provider 接口，将内部通用消息格式转换为 OpenAI SDK 格式，
// 支持流式输出、超时控制、重试机制、function_calling 工具调用。
type OpenAIProvider struct {
	client     *openai.Client
	model      string
	maxTokens  int
	timeout    int
	maxRetries int
}

// NewOpenAIProvider 根据 Config 创建 OpenAI 适配器实例。
// 如果 BaseURL 非空，使用自定义 API 地址（支持代理）。
func NewOpenAIProvider(cfg *config.Config) *OpenAIProvider {
	opts := []option.RequestOption{
		option.WithAPIKey(cfg.APIKey),
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}

	client := openai.NewClient(opts...)

	return &OpenAIProvider{
		client:     &client,
		model:      cfg.Model,
		maxTokens:  cfg.MaxTokens,
		timeout:    cfg.Timeout,
		maxRetries: cfg.MaxRetries,
	}
}

// convertTools 把内部 ToolSpec 列表转换为 OpenAI 的 ChatCompletionToolParam 列表。
//
// OpenAI 工具以 "function" 类型注册，参数使用 shared.FunctionDefinitionParam。
// Parameters 直接复用我们生成的 JSON Schema（map 形式），无需拆分 properties/required。
func (p *OpenAIProvider) convertTools(specs []tool.ToolSpec) []openai.ChatCompletionToolParam {
	out := make([]openai.ChatCompletionToolParam, 0, len(specs))
	for _, s := range specs {
		params := shared.FunctionDefinitionParam{
			Name:        s.Name,
			Description: openai.String(s.Description),
		}
		// InputSchema 是完整 JSON Schema；解析为 map[string]any 后整体塞入 parameters
		if len(s.InputSchema) > 0 {
			var m map[string]any
			if err := json.Unmarshal(s.InputSchema, &m); err == nil {
				params.Parameters = m
			}
		}
		out = append(out, openai.ChatCompletionToolParam{
			Type:     "function",
			Function: params,
		})
	}
	return out
}

// convertMessages 把内部 Message 数组转换为 OpenAI 的 ChatCompletionMessageParamUnion 列表。
//
// 协议映射：
//   - user + text: openai.UserMessage(text)
//   - user + tool_result: openai.ToolMessage(content, toolCallID)  ← 每条 tool_result 独立
//   - assistant + text: openai.AssistantMessage(text)
//   - assistant + tool_use: openai.AssistantMessage 含 ToolCalls 字段
//
// 注：OpenAI 协议中 tool_result 不能与普通 text 混在同一条 user 消息中，
// 每条 tool_result 对应一条独立的 role=tool 消息。
func (p *OpenAIProvider) convertMessages(messages []Message) []openai.ChatCompletionMessageParamUnion {
	params := make([]openai.ChatCompletionMessageParamUnion, 0, len(messages))

	for _, msg := range messages {
		switch msg.Role {
		case RoleUser:
			// 收集 text 块合并为单条 user 消息；tool_result 块每条单独成消息
			var textBuf strings.Builder
			var textHasContent bool
			for _, b := range msg.Content {
				switch blk := b.(type) {
				case *TextBlock:
					textBuf.WriteString(blk.Text)
					textHasContent = true
				case *ToolResultBlock:
					// 先 flush 累积的 text（如果有）
					if textHasContent {
						params = append(params, openai.UserMessage(textBuf.String()))
						textBuf.Reset()
						textHasContent = false
					}
					params = append(params, openai.ToolMessage(blk.Content, blk.ToolUseID))
				}
			}
			if textHasContent {
				params = append(params, openai.UserMessage(textBuf.String()))
			}

		case RoleAssistant:
			// 合并 text 与 tool_use 到同一条 assistant 消息（OpenAI 协议允许）
			var textBuf strings.Builder
			var textHasContent bool
			var toolCalls []openai.ChatCompletionMessageToolCallParam

			for _, b := range msg.Content {
				switch blk := b.(type) {
				case *TextBlock:
					textBuf.WriteString(blk.Text)
					textHasContent = true
				case *ToolUseBlock:
					// arguments 必须是字符串
					args := string(blk.Input)
					if args == "" {
						args = "{}"
					}
					toolCalls = append(toolCalls, openai.ChatCompletionMessageToolCallParam{
						ID: blk.ID,
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Arguments: args,
							Name:      blk.Name,
						},
						Type: "function",
					})
				}
			}

			if len(toolCalls) == 0 {
				// 纯文本助手消息
				if textHasContent {
					params = append(params, openai.AssistantMessage(textBuf.String()))
				}
			} else {
				// 带 tool_calls 的助手消息（必须用 union 形式显式构造）
				assistant := openai.ChatCompletionAssistantMessageParam{
					Role:      "assistant",
					ToolCalls: toolCalls,
				}
				if textHasContent {
					assistant.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
						OfString: openai.String(textBuf.String()),
					}
				}
				params = append(params, openai.ChatCompletionMessageParamUnion{
					OfAssistant: &assistant,
				})
			}
		}
	}
	return params
}

// StreamChat 发起一次 OpenAI 流式对话请求。
// toolSpecs 非空时随请求发送 tools 数组，LLM 可在响应中发出
// finish_reason="tool_calls" 的流式片段，由 doStream 解析为 ToolUseBlock。
func (p *OpenAIProvider) StreamChat(ctx context.Context, systemPrompt string, messages []Message, toolSpecs []tool.ToolSpec) (<-chan StreamChunk, error) {
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
func (p *OpenAIProvider) streamWithRetry(ctx context.Context, systemPrompt string, messages []Message, toolSpecs []tool.ToolSpec, ch chan<- StreamChunk) {
	msgParams := p.convertMessages(messages)

	// 如果有 System Prompt，作为第一条消息插入
	allMessages := make([]openai.ChatCompletionMessageParamUnion, 0, len(msgParams)+1)
	if systemPrompt != "" {
		allMessages = append(allMessages, openai.SystemMessage(systemPrompt))
	}
	allMessages = append(allMessages, msgParams...)

	params := openai.ChatCompletionNewParams{
		Messages: allMessages,
		Model:    p.model,
		MaxTokens: openai.Int(int64(p.maxTokens)),
		// 启用流式 Usage 返回：OpenAI 仅在设置此选项后，才会在最后一个 chunk 中
		// 携带完整的 token 用量统计（PromptTokens / CompletionTokens）
		StreamOptions: openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: openai.Bool(true),
		},
	}
	if len(toolSpecs) > 0 {
		params.Tools = p.convertTools(toolSpecs)
	}

	var lastErr error
	for attempt := 0; attempt <= p.maxRetries; attempt++ {
		if attempt > 0 {
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
			return
		}

		if !p.shouldRetry(lastErr) {
			ch <- StreamChunk{Err: lastErr, Done: true}
			return
		}
	}

	ch <- StreamChunk{Err: fmt.Errorf("重试 %d 次后仍失败: %w", p.maxRetries, lastErr), Done: true}
}

// doStream 执行一次流式请求，将响应 chunk 写入 channel。
// 返回 nil 表示流正常结束，非 nil 表示遇到错误。
//
// 流式 tool_calls 解析策略：OpenAI 流式响应按 index 增量发送 tool_calls 片段，
// 每个 tool_call 的 ID/Name/Arguments 可能跨多个 chunk 累积，doStream
// 用 map[index]*toolCallAccum 维护；流结束时按 index 升序构造所有
// ToolUseBlock，通过 StreamChunk.ToolUses 切片捎带出来。
func (p *OpenAIProvider) doStream(ctx context.Context, params openai.ChatCompletionNewParams, ch chan<- StreamChunk) error {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.timeout)*time.Second)
	defer cancel()

	stream := p.client.Chat.Completions.NewStreaming(ctx, params)

	// 累积每个 tool_call index 的增量输入
	type toolCallAccum struct {
		id         string
		name       string
		argsBuildr strings.Builder
	}
	pending := make(map[int64]*toolCallAccum)

	// Token 用量：OpenAI 在设置了 StreamOptions.IncludeUsage 后，
	// 仅在最后一个 chunk（len(evt.Choices)==0）中携带完整用量
	var inputTokens, outputTokens int64

	for stream.Next() {
		evt := stream.Current()

		// 提取 token 用量：最后一个 chunk 的 Choices 为空但 Usage 有值
		if evt.Usage.PromptTokens > 0 || evt.Usage.CompletionTokens > 0 {
			inputTokens = evt.Usage.PromptTokens
			outputTokens = evt.Usage.CompletionTokens
		}

		if len(evt.Choices) == 0 {
			continue
		}
		delta := evt.Choices[0].Delta

		// 文本增量
		if delta.Content != "" {
			select {
			case ch <- StreamChunk{Content: delta.Content}:
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

		// tool_calls 增量（按 index 累积）
		for _, tc := range delta.ToolCalls {
			acc, ok := pending[tc.Index]
			if !ok {
				acc = &toolCallAccum{}
				pending[tc.Index] = acc
			}
			if tc.ID != "" {
				acc.id = tc.ID
			}
			if tc.Function.Name != "" {
				acc.name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				acc.argsBuildr.WriteString(tc.Function.Arguments)
			}
		}
	}

	// 按 index 升序排列所有累积的 tool_call，构造 ToolUses 切片
	var toolUses []ToolUseBlock
	if len(pending) > 0 {
		indices := make([]int64, 0, len(pending))
		for idx := range pending {
			indices = append(indices, idx)
		}
		sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })
		toolUses = make([]ToolUseBlock, 0, len(indices))
		for _, idx := range indices {
			if acc, ok := pending[idx]; ok {
				args := acc.argsBuildr.String()
				if args == "" {
					args = "{}"
				}
				toolUses = append(toolUses, ToolUseBlock{
					ID:    acc.id,
					Name:  acc.name,
					Input: json.RawMessage(args),
				})
			}
		}
	}

	// 构造 token 用量
	usage := &TokenUsage{
		InputTokens:  int(inputTokens),
		OutputTokens: int(outputTokens),
	}

	// 检查流错误
	if err := stream.Err(); err != nil {
		// 用户主动取消视为正常中断
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
func (p *OpenAIProvider) shouldRetry(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	// 检查 OpenAI API 错误状态码
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.StatusCode == http.StatusUnauthorized:
			return false
		case apiErr.StatusCode == http.StatusTooManyRequests:
			return false
		case apiErr.StatusCode >= 500:
			return true
		default:
			return false
		}
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	return false
}
