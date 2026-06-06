// Package prompt 实现 System Prompt 的组装管线。
//
// 整个 System Prompt 由多个 Source（静态规则、环境上下文、AGENTS.md、
// 自动记忆等）各自产出一段内容，Builder 按 Placement 分组后拼成
// 最终的 SystemPrompt 结构体供 LLM Provider 使用。
//
// 关键设计：
//  1. Source 无状态、可单元测试、可任意扩展
//  2. Placement=System 的内容进 system 字段、可被 Anthropic 缓存
//  3. Placement=UserMessage 的内容合并为首条 user 消息
//  4. Env 在会话启动时一次性采集并传入，避免散落的环境调用
package prompt

import (
	"context"
	"fmt"
	"strings"

	"github.com/MeiCorl/CodePilot/src/internal/engine/prompt/sources"
)

// Builder 是 System Prompt 的组装器。
//
// 通过 NewBuilder 注册一组 Source，按注册顺序调用 Assemble，
// 并按 Placement 把 Section 分发到 SystemBlocks 或 LeadUserMessage。
//
// Builder 本身是无状态的（除 enabled 标志外不持有可变状态），可并发调用 Assemble。
type Builder struct {
	// sources 为已注册的 Source 列表，注册顺序与最终 SystemBlocks 顺序一致
	sources []sources.Source
	// enabled 控制系统是否实际产出内容；为 false 时 Assemble 直接返回空 SystemPrompt，
	// 对应 config.json 中 system_prompt.enabled = false 的场景。
	// 默认为 true；通过 SetEnabled 切换。
	enabled bool
}

// NewBuilder 构造一个 Builder 并按传入顺序注册所有 Source。
// 至少应注册 static + environment + agents_md + memory 四个 Source。
// 传入空切片是合法的——Assemble 会返回零值 SystemPrompt（用于关闭开关场景）。
//
// 新构造的 Builder 默认 enabled=true；通过 SetEnabled(false) 可关闭。
func NewBuilder(srcs ...sources.Source) *Builder {
	return &Builder{sources: srcs, enabled: true}
}

// SetEnabled 切换 Builder 的开关状态。
// 关闭后，Assemble 会立即返回零值 SystemPrompt，不调用任何 Source，
// 不消耗任何 token，Provider 也会跳过 system 字段构造。
//
// 设计动机：与「不注册任何 Source」语义不同——后者会调用 0 个 Source
// 但 Stats 仍为空、TotalTokens=0；前者（disabled）则完全短路，连 Sources
// 列表都不会被读取，调用方可在日志中通过「enabled=false」快速识别关闭原因。
func (b *Builder) SetEnabled(enabled bool) {
	b.enabled = enabled
}

// Enabled 返回当前开关状态，主要用于诊断与日志。
func (b *Builder) Enabled() bool {
	return b.enabled
}

// Assemble 顺序调用所有 Source 的 Assemble 方法，把 Section 按
// Placement 分组后拼成 SystemPrompt。
//
// 行为约定：
//  1. Builder 处于 disabled 状态时，立即返回零值 SystemPrompt（不开销）
//  2. 任一 Source 失败时立即返回错误，错误信息包含 Source 名称便于排查
//  3. 多个 PlacementUserMessage 段按 Source 顺序用 "\n\n" 连接为单条 LeadUserMessage
//  4. 所有 PlacementSystem 段标记 Cacheable=true（由 Source 自行决定
//     是否需要覆盖，本版本未提供覆盖入口；后续若需要可扩展 Section 字段）
//  5. TotalTokens 等于所有 Section.Tokens 之和
//  6. Stats 按 Source 注册顺序填充，即使 Section 为空（Content==""）
//     也保留条目，便于 WebUI 区分「这个 Source 没启用」与「没注册这个 Source」
//
// 本方法不修改任何 Source 的内部状态，可并发调用。
func (b *Builder) Assemble(ctx context.Context, env sources.Env) (sources.SystemPrompt, error) {
	// disabled 短路：直接返回零值 SystemPrompt
	if !b.enabled {
		return sources.SystemPrompt{}, nil
	}

	sp := sources.SystemPrompt{
		SystemBlocks: make([]sources.SystemBlock, 0, len(b.sources)),
		Stats:        make([]sources.SourceStat, 0, len(b.sources)),
	}

	var userParts []string

	for _, src := range b.sources {
		// 中途检查 ctx，避免 Source 内的长耗时操作阻塞取消信号
		if err := ctx.Err(); err != nil {
			return sources.SystemPrompt{}, fmt.Errorf("prompt builder 中途取消: %w", err)
		}

		section, err := src.Assemble(ctx, env)
		if err != nil {
			return sources.SystemPrompt{}, fmt.Errorf("source %q 装配失败: %w", src.Name(), err)
		}

		// Section.Name 应与 src.Name() 保持一致，这里做一次防御性校验，
		// 避免 Source 实现者忘记填写 Name 字段
		if section.Name == "" {
			section.Name = src.Name()
		}

		// 按 Placement 分发
		switch section.Placement {
		case sources.PlacementSystem:
			// 空内容也保留一个 SystemBlock，便于 WebUI 知道「这个 Source 启用了但产出空」
			// 但空 Text 的 system 段对 LLM 无意义，浪费 token；这里过滤掉
			if section.Content != "" {
				sp.SystemBlocks = append(sp.SystemBlocks, sources.SystemBlock{
					Text:      section.Content,
					Cacheable: true,
				})
			}
		case sources.PlacementUserMessage:
			// 多段非空内容用 "\n\n" 连接；遇到空段直接跳过
			if section.Content != "" {
				userParts = append(userParts, section.Content)
			}
		default:
			return sources.SystemPrompt{}, fmt.Errorf("source %q 返回了未知的 Placement=%d", src.Name(), section.Placement)
		}

		sp.Stats = append(sp.Stats, sources.SourceStat{
			Name:   section.Name,
			Tokens: section.Tokens,
		})
		sp.TotalTokens += section.Tokens
	}

	// 合并所有 UserMessage 段为单条 LeadUserMessage
	sp.LeadUserMessage = strings.Join(userParts, "\n\n")

	return sp, nil
}
