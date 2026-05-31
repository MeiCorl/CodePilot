package tui

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// ansiRegexp 匹配 ANSI 转义序列，用于从终端渲染内容中提取纯文本
var ansiRegexp = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// stripANSI 去除字符串中的 ANSI 转义序列，返回纯文本
func stripANSI(s string) string {
	return ansiRegexp.ReplaceAllString(s, "")
}

// copyTextToClipboard 将文本写入系统剪贴板
func copyTextToClipboard(text string) error {
	return clipboard.WriteAll(text)
}

// readClipboardFromSystem 从系统剪贴板读取文本
func readClipboardFromSystem() (string, error) {
	return clipboard.ReadAll()
}

// pasteIntoTextarea 将文本插入到 textarea 的当前光标位置。
// 利用 textarea 内置的 InsertString 方法在光标处插入文本。
func pasteIntoTextarea(ta *textarea.Model, text string) {
	if text == "" {
		return
	}
	ta.InsertString(text)
}

// extractSelectedText 从内容中按行范围提取选中的文本并去除 ANSI 转义码。
// contentLines 为已渲染内容的按行切片，startLine/endLine 为可见区域行索引（含）。
func extractSelectedText(contentLines []string, startLine, endLine int) string {
	lineCount := len(contentLines)
	if lineCount == 0 {
		return ""
	}

	// 先确保 start <= end
	if startLine > endLine {
		startLine, endLine = endLine, startLine
	}

	// 将起止行限制在有效范围 [0, lineCount-1] 内
	if startLine < 0 {
		startLine = 0
	}
	if startLine >= lineCount {
		return ""
	}
	if endLine < 0 {
		return ""
	}
	if endLine >= lineCount {
		endLine = lineCount - 1
	}

	var selected []string
	for i := startLine; i <= endLine; i++ {
		selected = append(selected, stripANSI(contentLines[i]))
	}
	return strings.Join(selected, "\n")
}

// ANSI 背景高亮控制码，用于渲染文本选择区域的视觉反馈
const (
	ansiBgHighlight = "\x1b[48;5;67m" // 钢蓝色背景，在暗色终端中有足够对比度凸显选中区域
	ansiBgReset     = "\x1b[49m"       // 仅重置背景色，不影响前景色
)

// screenPosToStringIndex 将屏幕可见列位置映射到含 ANSI 转义码的字符串字节索引。
// 按 rune 遍历：跳过 ANSI CSI 序列不计入宽度，每个 rune 按 runewidth 计算显示列数。
// 返回值始终在 rune 边界上，不会截断 UTF-8 多字节字符。
func screenPosToStringIndex(s string, screenCol int) int {
	visible := 0
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' {
			// 跳过 ANSI CSI 序列: ESC [ ... <final byte (A-Z|a-z)>
			i++
			if i < len(s) && s[i] == '[' {
				i++
				for i < len(s) {
					if (s[i] >= 'A' && s[i] <= 'Z') || (s[i] >= 'a' && s[i] <= 'z') {
						i++
						break
					}
					i++
				}
			} else if i < len(s) {
				i++
			}
			continue
		}
		// 按 rune 解码，确保 i 始终在 rune 边界上
		r, size := utf8.DecodeRuneInString(s[i:])
		w := runewidth.RuneWidth(r)
		// screenCol 落在当前 rune 的显示范围内 → 返回该 rune 的起始字节索引
		if visible+w > screenCol {
			return i
		}
		visible += w
		i += size
	}
	return i
}

// highlightLineRange 对单行渲染内容中的指定屏幕列范围应用高亮背景。
// line 是含 ANSI 转义码的渲染行，startCol/endCol 是屏幕列位置（可见字符索引）。
func highlightLineRange(line string, startCol, endCol int) string {
	startIdx := screenPosToStringIndex(line, startCol)
	endIdx := screenPosToStringIndex(line, endCol)
	if startIdx >= endIdx {
		return line
	}

	// 对高亮范围内的子串，在每个 SGR 重置码之后重新插入高亮背景色。
	// 原因：glamour/chroma 渲染的内容中经常出现 \x1b[0m（全属性重置），
	// 它会清除我们插入的高亮背景色，导致后续文本失去高亮。
	highlighted := reapplyBgAfterSgrReset(line[startIdx:endIdx], ansiBgHighlight)

	return line[:startIdx] + ansiBgHighlight + highlighted + ansiBgReset + line[endIdx:]
}

// reapplyBgAfterSgrReset 在字符串中的每个会重置背景色的 SGR 序列之后重新插入背景色。
// glamour/chroma 等渲染器输出的内容中频繁使用 \x1b[0m（SGR 全属性重置），
// 该序列会清除背景色。本函数在检测到这类重置后立即重新插入指定背景色，
// 确保高亮效果在整个选中范围内持续有效。
func reapplyBgAfterSgrReset(s string, bgCode string) string {
	var result strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			// 定位 CSI 序列的结束位置（以字母字符结尾）
			j := i + 2
			for j < len(s) {
				if (s[j] >= 'A' && s[j] <= 'Z') || (s[j] >= 'a' && s[j] <= 'z') {
					j++
					break
				}
				j++
			}
			csi := s[i:j]
			result.WriteString(csi)
			// 仅处理 SGR 序列（以 'm' 结尾）中会重置背景色的情况
			if len(csi) > 0 && csi[len(csi)-1] == 'm' && sgrResetsBackground(csi) {
				result.WriteString(bgCode)
			}
			i = j
		} else {
			result.WriteByte(s[i])
			i++
		}
	}
	return result.String()
}

// sgrResetsBackground 判断 SGR（Select Graphic Rendition）序列是否会重置背景色。
// 当参数中包含 0（全属性重置）、空参数（等价于 0）或 49（仅重置背景色）时，背景色会被清除。
func sgrResetsBackground(csi string) bool {
	if len(csi) < 3 {
		return false
	}
	// 提取 '[' 和 'm' 之间的参数部分
	params := csi[2 : len(csi)-1]
	if params == "" {
		return true // \x1b[m 等价于 \x1b[0m，执行全属性重置
	}
	for _, p := range strings.Split(params, ";") {
		if p == "0" || p == "" || p == "49" {
			return true
		}
	}
	return false
}

// renderSelectionHighlight 对 viewport 可见内容应用选择区域的高亮背景。
// vpView 是 viewport 渲染后的完整字符串，vpTopY 是 viewport 在屏幕上的起始行号，
// selStartY/X 和 selEndY/X 是屏幕坐标下的选择起止位置。
func renderSelectionHighlight(vpView string, vpTopY, selStartY, selStartX, selEndY, selEndX int) string {
	// 将选择坐标转为 viewport 内的相对行号
	relStartY := selStartY - vpTopY
	relEndY := selEndY - vpTopY

	// 确保起止顺序正确（归一化，使 start <= end）
	if relStartY > relEndY || (relStartY == relEndY && selStartX > selEndX) {
		relStartY, relEndY = relEndY, relStartY
		selStartX, selEndX = selEndX, selStartX
	}

	lines := strings.Split(vpView, "\n")
	lineCount := len(lines)

	// 将起止行限制在有效范围
	if relStartY < 0 {
		relStartY = 0
		selStartX = 0
	}
	if relEndY >= lineCount {
		relEndY = lineCount - 1
		selEndX = 9999
	}
	if relStartY > relEndY {
		return vpView
	}

	for i := relStartY; i <= relEndY; i++ {
		if i < 0 || i >= lineCount {
			continue
		}

		var lineStart, lineEnd int
		switch {
		case i == relStartY && i == relEndY:
			// 同一行：高亮 [startX, endX)
			lineStart = selStartX
			lineEnd = selEndX
		case i == relStartY:
			// 首行：从 startX 到行尾
			lineStart = selStartX
			lineEnd = 9999
		case i == relEndY:
			// 末行：从行首到 endX
			lineStart = 0
			lineEnd = selEndX
		default:
			// 中间行：整行高亮
			lineStart = 0
			lineEnd = 9999
		}

		lines[i] = highlightLineRange(lines[i], lineStart, lineEnd)
	}

	return strings.Join(lines, "\n")
}

// extractSelectedTextRange 从内容中按行列范围和列范围提取选中的文本并去除 ANSI 转义码。
// contentLines 为已渲染内容的按行切片，startLine/startCol 和 endLine/endCol 为可见区域的行列索引。
func extractSelectedTextRange(contentLines []string, startLine, startCol, endLine, endCol int) string {
	lineCount := len(contentLines)
	if lineCount == 0 {
		return ""
	}

	// 确保起止顺序正确
	if startLine > endLine || (startLine == endLine && startCol > endCol) {
		startLine, endLine = endLine, startLine
		startCol, endCol = endCol, startCol
	}

	// 将起止行限制在有效范围
	if startLine < 0 {
		startLine = 0
		startCol = 0
	}
	if startLine >= lineCount {
		return ""
	}
	if endLine < 0 {
		return ""
	}
	if endLine >= lineCount {
		endLine = lineCount - 1
		endCol = 9999
	}

	var selected []string
	for i := startLine; i <= endLine; i++ {
		plainLine := stripANSI(contentLines[i])

		var lineText string
		switch {
		case i == startLine && i == endLine:
			lineText = substringByVisiblePos(plainLine, startCol, endCol)
		case i == startLine:
			lineText = substringByVisiblePos(plainLine, startCol, len(plainLine))
		case i == endLine:
			lineText = substringByVisiblePos(plainLine, 0, endCol)
		default:
			lineText = plainLine
		}
		selected = append(selected, lineText)
	}
	return strings.Join(selected, "\n")
}

// substringByVisiblePos 按屏幕可见列位置截取纯文本子串。
// 入参 s 已去除 ANSI 码但仍可能含 UTF-8 宽字符，需按显示宽度定位 rune 边界再截取。
func substringByVisiblePos(s string, startCol, endCol int) string {
	visible := 0
	resultStart := -1
	resultEnd := len(s)

	for i, r := range s {
		w := runewidth.RuneWidth(r)
		// startCol 落在当前 rune 的显示范围内 → 标记起始字节位置
		if resultStart == -1 && visible+w > startCol {
			resultStart = i
		}
		visible += w
		// 累计显示宽度达到 endCol → 标记结束字节位置（当前 rune 末尾）
		if visible >= endCol {
			resultEnd = i + utf8.RuneLen(r)
			break
		}
	}

	if resultStart == -1 || resultStart >= resultEnd {
		return ""
	}
	return s[resultStart:resultEnd]
}

// renderCopyNotification 渲染复制成功的浮动提示，返回带样式的提示字符串。
// notification 为提示文本，width 为显示区域宽度。
func renderCopyNotification(notification string, width int) string {
	if notification == "" {
		return ""
	}

	style := lipgloss.NewStyle().
		Background(lipgloss.Color("34")).
		Foreground(lipgloss.Color("230")).
		Padding(0, 2)

	// 计算提示位置（右下角）
	notified := style.Render(" " + notification + " ")
	padding := width - lipgloss.Width(notified) - 2
	if padding < 0 {
		padding = 0
	}
	return lipgloss.NewStyle().
		MarginLeft(padding).
		Render(notified)
}

// formatCopyNotificationTime 格式化复制通知的显示时间
const copyNotificationDuration = 2 * time.Second
