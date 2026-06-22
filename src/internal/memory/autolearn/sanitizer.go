// sanitizer.go 实现记忆正文的敏感信息脱敏，作为「prompt 约束」之外的【第二道兜底】。
//
// [Why 两道防线] spec「高安全」要求记忆文件不得落盘密钥/密码/token 等敏感凭证：
//  1. 第一道是 prompt 约束——reviewSystemPrompt 明确禁止记录敏感凭证（模型自觉跳过）；
//  2. 第二道是本文件的正则脱敏——即便模型疏忽把敏感信息写进 content，落盘前 Sanitize
//     再扫一遍，命中常见凭证模式即替换为 [REDACTED]，杜绝「凭证进记忆文件 → 跨会话
//     泄露到后续 LLM 调用」的风险。
//
// [替换模板差异（关键设计）] 三类敏感模式的替换策略不同，故每个 pattern 配独立 replace 模板：
//   - 高熵凭证（sk-/AKIA/xox/gh PAT）：整体脱敏，replace 为纯占位符（模板不含 $ 引用，
//     整个匹配被丢弃替换为 [REDACTED]，凭证不留任何痕迹）；
//   - Bearer token：保留 "Bearer " 前缀（组1），仅 token 主体脱敏——前缀是语法标记非秘密，
//     保留便于人眼识别「这里曾有个 token」；
//   - 键值对口令：保留「键名+分隔符」（组1），仅值脱敏——键名（password/api_key 等）非秘密，
//     保留便于人眼定位。
// 若三类共用同一模板会导致高熵凭证串被原样保留（${1} 即凭证本身），脱敏失效。
//
// [设计取舍] 正则匹配是【保守的、低误报优先】策略：只匹配高置信度的凭证特征串，避免把
// 普通长字符串误判为密钥。首版以正则兜底为主，后续若需更强能力可替换为基于上下文的检测
// （接口已收敛在 Sanitize/HasSensitive 两个函数，便于替换）。

package autolearn

import "regexp"

// redactedPlaceholder 是敏感信息被替换后的占位符。
const redactedPlaceholder = "[REDACTED]"

// redactPattern 把「敏感凭证正则」与「对应替换模板」绑定。
//
// replace 为 regexp.ReplaceAllString 的模板字符串：
//   - 含 ${1}：保留组1（前缀/键名），仅匹配整体被替换为「组1 + 占位符」；
//   - 不含 $ 引用：整个匹配被替换为占位符字面量。
type redactPattern struct {
	// re 预编译的敏感凭证匹配正则。
	re *regexp.Regexp
	// replace 替换模板（含 ${1} 时保留组1，否则整体替换）。
	replace string
}

// sensitivePatterns 命中常见敏感凭证的模式集合（编译期预编译，避免每次脱敏重复编译）。
//
// 覆盖类别（均带 (?i) 大小写不敏感）：
//   - 带明确前缀的高熵凭证：OpenAI sk-、AWS AKIA、Slack xox-、GitHub ghp_/gho_/ghu_/ghs_/ghr_ PAT；
//   - Bearer token：Authorization: Bearer xxx 形态（保留 "Bearer " 前缀）；
//   - 显式赋值的口令：password= / passwd= / pwd= / secret= / token= / api_key= 等键值对，
//     保留键名+分隔符，仅值脱敏。值至少 4 个非空白非引号字符，过滤掉无值占位。
//
// [Why 不匹配 JWT 全串] JWT 等长 base64 串与普通 base64 编码内容难以区分，误报率高，
// 首版不纳入；后续若有需求可在 sensitivePatterns 追加更精确的 JWT 形态。
var sensitivePatterns = []redactPattern{
	{
		// 高熵凭证：整体脱敏，replace 为纯占位符（模板不含 $ 引用，凭证不留痕迹）。
		re: regexp.MustCompile(
			`(?i)(?:sk-[A-Za-z0-9]{20,}|AKIA[0-9A-Z]{16}|xox[baprs]-[A-Za-z0-9-]{10,}|gh[pousr]_[A-Za-z0-9]{36})`,
		),
		replace: redactedPlaceholder,
	},
	{
		// Bearer token：组1="Bearer " 前缀，保留前缀仅 token 主体脱敏。
		re:      regexp.MustCompile(`(?i)(Bearer\s+)[A-Za-z0-9\._\-=]{20,}`),
		replace: "${1}" + redactedPlaceholder,
	},
	{
		// 键值赋值口令：组1=键名+分隔符（含可能的引号），保留组1仅值脱敏。
		re: regexp.MustCompile(
			`(?i)((?:password|passwd|pwd|secret|api[_-]?key|access[_-]?key|token|client[_-]?secret)["']?\s*[:=]\s*["']?)[^\s"']{4,}`,
		),
		replace: "${1}" + redactedPlaceholder,
	},
}

// Sanitize 把文本中命中的敏感凭证片段替换为 [REDACTED]（Bearer / 键值对仅替换值部分，前缀与键名保留）。
//
// [应用时机] reviewer（Task 5）在 store.WriteMemory 落盘前，对每条决策的 Content
// 调用本函数兜底脱敏。对未命中任何敏感模式的文本，原样返回（零误报风险）。
func Sanitize(text string) string {
	for _, p := range sensitivePatterns {
		text = p.re.ReplaceAllString(text, p.replace)
	}
	return text
}

// HasSensitive 判断文本是否含至少一个敏感凭证片段，供测试与上层断言使用。
// 命中返回 true，否则 false。
func HasSensitive(text string) bool {
	for _, p := range sensitivePatterns {
		if p.re.MatchString(text) {
			return true
		}
	}
	return false
}
