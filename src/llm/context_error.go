// Package llm：context_error.go 提供「上下文超长错误」的统一识别能力。
//
// 背景：Provider 在请求体超过模型上下文窗口时，会返回带 4xx（通常 400）的错误。
// 这类错误与「普通参数错误 400」HTTP 状态码相同，但语义截然不同——前者可通过压缩历史
// 后重试恢复，后者压缩无意义（参数本身错误，重试多少次都一样）。因此必须【精确区分】，
// 仅对「真正的上下文超长」触发紧急压缩 + 重试，避免对普通 400 也做无谓的压缩与重试。
//
// 判定依据（各 Provider 实际返回形态）：
//   - Anthropic：HTTP 400，错误体 type 为 "invalid_request_error"，但 message 形如
//     "prompt is too long: N tokens > M maximum ..."。type 字段无法区分（超长与普通
//     参数错误同属 invalid_request_error），故判定 RawJSON() 是否包含 "prompt is too long"。
//   - OpenAI：HTTP 400，错误体 code 为 "context_length_exceeded"，message 形如
//     "This model's maximum context length is ... tokens ..."。code 字段是权威判定。
//
// 设计为纯函数（不依赖 Provider 实例），由 conversation 包在 runOneLLM 撞墙兜底路径调用。
package llm

import (
	"errors"
	"net/http"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	openai "github.com/openai/openai-go"
)

// IsContextTooLongError 判断 err 是否为「上下文超长」错误。
//
// 命中条件（满足任一）：
//  1. err 可解包为 *anthropic.Error，且 StatusCode == 400，且 RawJSON() 含 "prompt is too long"
//     （Anthropic 的超长错误唯一可区分特征——type 字段同为 invalid_request_error）。
//  2. err 可解包为 *openai.Error，且 StatusCode == 400，且 Code == "context_length_exceeded"；
//     Code 为空时降级匹配 message 含 "maximum context length"（兼容某些代理/网关不回 code 的情况）。
//
// 返回 false 的典型场景：普通 400 参数错误、401/403/429、5xx、网络错误、context 取消——
// 这些都不应触发紧急压缩（压缩无意义或会掩盖真实问题）。
//
// 注意：仅检查 HTTP 400。部分网关可能用 413 Request Entity Too Large 表达超长，但主流
// 官方 API（Anthropic / OpenAI）均用 400，此处与官方语义对齐，避免误判。
func IsContextTooLongError(err error) bool {
	if err == nil {
		return false
	}

	// ---- Anthropic ----
	var anthropicErr *anthropic.Error
	if errors.As(err, &anthropicErr) {
		if anthropicErr.StatusCode != http.StatusBadRequest {
			return false
		}
		// Anthropic 的 type 字段对超长与普通参数错误都是 invalid_request_error，无法区分；
		// 唯一可区分特征是 message 中的 "prompt is too long" 字样。RawJSON() 返回原始响应体。
		raw := anthropicErr.RawJSON()
		if raw != "" && strings.Contains(raw, "prompt is too long") {
			return true
		}
		return false
	}

	// ---- OpenAI ----
	var openaiErr *openai.Error
	if errors.As(err, &openaiErr) {
		if openaiErr.StatusCode != http.StatusBadRequest {
			return false
		}
		// code 字段是权威判定（官方固定返回 context_length_exceeded）。
		if openaiErr.Code == "context_length_exceeded" {
			return true
		}
		// 降级：某些代理/网关可能不回 code，按 message 关键字兜底匹配。
		if openaiErr.Code == "" && strings.Contains(openaiErr.Message, "maximum context length") {
			return true
		}
		return false
	}

	return false
}
