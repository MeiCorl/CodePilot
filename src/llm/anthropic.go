package llm

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"time"

	"github.com/MeiCorl/CodePilot/src/internal/config"
	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// AnthropicProvider 是 Anthropic（Claude 系列）的 LLM 适配器。
// 实现 Provider 接口，将内部通用消息格式转换为 Anthropic SDK 格式，
// 支持流式输出、超时控制、重试机制。
type AnthropicProvider struct {
	// client 为 Anthropic SDK 客户端实例
	client *anthropic.Client
	// model 为模型名称，如 "claude-sonnet-4-20250514"
	model string
	// maxTokens 为单次请求最大输出 token 数
	maxTokens int
	// timeout 为请求超时时间（秒）
	timeout int
	// maxRetries 为最大重试次数
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

// convertMessages 将内部通用 Message 数组转换为 Anthropic SDK 的 MessageParam 数组。
// System Prompt 不混入此数组，由 StreamChat 单独传入。
// 后续 ImageBlock 将映射为 anthropic.NewImageBlock。
func (p *AnthropicProvider) convertMessages(messages []Message) []anthropic.MessageParam {
	params := make([]anthropic.MessageParam, 0, len(messages))
	for _, msg := range messages {
		// 收集当前消息的所有 ContentBlock 为 Anthropic ContentBlockParamUnion
		blocks := make([]anthropic.ContentBlockParamUnion, 0, len(msg.Content))
		for _, block := range msg.Content {
			if block.Type() == ContentBlockTypeText {
				blocks = append(blocks, anthropic.NewTextBlock(block.ToText()))
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
// 通过 ctx 支持取消（用户 Esc 中断），超时由配置控制。
// 返回只读 channel 供消费方逐块读取流式响应。
func (p *AnthropicProvider) StreamChat(ctx context.Context, systemPrompt string, messages []Message) (<-chan StreamChunk, error) {
	ch := make(chan StreamChunk, 64)

	go func() {
		defer close(ch)
		p.streamWithRetry(ctx, systemPrompt, messages, ch)
	}()

	return ch, nil
}

// streamWithRetry 实现带重试的流式请求。
// 仅对网络错误和 HTTP 5xx 重试，采用指数退避策略。
// HTTP 401（认证错误）和 429（限流）不重试，直接通过 channel 返回错误。
func (p *AnthropicProvider) streamWithRetry(ctx context.Context, systemPrompt string, messages []Message, ch chan<- StreamChunk) {
	// 构建请求参数
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
func (p *AnthropicProvider) doStream(ctx context.Context, params anthropic.MessageNewParams, ch chan<- StreamChunk) error {
	// 包装超时 context
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.timeout)*time.Second)
	defer cancel()

	stream := p.client.Messages.NewStreaming(ctx, params)

	for stream.Next() {
		event := stream.Current()

		switch evt := event.AsAny().(type) {
		case anthropic.ContentBlockDeltaEvent:
			switch delta := evt.Delta.AsAny().(type) {
			case anthropic.TextDelta:
				select {
				case ch <- StreamChunk{Content: delta.Text}:
				case <-ctx.Done():
					ch <- StreamChunk{Done: true}
					return nil
				}
			}
		}
	}

	// 检查流错误
	if err := stream.Err(); err != nil {
		// ctx 取消视为正常中断
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			ch <- StreamChunk{Done: true}
			return nil
		}
		return err
	}

	// 流正常结束
	ch <- StreamChunk{Done: true}
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
