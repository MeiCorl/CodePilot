package sources

import (
	"context"
	"strings"
	"testing"

	"github.com/MeiCorl/CodePilot/src/internal/engine/prompt/template"
)

// TestStaticSource_Name 验证 Name 返回固定值 "static"。
func TestStaticSource_Name(t *testing.T) {
	s := NewStaticSource()
	if got := s.Name(); got != "static" {
		t.Errorf("Name() 应返回 'static'，得到 %q", got)
	}
}

// TestStaticSource_DefaultContent 验证默认产出包含 5 个 XML 风格子模块标签。
func TestStaticSource_DefaultContent(t *testing.T) {
	s := NewStaticSource()
	section, err := s.Assemble(context.Background(), Env{
		OS:      "linux",
		CWD:     "/tmp",
		Date:    "2026-06-06",
		Version: "1.0.5",
		GitStatus: template.GitStatus{
			Branch: "master",
			Dirty:  false,
		},
	})
	if err != nil {
		t.Fatalf("Assemble 不应返回错误，得到: %v", err)
	}

	// 验证 5 个标签都存在
	for _, tag := range []string{
		"<system_role>",
		"</system_role>",
		"<behavior_principles>",
		"</behavior_principles>",
		"<code_quality>",
		"</code_quality>",
		"<tool_usage>",
		"</tool_usage>",
		"<safety_boundary>",
		"</safety_boundary>",
	} {
		if !strings.Contains(section.Content, tag) {
			t.Errorf("默认 SP 应包含标签 %q，实际未找到", tag)
		}
	}

	// 验证 Placement
	if section.Placement != PlacementSystem {
		t.Errorf("Placement 应为 PlacementSystem，得到 %d", section.Placement)
	}
	// 验证 Tokens > 0
	if section.Tokens <= 0 {
		t.Errorf("Tokens 应 > 0，得到 %d", section.Tokens)
	}
	// 验证模板变量被替换
	if strings.Contains(section.Content, "{{OS}}") {
		t.Errorf("模板变量 {{OS}} 应被替换，实际仍存在")
	}
	if strings.Contains(section.Content, "{{VERSION}}") {
		t.Errorf("模板变量 {{VERSION}} 应被替换，实际仍存在")
	}
}

// TestStaticSource_BehaviorPrinciplesKeywords 验证行为准则子模块包含
// 5 个 checklist 要求的关键词：简洁、说要做的事、2~3 建议、不顺手优化、file_path:line_number。
func TestStaticSource_BehaviorPrinciplesKeywords(t *testing.T) {
	s := NewStaticSource()
	section, err := s.Assemble(context.Background(), Env{OS: "linux", CWD: "/tmp"})
	if err != nil {
		t.Fatalf("Assemble 失败: %v", err)
	}

	keywords := []string{"简洁", "一句话", "2~3", "顺手", "file_path:line_number"}
	for _, kw := range keywords {
		if !strings.Contains(section.Content, kw) {
			t.Errorf("行为准则应包含关键词 %q", kw)
		}
	}
}

// TestStaticSource_ToolUsageMentionsReadFile 验证工具使用原则中
// 出现 ReadFile 与 Bash 关键词，且为规约/警告语义。
func TestStaticSource_ToolUsageMentionsReadFile(t *testing.T) {
	s := NewStaticSource()
	section, err := s.Assemble(context.Background(), Env{OS: "linux", CWD: "/tmp"})
	if err != nil {
		t.Fatalf("Assemble 失败: %v", err)
	}
	if !strings.Contains(section.Content, "ReadFile") {
		t.Errorf("工具使用原则应提到 ReadFile")
	}
	if !strings.Contains(section.Content, "Bash") {
		t.Errorf("工具使用原则应提到 Bash（用于警告用 Bash+cat 替代 ReadFile）")
	}
}

// TestStaticSource_SafetyBoundaryMentions 验证安全边界子模块包含
// 关键词：破坏性操作需用户确认、不绕过 git hook、命令注入/SQL注入/XSS。
func TestStaticSource_SafetyBoundaryMentions(t *testing.T) {
	s := NewStaticSource()
	section, err := s.Assemble(context.Background(), Env{OS: "linux", CWD: "/tmp"})
	if err != nil {
		t.Fatalf("Assemble 失败: %v", err)
	}
	keywords := []string{"破坏性", "git hook", "命令注入", "SQL", "XSS"}
	for _, kw := range keywords {
		if !strings.Contains(section.Content, kw) {
			t.Errorf("安全边界应包含关键词 %q", kw)
		}
	}
}

// TestStaticSource_StaticOverride 验证 Env.StaticOverrides 按子模块名覆盖。
func TestStaticSource_StaticOverride(t *testing.T) {
	s := NewStaticSource()
	custom := "<system_role>\n我是定制的角色\n</system_role>"

	section, err := s.Assemble(context.Background(), Env{
		OS: "linux",
		CWD: "/tmp",
		StaticOverrides: map[string]string{
			ModuleSystemRole: custom,
		},
	})
	if err != nil {
		t.Fatalf("Assemble 失败: %v", err)
	}

	if !strings.Contains(section.Content, "我是定制的角色") {
		t.Errorf("StaticOverride 应替换默认 system_role 内容，实际未生效")
	}
	// 验证其它子模块未被影响
	if !strings.Contains(section.Content, "<behavior_principles>") {
		t.Errorf("其它子模块应保留默认")
	}
}

// TestStaticSource_DeterministicOutput 验证相同 Env 多次调用产出完全一致。
// 这是 Source 接口「纯函数 + 可并发」契约的硬约束。
func TestStaticSource_DeterministicOutput(t *testing.T) {
	s := NewStaticSource()
	env := Env{OS: "linux", CWD: "/tmp", Version: "1.0.5"}

	first, err := s.Assemble(context.Background(), env)
	if err != nil {
		t.Fatalf("第一次 Assemble 失败: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, err := s.Assemble(context.Background(), env)
		if err != nil {
			t.Fatalf("第 %d 次 Assemble 失败: %v", i+2, err)
		}
		if first.Content != again.Content {
			t.Errorf("第 %d 次产出与首次不一致（应确定性）", i+2)
		}
		if first.Tokens != again.Tokens {
			t.Errorf("第 %d 次 tokens 与首次不一致: %d vs %d", i+2, again.Tokens, first.Tokens)
		}
	}
}

// TestStaticSource_OrderPreserved 验证 5 个子模块在最终 Content 中按预期顺序排列。
func TestStaticSource_OrderPreserved(t *testing.T) {
	s := NewStaticSource()
	section, err := s.Assemble(context.Background(), Env{OS: "linux", CWD: "/tmp"})
	if err != nil {
		t.Fatalf("Assemble 失败: %v", err)
	}

	// 按预期顺序寻找每个子模块的起始位置
	modules := []string{
		"<system_role>",
		"<behavior_principles>",
		"<code_quality>",
		"<tool_usage>",
		"<safety_boundary>",
	}
	lastIdx := -1
	for _, m := range modules {
		idx := strings.Index(section.Content, m)
		if idx == -1 {
			t.Errorf("未找到子模块 %q", m)
			continue
		}
		if idx <= lastIdx {
			t.Errorf("子模块 %q 出现位置 (%d) 早于前一个子模块 (%d)，顺序错乱", m, idx, lastIdx)
		}
		lastIdx = idx
	}
}
