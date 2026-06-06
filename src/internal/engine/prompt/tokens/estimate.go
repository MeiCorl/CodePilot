// Package tokens 提供 System Prompt 相关的 token 估算能力。
//
// 当前仅暴露一个轻量级估算函数，用于 WebUI 状态栏展示与 Builder 内的
// 累计统计；不追求与 tiktoken 等精确 tokenizer 完全一致——token 估算
// 用于「让用户感知到当前 SP 有多长」，±20% 误差在可接受范围。
//
// 后续 Step 7（上下文管理）会引入更精确的、按模型分桶的 tokenizer；
// 届时 Estimate 应作为兜底存在，无法加载精确模型时降级到本函数。
package tokens

// Estimate 用 rune 数除以 2 估算 token 数。
//
// 设计依据：
//   - 英文文本：1 token ≈ 4 字符 → 估算系数 0.25
//   - 中文文本：1 token ≈ 1.5 字符（cl100k_base 实测）→ 估算系数 0.67
//   - 代码片段：1 token ≈ 3 字符（关键字短、操作符多）→ 估算系数 0.33
//   - 三者折中：取 rune/2（系数 0.5），对英文略偏高、对中文略偏低、对代码接近
//
// 这一估算的目的是「给出可读的数量级」而非「精确计费」，因此
// 1k 字符输入下应返回 400~600 之间的值，符合 System Prompt
// 状态栏展示的精度要求。
//
// 空串返回 0，不会因为空内容误报 1。
func Estimate(text string) int {
	if text == "" {
		return 0
	}
	// 用 []rune 转换而非 len(string) 是为了正确处理多字节字符（中文/emoji）
	// 避免高估中文文本的 token 数。
	runeCount := 0
	for range text {
		runeCount++
	}
	// +1 是为了向上取整（rune=1 时返回 1 而非 0），符合「至少有 1 token」直觉
	return (runeCount + 1) / 2
}
