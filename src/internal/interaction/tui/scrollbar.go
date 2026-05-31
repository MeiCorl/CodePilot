package tui

import (
	"github.com/charmbracelet/lipgloss"
)

const (
	// scrollbarTrackChar 滚动条轨道字符
	scrollbarTrackChar = "│"
	// scrollbarThumbChar 滚动条滑块字符
	scrollbarThumbChar = "█"
)

// renderScrollbar 渲染垂直滚动条视图。
// totalLines: 内容总行数，visibleLines: 可见行数，
// yOffset: 当前滚动偏移，height: 滚动条高度（等于 viewport 高度）。
func renderScrollbar(totalLines, visibleLines, yOffset, height int) string {
	if totalLines <= visibleLines || height <= 0 {
		return ""
	}

	// 滑块高度按可见比例计算，最小 1 行
	ratio := float64(visibleLines) / float64(totalLines)
	thumbHeight := max(1, int(float64(height)*ratio))

	// 滑块位置按滚动比例计算
	maxScroll := totalLines - visibleLines
	if maxScroll <= 0 {
		return ""
	}
	scrollRatio := float64(yOffset) / float64(maxScroll)
	thumbY := int(scrollRatio * float64(height-thumbHeight))

	thumbStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("62"))
	trackStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("238"))

	var lines []string
	for i := 0; i < height; i++ {
		if i >= thumbY && i < thumbY+thumbHeight {
			lines = append(lines, thumbStyle.Render(scrollbarThumbChar))
		} else {
			lines = append(lines, trackStyle.Render(scrollbarTrackChar))
		}
	}

	return lipgloss.NewStyle().MarginLeft(1).Render(
		lipgloss.JoinVertical(lipgloss.Left, lines...),
	)
}
