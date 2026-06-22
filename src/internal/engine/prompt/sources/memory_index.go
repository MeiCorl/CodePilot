// Package sources（memory_index.go）实现「自动记忆索引注入」Source（Step 8 记忆系统）。
//
// 会话启动时读取用户级（~/.codepilot/memory/MEMORY.md）+ 项目级（<cwd>/.codepilot/memory/MEMORY.md）
// 两份记忆索引，合并、按体积上限截断后，作为 LeadUserMessage（Placement=UserMessage）注入上下文，
// 让 LLM 在新会话开始即「想起」之前沉淀的用户偏好、反馈、项目知识与参考信息。
//
// 与 AGENTS.md 注入同构：同为 LeadUserMessage、同样做体积截断 + 缺失降级；区别仅在数据来源
// （AGENTS.md 读 H2 段落；本 Source 读 autolearn.Store 的索引条目）。
//
// [架构分层] 本 Source 归 sources 包（引擎层，上层），依赖 autolearn.Store（记忆层，下层）
// 读数据——上层→下层合规；autolearn 包不反向依赖 sources（避免下层依赖上层）。
package sources

import (
	"context"
	"strings"

	"github.com/MeiCorl/CodePilot/src/internal/engine/prompt/tokens"
	"github.com/MeiCorl/CodePilot/src/internal/logger"
	"github.com/MeiCorl/CodePilot/src/internal/memory/autolearn"
	"go.uber.org/zap"
)

// memoryIndexMaxLines / memoryIndexMaxBytes 为注入索引体积上限的【默认 fallback】。
//
// [Why 保留为常量] 与 config.defaultMemoryIndexMaxLines/Bytes 保持一致（200 行 / 25KB）。
// 当调用方未显式提供阈值（MemoryIndexOptions.MaxLines/MaxBytes <= 0）时回退到本默认值，
// 保证即便主流程忘记注入配置也能按用户规约截断。Task 6 接入 setting.json 后，正常路径
// 由主流程从 config.MemoryConfig 读取阈值经 MemoryIndexOptions 注入，覆盖本默认。
const (
	memoryIndexMaxLines = 200
	memoryIndexMaxBytes = 25 * 1024
)

// MemoryIndexOptions 控制 MemoryIndexSource 的注入行为，由主流程根据 setting.json 的
// memory 配置段（config.MemoryConfig）构造后注入。
//
// 字段语义：
//   - Enabled：记忆总开关。false 时 Assemble 短路返回空 Section（不注入索引）。
//     由 config.MemoryConfig.IsEnabled() 决定。与 store==nil 同效但语义独立——
//     Enabled 是显式业务开关，store==nil 是依赖缺失兜底，两者任一不满足即降级。
//   - MaxLines：索引注入行数上限，<=0 时回退到默认 memoryIndexMaxLines（200）。
//   - MaxBytes：索引注入字节上限，<=0 时回退到默认 memoryIndexMaxBytes（25KB）。
type MemoryIndexOptions struct {
	// Enabled 为记忆总开关，false 时不注入索引（enabled=false 降级）。
	Enabled bool
	// MaxLines 索引注入行数上限，<=0 用默认。
	MaxLines int
	// MaxBytes 索引注入字节上限，<=0 用默认。
	MaxBytes int
}

// MemoryIndexSource 实现 Source 接口，把 autolearn 两级记忆索引注入为 LeadUserMessage。
//
// 持有 autolearn.Store 引用（用户级/项目级根在 Store 构造时固化）与注入配置（opts）。
// 无内部可变状态，可并发调用 Assemble。store 为 nil 或 Enabled=false 时（记忆未启用）
// Assemble 返回空 Section，整体降级。
type MemoryIndexSource struct {
	store    *autolearn.Store
	enabled  bool
	maxLines int
	maxBytes int
}

// NewMemoryIndexSource 构造一个记忆索引注入 Source。
//
// store 为 autolearn 记忆存储器（由主流程计算用户级/项目级根后构造并注入），传 nil
// 表示记忆依赖未就绪。opts 控制总开关与体积阈值；opts 中 MaxLines/MaxBytes <=0 时
// 回退到包默认（200/25KB）。store==nil 或 opts.Enabled=false 时 Assemble 返回空 Section。
func NewMemoryIndexSource(store *autolearn.Store, opts MemoryIndexOptions) *MemoryIndexSource {
	maxLines := opts.MaxLines
	if maxLines <= 0 {
		maxLines = memoryIndexMaxLines
	}
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = memoryIndexMaxBytes
	}
	return &MemoryIndexSource{
		store:    store,
		enabled:  opts.Enabled,
		maxLines: maxLines,
		maxBytes: maxBytes,
	}
}

// Name 实现 Source 接口。沿用 "memory"，Task 7 接入后 Builder Stats 以此标识本 Source。
func (s *MemoryIndexSource) Name() string { return "memory" }

// Assemble 读取两级记忆索引并注入为 LeadUserMessage。
//
// 行为约定：
//  1. Enabled=false 或 store 为 nil → 返回空 Section（记忆关闭/未启用，不阻塞 SP 组装）
//  2. 分别读项目级 + 用户级索引（任一读失败记 warn 并按空处理，不报错上抛）
//  3. 合并渲染：项目级在前（与当前会话更相关），用户级在后，各自带域分组标签
//  4. 体积截断（按注入的 maxLines/maxBytes，超限截断 + warn 日志）
//  5. 外层包 <memory_index> 标签，Placement=UserMessage
//  6. 两级索引均为空 → 返回空 Section（Builder 自动过滤，不产生空注入）
//
// 本方法不依赖 Env（store 路径已固化），参数保留仅为满足 Source 接口签名。
func (s *MemoryIndexSource) Assemble(_ context.Context, _ Env) (Section, error) {
	// 总开关关闭（enabled=false 降级）或依赖缺失（store 未就绪）：短路返回空 Section。
	if !s.enabled || s.store == nil {
		return Section{
			Name:      "memory",
			Content:   "",
			Placement: PlacementUserMessage,
			Tokens:    0,
		}, nil
	}

	// 项目级在前、用户级在后读取；任一失败降级为空，不阻塞 SP 组装。
	projEntries, err := s.store.ReadIndex(autolearn.ScopeProject)
	if err != nil {
		logger.Warn("memory: 读取项目级记忆索引失败，按空处理", zap.Error(err))
		projEntries = nil
	}
	userEntries, err := s.store.ReadIndex(autolearn.ScopeUser)
	if err != nil {
		logger.Warn("memory: 读取用户级记忆索引失败，按空处理", zap.Error(err))
		userEntries = nil
	}

	body := renderMemoryIndexBody(projEntries, userEntries)
	if strings.TrimSpace(body) == "" {
		// 两级均为空（首次启动 / 无记忆）→ 空 Section，让 Builder 过滤
		return Section{
			Name:      "memory",
			Content:   "",
			Placement: PlacementUserMessage,
			Tokens:    0,
		}, nil
	}

	body = s.truncateMemoryIndex(body)
	content := "<memory_index>\n" + body + "\n</memory_index>"

	return Section{
		Name:      "memory",
		Content:   content,
		Placement: PlacementUserMessage,
		Tokens:    tokens.Estimate(content),
	}, nil
}

// renderMemoryIndexBody 把两级索引渲染为带域分组标签的文本。
// 项目级在前（与当前会话更相关），用户级在后；任一级为空则省略对应分组，避免空标签。
func renderMemoryIndexBody(projEntries, userEntries []autolearn.IndexEntry) string {
	var parts []string
	if projText := autolearn.RenderEntries(projEntries); strings.TrimSpace(projText) != "" {
		parts = append(parts, "项目级记忆：\n"+strings.TrimRight(projText, "\n"))
	}
	if userText := autolearn.RenderEntries(userEntries); strings.TrimSpace(userText) != "" {
		parts = append(parts, "用户级记忆：\n"+strings.TrimRight(userText, "\n"))
	}
	return strings.Join(parts, "\n\n")
}

// truncateMemoryIndex 按行数与字节上限截断索引文本，超限打 warn 日志。
//
// [Why] 记忆索引可能随使用不断增长，不截断会逐步蚕食上下文窗口；与 AGENTS.md 的 64KB
// 截断同思路。先按行截断（保留前 s.maxLines 行，行级截断不会切断索引条目主体），
// 再按字节截断（兜底 s.maxBytes，避免单条超长简介撑爆）。阈值由构造时注入
// （MemoryIndexOptions.MaxLines/MaxBytes），Task 6 后由主流程从 config.MemoryConfig 读取。
func (s *MemoryIndexSource) truncateMemoryIndex(body string) string {
	lines := strings.Split(body, "\n")
	if len(lines) > s.maxLines {
		logger.Warn("memory: 索引超过行数上限，已截断",
			zap.Int("original_lines", len(lines)),
			zap.Int("truncated_to", s.maxLines),
		)
		lines = lines[:s.maxLines]
	}
	out := strings.Join(lines, "\n")
	if len(out) > s.maxBytes {
		logger.Warn("memory: 索引超过字节上限，已截断",
			zap.Int("original_bytes", len(out)),
			zap.Int("truncated_to", s.maxBytes),
		)
		out = out[:s.maxBytes]
	}
	return out
}
