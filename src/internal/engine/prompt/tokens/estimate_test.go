package tokens

import "testing"

// TestEstimate_Empty 验证空串返回 0。
func TestEstimate_Empty(t *testing.T) {
	if got := Estimate(""); got != 0 {
		t.Errorf("空串应返回 0，得到 %d", got)
	}
}

// TestEstimate_AsciiShort 验证 1-2 字符的 ASCII 文本返回 1（向上取整）。
func TestEstimate_AsciiShort(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"a", 1},
		{"ab", 1}, // (2+1)/2 = 1
		{"abc", 2},
		{"abcd", 2},
	}
	for _, c := range cases {
		if got := Estimate(c.in); got != c.want {
			t.Errorf("Estimate(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestEstimate_Range1000Chars 验证 1000 字符输入返回 400~600 之间的值。
// 对应 checklist.md 中「1000 字符 → 400~600」的验收项。
func TestEstimate_Range1000Chars(t *testing.T) {
	text := ""
	for i := 0; i < 1000; i++ {
		text += "a"
	}
	got := Estimate(text)
	if got < 400 || got > 600 {
		t.Errorf("1000 字符 ASCII 应得 400~600，得到 %d", got)
	}
}

// TestEstimate_ChineseText 验证中文文本不被高估（rune 而非 byte 计数）。
// 1000 个中文字符的 byte 数是 3000，但 rune 数是 1000，应得 ~500 token。
func TestEstimate_ChineseText(t *testing.T) {
	text := ""
	for i := 0; i < 1000; i++ {
		text += "中"
	}
	got := Estimate(text)
	if got < 400 || got > 600 {
		t.Errorf("1000 个中文字符应得 400~600（rune 计数），得到 %d", got)
	}
	// 关键：如果是 len(string) 而不是 rune 数，会得到 (3000+1)/2 = 1500，远超 600
	if got > 700 {
		t.Errorf("中文文本应基于 rune 计数而非 byte，得到 %d 表明走了 byte 路径", got)
	}
}

// TestEstimate_MixedContent 验证中英混合文本的估算在合理范围。
func TestEstimate_MixedContent(t *testing.T) {
	// 50 字符 ASCII + 50 个汉字 = 100 rune
	text := "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz中文中文中文中文中文中文中文中文中文中文中文中文中文中文中文中文中文中文中文"
	got := Estimate(text)
	// 100 rune → (100+1)/2 = 50
	if got < 45 || got > 55 {
		t.Errorf("混合 100 rune 应得 ~50，得到 %d", got)
	}
}
