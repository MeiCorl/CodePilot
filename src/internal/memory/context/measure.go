// measure.go 提供上下文管理所需的细粒度 token 度量能力。
//
// Step 7 之前，token 估算逻辑（estimateTextTokens / isCJK）散落在
// conversation 包的 manager.go 内，仅服务于状态栏展示与溢出保护。Step 7 引入
// 两层压缩策略（轻量预防 + 重量兜底）后，压缩器、预览生成、协调器等组件都
// 需要对「文本 / 单个内容块 / 单条消息 / 消息列表」做统一、一致的 token 估算，
// 故将估算逻辑下沉到记忆层 context 包集中维护：既消除重复代码，又保证全链路
// 用同一把尺子度量——避免因估算口径不一致导致压缩阈值判断抖动（同一历史在
// 不同地方量出不同 token，会让「是否触发压缩」的决策不稳定、prompt cache 命中率波动）。
//
// 估算口径刻意保持与 Step 1~6 完全一致（CJK 2 字符/token、非 CJK 4 字符/token、
// 每条消息 15 token 结构开销），由 conversation 包既有测试守护回归，确保下沉
// 是「纯粹的位置迁移」而非行为变更。

package context

import "github.com/MeiCorl/CodePilot/src/llm"

// MessageOverhead 为单条消息的固定结构开销（token 数）。
//
// 来源：实测 Anthropic / OpenAI 协议下，每条消息除正文外还要携带 role 标签、
// JSON 结构、消息边界标记等，约占 10~15 token。取上限 15：上下文管理宁可
// 高估（更早触发压缩、更早响应溢出），也不要低估（低估会导致实际已逼近上限
// 却误判仍有余量，最终撞墙报错）。
const MessageOverhead = 15

// EstimateTextTokens 对纯文本做粗略 token 估算。
//
// 口径（与原 conversation 包 estimateTextTokens 完全一致，逐字迁移）：
//   - CJK 字符：约 2 字符 = 1 token（中日韩常用字在主流 BPE 词表里多为单 token）
//   - 非 CJK 字符：约 4 字符 = 1 token（英文单词、标点符号的常见比例）
//
// 不追求精确计费（精确 tokenizer 如 tiktoken 留待后续步骤），只要求：
//  1. 数量级正确，足以支撑压缩阈值判断与状态栏展示；
//  2. 确定性——同一文本多次估算必须相等（压缩前后的 token 对比、缓存稳定性
//     判断都依赖这一点；随机或抖动的估算会让「是否触发压缩」每次结果不同）。
//
// 空串返回 0。
func EstimateTextTokens(text string) int {
	cjkCount := 0
	nonCJKCount := 0
	for _, r := range text {
		if isCJK(r) {
			cjkCount++
		} else {
			nonCJKCount++
		}
	}
	// CJK: 约 2 字符 = 1 token
	// 非 CJK: 约 4 字符 = 1 token
	cjkTokens := cjkCount / 2
	nonCJKTokens := nonCJKCount / 4
	// 有字符但整除为 0 时兜底为 1（如单个 CJK 字符应算 1 token 而非 0），
	// 避免「明明有内容却被估成 0」导致压缩判断误判为「无需处理」。
	if cjkCount > 0 && cjkTokens == 0 {
		cjkTokens = 1
	}
	if nonCJKCount > 0 && nonCJKTokens == 0 {
		nonCJKTokens = 1
	}
	return cjkTokens + nonCJKTokens
}

// EstimateBlockTokens 估算单个 ContentBlock 的 token 数。
//
// 统一基于 block.ToText() 估算，与原 conversation 包口径一致：
//   - TextBlock：取正文文本；
//   - ToolResultBlock：取 Content（工具结果正文，第一层轻量压缩的主要体积来源）；
//   - ToolUseBlock：取 name+id 摘要 —— 其 ToText() 返回形如
//     "tool_use(<name>, id=<id>)"，本就不含完整 input JSON；完整 input 在请求
//     结构里由 Provider 单独序列化计入，此处不重复计，否则会对同一个 input 在
//     block 维度与请求体维度双重计数。
//
// nil block 视为 0（防御性，理论上 Content 切片不会出现 nil 元素）。
func EstimateBlockTokens(block llm.ContentBlock) int {
	if block == nil {
		return 0
	}
	return EstimateTextTokens(block.ToText())
}

// EstimateMessageTokens 估算单条消息的 token 数。
//
// = MessageOverhead（每条消息固定结构开销）+ 该消息所有 ContentBlock 的 token 累加。
// 即使消息无任何 block（空 Content），仍计入 MessageOverhead，因为 role 标签等
// 结构在协议层依然占位。
func EstimateMessageTokens(msg llm.Message) int {
	total := MessageOverhead
	for i := range msg.Content {
		total += EstimateBlockTokens(msg.Content[i])
	}
	return total
}

// EstimateMessagesTokens 估算消息列表的累计 token 数。
//
// 对每条消息调用 EstimateMessageTokens 累加，用于：
//   - 状态栏 token 展示（ConversationManager.TokenEstimate）；
//   - 第二层摘要压缩的「尾部近期原文保留量」切分（splitByTailTokens）；
//   - 协调器压缩前后 token 对比（CompactionResult）。
func EstimateMessagesTokens(msgs []llm.Message) int {
	total := 0
	for i := range msgs {
		total += EstimateMessageTokens(msgs[i])
	}
	return total
}

// isCJK 判断一个 rune 是否为 CJK（中日韩）字符。
//
// 覆盖常用 CJK 区段：统一表意文字（含扩展 A、兼容）、平假名/片假名、全角符号、
// CJK 标点。这些区段的字符在主流 BPE 词表里多按「单字符≈单 token」处理，故
// 采用更激进的 2 字符/token 估算（比英文的 4 字符/token 更密集）。
// 未覆盖更冷门的扩展 B~G 区段（罕见字），对估算精度影响可忽略。
func isCJK(r rune) bool {
	// CJK Unified Ideographs（常用汉字）
	if r >= 0x4E00 && r <= 0x9FFF {
		return true
	}
	// CJK Unified Ideographs Extension A
	if r >= 0x3400 && r <= 0x4DBF {
		return true
	}
	// CJK Compatibility Ideographs
	if r >= 0xF900 && r <= 0xFAFF {
		return true
	}
	// Hiragana & Katakana（日文假名）
	if r >= 0x3040 && r <= 0x30FF {
		return true
	}
	// Fullwidth Forms（全角符号）
	if r >= 0xFF00 && r <= 0xFFEF {
		return true
	}
	// CJK punctuation and symbols（CJK 标点）
	if r >= 0x3000 && r <= 0x303F {
		return true
	}
	return false
}
