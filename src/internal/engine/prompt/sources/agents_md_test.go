package sources

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MeiCorl/CodePilot/src/internal/engine/prompt/template"
)

// makeSource 构造一个 AgentsMDSource，把 home 与 cwd 注入到结构体字段，
// 避免在测试中污染全局 user.homeDir 与进程 cwd。
func makeSource(home, cwd string) *AgentsMDSource {
	s := NewAgentsMDSource()
	s.HomeDirForTest = home
	s.GetwdForTest = func() (string, error) { return cwd, nil }
	return s
}

// writeFile 在 dir 下写一个 filename 文件，内容为 content。
func writeFile(t *testing.T, dir, filename, content string) string {
	t.Helper()
	path := filepath.Join(dir, filename)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
	return path
}

// TestAgentsMDSource_Name 验证 Name 返回固定值 "agents_md"。
func TestAgentsMDSource_Name(t *testing.T) {
	s := NewAgentsMDSource()
	if got := s.Name(); got != "agents_md" {
		t.Errorf("Name() 应返回 'agents_md'，得到 %q", got)
	}
}

// TestAgentsMDSource_BothMissing 验证全局与项目级文件都不存在时返回空 Content。
// 是「首次启动 / 项目无 AGENTS.md」的正常场景，不应报错。
func TestAgentsMDSource_BothMissing(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	s := makeSource(home, cwd)

	section, err := s.Assemble(context.Background(), Env{})
	if err != nil {
		t.Fatalf("Assemble 不应返回错误（文件缺失是正常场景），得到: %v", err)
	}
	if section.Content != "" {
		t.Errorf("两侧文件都缺失时 Content 应为空，得到 %q", section.Content)
	}
	if section.Placement != PlacementUserMessage {
		t.Errorf("Placement 应为 UserMessage，得到 %d", section.Placement)
	}
	if section.Tokens != 0 {
		t.Errorf("空内容时 Tokens 应为 0，得到 %d", section.Tokens)
	}
}

// TestAgentsMDSource_GlobalOnly 验证仅全局文件存在时正确加载。
func TestAgentsMDSource_GlobalOnly(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeFile(t, home, filepath.Join(agentsMDDirName, agentsMDFileName),
		"## global-rule\nuse tabs for indent\n")

	s := makeSource(home, cwd)
	section, err := s.Assemble(context.Background(), Env{})
	if err != nil {
		t.Fatalf("Assemble 失败: %v", err)
	}
	if !strings.Contains(section.Content, "global-rule") {
		t.Errorf("Content 应包含 global-rule")
	}
	if !strings.Contains(section.Content, "use tabs for indent") {
		t.Errorf("Content 应包含 'use tabs for indent'")
	}
	if !strings.HasPrefix(section.Content, "<project_instructions>") {
		t.Errorf("Content 应以 <project_instructions> 开头")
	}
}

// TestAgentsMDSource_ProjectOnly 验证仅项目级文件存在时正确加载。
func TestAgentsMDSource_ProjectOnly(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeFile(t, cwd, agentsMDFileName,
		"## project-rule\nuse 2-space indent\n")

	s := makeSource(home, cwd)
	section, err := s.Assemble(context.Background(), Env{})
	if err != nil {
		t.Fatalf("Assemble 失败: %v", err)
	}
	if !strings.Contains(section.Content, "project-rule") {
		t.Errorf("Content 应包含 project-rule")
	}
	if !strings.Contains(section.Content, "use 2-space indent") {
		t.Errorf("Content 应包含 'use 2-space indent'")
	}
}

// TestAgentsMDSource_ProjectOverridesGlobal 验证同名段项目级**完全覆盖**全局。
// 这是 checklist 中的关键验收项：同名段是覆盖而非拼接。
func TestAgentsMDSource_ProjectOverridesGlobal(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeFile(t, home, filepath.Join(agentsMDDirName, agentsMDFileName),
		"## code style\nUSE TABS\n")
	writeFile(t, cwd, agentsMDFileName,
		"## code style\nUSE SPACES\n")

	s := makeSource(home, cwd)
	section, err := s.Assemble(context.Background(), Env{})
	if err != nil {
		t.Fatalf("Assemble 失败: %v", err)
	}

	// 项目级内容应出现
	if !strings.Contains(section.Content, "USE SPACES") {
		t.Errorf("项目级 body 应出现")
	}
	// 全局 body 应**不**出现（被完全覆盖，不是拼接）
	if strings.Contains(section.Content, "USE TABS") {
		t.Errorf("全局同名段 body 应被完全覆盖，实际仍包含 'USE TABS'：\n%s", section.Content)
	}
}

// TestAgentsMDSource_DifferentSectionsPreserved 验证不同名段全部保留。
func TestAgentsMDSource_DifferentSectionsPreserved(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeFile(t, home, filepath.Join(agentsMDDirName, agentsMDFileName),
		"## global-A\nbody of A\n\n## global-B\nbody of B\n")
	writeFile(t, cwd, agentsMDFileName,
		"## project-C\nbody of C\n\n## project-D\nbody of D\n")

	s := makeSource(home, cwd)
	section, err := s.Assemble(context.Background(), Env{})
	if err != nil {
		t.Fatalf("Assemble 失败: %v", err)
	}

	// 4 段 body 全部应出现
	for _, want := range []string{"body of A", "body of B", "body of C", "body of D"} {
		if !strings.Contains(section.Content, want) {
			t.Errorf("Content 应包含 %q，实际:\n%s", want, section.Content)
		}
	}
	// 全局段在前，项目级段在后（按 spec：先列全局独有段、再列项目级独有段）
	idxA := strings.Index(section.Content, "body of A")
	idxB := strings.Index(section.Content, "body of B")
	idxC := strings.Index(section.Content, "body of C")
	idxD := strings.Index(section.Content, "body of D")
	if !(idxA < idxC && idxB < idxC && idxA < idxD && idxB < idxD) {
		t.Errorf("全局段 (A/B) 应排在项目级段 (C/D) 之前，得到位置 A=%d B=%d C=%d D=%d",
			idxA, idxB, idxC, idxD)
	}
}

// TestAgentsMDSource_NoH2SingleSection 验证文件无 H2 时整段内容作为单个 name="" section。
// 覆盖当前项目根目录 AGENTS.md 的实际格式（无 H2，纯文本）。
func TestAgentsMDSource_NoH2SingleSection(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeFile(t, cwd, agentsMDFileName,
		"项目实现背景、计划及技术架构分成参考：@.harness/PROJECT.md\n项目实现进度参考：@.harness/PROGRESS.md\n")

	s := makeSource(home, cwd)
	section, err := s.Assemble(context.Background(), Env{})
	if err != nil {
		t.Fatalf("Assemble 失败: %v", err)
	}
	if !strings.Contains(section.Content, "@.harness/PROJECT.md") {
		t.Errorf("无 H2 整段内容应原样保留")
	}
	// 验证没有多余的 ## 前缀
	if strings.Contains(section.Content, "## ") {
		t.Errorf("无 H2 时不应插入 ## 标记，实际:\n%s", section.Content)
	}
}

// TestAgentsMDSource_TruncateAt64KB 验证单文件超 64KB 时被截断（不报错）。
func TestAgentsMDSource_TruncateAt64KB(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()

	// 构造 100KB 的内容：每行 100 字节，1000 行
	var sb strings.Builder
	line := strings.Repeat("a", 99) + "\n" // 每行 99 字符 + 换行 = 100 字节
	for i := 0; i < 1000; i++ {
		sb.WriteString(line)
	}
	writeFile(t, cwd, agentsMDFileName, sb.String())

	s := makeSource(home, cwd)
	section, err := s.Assemble(context.Background(), Env{})
	if err != nil {
		t.Fatalf("Assemble 不应因超限而报错，得到: %v", err)
	}
	// Content 长度应 ≤ agentsMDMaxBytes + 标签开销（约 30 字节）
	if len(section.Content) > agentsMDMaxBytes+50 {
		t.Errorf("Content 长度应 ≤ %d，实际 %d", agentsMDMaxBytes+50, len(section.Content))
	}
	// Content 至少应包含一些原始字符
	if !strings.Contains(section.Content, "aaa") {
		t.Errorf("截断后内容应仍包含原始字符")
	}
}

// TestAgentsMDSource_EmptyFile 验证空文件被视为「无内容」不报错。
func TestAgentsMDSource_EmptyFile(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeFile(t, cwd, agentsMDFileName, "")
	writeFile(t, home, filepath.Join(agentsMDDirName, agentsMDFileName), "")

	s := makeSource(home, cwd)
	section, err := s.Assemble(context.Background(), Env{})
	if err != nil {
		t.Fatalf("Assemble 失败: %v", err)
	}
	if section.Content != "" {
		t.Errorf("空文件应得空 Content，得到 %q", section.Content)
	}
}

// TestAgentsMDSource_WhitespaceOnly 验证纯空白文件视为空。
func TestAgentsMDSource_WhitespaceOnly(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeFile(t, cwd, agentsMDFileName, "   \n\n\t  \n")

	s := makeSource(home, cwd)
	section, err := s.Assemble(context.Background(), Env{})
	if err != nil {
		t.Fatalf("Assemble 失败: %v", err)
	}
	if section.Content != "" {
		t.Errorf("纯空白文件应得空 Content，得到 %q", section.Content)
	}
}

// TestAgentsMDSource_LeadUserMessagePlacement 验证 Placement=UserMessage。
func TestAgentsMDSource_LeadUserMessagePlacement(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeFile(t, cwd, agentsMDFileName, "## rule\nbody\n")

	s := makeSource(home, cwd)
	section, err := s.Assemble(context.Background(), Env{})
	if err != nil {
		t.Fatalf("Assemble 失败: %v", err)
	}
	if section.Placement != PlacementUserMessage {
		t.Errorf("Placement 应为 PlacementUserMessage，得到 %d", section.Placement)
	}
}

// TestAgentsMDSource_TemplateVarsSubstituted 验证 {{VERSION}}/{{DATE}} 等被替换。
func TestAgentsMDSource_TemplateVarsSubstituted(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeFile(t, cwd, agentsMDFileName,
		"## version info\ncurrent version: {{VERSION}}, date: {{DATE}}\n")

	s := makeSource(home, cwd)
	section, err := s.Assemble(context.Background(), Env{Version: "1.0.5", Date: "2026-06-06"})
	if err != nil {
		t.Fatalf("Assemble 失败: %v", err)
	}
	if !strings.Contains(section.Content, "current version: 1.0.5") {
		t.Errorf("{{VERSION}} 应被替换为 1.0.5")
	}
	if !strings.Contains(section.Content, "date: 2026-06-06") {
		t.Errorf("{{DATE}} 应被替换为 2026-06-06")
	}
	// 未替换的占位符应不残留
	if strings.Contains(section.Content, "{{VERSION}}") {
		t.Errorf("{{VERSION}} 应被替换，残留")
	}
	if strings.Contains(section.Content, "{{DATE}}") {
		t.Errorf("{{DATE}} 应被替换，残留")
	}
}

// TestParseSections_BasicSplit 验证 parseSections 按 H2 切分。
func TestParseSections_BasicSplit(t *testing.T) {
	in := `## alpha
body alpha line 1
body alpha line 2
## beta
body beta
## gamma
body gamma
`
	got := parseSections(in)
	if len(got) != 3 {
		t.Fatalf("应有 3 段，得到 %d 段", len(got))
	}
	if got[0].name != "alpha" || !strings.Contains(got[0].body, "body alpha line 1") {
		t.Errorf("第 1 段不符: %+v", got[0])
	}
	if got[1].name != "beta" || got[1].body != "body beta" {
		t.Errorf("第 2 段不符: %+v", got[1])
	}
	if got[2].name != "gamma" || got[2].body != "body gamma" {
		t.Errorf("第 3 段不符: %+v", got[2])
	}
}

// TestParseSections_PrefaceAsEmptyName 验证 H2 之前的前言作为 name="" 段。
func TestParseSections_PrefaceAsEmptyName(t *testing.T) {
	in := `这是前言，没有 H2
跨多行
## section1
body1
`
	got := parseSections(in)
	if len(got) != 2 {
		t.Fatalf("应有 2 段（前言 + section1），得到 %d 段", len(got))
	}
	if got[0].name != "" {
		t.Errorf("前言段 name 应为空，得到 %q", got[0].name)
	}
	if !strings.Contains(got[0].body, "这是前言") {
		t.Errorf("前言 body 不符: %+v", got[0])
	}
	if got[1].name != "section1" {
		t.Errorf("第 2 段 name 应为 section1，得到 %q", got[1].name)
	}
}

// TestParseSections_H1NotH2 验证 H1（# ）与 H3（### ）不被识别为 H2。
// 期望：H1/H3 之前的内容作为 name="" 的前言段，H2 自身为独立段，共 2 段。
func TestParseSections_H1NotH2(t *testing.T) {
	in := `# h1
body h1
### h3
body h3
## h2
body h2
`
	got := parseSections(in)
	if len(got) != 2 {
		t.Fatalf("应得 2 段（H1/H3 前言 + H2），得到 %d 段", len(got))
	}
	// 第 1 段：name="" 前言，包含 H1 和 H3 行
	if got[0].name != "" {
		t.Errorf("第 1 段应为前言 (name=\"\")，得到 name=%q", got[0].name)
	}
	if !strings.Contains(got[0].body, "# h1") {
		t.Errorf("前言段应包含 H1 行")
	}
	if !strings.Contains(got[0].body, "### h3") {
		t.Errorf("前言段应包含 H3 行")
	}
	// 第 2 段：H2
	if got[1].name != "h2" || got[1].body != "body h2" {
		t.Errorf("第 2 段应为 name=h2 body='body h2'，得到 %+v", got[1])
	}
}

// TestMergeSections_ProjectOverridesGlobal 验证同名段项目级覆盖全局。
func TestMergeSections_ProjectOverridesGlobal(t *testing.T) {
	global := []section{
		{name: "rule", body: "GLOBAL body", order: 0},
	}
	project := []section{
		{name: "rule", body: "PROJECT body", order: 0},
	}
	got := mergeSections(global, project)
	if len(got) != 1 {
		t.Fatalf("应有 1 段，得到 %d 段", len(got))
	}
	if got[0].body != "PROJECT body" {
		t.Errorf("项目级应完全覆盖全局，body=%q", got[0].body)
	}
}

// TestMergeSections_EmptyInputs 验证空入参处理。
func TestMergeSections_EmptyInputs(t *testing.T) {
	cases := []struct {
		name           string
		global, project []section
		wantLen        int
	}{
		{"both nil", nil, nil, 0},
		{"global only", []section{{name: "a", body: "x"}}, nil, 1},
		{"project only", nil, []section{{name: "a", body: "x"}}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mergeSections(c.global, c.project)
			if len(got) != c.wantLen {
				t.Errorf("want %d sections, got %d", c.wantLen, len(got))
			}
		})
	}
}

// TestAgentsMDSource_UsesCwdFromEnv 验证 Env.CWD 优先于 osGetwd。
// 这是 handler 预填 CWD 时的正确行为。
func TestAgentsMDSource_UsesCwdFromEnv(t *testing.T) {
	realHome := t.TempDir()
	envCwd := t.TempDir()
	otherCwd := t.TempDir()

	// 在 envCwd 下放项目级 AGENTS.md，otherCwd 下放「干扰文件」
	writeFile(t, envCwd, agentsMDFileName, "## from-env-cwd\nenv content\n")
	writeFile(t, otherCwd, agentsMDFileName, "## from-other-cwd\nother content\n")

	// Source 的 GetwdForTest 返回 otherCwd，但 Env.CWD 指向 envCwd
	s := &AgentsMDSource{
		HomeDirForTest: realHome,
		GetwdForTest:   func() (string, error) { return otherCwd, nil },
	}
	section, err := s.Assemble(context.Background(), Env{CWD: envCwd})
	if err != nil {
		t.Fatalf("Assemble 失败: %v", err)
	}
	if !strings.Contains(section.Content, "env content") {
		t.Errorf("应使用 Env.CWD 指向的目录，实际:\n%s", section.Content)
	}
	if strings.Contains(section.Content, "other content") {
		t.Errorf("不应使用 osGetwdForTest 的结果")
	}
}

// TestAgentsMDSource_IntegrationWithTemplate 验证与 template 包集成。
// 通过 Env.GitStatus 验证类型别名（template.GitStatus）可用。
func TestAgentsMDSource_IntegrationWithTemplate(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeFile(t, cwd, agentsMDFileName,
		"## branch info\non branch: {{GIT_BRANCH}}\n")

	s := makeSource(home, cwd)
	section, err := s.Assemble(context.Background(), Env{
		GitStatus: template.GitStatus{Branch: "master"},
	})
	if err != nil {
		t.Fatalf("Assemble 失败: %v", err)
	}
	if !strings.Contains(section.Content, "on branch: master") {
		t.Errorf("{{GIT_BRANCH}} 应被替换为 master，实际:\n%s", section.Content)
	}
}
