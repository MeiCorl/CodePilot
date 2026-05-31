package tui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestScreenPosToStringIndex_RuneBoundary 确保返回值始终在 rune 边界上

// TestScreenPosToStringIndex_ASCII 验证纯 ASCII 字符串的列映射
func TestScreenPosToStringIndex_ASCII(t *testing.T) {
	s := "Hello, World!"
	tests := []struct {
		screenCol int
		wantByte  int
	}{
		{0, 0},   // H
		{5, 5},   // ,
		{7, 7},   // W
		{12, 12}, // !
		{20, 13}, // 超出长度，返回字符串末尾
	}
	for _, tt := range tests {
		got := screenPosToStringIndex(s, tt.screenCol)
		if got != tt.wantByte {
			t.Errorf("screenPosToStringIndex(%q, %d) = %d, want %d", s, tt.screenCol, got, tt.wantByte)
		}
	}
}

// TestScreenPosToStringIndex_CJK 验证含中文等多字节字符时的列映射
func TestScreenPosToStringIndex_CJK(t *testing.T) {
	// "你好" = 6 字节, 每字占 2 屏幕列, 共 4 列
	s := "你好"
	tests := []struct {
		screenCol int
		wantByte  int
	}{
		{0, 0}, // 第 0 列 → 第 1 个"你"的起始
		{1, 0}, // 第 1 列仍在"你"范围内 → 返回"你"的起始
		{2, 3}, // 第 2 列 → "好"的起始（跳过"你"的 3 字节）
		{3, 3}, // 第 3 列仍在"好"范围内
		{4, 6}, // 超出 → 字符串末尾
	}
	for _, tt := range tests {
		got := screenPosToStringIndex(s, tt.screenCol)
		if got != tt.wantByte {
			t.Errorf("screenPosToStringIndex(%q, %d) = %d, want %d", s, tt.screenCol, got, tt.wantByte)
		}
		// 确保返回值在 rune 边界上
		if got < len(s) && !utf8.RuneStart(s[got]) {
			t.Errorf("screenPosToStringIndex(%q, %d) = %d 不在 rune 边界上", s, tt.screenCol, got)
		}
	}
}

// TestScreenPosToStringIndex_MixedASCIIAndCJK 验证 ASCII 与中文混合场景
func TestScreenPosToStringIndex_MixedASCIIAndCJK(t *testing.T) {
	// "AB你好CD" → A(1) B(1) 你(2) 好(2) C(1) D(1) = 8 屏幕列
	s := "AB你好CD"
	tests := []struct {
		screenCol int
		wantByte  int
	}{
		{0, 0}, // A
		{1, 1}, // B
		{2, 2}, // 你（起始字节）
		{3, 2}, // 仍在"你"范围内
		{4, 5}, // 好（起始字节）
		{5, 5}, // 仍在"好"范围内
		{6, 8}, // C
		{7, 9}, // D
	}
	for _, tt := range tests {
		got := screenPosToStringIndex(s, tt.screenCol)
		if got != tt.wantByte {
			t.Errorf("screenPosToStringIndex(%q, %d) = %d, want %d", s, tt.screenCol, got, tt.wantByte)
		}
	}
}

// TestScreenPosToStringIndex_ANSI 验证含 ANSI 转义码时跳过不计宽度
func TestScreenPosToStringIndex_ANSI(t *testing.T) {
	// "\x1b[31mABC\x1b[0m" = 红色 ABC，视觉上只有 3 列
	s := "\x1b[31mABC\x1b[0m"
	tests := []struct {
		screenCol int
		wantByte  int
	}{
		{0, 5},  // A（跳过 \x1b[31m 5 字节）
		{1, 6},  // B
		{2, 7},  // C
		{3, 12}, // 超出 → 末尾（跳过 \x1b[0m 4字节）
	}
	for _, tt := range tests {
		got := screenPosToStringIndex(s, tt.screenCol)
		if got != tt.wantByte {
			t.Errorf("screenPosToStringIndex(%q, %d) = %d, want %d", s, tt.screenCol, got, tt.wantByte)
		}
	}
}

// TestScreenPosToStringIndex_RuneBoundary 确保返回值始终在 rune 边界上
func TestScreenPosToStringIndex_RuneBoundary(t *testing.T) {
	s := "Hello你好World世界"
	for col := 0; col <= 20; col++ {
		idx := screenPosToStringIndex(s, col)
		if idx > len(s) {
			t.Fatalf("col=%d: index %d out of bounds (len=%d)", col, idx, len(s))
		}
		if idx < len(s) && !utf8.RuneStart(s[idx]) {
			t.Errorf("col=%d: index %d 不是 rune 边界 (byte 0x%02X)", col, idx, s[idx])
		}
	}
}

// TestSubstringByVisiblePos_CJK 验证按显示宽度截取含中文的文本
func TestSubstringByVisiblePos_CJK(t *testing.T) {
	// "你好世界" = 4 字 × 2 列 = 8 屏幕列
	s := "你好世界"
	tests := []struct {
		startCol int
		endCol   int
		want     string
	}{
		{0, 2, "你"},   // 第 0-1 列 = "你"
		{0, 4, "你好"}, // 第 0-3 列 = "你好"
		{2, 6, "好世"}, // 第 2-5 列 = "好世"
		{4, 8, "世界"}, // 第 4-7 列 = "世界"
	}
	for _, tt := range tests {
		got := substringByVisiblePos(s, tt.startCol, tt.endCol)
		if got != tt.want {
			t.Errorf("substringByVisiblePos(%q, %d, %d) = %q, want %q", s, tt.startCol, tt.endCol, got, tt.want)
		}
	}
}

// TestSubstringByVisiblePos_Mixed 验证 ASCII 与中文混合截取
func TestSubstringByVisiblePos_Mixed(t *testing.T) {
	// "AB你好CD" = 8 屏幕列
	s := "AB你好CD"
	tests := []struct {
		startCol int
		endCol   int
		want     string
	}{
		{0, 2, "AB"},   // 第 0-1 列
		{2, 6, "你好"}, // 第 2-5 列
		{6, 8, "CD"},   // 第 6-7 列
		{1, 4, "B你"},  // 第 1 列(B) + 第 2-3 列(你)
		{0, 8, "AB你好CD"},
	}
	for _, tt := range tests {
		got := substringByVisiblePos(s, tt.startCol, tt.endCol)
		if got != tt.want {
			t.Errorf("substringByVisiblePos(%q, %d, %d) = %q, want %q", s, tt.startCol, tt.endCol, got, tt.want)
		}
	}
}

// TestSgrResetsBackground 验证 SGR 序列背景重置判断
func TestSgrResetsBackground(t *testing.T) {
	tests := []struct {
		csi  string
		want bool
	}{
		{"\x1b[0m", true},      // 全属性重置
		{"\x1b[m", true},       // 无参数，等价于 \x1b[0m
		{"\x1b[49m", true},     // 仅重置背景色
		{"\x1b[0;1m", true},    // 包含 0 参数（重置 + 加粗）
		{"\x1b[38;5;203m", false}, // 仅设置前景色
		{"\x1b[1m", false},     // 仅设置加粗
		{"\x1b[4m", false},     // 仅设置下划线
	}
	for _, tt := range tests {
		got := sgrResetsBackground(tt.csi)
		if got != tt.want {
			t.Errorf("sgrResetsBackground(%q) = %v, want %v", tt.csi, got, tt.want)
		}
	}
}

// TestReapplyBgAfterSgrReset 验证在 SGR 重置码后重新插入背景色
func TestReapplyBgAfterSgrReset(t *testing.T) {
	// 模拟 glamour 渲染的行内容：多段样式之间用 \x1b[0m 重置
	input := "Hello\x1b[0m World\x1b[0m"
	result := reapplyBgAfterSgrReset(input, ansiBgHighlight)

	// 每个 \x1b[0m 之后都应重新插入高亮背景色
	expected := "Hello\x1b[0m\x1b[48;5;67m World\x1b[0m\x1b[48;5;67m"
	if result != expected {
		t.Errorf("reapplyBgAfterSgrReset result mismatch\ngot:  %q\nwant: %q", result, expected)
	}
}

// TestHighlightLineRange_SgrResetMaintainsBg 验证含 SGR 重置码的行高亮不会丢失背景色
func TestHighlightLineRange_SgrResetMaintainsBg(t *testing.T) {
	// 模拟 glamour/chroma 渲染的代码行：关键字和普通文本之间有 SGR 重置
	line := "\x1b[38;5;203mHello\x1b[0m World\x1b[0m"
	result := highlightLineRange(line, 0, 9999)

	// 高亮背景应在每个 SGR 重置码之后被重新插入
	if !strings.Contains(result, "\x1b[0m"+ansiBgHighlight) {
		t.Errorf("expected highlight bg to be re-applied after SGR reset, got: %q", result)
	}

	// 去除所有 ANSI 码后应保留完整原始可见文本
	stripped := stripANSI(result)
	if stripped != "Hello World" {
		t.Errorf("stripped result = %q, want %q", stripped, "Hello World")
	}
}

// TestHighlightLineRange_NoCorruption 验证高亮不会破坏 UTF-8 字符
func TestHighlightLineRange_NoCorruption(t *testing.T) {
	line := "AB你好CD"
	result := highlightLineRange(line, 2, 6)

	// 结果应包含高亮码但不破坏任何字符
	if !strings.Contains(result, ansiBgHighlight) {
		t.Error("高亮码未插入")
	}
	// 去除 ANSI 码后应等于原始文本（证明没有字符被截断）
	stripped := stripANSI(result)
	if stripped != line {
		t.Errorf("高亮后去除 ANSI 码得到 %q, want %q (字符被破坏)", stripped, line)
	}
	// 确保结果是有效 UTF-8
	for _, r := range result {
		if r == utf8.RuneError {
			t.Errorf("高亮后产生无效 UTF-8 字符: %q", result)
		}
	}
}
