package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// contextBarWidth 上下文窗口进度条宽度（字符数）
const contextBarWidth = 12

// StatusBarView 返回底部状态栏视图。
// 展示当前模型名称、上下文窗口剩余额度（可视化进度条）和运行状态。
// 进度条颜色根据使用率动态变化：绿色(<50%) → 橙色(50-80%) → 红色(>80%)。
func StatusBarView(model string, usedTokens int, totalTokens int, isStreaming bool, width int) string {
	modelStr := fmt.Sprintf(" 模型: %s", model)

	// 计算上下文窗口使用比例
	var usedPct float64
	if totalTokens > 0 {
		usedPct = float64(usedTokens) / float64(totalTokens) * 100
	}
	if usedPct > 100 {
		usedPct = 100
	}
	remainingPct := 100 - usedPct

	// 进度条：█ 表示可用额度，░ 表示已用额度
	usedBarWidth := int(float64(contextBarWidth) * usedPct / 100)
	remainingBarWidth := contextBarWidth - usedBarWidth

	// 根据使用率选择进度条颜色（针对剩余额度部分）
	var barColor string
	switch {
	case usedPct < 50:
		barColor = "82" // 亮绿色 - 充裕
	case usedPct < 80:
		barColor = "214" // 橙色 - 偏紧
	default:
		barColor = "196" // 红色 - 紧张
	}

	// 渲染进度条：可用部分用彩色 █，已用部分用暗色 ░
	barStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(barColor))
	usedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("93"))
	bar := barStyle.Render(strings.Repeat("█", remainingBarWidth)) +
		usedStyle.Render(strings.Repeat("░", usedBarWidth))

	contextStr := fmt.Sprintf(" 上下文: %s %.0f%%", bar, remainingPct)

	// 运行状态
	var statusStr string
	if isStreaming {
		statusStr = " ⣻ 思考中..."
	} else {
		statusStr = " 就绪"
	}

	barBgStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("62")).
		Foreground(lipgloss.Color("230")).
		Padding(0, 0)

	content := modelStr + " │" + contextStr + " │" + statusStr + " "
	rendered := barBgStyle.Render(content)

	// 截断超宽内容，确保状态栏不超过给定宽度（避免写满终端最后一列触发 pending wrap）
	if lipgloss.Width(rendered) > width {
		rendered = lipgloss.NewStyle().MaxWidth(width).Render(rendered)
	}

	// 填充剩余宽度，确保状态栏铺满终端
	renderedWidth := lipgloss.Width(rendered)
	if renderedWidth < width {
		padStyle := lipgloss.NewStyle().
			Background(lipgloss.Color("62")).
			Width(width - renderedWidth).
			Render("")
		rendered = lipgloss.JoinHorizontal(lipgloss.Bottom, rendered, padStyle)
	}

	return rendered
}
