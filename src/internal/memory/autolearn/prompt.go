// prompt.go 实现「自动学习记忆」后台回顾器（Task 5 reviewer）所依赖的：
//   - 回顾专用 System Prompt 模板（reviewSystemPrompt）
//   - 回顾请求的输入快照（ReviewInput）与 user 消息渲染（renderReviewUserPrompt）
//   - 回顾 LLM 产出的结构化决策（ReviewDecision）与 JSON 解析（parseReviewDecisions）
//
// [设计定位] 本文件【不发起 LLM 调用、不读写文件】——纯 prompt 文本与结构化数据加工。
// LLM 调用（provider.StreamChat，toolSpecs=nil 强制禁工具）与落盘（store.WriteMemory +
// UpsertIndexEntry）由 reviewer.go（Task 5）编排。这样 prompt 与解析逻辑可独立单测，
// 也让 reviewer 主体聚焦于「触发判定 + 异步隔离 + 失败降级」。
//
// [与 Step 7 摘要 prompt 的同构] 复用项目既有结构化输出风格：
//   - 中文指令 + 【硬性约束】分段（参照 summary_compactor.go 的 summarySystemPrompt）；
//   - 明确「禁止调用工具」「禁止脑补」「只输出 JSON」三重约束；
//   - 输出为结构化 JSON 数组，空数组表达「无值得记忆」语义（对应摘要的「无」兜底）。
//
// [防御性解析] LLM 输出不可信：可能把 JSON 包在 ```json``` 围栏里、前后掺杂解释文字、
// 返回非法 JSON、单条字段缺失或取值越界。parseReviewDecisions 对每一种异常都做降级
// （整体非法 → 返回 nil + warn；单条非法 → 跳过该条 + warn），绝不 panic，符合
// spec「回顾全链路任一环节失败均静默降级」的高可用要求。

package autolearn

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/MeiCorl/CodePilot/src/internal/logger"
	"go.uber.org/zap"
)

// reviewSystemPrompt 是回顾阶段的 System Prompt（固定文案）。
//
// 刻意约束（与 spec 能力清单第 8~11 条一一对应）：
//  1. 【禁工具】toolSpecs=nil 已在协议层禁用，Prompt 内再强调作双保险，防止模型在
//     回顾里「虚构工具调用」污染决策。
//  2. 【敏感约束】明确禁止记录 API key / 密码 / token 等凭证——这是第一道防线；
//     sanitizer.go 的正则脱敏是第二道兜底。
//  3. 【4 类定义 + 归属域】让模型判断「该归用户级还是项目级」，type 字段严格枚举，
//     便于下游按归属域落盘到对应 memory 根目录。
//  4. 【索引比对去重】下文 user 消息会注入当前 MEMORY.md 索引，模型据此决定 new/update
//     并给出目标 slug，消除「同主题重复新建」与「自相矛盾的记忆」。
//  5. 【空数组语义】无值得记忆信息时返回 []，reviewer 据此不写任何文件。
const reviewSystemPrompt = `你是 CodePilot 的记忆回顾助手。你的任务是回顾刚刚结束的一轮对话，判断其中是否包含值得长期记住的信息，并按固定分类给出结构化决策。

【硬性约束】
- 禁止调用任何工具，只输出一个 JSON 数组，不要输出任何额外文字或解释。
- 不得记录任何敏感凭证：包括但不限于 API key、密钥、密码、token、私钥、.env 中的密钥、数据库连接串中的口令。遇到这类信息一律跳过，绝不写入记忆。
- 决策必须客观、准确，只总结对话中确实出现的信息，不得脑补或推测。
- 不要记录一次性、临时性、与长期工作无关的内容（如本次任务的临时细节、闲聊、一次性的报错堆栈、可直接从代码读到的明显事实）。
- 若用户明确使用「记住」「以后」「后续」「默认」「每次」「总是」「都要」「约定」「偏好」「习惯」「必须」「不要」等表达来声明长期做事方式，且内容不属于敏感凭证或撤销/遗忘请求，则必须沉淀为长期记忆，不允许返回空数组。
- 编码、行尾、缩进、命名规范、提交信息格式、回复语言、测试命令、工具使用习惯、代码风格、文件组织方式等长期约定均属于 user_preference。示例：「后续生成代码文件都使用 UTF-8 编码」应记录为 user_preference，而不是视为临时任务。

【记忆分类与归属域】
仅产出以下 4 类记忆，type 字段必须严格取以下枚举之一：
1. user_preference（用户偏好）：用户明确表达的做事方式约定（如缩进风格、命名规范、提交信息格式）。归属用户级，跨所有项目生效。
2. user_feedback（用户反馈）：用户对 Agent 输出的纠正性反馈与正确做法（如「上次漏了错误处理，应该在……处补上」）。归属用户级。
3. project_knowledge（项目知识）：关于当前项目的技术架构、部署运维、内部约定等信息。归属项目级，跟随当前项目。
4. reference（参考信息）：外部链接与资料（如某 API 文档地址、内部 wiki 链接、DB 使用手册位置）。归属项目级。

【索引比对与去重】
下文「当前已有记忆索引」会列出已有的记忆。若本轮要沉淀的信息与索引中某条【同主题】，请用 action=update 覆盖该条（给出其原有 slug），不要重复新建；若为新主题，用 action=new 并给出一个新的语义化 slug（仅小写字母、数字与连字符，如 indent-style、deploy-flow）。

【输出格式】
只输出一个 JSON 数组。数组中每个元素结构如下：
{
  "action": "new 或 update",
  "type": "user_preference | user_feedback | project_knowledge | reference",
  "slug": "语义化文件名，仅小写字母、数字与连字符",
  "title": "记忆标题（单行简述）",
  "summary": "一句话简介，将出现在 MEMORY.md 索引中",
  "content": "记忆正文（Markdown，客观陈述事实与做法）"
}

若本轮没有任何值得长期记住的信息，直接输出空数组：[]`

// reviewSummaryPrefix 为回顾请求 user 消息的可识别前缀标记，便于日志识别「这是一条回顾请求」。
const reviewSummaryPrefix = "[记忆回顾]"

// ReviewInput 是一次回顾的输入快照，由 reviewer 从本轮 Agent Loop 历史中提取，
// 经 renderReviewUserPrompt 渲染为回顾请求的 user 消息文本。
//
// [Why] 回顾走【独立无状态 LLM 调用】（不回写主对话历史），故需把本轮关键信息
// 「浓缩」成快照喂给回顾 LLM：只取用户输入、最终回复、工具调用名摘要（不含入参
// 出参全文，避免泄露敏感数据 + 控制回顾成本），以及当前两级 MEMORY.md 索引文本
// 供模型比对去重。
type ReviewInput struct {
	// UserInput 本轮用户原始输入。
	UserInput string
	// FinalReply 本轮 Agent 最终回复（assistant 文本块拼接，不含工具调用细节）。
	FinalReply string
	// ToolCallNames 本轮工具调用名摘要（仅工具名，去重保序，不含入参出参全文）。
	ToolCallNames []string
	// UserIndexText 用户级 MEMORY.md 索引文本（可空，缺失视为无用户级记忆）。
	UserIndexText string
	// ProjectIndexText 项目级 MEMORY.md 索引文本（可空，缺失视为无项目级记忆）。
	ProjectIndexText string
}

// ReviewAction 回顾决策的动作类型：新建或覆盖已有记忆。
type ReviewAction string

const (
	// ReviewActionNew 新建一条记忆文件（该主题在索引中不存在）。
	ReviewActionNew ReviewAction = "new"
	// ReviewActionUpdate 覆盖一条已有记忆（该主题在索引中已存在，给出其原 slug）。
	ReviewActionUpdate ReviewAction = "update"
)

// ReviewDecision 是回顾 LLM 产出的单条记忆决策，对应输出 JSON 数组中的一个元素。
//
// reviewer（Task 5）解析出该结构后：
//   - action=new：调 store.WriteMemory 新建文件 + UpsertIndexEntry 新增索引行；
//   - action=update：先校验 slug 是否真实存在于对应域索引（防模型虚构 slug 覆盖错文件），
//     存在则覆盖写 + 更新索引行简介，不存在则降级为跳过 + 日志。
//
// Slug 在 parseReviewDecisions 内已过 normalizeSlug 规范化 + isSafeSlug 校验。
type ReviewDecision struct {
	// Action 动作类型，取 ReviewActionNew / ReviewActionUpdate 之一。
	Action ReviewAction `json:"action"`
	// Type 记忆分类，已过 IsValidType 校验（合法 4 类之一）。
	Type MemoryType `json:"type"`
	// Slug 记忆文件名（不含 .md），已规范化 + 安全校验。
	Slug string `json:"slug"`
	// Title 记忆标题（单行），title/summary 互为兜底，最终保证非空。
	Title string `json:"title"`
	// Summary 一句话简介，进 MEMORY.md 索引行，title/summary 互为兜底保证非空。
	Summary string `json:"summary"`
	// Content 记忆正文（Markdown），写文件时正文区，sanitizer.Sanitize 兜底脱敏后落盘。
	Content string `json:"content"`
}

// renderReviewUserPrompt 把回顾输入快照渲染为回顾请求的 user 消息文本。
//
// 结构：【当前已有记忆索引】（项目级在前更相关 + 用户级）+ 【本轮对话快照】
// （用户输入 / 工具调用摘要 / 最终回复）+ 结尾指令。
//
// 两级索引任一为空均优雅省略对应分段；两级都空时标注「暂无已有记忆」，
// 让模型明确无需顾虑去重。
func renderReviewUserPrompt(in ReviewInput) string {
	var sb strings.Builder
	sb.WriteString(reviewSummaryPrefix)
	sb.WriteString("\n\n【当前已有记忆索引】\n")

	project := strings.TrimSpace(in.ProjectIndexText)
	user := strings.TrimSpace(in.UserIndexText)
	if project == "" && user == "" {
		sb.WriteString("（暂无已有记忆）\n")
	} else {
		if project != "" {
			sb.WriteString("--- 项目级记忆（跟随当前项目）---\n")
			sb.WriteString(project)
			sb.WriteString("\n")
		}
		if user != "" {
			sb.WriteString("--- 用户级记忆（跨所有项目）---\n")
			sb.WriteString(user)
			sb.WriteString("\n")
		}
	}

	sb.WriteString("\n【本轮对话快照】\n")
	sb.WriteString("用户输入：\n")
	sb.WriteString(nonEmptyOrPlaceholder(in.UserInput))
	sb.WriteString("\n\n")
	sb.WriteString("本轮工具调用：")
	sb.WriteString(toolNamesSummary(in.ToolCallNames))
	sb.WriteString("\n\n")
	sb.WriteString("Agent 最终回复：\n")
	sb.WriteString(nonEmptyOrPlaceholder(in.FinalReply))
	sb.WriteString("\n\n请基于以上信息，输出记忆决策 JSON 数组。")
	return sb.String()
}

// jsonFenceRe 匹配 markdown 代码围栏（``` 或 ```json）及其包裹内容，
// 用于从 LLM 输出中剥离可能存在的代码块包装。(?s) 让 . 匹配换行。
var jsonFenceRe = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)```")

// parseReviewDecisions 解析回顾 LLM 返回的文本为决策切片。
//
// 防御性降级策略（对应 checklist Task 4「非法 JSON 降级」「空数组语义」两项）：
//   - 整体非 JSON / 数组缺失 / 解析失败：返回 nil + warn（reviewer 据此不写任何文件）；
//   - 单条字段非法（action 非 new/update、type 非合法 4 类、slug/content 空）：
//     跳过该条 + warn，不影响其余合法条目；
//   - 空数组 []：返回 nil（语义即「无值得记忆」）；
//   - slug 经 normalizeSlug 规范化后仍非法（如纯符号）：跳过该条 + warn；
//   - title/summary 缺失：互为兜底，都空则取 content 前 40 字符。
//
// 容忍模型「不守规矩」：先用 extractJSONArray 剥离 ```json``` 围栏 + 截取首个 [ 到
// 最后一个 ] 之间内容，过滤掉模型在 JSON 前后掺杂的解释性文字。
func parseReviewDecisions(raw string) []ReviewDecision {
	rawJSON := extractJSONArray(raw)
	if rawJSON == "" {
		// 无任何数组结构：可能是模型输出纯文本解释、空对象 {}，或空响应。
		// 一律视为「无决策」，静默返回 nil（非错误，不记 warn 噪音——空数组是正常语义）。
		return nil
	}

	var raws []ReviewDecision
	if err := json.Unmarshal([]byte(rawJSON), &raws); err != nil {
		logger.Warn("autolearn: 回顾决策 JSON 解析失败，降级为无决策",
			zap.Error(err),
			zap.String("snippet", truncateForLog(raw, 200)),
		)
		return nil
	}

	// [Why] 用 nil 切片而非 make([]T,0,n)：只有真的收集到合法决策时才 append 使其非 nil。
	// 这样「空数组 []」「无数组结构」「全部条目被校验跳过」三种情况统一返回 nil，
	// 调用方（reviewer）用 len(decisions)==0 或 decisions==nil 判断「无决策、不写文件」语义一致。
	var out []ReviewDecision
	for _, d := range raws {
		if !validDecision(d) {
			continue
		}
		// 容忍模型产出的大写/空格/下划线 slug：源头规范化，与 store 写入侧同规则。
		d.Slug = normalizeSlug(d.Slug)
		if !isSafeSlug(d.Slug) {
			logger.Warn("autolearn: 跳过规范化后仍非法的决策 slug",
				zap.String("type", string(d.Type)),
				zap.String("action", string(d.Action)),
			)
			continue
		}
		normalizeMeta(&d)
		out = append(out, d)
	}
	return out
}

// extractJSONArray 从 LLM 返回文本中提取首个 JSON 数组片段。
//
// 步骤：去首尾空白 → 若被 ```json``` 围栏包裹则取围栏内文本 → 截取首个 '[' 到
// 最后一个 ']' 之间内容（容忍模型在 JSON 前后掺杂文字）。找不到合法区间返回空串。
func extractJSONArray(raw string) string {
	s := strings.TrimSpace(raw)
	if m := jsonFenceRe.FindStringSubmatch(s); m != nil {
		s = strings.TrimSpace(m[1])
	}
	start := strings.Index(s, "[")
	end := strings.LastIndex(s, "]")
	if start == -1 || end == -1 || end <= start {
		return ""
	}
	return s[start : end+1]
}

// validDecision 校验单条决策的字段合法性，非法时记 warn 并返回 false。
// 校验项：action 枚举、type 合法 4 类、slug/content 非空。
func validDecision(d ReviewDecision) bool {
	if d.Action != ReviewActionNew && d.Action != ReviewActionUpdate {
		logger.Warn("autolearn: 跳过非法 action 的决策",
			zap.String("action", string(d.Action)),
			zap.String("type", string(d.Type)),
		)
		return false
	}
	if !IsValidType(d.Type) {
		logger.Warn("autolearn: 跳过非法 type 的决策",
			zap.String("type", string(d.Type)),
			zap.String("action", string(d.Action)),
		)
		return false
	}
	if strings.TrimSpace(d.Slug) == "" {
		logger.Warn("autolearn: 跳过空 slug 的决策",
			zap.String("type", string(d.Type)),
		)
		return false
	}
	if strings.TrimSpace(d.Content) == "" {
		logger.Warn("autolearn: 跳过空 content 的决策",
			zap.String("type", string(d.Type)),
			zap.String("slug", d.Slug),
		)
		return false
	}
	return true
}

// normalizeMeta 对决策的 title/summary 做互为兜底：任一空则用另一者填充；
// 两者皆空时取 content 前若干字符兜底，保证最终 title/summary 均非空
// （MEMORY.md 索引行需要非空 summary 才有可读性）。
func normalizeMeta(d *ReviewDecision) {
	title := strings.TrimSpace(d.Title)
	summary := strings.TrimSpace(d.Summary)
	if title == "" && summary == "" {
		fallback := truncateForLog(strings.TrimSpace(d.Content), 40)
		d.Title = fallback
		d.Summary = fallback
		return
	}
	if title == "" {
		d.Title = summary
	}
	if summary == "" {
		d.Summary = title
	}
}

// toolNamesSummary 把工具调用名列表渲染为「toolA, toolB, toolC」式摘要。
// 空列表返回「（无）」，让回顾 LLM 明确本轮未调用工具。
func toolNamesSummary(names []string) string {
	if len(names) == 0 {
		return "（无）"
	}
	seen := make(map[string]struct{}, len(names))
	uniq := make([]string, 0, len(names))
	for _, n := range names {
		if n == "" {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		uniq = append(uniq, n)
	}
	if len(uniq) == 0 {
		return "（无）"
	}
	return strings.Join(uniq, ", ")
}

// nonEmptyOrPlaceholder 返回去空白后的文本，为空时返回占位符「（空）」。
// 用于回顾快照渲染，避免空字段让模型误以为信息缺失而脑补。
func nonEmptyOrPlaceholder(s string) string {
	if t := strings.TrimSpace(s); t != "" {
		return s
	}
	return "（空）"
}

// truncateForLog 把文本截断到 maxRunes 个 rune 以内，超长追加省略号。
// 用于日志 snippet，避免把超长 LLM 输出整段打日志。
func truncateForLog(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…"
}
