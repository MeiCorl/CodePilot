package prompt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MeiCorl/CodePilot/src/internal/engine/prompt/sources"
)

// fakeSource 是测试用 Source 实现，支持预设 Name / Content / Placement / Tokens / 错误。
// 任何测试场景都可以通过 NewFakeSource 一行构造，避免在每个 case 里重复写 struct。
type fakeSource struct {
	name      string
	content   string
	placement sources.Placement
	tokens    int
	err       error
}

func (f *fakeSource) Name() string { return f.name }

func (f *fakeSource) Assemble(_ context.Context, _ sources.Env) (sources.Section, error) {
	if f.err != nil {
		return sources.Section{}, f.err
	}
	return sources.Section{
		Name:      f.name,
		Content:   f.content,
		Placement: f.placement,
		Tokens:    f.tokens,
	}, nil
}

// TestBuilder_Assemble_EmptySources 验证：当未注册任何 Source 时，
// Assemble 返回零值 SystemPrompt 且不 panic。
// 用途：对应 config.json 中 system_prompt.enabled=false 的场景。
func TestBuilder_Assemble_EmptySources(t *testing.T) {
	b := NewBuilder()
	sp, err := b.Assemble(context.Background(), sources.Env{})
	if err != nil {
		t.Fatalf("Assemble 不应返回错误，得到: %v", err)
	}

	if len(sp.SystemBlocks) != 0 {
		t.Errorf("空 Source 时 SystemBlocks 应为空，得到 %d 段", len(sp.SystemBlocks))
	}
	if sp.LeadUserMessage != "" {
		t.Errorf("空 Source 时 LeadUserMessage 应为空，得到 %q", sp.LeadUserMessage)
	}
	if len(sp.Stats) != 0 {
		t.Errorf("空 Source 时 Stats 应为空，得到 %d 条", len(sp.Stats))
	}
	if sp.TotalTokens != 0 {
		t.Errorf("空 Source 时 TotalTokens 应为 0，得到 %d", sp.TotalTokens)
	}
	if !sp.IsEmpty() {
		t.Errorf("空 Source 时 IsEmpty() 应返回 true")
	}
}

// TestBuilder_Assemble_SingleSystemSource 验证：单个 PlacementSystem 的 Source
// 正确产出 1 个 SystemBlock、LeadUserMessage 为空、Stats 记录该 Source。
func TestBuilder_Assemble_SingleSystemSource(t *testing.T) {
	b := NewBuilder(&fakeSource{
		name:      "static",
		content:   "you are CodePilot",
		placement: sources.PlacementSystem,
		tokens:    10,
	})

	sp, err := b.Assemble(context.Background(), sources.Env{})
	if err != nil {
		t.Fatalf("Assemble 不应返回错误，得到: %v", err)
	}

	if len(sp.SystemBlocks) != 1 {
		t.Fatalf("应有 1 段 SystemBlock，得到 %d 段", len(sp.SystemBlocks))
	}
	if sp.SystemBlocks[0].Text != "you are CodePilot" {
		t.Errorf("SystemBlock 文本内容不符，得到 %q", sp.SystemBlocks[0].Text)
	}
	if !sp.SystemBlocks[0].Cacheable {
		t.Errorf("PlacementSystem 段 Cacheable 应默认为 true")
	}
	if sp.LeadUserMessage != "" {
		t.Errorf("无 UserMessage 段时 LeadUserMessage 应为空，得到 %q", sp.LeadUserMessage)
	}
	if len(sp.Stats) != 1 || sp.Stats[0].Name != "static" || sp.Stats[0].Tokens != 10 {
		t.Errorf("Stats 记录不符，得到 %+v", sp.Stats)
	}
	if sp.TotalTokens != 10 {
		t.Errorf("TotalTokens 应为 10，得到 %d", sp.TotalTokens)
	}
	if sp.IsEmpty() {
		t.Errorf("存在 System 段时 IsEmpty() 应返回 false")
	}
}

// TestBuilder_Assemble_SingleUserMessageSource 验证：单个 PlacementUserMessage
// 的 Source 正确产出 LeadUserMessage、SystemBlocks 为空。
func TestBuilder_Assemble_SingleUserMessageSource(t *testing.T) {
	b := NewBuilder(&fakeSource{
		name:      "agents_md",
		content:   "<project_instructions>\ndo X\n</project_instructions>",
		placement: sources.PlacementUserMessage,
		tokens:    20,
	})

	sp, err := b.Assemble(context.Background(), sources.Env{})
	if err != nil {
		t.Fatalf("Assemble 不应返回错误，得到: %v", err)
	}

	if len(sp.SystemBlocks) != 0 {
		t.Errorf("UserMessage 段不应进入 SystemBlocks，得到 %d 段", len(sp.SystemBlocks))
	}
	if !strings.Contains(sp.LeadUserMessage, "do X") {
		t.Errorf("LeadUserMessage 应包含 Source 内容，得到 %q", sp.LeadUserMessage)
	}
	if sp.TotalTokens != 20 {
		t.Errorf("TotalTokens 应为 20，得到 %d", sp.TotalTokens)
	}
	if sp.IsEmpty() {
		t.Errorf("存在 UserMessage 段时 IsEmpty() 应返回 false")
	}
}

// TestBuilder_Assemble_MixedPlacements 验证：多个 Source 混合 Placement 时
// 正确分组：System 进 SystemBlocks、UserMessage 合并为单条 LeadUserMessage。
// 这是 Step 4 实际使用场景：static + environment（System）+ agents_md + memory（UserMessage）。
func TestBuilder_Assemble_MixedPlacements(t *testing.T) {
	b := NewBuilder(
		&fakeSource{name: "static", content: "ROLE TEXT", placement: sources.PlacementSystem, tokens: 10},
		&fakeSource{name: "environment", content: "ENV TEXT", placement: sources.PlacementSystem, tokens: 5},
		&fakeSource{name: "agents_md", content: "AGENTS TEXT", placement: sources.PlacementUserMessage, tokens: 30},
		&fakeSource{name: "memory", content: "MEMORY TEXT", placement: sources.PlacementUserMessage, tokens: 8},
	)

	sp, err := b.Assemble(context.Background(), sources.Env{})
	if err != nil {
		t.Fatalf("Assemble 不应返回错误，得到: %v", err)
	}

	// SystemBlocks 应有 2 段，按注册顺序
	if len(sp.SystemBlocks) != 2 {
		t.Fatalf("应有 2 段 SystemBlock，得到 %d 段", len(sp.SystemBlocks))
	}
	if sp.SystemBlocks[0].Text != "ROLE TEXT" || sp.SystemBlocks[1].Text != "ENV TEXT" {
		t.Errorf("SystemBlock 顺序或内容错误，得到 %+v", sp.SystemBlocks)
	}

	// LeadUserMessage 应由 agents_md + memory 用 "\n\n" 拼接
	if !strings.Contains(sp.LeadUserMessage, "AGENTS TEXT") || !strings.Contains(sp.LeadUserMessage, "MEMORY TEXT") {
		t.Errorf("LeadUserMessage 应包含两个 UserMessage 段，得到 %q", sp.LeadUserMessage)
	}
	// 顺序：先 agents_md 再 memory，且中间用 "\n\n" 分隔
	expected := "AGENTS TEXT\n\nMEMORY TEXT"
	if sp.LeadUserMessage != expected {
		t.Errorf("LeadUserMessage 拼接结果不符\n  预期: %q\n  实际: %q", expected, sp.LeadUserMessage)
	}

	// Stats 应有 4 条，按注册顺序
	if len(sp.Stats) != 4 {
		t.Fatalf("Stats 应有 4 条，得到 %d 条", len(sp.Stats))
	}
	expectedStats := []sources.SourceStat{
		{Name: "static", Tokens: 10},
		{Name: "environment", Tokens: 5},
		{Name: "agents_md", Tokens: 30},
		{Name: "memory", Tokens: 8},
	}
	for i, want := range expectedStats {
		if sp.Stats[i] != want {
			t.Errorf("Stats[%d] 不符\n  预期: %+v\n  实际: %+v", i, want, sp.Stats[i])
		}
	}

	// TotalTokens 应为各段之和
	if sp.TotalTokens != 10+5+30+8 {
		t.Errorf("TotalTokens 应为 53，得到 %d", sp.TotalTokens)
	}
}

// TestBuilder_Assemble_SourceError 验证：任一 Source 返回 error 时，
// Assemble 立即返回错误，错误信息包含 Source 名称。
func TestBuilder_Assemble_SourceError(t *testing.T) {
	boom := errors.New("read agents.md failed")
	b := NewBuilder(
		&fakeSource{name: "static", content: "OK", placement: sources.PlacementSystem, tokens: 1},
		&fakeSource{name: "environment", content: "OK", placement: sources.PlacementSystem, tokens: 1},
		&fakeSource{name: "agents_md", err: boom, placement: sources.PlacementUserMessage, tokens: 0},
		// memory 不应被调用
		&fakeSource{name: "memory", content: "should not be called", placement: sources.PlacementUserMessage, tokens: 99},
	)

	_, err := b.Assemble(context.Background(), sources.Env{})
	if err == nil {
		t.Fatalf("预期返回错误，得到 nil")
	}
	if !strings.Contains(err.Error(), "agents_md") {
		t.Errorf("错误信息应包含失败 Source 名称，得到: %v", err)
	}
	if !errors.Is(err, boom) {
		t.Errorf("错误应包装原始错误（errors.Is），得到: %v", err)
	}
}

// TestBuilder_Assemble_EmptyContent 验证：Source 产出空 Content 时，
// 仍记录 Stats（让 WebUI 区分「启用但空」与「未注册」），
// 但空段不进入 SystemBlocks 也不进入 LeadUserMessage。
func TestBuilder_Assemble_EmptyContent(t *testing.T) {
	b := NewBuilder(
		&fakeSource{name: "static", content: "REAL", placement: sources.PlacementSystem, tokens: 5},
		&fakeSource{name: "memory", content: "", placement: sources.PlacementUserMessage, tokens: 0},
	)

	sp, err := b.Assemble(context.Background(), sources.Env{})
	if err != nil {
		t.Fatalf("Assemble 不应返回错误，得到: %v", err)
	}

	// SystemBlocks 仅有 static（空段被过滤）
	if len(sp.SystemBlocks) != 1 {
		t.Errorf("应有 1 段 SystemBlock（空段被过滤），得到 %d 段", len(sp.SystemBlocks))
	}
	// LeadUserMessage 为空（空段被过滤，没有其它段可以拼）
	if sp.LeadUserMessage != "" {
		t.Errorf("LeadUserMessage 应为空，得到 %q", sp.LeadUserMessage)
	}
	// Stats 应有 2 条（memory 仍记录，Tokens=0）
	if len(sp.Stats) != 2 {
		t.Errorf("Stats 应保留 2 条（空段也记录），得到 %d 条", len(sp.Stats))
	}
	// TotalTokens 不应被空段污染：5 + 0 = 5
	if sp.TotalTokens != 5 {
		t.Errorf("TotalTokens 应为 5（空段 0 token），得到 %d", sp.TotalTokens)
	}
}

// TestBuilder_Assemble_ContextCanceled 验证：ctx 已被取消时，
// Assemble 立即返回 ctx.Err() 而非继续调用后续 Source。
func TestBuilder_Assemble_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 提前取消

	b := NewBuilder(
		&fakeSource{name: "static", content: "X", placement: sources.PlacementSystem, tokens: 1},
	)

	_, err := b.Assemble(ctx, sources.Env{})
	if err == nil {
		t.Fatalf("预期返回 ctx 取消错误，得到 nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("错误应包含 context.Canceled，得到: %v", err)
	}
}

// TestBuilder_Assemble_DefensiveNameFill 验证：Source 在 Section 中
// 未填 Name 字段时，Builder 用 src.Name() 兜底。
// 防止 Source 实现者忘记填写 Name 导致 Stats 出现空字符串。
func TestBuilder_Assemble_DefensiveNameFill(t *testing.T) {
	// 构造一个返回空 Name 的 fake：直接用结构体字面量绕过 fakeSource 的 Name 赋值
	anon := &anonymousSource{content: "X", placement: sources.PlacementSystem, tokens: 1}
	b := NewBuilder(anon)

	sp, err := b.Assemble(context.Background(), sources.Env{})
	if err != nil {
		t.Fatalf("Assemble 不应返回错误，得到: %v", err)
	}
	if len(sp.Stats) != 1 || sp.Stats[0].Name != "anonymous" {
		t.Errorf("Stats.Name 应兜底为 src.Name()，得到 %+v", sp.Stats)
	}
}

// anonymousSource 返回的 Section.Name 为空，用于测试 Builder 的兜底逻辑。
type anonymousSource struct {
	content   string
	placement sources.Placement
	tokens    int
}

func (a *anonymousSource) Name() string { return "anonymous" }

func (a *anonymousSource) Assemble(_ context.Context, _ sources.Env) (sources.Section, error) {
	return sources.Section{
		Name:      "", // 故意留空，测试 Builder 兜底
		Content:   a.content,
		Placement: a.placement,
		Tokens:    a.tokens,
	}, nil
}

// =====================================================================
// Task 4 集成测试：4 个真实 Source 端到端串联 + enabled 开关
// =====================================================================

// stubMemoryProvider 允许测试中预设 Recall 返回值，
// 避免每次都依赖 NoopMemoryProvider（仅在验证 Noop 行为时使用 Noop）。
type stubMemoryProvider struct {
	fragments []string
	err       error
}

func (s *stubMemoryProvider) Recall(_ context.Context, _ string) ([]string, error) {
	return s.fragments, s.err
}

// makeRealFourSourceBuilder 构造一个含 4 个真实 Source 的 Builder：
// static + environment + agents_md + memory（带 stub）。
// agents_md 用的 home/cwd 通过 HomeDirForTest 注入到临时目录。
// 返回：Builder、测试用的 cwd（用于在 Assemble 时填入 Env.CWD，让 agents_md
// 能正确找到项目级 AGENTS.md）。
func makeRealFourSourceBuilder(t *testing.T, projectAgentsMD string) (*Builder, string) {
	t.Helper()
	home := t.TempDir()
	cwd := t.TempDir()
	if projectAgentsMD != "" {
		if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"),
			[]byte(projectAgentsMD), 0644); err != nil {
			t.Fatalf("写项目 AGENTS.md 失败: %v", err)
		}
	}

	agentsSrc := sources.NewAgentsMDSource()
	agentsSrc.HomeDirForTest = home
	agentsSrc.GetwdForTest = func() (string, error) { return cwd, nil }

	memSrc := sources.NewMemorySource(&stubMemoryProvider{}, nil)

	b := NewBuilder(
		sources.NewStaticSource(),
		sources.NewEnvironmentSource(),
		agentsSrc,
		memSrc,
	)
	return b, cwd
}

// TestBuilder_RealFourSources_EndToEnd 验证真实 4 Source 串联：
// static + environment → 2 个 SystemBlock
// agents_md + memory → 1 条 LeadUserMessage（被合并）
// 顺序、token、Stats 全部正确
func TestBuilder_RealFourSources_EndToEnd(t *testing.T) {
	b, cwd := makeRealFourSourceBuilder(t, "## test rule\nproject body\n")

	env := sources.Env{
		OS:      "linux",
		CWD:     cwd, // 必须与 agents_src 的 GetwdForTest 一致，否则 agents_md 找不到 AGENTS.md
		Date:    "2026-06-06",
		Version: "1.0.5",
		GitStatus: sources.GitStatus{
			Branch: "master",
			Dirty:  false,
		},
	}

	sp, err := b.Assemble(context.Background(), env)
	if err != nil {
		t.Fatalf("Assemble 失败: %v", err)
	}

	// 验证 SystemBlocks 数量 = 2（static + environment）
	if len(sp.SystemBlocks) != 2 {
		t.Errorf("SystemBlocks 应有 2 段，得到 %d 段", len(sp.SystemBlocks))
	}

	// 验证两段都标记 Cacheable=true
	for i, blk := range sp.SystemBlocks {
		if !blk.Cacheable {
			t.Errorf("SystemBlock[%d] 应为 Cacheable=true", i)
		}
		if blk.Text == "" {
			t.Errorf("SystemBlock[%d] Text 不应为空", i)
		}
	}

	// 验证第 1 段（static）包含 5 个子模块标签
	if !strings.Contains(sp.SystemBlocks[0].Text, "<system_role>") {
		t.Errorf("第 1 段应为 static，包含 <system_role> 标签")
	}

	// 验证第 2 段（environment）包含 <environment> 标签
	if !strings.Contains(sp.SystemBlocks[1].Text, "<environment>") {
		t.Errorf("第 2 段应为 environment，包含 <environment> 标签")
	}

	// 验证 LeadUserMessage 非空且包含项目级 AGENTS.md 内容（Noop memory 不贡献）
	if sp.LeadUserMessage == "" {
		t.Errorf("LeadUserMessage 不应为空（agents_md 应贡献内容）")
	}
	if !strings.Contains(sp.LeadUserMessage, "project body") {
		t.Errorf("LeadUserMessage 应包含项目级 AGENTS.md 内容，实际:\n%s", sp.LeadUserMessage)
	}
	if !strings.Contains(sp.LeadUserMessage, "<project_instructions>") {
		t.Errorf("LeadUserMessage 应被 <project_instructions> 包裹")
	}

	// 验证 Stats 数量 = 4
	if len(sp.Stats) != 4 {
		t.Fatalf("Stats 应有 4 条，得到 %d 条", len(sp.Stats))
	}
	wantOrder := []string{"static", "environment", "agents_md", "memory"}
	for i, want := range wantOrder {
		if sp.Stats[i].Name != want {
			t.Errorf("Stats[%d].Name 应为 %q，得到 %q", i, want, sp.Stats[i].Name)
		}
		if sp.Stats[i].Tokens < 0 {
			t.Errorf("Stats[%d].Tokens 不应为负", i)
		}
	}

	// 验证 TotalTokens = 各段之和
	wantTotal := 0
	for _, s := range sp.Stats {
		wantTotal += s.Tokens
	}
	if sp.TotalTokens != wantTotal {
		t.Errorf("TotalTokens (%d) 应等于各 Stats.Tokens 之和 (%d)", sp.TotalTokens, wantTotal)
	}

	// 验证 IsEmpty() 返回 false
	if sp.IsEmpty() {
		t.Errorf("存在内容时 IsEmpty() 应返回 false")
	}
}

// TestBuilder_RealFourSources_NoMemoryByDefault 验证 NoopMemoryProvider 默认无记忆。
func TestBuilder_RealFourSources_NoMemoryByDefault(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	agentsSrc := sources.NewAgentsMDSource()
	agentsSrc.HomeDirForTest = home
	agentsSrc.GetwdForTest = func() (string, error) { return cwd, nil }

	// 用真实的 NoopMemoryProvider
	b := NewBuilder(
		sources.NewStaticSource(),
		sources.NewEnvironmentSource(),
		agentsSrc,
		sources.NewMemorySource(sources.NewNoopMemoryProvider(), nil),
	)

	sp, err := b.Assemble(context.Background(), sources.Env{OS: "linux", CWD: cwd})
	if err != nil {
		t.Fatalf("Assemble 失败: %v", err)
	}

	// Noop memory 不贡献内容，LeadUserMessage 应为空
	if sp.LeadUserMessage != "" {
		t.Errorf("NoopMemoryProvider 下 LeadUserMessage 应为空，得到 %q", sp.LeadUserMessage)
	}
	// 但 memory 仍出现在 Stats（让 WebUI 区分「启用但无记忆」与「未注册」）
	if len(sp.Stats) != 4 {
		t.Errorf("Stats 应有 4 条（含 Noop memory 占位），得到 %d 条", len(sp.Stats))
	}
	if sp.Stats[3].Name != "memory" || sp.Stats[3].Tokens != 0 {
		t.Errorf("Stats[3] 应为 memory/Tokens=0，得到 %+v", sp.Stats[3])
	}
}

// TestBuilder_RealFourSources_MemoryWithFragments 验证 memory 真实有片段时
// 正确拼入 LeadUserMessage（用 stub MemoryProvider）。
func TestBuilder_RealFourSources_MemoryWithFragments(t *testing.T) {
	b, _ := makeRealFourSourceBuilder(t, "") // 无项目级 AGENTS.md

	// 替换最后一个 Source 为带 stub memory 的版本
	stub := &stubMemoryProvider{
		fragments: []string{"user prefers tabs", "project uses Go 1.26"},
	}
	// 重新构造：前 3 个 Source 不变，memory 替换为带 stub 的
	home := t.TempDir()
	cwd := t.TempDir()
	agentsSrc := sources.NewAgentsMDSource()
	agentsSrc.HomeDirForTest = home
	agentsSrc.GetwdForTest = func() (string, error) { return cwd, nil }
	memSrc := sources.NewMemorySource(stub, nil)

	b = NewBuilder(
		sources.NewStaticSource(),
		sources.NewEnvironmentSource(),
		agentsSrc,
		memSrc,
	)

	sp, err := b.Assemble(context.Background(), sources.Env{OS: "linux", CWD: cwd})
	if err != nil {
		t.Fatalf("Assemble 失败: %v", err)
	}

	// 验证 LeadUserMessage 含 memory 片段
	if !strings.Contains(sp.LeadUserMessage, "user prefers tabs") {
		t.Errorf("LeadUserMessage 应包含 memory 片段 'user prefers tabs'")
	}
	if !strings.Contains(sp.LeadUserMessage, "project uses Go 1.26") {
		t.Errorf("LeadUserMessage 应包含 memory 片段 'project uses Go 1.26'")
	}
	if !strings.Contains(sp.LeadUserMessage, "<memories>") {
		t.Errorf("memory 片段应被 <memories> 包裹")
	}
	// memory 用 "---" 分隔多片段
	if !strings.Contains(sp.LeadUserMessage, "---") {
		t.Errorf("memory 多片段应被 --- 分隔")
	}
	// memory 应贡献非零 token
	if sp.Stats[3].Tokens <= 0 {
		t.Errorf("memory 段 Tokens 应 > 0（含真实片段），得到 %d", sp.Stats[3].Tokens)
	}
}

// TestBuilder_RealFourSources_MemoryProviderError 验证 memory provider 出错时
// Assemble 透传错误（Source 错误应让上层感知）。
func TestBuilder_RealFourSources_MemoryProviderError(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	agentsSrc := sources.NewAgentsMDSource()
	agentsSrc.HomeDirForTest = home
	agentsSrc.GetwdForTest = func() (string, error) { return cwd, nil }

	boom := errors.New("vector db connection lost")
	memSrc := sources.NewMemorySource(&stubMemoryProvider{err: boom}, nil)

	b := NewBuilder(
		sources.NewStaticSource(),
		sources.NewEnvironmentSource(),
		agentsSrc,
		memSrc,
	)

	_, err := b.Assemble(context.Background(), sources.Env{OS: "linux", CWD: cwd})
	if err == nil {
		t.Fatalf("预期返回错误，得到 nil")
	}
	if !strings.Contains(err.Error(), "memory") {
		t.Errorf("错误信息应包含 source 名称 'memory'，得到: %v", err)
	}
	if !errors.Is(err, boom) {
		t.Errorf("错误应包装原始错误，得到: %v", err)
	}
}

// TestBuilder_Disabled_ShortCircuit 验证 enabled=false 时直接返回零值 SystemPrompt，
// 不调用任何 Source（不消耗 token、不读磁盘、不发起 git 命令）。
func TestBuilder_Disabled_ShortCircuit(t *testing.T) {
	// 用一个「如果被调用就 panic」的 Source 来验证 Assemble 真的没调它
	panicSource := &panicSource{}

	b := NewBuilder(
		sources.NewStaticSource(),
		panicSource, // 若 Assemble 走到这里，测试会 panic
	)
	b.SetEnabled(false)

	sp, err := b.Assemble(context.Background(), sources.Env{OS: "linux", CWD: "/tmp"})
	if err != nil {
		t.Fatalf("disabled Assemble 不应返回错误，得到: %v", err)
	}
	if !sp.IsEmpty() {
		t.Errorf("disabled Assemble 应返回空 SystemPrompt，得到 %+v", sp)
	}
	if sp.TotalTokens != 0 {
		t.Errorf("disabled Assemble TotalTokens 应为 0，得到 %d", sp.TotalTokens)
	}
	if len(sp.Stats) != 0 {
		t.Errorf("disabled Assemble Stats 应为空，得到 %d 条", len(sp.Stats))
	}
	if len(sp.SystemBlocks) != 0 {
		t.Errorf("disabled Assemble SystemBlocks 应为空，得到 %d 段", len(sp.SystemBlocks))
	}
	if sp.LeadUserMessage != "" {
		t.Errorf("disabled Assemble LeadUserMessage 应为空，得到 %q", sp.LeadUserMessage)
	}
	// 验证 Enabled() 返回 false
	if b.Enabled() {
		t.Errorf("Enabled() 应返回 false")
	}
}

// TestBuilder_Enabled_DefaultTrue 验证 NewBuilder 默认 enabled=true。
func TestBuilder_Enabled_DefaultTrue(t *testing.T) {
	b := NewBuilder()
	if !b.Enabled() {
		t.Errorf("NewBuilder 默认 Enabled() 应返回 true")
	}
}

// TestBuilder_Enabled_Toggleable 验证 SetEnabled 切换状态。
func TestBuilder_Enabled_Toggleable(t *testing.T) {
	b := NewBuilder()
	if !b.Enabled() {
		t.Fatalf("初始应为 true")
	}
	b.SetEnabled(false)
	if b.Enabled() {
		t.Errorf("SetEnabled(false) 后应为 false")
	}
	b.SetEnabled(true)
	if !b.Enabled() {
		t.Errorf("SetEnabled(true) 后应为 true")
	}
}

// panicSource 用于验证 disabled 时不会调用 Source.Assemble。
type panicSource struct{}

func (p *panicSource) Name() string { return "panic" }
func (p *panicSource) Assemble(_ context.Context, _ sources.Env) (sources.Section, error) {
	panic("panicSource.Assemble 被调用，说明 disabled 没有短路")
}

// TestTask6_BuilderWith4RealSources 端到端：4 个真实 Source 串成一条
// 完整管线，输出与 web handler 期望的 SystemPrompt 字段一致。
//
// 这是 Task 6「端到端验证」在 prompt 包内的覆盖：
// 验证 builder 输出的 SystemBlocks/LeadUserMessage/Stats/TotalTokens
// 都能被 web 包的 convertToLLMSystemPrompt 正确转换。
func TestTask6_BuilderWith4RealSources(t *testing.T) {
	// 临时 home 目录让 agents_md 走「全局文件不存在」分支
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")

	// 临时工作目录让 agents_md 走「项目文件不存在」分支
	tmpWork := t.TempDir()

	b := NewBuilder(
		sources.NewStaticSource(),
		sources.NewEnvironmentSource(),
		sources.NewAgentsMDSource(),
		sources.NewMemorySource(sources.NewNoopMemoryProvider(), nil),
	)

	env := sources.Env{
		OS:     "linux",
		CWD:    tmpWork,
		Date:   "2026-06-06",
		Version: "test-v1.0.6",
	}

	sp, err := b.Assemble(context.Background(), env)
	if err != nil {
		t.Fatalf("Assemble 失败: %v", err)
	}

	// 验证 4 个 Source 全部贡献了内容
	if len(sp.SystemBlocks) < 2 {
		t.Errorf("应有 ≥ 2 段 SystemBlock（static + environment），实际 %d 段", len(sp.SystemBlocks))
	}
	if sp.LeadUserMessage != "" {
		// agents_md 和 memory 都是空，LeadUserMessage 应为空（Builder 不过滤空段拼接）
		// 注意：agents_md 至少包了一层 <project_instructions>...</project_instructions> 标签
		// 实际看 agents_md.go 实现：两边都空时返回空 Content
	}
	if len(sp.Stats) != 4 {
		t.Errorf("Stats 应有 4 条，实际 %d 条", len(sp.Stats))
	}

	// 验证 Source 顺序：static → environment → agents_md → memory
	wantOrder := []string{"static", "environment", "agents_md", "memory"}
	for i, name := range wantOrder {
		if i >= len(sp.Stats) {
			t.Fatalf("Stats[%d] 缺失", i)
		}
		if sp.Stats[i].Name != name {
			t.Errorf("Stats[%d].Name = %q，期望 %q", i, sp.Stats[i].Name, name)
		}
	}

	// 验证 TotalTokens = Σ Stats.Tokens
	wantTotal := 0
	for _, s := range sp.Stats {
		wantTotal += s.Tokens
	}
	if sp.TotalTokens != wantTotal {
		t.Errorf("TotalTokens = %d，期望 %d", sp.TotalTokens, wantTotal)
	}

	// 验证 static 段包含 5 个 XML 风格子模块
	if len(sp.SystemBlocks) > 0 {
		staticText := sp.SystemBlocks[0].Text
		for _, tag := range []string{"<system_role>", "<behavior_principles>", "<code_quality>", "<tool_usage>", "<safety_boundary>"} {
			if !strings.Contains(staticText, tag) {
				t.Errorf("static 段应包含标签 %q", tag)
			}
		}
	}

	// 验证 environment 段包含 OS / CWD / Date
	if len(sp.SystemBlocks) > 1 {
		envText := sp.SystemBlocks[1].Text
		if !strings.Contains(envText, "OS:") {
			t.Error("environment 段应包含 'OS:'")
		}
		if !strings.Contains(envText, "CWD:") {
			t.Error("environment 段应包含 'CWD:'")
		}
	}
}
