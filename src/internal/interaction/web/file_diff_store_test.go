package web

import (
	"strings"
	"sync"
	"testing"

	"github.com/MeiCorl/CodePilot/src/internal/tool"
)

// TestFileDiffStore_SetGet 验证基本 set/get 路径。
func TestFileDiffStore_SetGet(t *testing.T) {
	s := NewFileDiffStore()
	entry := tool.FileDiffEntry{
		FilePath: "/tmp/foo.go",
		Before:   "package x\n",
		After:    "package x\nconst Y = 1\n",
	}
	if !s.Set("id-1", entry) {
		t.Fatalf("Set 应成功")
	}
	got, ok := s.Get("id-1")
	if !ok {
		t.Fatalf("Get 应找到")
	}
	if got.FilePath != entry.FilePath || got.Before != entry.Before || got.After != entry.After {
		t.Fatalf("内容不一致: got=%+v want=%+v", got, entry)
	}
	if got.Language != "go" {
		t.Fatalf("Language 期望 go, 实际 %q", got.Language)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatalf("UpdatedAt 应被自动填充")
	}
}

// TestFileDiffStore_SetOverwrite 验证同名 toolUseID 覆盖语义。
func TestFileDiffStore_SetOverwrite(t *testing.T) {
	s := NewFileDiffStore()
	s.Set("id-1", tool.FileDiffEntry{FilePath: "/a", After: "v1"})
	s.Set("id-1", tool.FileDiffEntry{FilePath: "/b", After: "v2"})
	got, _ := s.Get("id-1")
	if got.FilePath != "/b" || got.After != "v2" {
		t.Fatalf("覆盖语义失败: %+v", got)
	}
	if s.Len() != 1 {
		t.Fatalf("Len 期望 1, 实际 %d", s.Len())
	}
}

// TestFileDiffStore_TooLarge 验证超过 2MB 的 diff 被拒绝。
func TestFileDiffStore_TooLarge(t *testing.T) {
	s := NewFileDiffStore()
	big := strings.Repeat("a", FileDiffMaxBytes+1)
	if s.Set("id-big", tool.FileDiffEntry{Before: big, After: "ok"}) {
		t.Fatalf("超过容量的 Set 应返回 false")
	}
	if _, ok := s.Get("id-big"); ok {
		t.Fatalf("超容量数据不应被 Get 到")
	}
	if s.Len() != 0 {
		t.Fatalf("超容量后 Len 应为 0, 实际 %d", s.Len())
	}
}

// TestFileDiffStore_EmptyToolUseID 验证空主键被拒绝。
func TestFileDiffStore_EmptyToolUseID(t *testing.T) {
	s := NewFileDiffStore()
	if s.Set("", tool.FileDiffEntry{After: "x"}) {
		t.Fatalf("空 toolUseID 应被拒绝")
	}
	if s.Len() != 0 {
		t.Fatalf("空主键后 Len 应为 0")
	}
}

// TestFileDiffStore_Delete 验证删除。
func TestFileDiffStore_Delete(t *testing.T) {
	s := NewFileDiffStore()
	s.Set("id-1", tool.FileDiffEntry{After: "v1"})
	s.Delete("id-1")
	if _, ok := s.Get("id-1"); ok {
		t.Fatalf("Delete 后 Get 应返回 ok=false")
	}
	// 删不存在的 key 不应 panic
	s.Delete("never-exists")
}

// TestFileDiffStore_Concurrent 验证并发 set/get 安全。
func TestFileDiffStore_Concurrent(t *testing.T) {
	s := NewFileDiffStore()
	const N = 200
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			id := keyFromInt(i)
			s.Set(id, tool.FileDiffEntry{After: "x"})
			_, _ = s.Get(id)
		}(i)
	}
	wg.Wait()
	if s.Len() != N {
		t.Fatalf("并发写后 Len 期望 %d, 实际 %d", N, s.Len())
	}
}

// TestFileDiffStore_NilSafe 验证 nil Store 上调用方法不 panic。
func TestFileDiffStore_NilSafe(t *testing.T) {
	var s *FileDiffStore // nil
	if s.Set("x", tool.FileDiffEntry{After: "y"}) {
		t.Fatalf("nil store Set 应返回 false")
	}
	if _, ok := s.Get("x"); ok {
		t.Fatalf("nil store Get 应返回 ok=false")
	}
	s.Delete("x") // 不应 panic
	if s.Len() != 0 {
		t.Fatalf("nil store Len 应为 0")
	}
}

// TestFileDiffStore_ImplementsSink 编译期断言 *FileDiffStore 满足 tool.FileDiffSink。
func TestFileDiffStore_ImplementsSink(t *testing.T) {
	var _ tool.FileDiffSink = NewFileDiffStore()
}

// TestDetectLanguage 验证后缀到 highlight.js 语言的映射。
func TestDetectLanguage(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/a/b/main.go", "go"},
		{"/a/b/README.md", "markdown"},
		{"/a/b/data.json", "json"},
		{"/a/b/cfg.xml", "xml"},
		{"/a/b/script.py", "python"},
		{"/a/b/app.ts", "typescript"},
		{"/a/b/app.js", "javascript"},
		{"/a/b/site.css", "css"},
		{"/a/b/cfg.yaml", "yaml"},
		{"/a/b/cfg.yml", "yaml"},
		{"/a/b/page.html", "xml"},
		{"/a/b/tab.sql", "sql"},
		{"/a/b/run.sh", "bash"},
		{"/a/b/run.bash", "bash"},
		{"/a/b/UPPER.GO", "go"}, // 大小写不敏感
		{"/a/b/data.bin", ""},
		{"/a/b/noext", ""},
	}
	for _, c := range cases {
		got := DetectLanguage(c.path)
		if got != c.want {
			t.Errorf("DetectLanguage(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

// keyFromInt 把 int 拼接成测试用的 key；与并发测试解耦的可读辅助。
func keyFromInt(i int) string {
	return "k-" + itoaShort(i)
}

// itoaShort 是 strconv.Itoa 的轻量替代，避免额外 import（测试文件）。
func itoaShort(i int) string {
	if i == 0 {
		return "0"
	}
	const digits = "0123456789"
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = digits[i%10]
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
