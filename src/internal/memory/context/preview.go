// preview.go 生成工具结果落盘后的「头部截断预览 + 存盘路径尾注」。
//
// 服务于第一层「轻量预防」压缩（见 light_compactor.go）：当某个工具结果体积超过阈值
// 被存盘后，内存中的对话历史不再保留完整原文，而是用本文件生成的预览替代——
// 截取原文头部一小段 + 尾注告知 LLM「完整结果已存盘于 <路径>，需要时可用 ReadFile
// 重新读取」。这样既大幅压缩单条消息体积，又不丢失「结果存在 + 如何取回」的关键信息。
//
// 设计要点：
//   - 截断按 rune 进行，绝不截断多字节字符（CJK / emoji），避免产生乱码残片。
//   - 头部预算用 measure 的 token 估算反推字符数（与全链路度量同口径），保证预览体积
//     可控且与阈值判断用同一把尺子。
//   - 原文本身就短于预览预算时，直接返回原文（不加尾注）——等价于「无需预览化」，
//     由 LightCompactor 据此判断是否真的发生替换。
//   - 尾注以固定后缀结尾（previewSuffixMarker），供 isPreview 判定「该 block 是否已是
//     预览态」，从而在多轮重跑时跳过已处理的 block，避免重复 IO。

package context

import "strings"

// previewSuffixMarker 是预览尾注的固定结尾片段，作为 isPreview 判定的锚点。
//
// 选择「，需要时可用 ReadFile 重新读取准确内容）」这段中文+全角括号作为锚点：
// 工具结果原文（代码/日志/命令输出）几乎不可能以此结尾，判定可靠；同时它独立于
// 可变的 filePath，保证 HasSuffix 判定稳定。
const previewSuffixMarker = "，需要时可用 ReadFile 重新读取准确内容）"

// BuildPreview 生成工具结果的「头部预览 + 存盘路径尾注」。
//
// 参数：
//   - content：工具结果原文。
//   - filePath：完整结果已落盘的路径（由 ToolResultStore.Save 返回 / Path 计算）。
//   - previewTokens：预览头部保留的 token 预算（来自配置 CompactionConfig.PreviewTokens）。
//
// 返回值语义：
//   - 原文 token 估算 ≤ previewTokens（原文足够短）：原样返回 content，不截断、不加尾注。
//     此时调用方据此判定「未发生替换」——预览化对短文本无收益。
//   - 原文 token 估算 > previewTokens：返回「头部截断（≈previewTokens）+ 换行 + 尾注」，
//     尾注格式为「（完整结果已存盘：<filePath>，需要时可用 ReadFile 重新读取准确内容）」。
func BuildPreview(content, filePath string, previewTokens int) string {
	// 原文够短则无需预览化——直接返回原文（等价于不替换）。
	if EstimateTextTokens(content) <= previewTokens {
		return content
	}
	head := truncateToTokenBudget(content, previewTokens)
	// 两行换行分隔原文头部与尾注，便于 LLM 区分「结果片段」与「存盘提示」。
	return head + "\n\n（完整结果已存盘：" + filePath + previewSuffixMarker
}

// truncateToTokenBudget 按 rune 截取 content 头部，使其 token 估算 ≈ budget。
//
// 逐 rune 累加 token 贡献（CJK 0.5、非 CJK 0.25，与 EstimateTextTokens 同口径），
// 累计达到 budget 时在该 rune 之后截断（含该 rune），保证：
//  1. 截断点落在 rune 边界，绝不切断多字节字符；
//  2. 截取片段的 token 估算略大于等于 budget（可接受，预览本就是近似值）。
//
// budget <= 0 时返回空串（防御性，正常流程 previewTokens 为正）。
// content 较短、累加完所有 rune 仍未达 budget 时，返回完整 content（兜底全保留）。
func truncateToTokenBudget(content string, budget int) string {
	if budget <= 0 {
		return ""
	}
	runes := []rune(content)
	var tokens float64
	cutIdx := len(runes) // 默认全保留（content 累加完仍未达 budget 的兜底）
	for i, r := range runes {
		if isCJK(r) {
			tokens += 0.5
		} else {
			tokens += 0.25
		}
		if int(tokens) >= budget {
			cutIdx = i + 1
			break
		}
	}
	return string(runes[:cutIdx])
}

// isPreview 判定内容是否已是预览态（以预览尾注的固定后缀结尾）。
//
// 用于 LightCompactor 跳过「已是预览」的 block，避免每轮重跑时对已处理结果重复
// 存盘/替换（存盘子系统的幂等已保证不重复写文件，但 in-place 替换与日志仍应跳过，
// 以保证 changed 语义准确、日志无噪音）。
//
// 判定依据是 previewSuffixMarker 这段固定锚点（见其注释对可靠性的说明）。
func isPreview(content string) bool {
	return strings.HasSuffix(content, previewSuffixMarker)
}
