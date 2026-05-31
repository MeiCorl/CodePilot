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
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

// OpenAIProvider 是 OpenAI（GPT 系列）的 LLM 适配器。
// 实现 Provider 接口，将内部通用消息格式转换为 OpenAI SDK 格式，
// 支持流式输出、超时控制、重试机制。
type OpenAIProvider struct {
	// client 为 OpenAI SDK 客户端实例
	client *openai.Client
	// model 为模型名称，如 "gpt-4o"
	model string
	// maxTokens 为单次请求最大输出 token 数
	maxTokens int
	// timeout 为请求超时时间（秒）
	timeout int
	// maxRetries 为最大重试次数
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

// convertMessages 将内部通用 Message 数组转换为 OpenAI SDK 的 ChatCompletionMessageParamUnion 数组。
// System Prompt 不混入此数组，由 StreamChat 单独传入。
// 后续 ImageBlock 将映射为 openai.ImagePart(...)。
func (p *OpenAIProvider) convertMessages(messages []Message) []openai.ChatCompletionMessageParamUnion {
	params := make([]openai.ChatCompletionMessageParamUnion, 0, len(messages))
	for _, msg := range messages {
		// 当前仅支持 TextBlock，直接提取文本内容
		text := ""
		for _, block := range msg.Content {
			if block.Type() == ContentBlockTypeText {
				text += block.ToText()
			}
		}

		switch msg.Role {
		case RoleUser:
			params = append(params, openai.UserMessage(text))
		case RoleAssistant:
			params = append(params, openai.AssistantMessage(text))
		}
	}
	return params
}

// StreamChat 发起一次 OpenAI 流式对话请求。
// 通过 ctx 支持取消（用户 Esc 中断），超时由配置控制。
// 返回只读 channel 供消费方逐块读取流式响应。
func (p *OpenAIProvider) StreamChat(ctx context.Context, systemPrompt string, messages []Message) (<-chan StreamChunk, error) {
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
func (p *OpenAIProvider) streamWithRetry(ctx context.Context, systemPrompt string, messages []Message, ch chan<- StreamChunk) {
	msgParams := p.convertMessages(messages)

	// 如果有 System Prompt，作为第一条消息插入
	allMessages := make([]openai.ChatCompletionMessageParamUnion, 0, len(msgParams)+1)
	if systemPrompt != "" {
		allMessages = append(allMessages, openai.SystemMessage(systemPrompt))
	}
	allMessages = append(allMessages, msgParams...)

	params := openai.ChatCompletionNewParams{
		Messages:  allMessages,
		Model:     p.model,
		MaxTokens: openai.Int(int64(p.maxTokens)),
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
func (p *OpenAIProvider) doStream(ctx context.Context, params openai.ChatCompletionNewParams, ch chan<- StreamChunk) error {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.timeout)*time.Second)
	defer cancel()

	stream := p.client.Chat.Completions.NewStreaming(ctx, params)

	for stream.Next() {
		evt := stream.Current()
		if len(evt.Choices) > 0 {
			content := evt.Choices[0].Delta.Content
			if content != "" {
				select {
				case ch <- StreamChunk{Content: content}:
				case <-ctx.Done():
					ch <- StreamChunk{Done: true}
					return nil
				}
			}
		}
	}

	if err := stream.Err(); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			ch <- StreamChunk{Done: true}
			return nil
		}
		return err
	}

	ch <- StreamChunk{Done: true}
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
