package security

import (
	"path/filepath"
)

// BuildPathPattern 把 LLM 传入的目标路径转成"目录级 glob Pattern"，
// 供"永久允许"或"本会话允许"时写入 setting.json 或 sessionRules。
//
// 规则（按用户决策"目录级粒度"）：
//   - 若是绝对路径或可基于 workdir 拼接成绝对路径 → 取父目录 + "/*"
//   - 路径解析失败或为空 → 退回 "工具级豁免" "*"
//
// 示例：
//   - target="/tmp/notes.md", workdir="/home/user/proj" → "/tmp/*"
//   - target="/tmp/sub" (目录), workdir=...            → "/tmp/*"
//   - target="relative/foo.md", workdir="/home/user"   → "/home/user/*"
//   - target="", workdir="..."                          → "*"
//
// 本函数是路径级授权规则与工具级豁免的桥梁：把"目标路径"语义转为
// "父目录 + /*" 形式，使后续 MatchPathRule 走 filepath.Match 命中同目录
// 的所有文件，符合"最小授权"原则的"放行目录而非具体文件"变体。
func BuildPathPattern(targetPath, workdir string) string {
	if targetPath == "" {
		return "*"
	}
	abs := normalizeForRule(targetPath, workdir)
	if abs == "" {
		return "*"
	}
	dir := filepath.Dir(abs)
	return filepath.Join(dir, "*")
}
