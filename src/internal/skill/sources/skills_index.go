// Package sources（skills_index.go）实现 Skill 系统的「渐进式披露索引注入」Source（Step 10 Skill 系统）。
//
// 会话启动时读取 skill.Registry 中已合并的 Skill 列表，按来源级别
// （项目级 → 用户级 → 内置级）分组，把每个 Skill 的 name + description
// + source 渲染为 markdown 索引段，作为 LeadUserMessage（Placement=UserMessage）
// 注入到 System Prompt。LLM 据此知道「有哪些 Skill 可用」，但看不到完整 SKILL.md
// 正文——必须通过 use_skill 工具按需加载，符合 spec §C.1 / §C.2 渐进式披露硬约束。
//
// 与 memory_index.go 同构：同为 LeadUserMessage、同样按域分组、缺失时降级为空 Section。
// 区别仅在数据来源（memory_index 读 autolearn.Store，本 Source 读 skill.Registry）
// 与渲染粒度（memory 索引是单行简介列表，Skill 索引是三档分组的结构化列表）。
//
// [架构分层] 本包归 skill/sources 子包（工具层，spec §A 子包划分），依赖
// skill.Registry（同子包，sibling 包，不形成反向依赖）；与 engine/prompt/sources
// 仅是接口消费关系（实现其 Source 接口），不形成循环 import。
package sources

import (
	"context"
	"strings"

	"github.com/MeiCorl/CodePilot/src/internal/engine/prompt/sources"
	"github.com/MeiCorl/CodePilot/src/internal/engine/prompt/tokens"
	"github.com/MeiCorl/CodePilot/src/internal/skill"
)

// skillsIndexMaxLines 为 Skill 索引的【默认行数上限】。与 spec §C.1 对齐——
// Skill 数量一般较小（项目级 + 用户级 + 内置级总和通常 < 50），200 行上限足够；
// 同时作为「多 Skill 注入场景下避免 LeadUserMessage 过度膨胀」的兜底。
const skillsIndexMaxLines = 200

// SourceName 暴露 Source 标识，便于测试断言与 Builder Stats key 复用。
//
// [Why] 与 skills_index.go 文件名 + Name() 返回值保持一致，集中常量避免
// 散落字面量导致"Source 注册名"与"Stats key"漂移。
const SourceName = "skills_index"

// SkillsIndexSource 把 skill.Registry 中已注册的 Skill 列表注入为
// LeadUserMessage，让 LLM 知道有哪些 Skill 可用。
//
// 持有 skill.Registry 引用（运行期可能新增/覆盖 Skill，对应 Assemble 每次
// 重新读取 ListBySource 拿最新数据）。registry 为 nil 时 Assemble 返回空 Section，
// 整体降级（与 memory_index 同思路）。
//
// 关键设计（spec §C.2 渐进式披露硬约束）：
//  1. Content **只**含 name + description + source 标签，不暴露 SKILL.md 完整正文
//  2. 完整 Skill 内容必须通过 use_skill 工具按需加载，避免污染 system prompt
//  3. 三档按"项目级 → 用户级 → 内置级"顺序展示（与 spec §A.4 优先级一致）
type SkillsIndexSource struct {
	// registry 是已合并的 Skill 注册表，s.skillsIndexAssemble 通过
	// registry.ListBySource 分组读取 Skill 列表。
	registry *skill.Registry
	// maxLines 是渲染文本的最大行数（防御性截断）。<=0 时回退到包默认
	// skillsIndexMaxLines（200）。
	maxLines int
}

// NewSkillsIndexSource 构造一个 Skill 索引注入 Source。
//
// r 为已合并的 Skill 注册表（由 main.go 通过 skill.LoadAll 构造）；传 nil
// 表示 Skill 系统未就绪，Assemble 短路返回空 Section，整体降级。
func NewSkillsIndexSource(r *skill.Registry) *SkillsIndexSource {
	return &SkillsIndexSource{
		registry: r,
		maxLines: skillsIndexMaxLines,
	}
}

// Name 实现 Source 接口。返回常量 "skills_index"，与 Builder Stats /
// dev_export_sp payload 的 key 保持一致。
func (s *SkillsIndexSource) Name() string {
	return SourceName
}

// Assemble 读取 skill.Registry 中已注册的 Skill 列表，按三档分组后
// 渲染为带 <skills_index> 标签的 markdown 段，作为 LeadUserMessage 注入。
//
// 行为约定：
//  1. registry 为 nil → 返回空 Section（Skill 系统未就绪，不阻塞 SP 组装）
//  2. registry 为空（无任何 Skill 加载成功）→ 返回空 Section（避免污染 LeadUserMessage）
//  3. 按"项目级 → 用户级 → 内置级"顺序渲染各档，每档 Skill 之间空行分隔
//  4. Content **只**含 name + description + source 标签，绝不拼接 Skill.Body()
//     （spec §C.2 渐进式披露硬约束——完整内容只能通过 use_skill 工具按需加载）
//  5. 外层包 <skills_index> 标签，Placement=UserMessage
//  6. token 估算走 tokens.Estimate(content) 填入 Section.Tokens
//
// 本方法不依赖 Env（registry 已固化），参数保留仅为满足 Source 接口签名。
func (s *SkillsIndexSource) Assemble(_ context.Context, _ sources.Env) (sources.Section, error) {
	// 依赖缺失（registry 未就绪）或 0 Skill 场景：短路返回空 Section，
	// Builder 自动过滤空段，不产生空 LeadUserMessage 注入。
	if s.registry == nil || s.registry.Count() == 0 {
		return sources.Section{
			Name:      SourceName,
			Content:   "",
			Placement: sources.PlacementUserMessage,
			Tokens:    0,
		}, nil
	}

	body := s.renderIndexBody()
	if strings.TrimSpace(body) == "" {
		return sources.Section{
			Name:      SourceName,
			Content:   "",
			Placement: sources.PlacementUserMessage,
			Tokens:    0,
		}, nil
	}

	body = s.truncateIndexBody(body)
	content := "<skills_index>\n" + body + "\n</skills_index>"

	return sources.Section{
		Name:      SourceName,
		Content:   content,
		Placement: sources.PlacementUserMessage,
		Tokens:    tokens.Estimate(content),
	}, nil
}

// renderIndexBody 按"项目级 → 用户级 → 内置级"顺序渲染三档 Skill 索引。
//
// [Why] 渲染顺序与 skill.Registry.Register 的加载顺序一致（内置先到、用户次之、
// 项目最后），但 spec §A.4 要求"项目级优先级最高"，故此处按"项目级 → 用户级 →
// 内置级"反向展示——让 LLM 优先看到最相关的项目级 Skill。
//
// 单 Skill 格式：
//
//	[<source>] <name>
//	  描述: <description>
//
// 空档自动跳过（不出现 "[project]" 空标签），避免污染索引。
func (s *SkillsIndexSource) renderIndexBody() string {
	header := "以下是当前可用的 Skill 列表（渐进式披露：仅当 LLM 判定需要时才通过 use_skill 工具加载完整内容）："

	projectSkills := s.registry.ListBySource(skill.SourceProject)
	userSkills := s.registry.ListBySource(skill.SourceUser)
	builtinSkills := s.registry.ListBySource(skill.SourceBuiltin)

	var parts []string
	parts = append(parts, header)
	if block := renderSourceBlock("project", projectSkills); block != "" {
		parts = append(parts, block)
	}
	if block := renderSourceBlock("user", userSkills); block != "" {
		parts = append(parts, block)
	}
	if block := renderSourceBlock("builtin", builtinSkills); block != "" {
		parts = append(parts, block)
	}

	return strings.Join(parts, "\n\n")
}

// renderSourceBlock 渲染单档 Skill 列表。空档返回空字符串，由调用方跳过。
//
// [Why 显式接受 source 字符串而非 *Skill.Source] 渲染层只关心人类可读标签，
// 不应耦合 Source 枚举；Source.String() 已是稳定契约，直接复用避免转换层漂移。
func renderSourceBlock(source string, list []*skill.Skill) string {
	if len(list) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, sk := range list {
		if sk == nil {
			continue
		}
		// 单 Skill 格式：[<source>] <name>\n  描述: <description>
		sb.WriteString("[")
		sb.WriteString(source)
		sb.WriteString("] ")
		sb.WriteString(sk.Name)
		sb.WriteString("\n  描述: ")
		sb.WriteString(sk.Description)
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// truncateIndexBody 按行数上限截断索引文本。
//
// [Why] 防御性截断：正常场景 Skill 数量小（< 50），不会触发；但允许用户
// 在项目级 .codepilot/skills/ 大量放 Skill 时不撑爆 LeadUserMessage。
// 阈值取包默认 skillsIndexMaxLines（200），构造时未暴露 options（保持
// 构造函数简单），后续若需配置可在 NewSkillsIndexSource 扩展参数。
func (s *SkillsIndexSource) truncateIndexBody(body string) string {
	maxLines := s.maxLines
	if maxLines <= 0 {
		maxLines = skillsIndexMaxLines
	}
	lines := strings.Split(body, "\n")
	if len(lines) <= maxLines {
		return body
	}
	return strings.Join(lines[:maxLines], "\n")
}

// 编译期接口断言：确保 SkillsIndexSource 实现 sources.Source 接口。
// 若方法签名漂移（如 Name() 返回类型变了），此处编译失败可立即发现。
var _ sources.Source = (*SkillsIndexSource)(nil)
