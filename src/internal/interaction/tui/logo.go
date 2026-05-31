package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Version 为 CodePilot 当前版本号
const Version = "1.0.0"

// owlFaceLines 猫头鹰吉祥物的紧凑版面部图案（3行）。
// 选取最具辨识度的上半部分（耳朵、双眼、喙），保持视觉符号一致性同时大幅缩减垂直空间。
var owlFaceLines = []string{
	"  ,~~.~~.  ",
	" ( o  o ) ",
	"  \\  =  /  ",
}

// LogoView 返回紧凑的 Logo 区域视图。
// 布局：猫头鹰面部（3行）与品牌信息逐行并排 → 分隔线，总计 4 行。
// 相比完整版（banner + 完整猫头鹰 + 字符框 ≈ 14 行），节省约 70% 垂直空间。
func LogoView(width int) string {
	owlStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("214"))

	nameStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("170")).
		Bold(true)

	taglineStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("244"))

	versionStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("214"))

	dividerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("62"))

	// 第1行：猫头鹰耳朵 + 产品名 + 版本号
	line1 := owlStyle.Render(owlFaceLines[0]) +
		nameStyle.Render("CodePilot ") +
		versionStyle.Render("v"+Version)

	// 第2行：猫头鹰眼睛 + 标语
	line2 := owlStyle.Render(owlFaceLines[1]) +
		taglineStyle.Render(" Your AI Coding Agent")

	// 第3行：猫头鹰喙 + 项目地址
	urlStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("61"))

	line3 := owlStyle.Render(owlFaceLines[2]) +
		urlStyle.Render("项目地址：https://github.com/MeiCorl/CodePilot")

	// 底部分隔线
	divider := dividerStyle.Render(strings.Repeat("─", max(width, 20)))

	return line1 + "\n" + line2 + "\n" + line3 + "\n" + divider
}
