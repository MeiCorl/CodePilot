// Package sources（memory.go）实现「自动记忆」Source 的占位实现。
//
// 真实实现（基于历史会话总结、用户偏好抽取等）将在 Step 8（记忆系统）中提供；
// 当前步骤仅定义接口契约与 NoopMemoryProvider，让 Builder 与上层能在不依赖
// 真实实现的情况下完成接入与测试，避免后续 Step 8 改动引发级联修改。
//
// 设计要点：
//  1. MemoryProvider 是独立接口，定义「按 query 召回记忆」的最小契约
//  2. Builder 通过 MemorySource 适配层调用 MemoryProvider，把记忆片段拼为
//     一段 Markdown 文本输出（Placement=UserMessage）
//  3. NoopMemoryProvider 永远返回空切片，让当前步骤的 SP 拼装结果与 Step 1~3
//     的零记忆状态保持一致
//  4. Step 8 替换为真实实现时，只需把 NoopMemoryProvider 换成真实现即可，
//     Builder / Source 契约不变
package sources

import (
	"context"
	"strings"

	"github.com/MeiCorl/CodePilot/src/internal/engine/prompt/template"
	"github.com/MeiCorl/CodePilot/src/internal/engine/prompt/tokens"
)

// MemoryProvider 是 Step 8 真实实现需要满足的接口契约。
//
// 任何想要接入 Builder 的记忆后端（向量库、KV 库、文件系统等）都需实现本接口。
// 当前步骤唯一实现是 NoopMemoryProvider。
//
// 设计取舍：本接口只暴露最小召回能力（Recall），不暴露写入/更新/删除等
// 写操作——因为 SP 组装是只读上下文，记忆的写操作由 Step 8 单独的
// 后台逻辑在会话结束后触发，不应混在 SP 组装管线里。
type MemoryProvider interface {
	// Recall 基于 query（通常为会话上下文或最近一次用户消息）召回相关记忆片段。
	// 返回的每个片段是一段可读文本（Markdown / 纯文本均可），
	// Builder 会按顺序拼接为 LeadUserMessage 的一部分。
	//
	// 行为约定：
	//  1. 召回 0 个片段时返回 (nil, nil)，不视为错误
	//  2. 出错时返回 error；Builder 会包装并上抛（Source 错误应被上层感知）
	//  3. 返回的片段不应为空字符串；上游组装时也会过滤
	Recall(ctx context.Context, query string) ([]string, error)
}

// NoopMemoryProvider 是 MemoryProvider 的空实现，永远返回空切片。
//
// 用作 Builder 的默认记忆后端，让本步骤的 SP 拼装结果与
// Step 1~3（无自动记忆）的行为完全一致。
// Step 8 替换为真实实现时无需修改任何调用方代码。
type NoopMemoryProvider struct{}

// NewNoopMemoryProvider 构造一个 NoopMemoryProvider 实例。
func NewNoopMemoryProvider() *NoopMemoryProvider { return &NoopMemoryProvider{} }

// Recall 永远返回 (nil, nil)，表示「无记忆可召回」。
func (n *NoopMemoryProvider) Recall(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

// MemorySource 把 MemoryProvider 适配为 Source 接口。
//
// 这是典型的「适配器模式」应用：MemoryProvider 是 Step 8 抽象出的能力接口，
// Source 是本步骤定义的内容来源接口，二者粒度不同（一个返回切片，一个返回 Section），
// 通过 MemorySource 这个薄适配层对接。
//
// 适配规则：
//  1. Provider.Recall 返回的多个片段按顺序用 "\n\n---\n\n" 分隔
//  2. 包外层 <memories> 标签
//  3. 模板变量（{{VERSION}} 等）由 Render 替换
//  4. Provider 返回空 / 全空片段 → 整个 Section.Content 为空（Builder 会过滤）
//  5. Provider 报错 → 透传给 Builder（Source 错误应让上层感知）
type MemorySource struct {
	// provider 是底层记忆后端，Builder 通过 NewMemorySource 注入
	provider MemoryProvider
	// queryFn 用于在 Assemble 时从 env 派生 query（当前用 Env.CWD 作为占位）
	// 后续可改为最近一次用户消息摘要（Step 7 上下文管理后才有）
	queryFn func(env Env) string
}

// NewMemorySource 构造一个 MemorySource 适配器。
//
// 参数：
//   - provider: 任意实现 MemoryProvider 的实例（本步骤通常传 NoopMemoryProvider）
//   - queryFn: 从 Env 派生召回 query 的函数；传 nil 时默认返回 Env.CWD
func NewMemorySource(provider MemoryProvider, queryFn func(env Env) string) *MemorySource {
	if queryFn == nil {
		queryFn = func(env Env) string { return env.CWD }
	}
	return &MemorySource{
		provider: provider,
		queryFn:  queryFn,
	}
}

// Name 实现 Source 接口。
func (m *MemorySource) Name() string { return "memory" }

// Assemble 调用 provider.Recall 召回记忆片段，拼接后输出为 LeadUserMessage 的一部分。
//
// 输出 Content 格式：
//
//	<memories>
//	片段 1
//
//	---
//
//	片段 2
//	</memories>
//
// 任一错误（Provider 错误、ctx 取消）直接透传给 Builder。
func (m *MemorySource) Assemble(ctx context.Context, env Env) (Section, error) {
	query := m.queryFn(env)
	fragments, err := m.provider.Recall(ctx, query)
	if err != nil {
		return Section{}, err
	}

	// 过滤空片段
	nonEmpty := fragments[:0]
	for _, f := range fragments {
		if strings.TrimSpace(f) != "" {
			nonEmpty = append(nonEmpty, f)
		}
	}
	if len(nonEmpty) == 0 {
		// 无记忆可召回 → 返回空 Content，让 Builder 过滤
		return Section{
			Name:      "memory",
			Content:   "",
			Placement: PlacementUserMessage,
			Tokens:    0,
		}, nil
	}

	body := strings.Join(nonEmpty, "\n\n---\n\n")
	// 模板变量替换
	body = template.Render(body, env)
	// 外层包裹 <memories> 标签
	content := "<memories>\n" + body + "\n</memories>"

	return Section{
		Name:      "memory",
		Content:   content,
		Placement: PlacementUserMessage,
		Tokens:    tokens.Estimate(content),
	}, nil
}
