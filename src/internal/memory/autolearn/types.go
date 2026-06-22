// Package autolearn 实现 CodePilot 的「自动学习记忆」子系统（Step 8 记忆系统）。
//
// 自动学习记忆是 CodePilot 长期记忆的第三类：
//   - 静态记忆：System Prompt、工具集描述（Step 4 已落地）
//   - 半静态记忆：AGENTS.md、环境信息（Step 4 已落地）
//   - 自动学习记忆（本包）：Agent 在使用过程中由后台回顾器自主总结、按分类沉淀为
//     独立 md 文件，跨会话持久化；新会话启动时通过 MEMORY.md 索引注入上下文，
//     让 Agent「想起」之前沉淀的用户偏好、反馈、项目知识与参考信息。
//
// 本包文件职责拆分：
//   - types.go：数据模型（记忆类型、存储域、记忆记录、索引行）
//   - store.go：文件持久化抽象（记忆文件读写、MEMORY.md 索引读写、原子写、路径逃逸防护）
//   - source.go（Task 2）：prompt.Source 实现，把索引注入 LeadUserMessage
//   - reviewer.go（Task 5）：后台异步回顾器
//   - prompt.go（Task 4）：回顾专用 prompt 模板
//   - sanitizer.go（Task 4）：敏感信息脱敏
//
// 存储布局：
//
//	~/.codepilot/memory/            用户级（偏好 + 反馈，跨所有项目）
//	  MEMORY.md                     索引（按 4 类分块，每行 - [type](slug.md)——简介）
//	  <slug>.md                     单条记忆（YAML frontmatter + 正文）
//	<cwd>/.codepilot/memory/        项目级（项目知识 + 参考，跟随当前项目）
//	  MEMORY.md
//	  <slug>.md
package autolearn

import "time"

// MemoryType 记忆的四种分类。
//
// 分类与存储域是固定映射（见 ScopeOf）：
//   - 用户级（~/.codepilot/memory/）：偏好、反馈——与具体项目无关，跨项目生效
//   - 项目级（<cwd>/.codepilot/memory/）：项目知识、参考——跟随项目目录
//
// 这四类也作为 MEMORY.md 索引的分块维度，渲染顺序见 memoryTypeOrder。
type MemoryType string

const (
	// MemoryTypeUserPreference 用户偏好：用户明确表达的做事方式约定
	// （如「缩进用 4 个空格，不要用 tab」）。归属用户级，跨所有项目生效。
	MemoryTypeUserPreference MemoryType = "user_preference"

	// MemoryTypeUserFeedback 用户反馈：用户对 Agent 输出的纠正性反馈与正确做法
	// （如「上次生成的代码漏了错误处理，应该在……处补上」）。归属用户级。
	MemoryTypeUserFeedback MemoryType = "user_feedback"

	// MemoryTypeProjectKnowledge 项目知识：关于当前项目的技术架构、部署运维、
	// 内部约定等信息。归属项目级，跟随项目目录。
	MemoryTypeProjectKnowledge MemoryType = "project_knowledge"

	// MemoryTypeReference 参考信息：外部链接与资料（如某 API 文档地址、
	// 内部 wiki 链接、DB 使用手册位置）。归属项目级。
	MemoryTypeReference MemoryType = "reference"
)

// StorageScope 记忆的存储域。
type StorageScope string

const (
	// ScopeUser 用户级：~/.codepilot/memory/，跨所有项目生效。
	ScopeUser StorageScope = "user"

	// ScopeProject 项目级：<cwd>/.codepilot/memory/，跟随当前项目。
	ScopeProject StorageScope = "project"
)

// memoryTypeOrder 定义 4 类记忆在 MEMORY.md 索引中的分块渲染顺序。
// 顺序固定，保证索引文件在多次重写下仍稳定可读、可 diff，且 Source 注入顺序确定。
var memoryTypeOrder = []MemoryType{
	MemoryTypeUserPreference,
	MemoryTypeUserFeedback,
	MemoryTypeProjectKnowledge,
	MemoryTypeReference,
}

// AllMemoryTypes 返回全部 4 类记忆（按固定顺序的拷贝）。
// 供索引渲染分块、回顾 prompt 列举分类等场景使用。返回拷贝避免外部误改包内顺序表。
func AllMemoryTypes() []MemoryType {
	out := make([]MemoryType, len(memoryTypeOrder))
	copy(out, memoryTypeOrder)
	return out
}

// IsValidType 判断 t 是否为合法的 4 类之一。
// 用于校验回顾 LLM 产出的类型、解析历史索引时过滤未知标签，避免脏数据写入。
func IsValidType(t MemoryType) bool {
	for _, v := range memoryTypeOrder {
		if v == t {
			return true
		}
	}
	return false
}

// ScopeOf 返回某类记忆应当归属的存储域。
// 偏好 / 反馈 → 用户级；项目知识 / 参考 → 项目级。
// t 非法时默认返回项目级（调用方应先用 IsValidType 校验，避免误用）。
func ScopeOf(t MemoryType) StorageScope {
	switch t {
	case MemoryTypeUserPreference, MemoryTypeUserFeedback:
		return ScopeUser
	default:
		return ScopeProject
	}
}

// Frontmatter 是单条记忆文件头部的 YAML frontmatter。
//
// [Why] 仅含受控标量字段（类型枚举、单行标题、时间戳）；记忆正文 Content 不进
// frontmatter，而是放在 --- 闭合标记之后的正文区——这样长文本与特殊字符
// 无需经过 YAML 转义，既避免转义 bug，也让人可直接阅读编辑 md 文件。
type Frontmatter struct {
	// Type 记忆分类，取值限定为 4 类之一（IsValidType 校验）。
	Type MemoryType `yaml:"type"`
	// Title 记忆标题（单行简述），供索引与人类阅读。
	Title string `yaml:"title"`
	// CreatedAt 首次创建时间（RFC3339），由回顾器在新建时注入。
	CreatedAt time.Time `yaml:"created_at"`
	// UpdatedAt 最近一次更新时间（RFC3339），覆盖写时刷新。
	UpdatedAt time.Time `yaml:"updated_at"`
}

// Memory 是一条完整的自动学习记忆记录，对应磁盘上一个 md 文件。
//
// 文件布局示例：
//
//	---
//	type: user_preference
//	title: 缩进风格
//	created_at: 2026-06-17T10:00:00Z
//	updated_at: 2026-06-17T10:05:00Z
//	---
//
//	使用 4 个空格代替 TAB，不要使用 tab 字符。
//
// 字段说明：
//   - Frontmatter（匿名嵌入）：进 YAML 头部的 4 个标量字段
//   - Slug：记忆文件名（不含 .md），非持久化字段，由 store 读写时据文件名填充/校验
//   - Content：记忆正文（Markdown），不进 YAML（yaml:"-"）
//
// Slug / Content 标记 yaml:"-" 是防御性的：即便误对整个 Memory 做 yaml.Marshal，
// 也不会把正文塞进 frontmatter 造成格式错乱。
type Memory struct {
	Frontmatter
	// Slug 为记忆文件名（不含 .md 后缀）。由回顾器生成，经 normalizeSlug 规范化。
	Slug string `yaml:"-"`
	// Content 为记忆正文（frontmatter 闭合 --- 之后的全部内容），不参与 YAML 序列化。
	Content string `yaml:"-"`
}

// IndexEntry 是 MEMORY.md 索引中的一行。
//
// 索引行渲染格式（分隔符为中文双破折号 ——，与用户规约一致）：
//
//	- [user_preference](indent-style.md)——使用4个空格代替TAB
//
// [Why] Type 同时出现在行内标签 [type]，使索引即便在缺少 H2 分块标题时，
// 仍可逐行解析归类——解析只依赖行内标签，渲染时的分块标题仅供人类阅读。
type IndexEntry struct {
	// Type 记忆分类（同时渲染进行内 [type] 标签）。
	Type MemoryType
	// Slug 记忆文件名（不含 .md）。
	Slug string
	// Summary 一句话简介，展示在 —— 之后。
	Summary string
}
