package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSetDefaults 验证可选字段使用默认值
func TestSetDefaults(t *testing.T) {
	cfg := &Config{
		Provider:   "anthropic",
		Model:     "claude-sonnet-4-20250514",
		APIKey:    "test-key",
		MaxTokens: 4096,
	}
	cfg.setDefaults()

	if cfg.Timeout != defaultTimeout {
		t.Errorf("Timeout 默认值错误: 期望 %d, 实际 %d", defaultTimeout, cfg.Timeout)
	}
	if cfg.MaxRetries != defaultMaxRetries {
		t.Errorf("MaxRetries 默认值错误: 期望 %d, 实际 %d", defaultMaxRetries, cfg.MaxRetries)
	}
	if cfg.ToolExecutionTimeoutSeconds != defaultToolExecutionTimeoutSec {
		t.Errorf("ToolExecutionTimeoutSeconds 默认值错误: 期望 %d, 实际 %d",
			defaultToolExecutionTimeoutSec, cfg.ToolExecutionTimeoutSeconds)
	}
	if cfg.ToolWorkingDirectory != "" {
		t.Errorf("ToolWorkingDirectory 默认值应为空字符串, 实际 %q", cfg.ToolWorkingDirectory)
	}
}

// TestLoadFromPathSuccess 验证正常加载完整配置
func TestLoadFromPathSuccess(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "setting.json")
	content, _ := json.Marshal(Config{
		Provider:   "openai",
		Model:     "gpt-4o",
		APIKey:    "sk-test",
		MaxTokens: 4096,
		Timeout:   30,
	})
	os.WriteFile(cfgPath, content, 0644)

	cfg, err := LoadFromPath(cfgPath)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if cfg.Provider != "openai" {
		t.Errorf("Provider 错误: 期望 openai, 实际 %s", cfg.Provider)
	}
	if cfg.Timeout != 30 {
		t.Errorf("Timeout 错误: 期望 30, 实际 %d", cfg.Timeout)
	}
}

// TestLoadFromPathDefaults 验证不填写可选字段时使用默认值
func TestLoadFromPathDefaults(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "setting.json")
	// 不填写 Timeout 和 MaxRetries
	content, _ := json.Marshal(map[string]any{
		"provider":   "anthropic",
		"model":     "claude-sonnet-4-20250514",
		"api_key":   "sk-test",
		"max_tokens": 4096,
	})
	os.WriteFile(cfgPath, content, 0644)

	cfg, err := LoadFromPath(cfgPath)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if cfg.Timeout != 180 {
		t.Errorf("Timeout 默认值错误: 期望 180, 实际 %d", cfg.Timeout)
	}
	if cfg.MaxRetries != 2 {
		t.Errorf("MaxRetries 默认值错误: 期望 2, 实际 %d", cfg.MaxRetries)
	}
	if cfg.ToolExecutionTimeoutSeconds != 30 {
		t.Errorf("ToolExecutionTimeoutSeconds 默认值错误: 期望 30, 实际 %d", cfg.ToolExecutionTimeoutSeconds)
	}
	if cfg.ToolWorkingDirectory != "" {
		t.Errorf("ToolWorkingDirectory 默认值应为空, 实际 %q", cfg.ToolWorkingDirectory)
	}
	if len(cfg.Tools.Enabled) != 0 {
		t.Errorf("Tools.Enabled 默认应为空, 实际 %v", cfg.Tools.Enabled)
	}
}

// TestLoadFromPathWithToolsConfig 验证 tools 段被正确解析。
func TestLoadFromPathWithToolsConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "setting.json")
	content, _ := json.Marshal(map[string]any{
		"provider":                      "anthropic",
		"model":                         "claude-sonnet-4-20250514",
		"api_key":                       "sk-test",
		"max_tokens":                    4096,
		"tools":                         map[string]any{"enabled": []string{"ReadFile", "Bash"}},
		"tool_execution_timeout_seconds": 5,
		"tool_working_directory":        "f:/CodePilot",
	})
	os.WriteFile(cfgPath, content, 0644)

	cfg, err := LoadFromPath(cfgPath)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if got, want := cfg.Tools.Enabled, []string{"ReadFile", "Bash"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Tools.Enabled 错误: 期望 %v, 实际 %v", want, got)
	}
	if cfg.ToolExecutionTimeoutSeconds != 5 {
		t.Errorf("ToolExecutionTimeoutSeconds 错误: 期望 5, 实际 %d", cfg.ToolExecutionTimeoutSeconds)
	}
	if cfg.ToolWorkingDirectory != "f:/CodePilot" {
		t.Errorf("ToolWorkingDirectory 错误: 期望 f:/CodePilot, 实际 %q", cfg.ToolWorkingDirectory)
	}
}

// TestLoadFromPathNotFound 验证文件不存在时的错误提示
func TestLoadFromPathNotFound(t *testing.T) {
	_, err := LoadFromPath("/nonexistent/path/setting.json")
	if err == nil {
		t.Fatal("期望返回错误，实际为 nil")
	}
	msg := err.Error()
	if len(msg) == 0 {
		t.Error("错误消息为空")
	}
}

// TestValidateUnsupportedProvider 验证不支持的供应商报错
func TestValidateUnsupportedProvider(t *testing.T) {
	cfg := &Config{
		Provider:   "gemini",
		Model:     "gemini-pro",
		APIKey:    "test",
		MaxTokens: 4096,
	}
	err := cfg.validate()
	if err == nil {
		t.Fatal("期望返回错误，实际为 nil")
	}
}

// ----------------------------------------------------------------------------
// Step 11 — HookConfig 单元测试
// 覆盖 A 组 9 项验收清单 + applyHookDefaults / MergeHooks / ValidateHookConfig 三组场景
// ----------------------------------------------------------------------------

// TestApplyHookDefaultsNil 验证：applyHookDefaults(nil) 安全无副作用。
func TestApplyHookDefaultsNil(t *testing.T) {
	applyHookDefaults(nil) // 仅确保不 panic
}

// TestApplyHookDefaultsAllNil 验证：nil / 部分字段 / 全字段三种场景。
func TestApplyHookDefaultsAllNil(t *testing.T) {
	t.Run("nil pointer", func(t *testing.T) {
		var h *HookConfig
		applyHookDefaults(h)
		if h != nil {
			t.Errorf("nil 指针调用后不应被修改，实际: %+v", h)
		}
	})

	t.Run("all nil fields", func(t *testing.T) {
		h := &HookConfig{}
		applyHookDefaults(h)
		if h.Enabled == nil || !*h.Enabled {
			t.Errorf("Enabled 默认应为 true，实际: %v", h.Enabled)
		}
		if h.Entries != nil {
			t.Errorf("Entries 应保持 nil（零配置降级），实际: %+v", h.Entries)
		}
	})

	t.Run("partial fields", func(t *testing.T) {
		off := false
		h := &HookConfig{
			Enabled: &off, // 用户显式关闭
			Entries: []HookEntryConfig{{Name: "x"}},
		}
		applyHookDefaults(h)
		if h.Enabled == nil || *h.Enabled != false {
			t.Errorf("用户显式 false 应保留，实际: %v", h.Enabled)
		}
		if len(h.Entries) != 1 {
			t.Errorf("Entries 应保留用户配置，实际长度: %d", len(h.Entries))
		}
	})

	t.Run("full fields", func(t *testing.T) {
		on := true
		h := &HookConfig{
			Enabled: &on,
			Entries: []HookEntryConfig{{Name: "a"}, {Name: "b"}},
		}
		applyHookDefaults(h)
		if h.Enabled == nil || !*h.Enabled {
			t.Errorf("Enabled 应保留 true，实际: %v", h.Enabled)
		}
		if len(h.Entries) != 2 {
			t.Errorf("Entries 应保留用户配置，实际长度: %d", len(h.Entries))
		}
	})
}

// TestHookConfigIsEnabled 验证 IsEnabled 在 nil / true / false 三态下的返回值。
func TestHookConfigIsEnabled(t *testing.T) {
	t.Run("nil Enabled", func(t *testing.T) {
		var h HookConfig
		if !h.IsEnabled() {
			t.Error("Enabled=nil 时应默认返回 true")
		}
	})
	t.Run("explicit true", func(t *testing.T) {
		on := true
		h := HookConfig{Enabled: &on}
		if !h.IsEnabled() {
			t.Error("Enabled=&true 时应返回 true")
		}
	})
	t.Run("explicit false", func(t *testing.T) {
		off := false
		h := HookConfig{Enabled: &off}
		if h.IsEnabled() {
			t.Error("Enabled=&false 时应返回 false")
		}
	})
}

// TestMergeHooksGlobalFull 验证：global 全配 + project 全零 → 沿用 global。
func TestMergeHooksGlobalFull(t *testing.T) {
	global := HookConfig{
		Enabled: ptrBool(true),
		Entries: []HookEntryConfig{
			{Name: "g1", Event: "pre_tool_use", Action: HookActionConfig{Type: "command"}},
			{Name: "g2", Event: "post_tool_use", Action: HookActionConfig{Type: "http"}},
		},
	}
	project := HookConfig{}
	merged := MergeHooks(global, project)

	if !merged.IsEnabled() {
		t.Error("合并后 Enabled 应为 true")
	}
	if len(merged.Entries) != 2 {
		t.Fatalf("合并后 Entries 长度应为 2, 实际 %d", len(merged.Entries))
	}
	if merged.Entries[0].Name != "g1" || merged.Entries[1].Name != "g2" {
		t.Errorf("Entries 顺序/内容未沿用 global: %+v", merged.Entries)
	}
}

// TestMergeHooksProjectFull 验证：global 零 + project 全配 → 用 project。
func TestMergeHooksProjectFull(t *testing.T) {
	off := false
	global := HookConfig{Enabled: &off, Entries: nil}
	project := HookConfig{
		Enabled: ptrBool(true),
		Entries: []HookEntryConfig{
			{Name: "p1", Event: "session_start", Action: HookActionConfig{Type: "prompt"}},
		},
	}
	merged := MergeHooks(global, project)

	if !merged.IsEnabled() {
		t.Error("合并后 Enabled 应为 project 的 true")
	}
	if len(merged.Entries) != 1 || merged.Entries[0].Name != "p1" {
		t.Errorf("Entries 应来自 project, 实际: %+v", merged.Entries)
	}
}

// TestMergeHooksPartialOverride 验证：仅覆盖部分字段时未配置字段沿用全局。
func TestMergeHooksPartialOverride(t *testing.T) {
	t.Run("only Enabled override", func(t *testing.T) {
		global := HookConfig{
			Enabled: ptrBool(true),
			Entries: []HookEntryConfig{{Name: "g1", Event: "pre_tool_use", Action: HookActionConfig{Type: "command"}}},
		}
		off := false
		project := HookConfig{Enabled: &off} // 只覆盖 Enabled
		merged := MergeHooks(global, project)

		if merged.IsEnabled() {
			t.Error("Enabled 应被 project=false 覆盖")
		}
		if len(merged.Entries) != 1 || merged.Entries[0].Name != "g1" {
			t.Errorf("Entries 应沿用 global, 实际: %+v", merged.Entries)
		}
	})

	t.Run("only Entries override", func(t *testing.T) {
		global := HookConfig{
			Enabled: ptrBool(true),
			Entries: []HookEntryConfig{{Name: "g1", Event: "pre_tool_use", Action: HookActionConfig{Type: "command"}}},
		}
		project := HookConfig{
			Entries: []HookEntryConfig{{Name: "p1", Event: "post_tool_use", Action: HookActionConfig{Type: "http"}}},
		}
		merged := MergeHooks(global, project)

		if !merged.IsEnabled() {
			t.Error("Enabled 应沿用 global=true")
		}
		if len(merged.Entries) != 1 || merged.Entries[0].Name != "p1" {
			t.Errorf("Entries 应整体替换为 project, 实际: %+v", merged.Entries)
		}
	})
}

// TestValidateHookConfigLegal 验证合法配置通过校验。
func TestValidateHookConfigLegal(t *testing.T) {
	h := &HookConfig{
		Enabled: ptrBool(true),
		Entries: []HookEntryConfig{
			{Name: "fmt-go", Event: "post_tool_use", Action: HookActionConfig{Type: "command"}},
			{Name: "slack", Event: "session_start", Action: HookActionConfig{Type: "http"}},
			{Name: "reminder", Event: "pre_tool_use", Action: HookActionConfig{Type: "prompt"}},
			{Name: "audit", Event: "post_tool_use", Action: HookActionConfig{Type: "agent"}},
			{Name: "startup", Event: "program_start", Action: HookActionConfig{Type: "command"}, Async: true},
			{Name: "once", Event: "iteration_end", Action: HookActionConfig{Type: "command"}, Once: true},
		},
	}
	if err := ValidateHookConfig(h); err != nil {
		t.Fatalf("合法配置不应报错: %v", err)
	}
}

// TestValidateHookConfigErrors 验证各类非法场景报错。
func TestValidateHookConfigErrors(t *testing.T) {
	tests := []struct {
		desc      string
		h         HookConfig
		wantSubstr string
	}{
		{
			desc: "empty Name",
			h: HookConfig{Entries: []HookEntryConfig{
				{Name: "", Event: "pre_tool_use", Action: HookActionConfig{Type: "command"}},
			}},
			wantSubstr: "name 不能为空",
		},
		{
			desc: "invalid Event",
			h: HookConfig{Entries: []HookEntryConfig{
				{Name: "x", Event: "not_a_real_event", Action: HookActionConfig{Type: "command"}},
			}},
			wantSubstr: "event=",
		},
		{
			desc: "invalid Action.Type",
			h: HookConfig{Entries: []HookEntryConfig{
				{Name: "x", Event: "pre_tool_use", Action: HookActionConfig{Type: "weird"}},
			}},
			wantSubstr: "action.type=",
		},
		{
			desc: "empty Action.Type",
			h: HookConfig{Entries: []HookEntryConfig{
				{Name: "x", Event: "pre_tool_use", Action: HookActionConfig{Type: ""}},
			}},
			wantSubstr: "action.type 不能为空",
		},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			err := ValidateHookConfig(&tc.h)
			if err == nil {
				t.Fatalf("期望报错，实际 nil")
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("错误信息应含 %q，实际: %v", tc.wantSubstr, err)
			}
		})
	}
}

// TestHookConfigZeroConfigSafety 验证：HookConfig 零值经过 setDefaults 后,
// Enabled=true + Entries=nil,validate 通过,实现零配置安全降级。
func TestHookConfigZeroConfigSafety(t *testing.T) {
	cfg := &Config{
		Provider:  "anthropic",
		Model:     "claude-sonnet-4-20250514",
		APIKey:    "test-key",
		MaxTokens: 4096,
	}
	cfg.setDefaults()
	if !cfg.Hook.IsEnabled() {
		t.Error("零配置 setDefaults 后 Hook.Enabled 应默认 true")
	}
	if cfg.Hook.Entries != nil {
		t.Errorf("零配置 Entries 应保持 nil, 实际: %+v", cfg.Hook.Entries)
	}
	if err := cfg.validate(); err != nil {
		t.Errorf("零配置 Hook 不应校验失败: %v", err)
	}
}

// TestHookActionConfigJSONRoundTrip 验证：HookActionConfig 的自定义 MarshalJSON/UnmarshalJSON
// 实现能正确往返「type 与 type-specific 字段同层」的 JSON 结构。
func TestHookActionConfigJSONRoundTrip(t *testing.T) {
	original := `{"type":"command","command":"echo $TOOL_INPUT_FILE_PATH","timeout":"10s","env":{"NO_COLOR":"1"}}`
	var a HookActionConfig
	if err := json.Unmarshal([]byte(original), &a); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	if a.Type != "command" {
		t.Errorf("Type 错误: 期望 command, 实际 %q", a.Type)
	}
	if len(a.Raw) == 0 {
		t.Fatal("Raw 应保留 type-specific 原始 JSON")
	}
	// 重新序列化应保留原始字段
	out, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	// 解析成 map 比较键集合
	var origMap, outMap map[string]any
	if err := json.Unmarshal([]byte(original), &origMap); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &outMap); err != nil {
		t.Fatal(err)
	}
	for k, v := range origMap {
		got, ok := outMap[k]
		if !ok {
			t.Errorf("序列化后缺失字段 %q", k)
			continue
		}
		// 简化比较：JSON 序列化后字符串内容应一致
		origJSON, _ := json.Marshal(v)
		gotJSON, _ := json.Marshal(got)
		if string(origJSON) != string(gotJSON) {
			t.Errorf("字段 %q 内容不一致: 原 %s, 现 %s", k, origJSON, gotJSON)
		}
	}
}

// TestLoadFromPathWithHookConfig 验证：从 JSON 加载包含 hooks 段的配置,
// 经 setDefaults + validate 后字段正确。
func TestLoadFromPathWithHookConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "setting.json")
	content, _ := json.Marshal(map[string]any{
		"provider":   "anthropic",
		"model":      "claude-sonnet-4-20250514",
		"api_key":    "sk-test",
		"max_tokens": 4096,
		"hook": map[string]any{
			"enabled": true,
			"entries": []map[string]any{
				{
					"name":  "auto-format",
					"event": "post_tool_use",
					"condition": map[string]any{
						"all": []map[string]any{
							{"field": "tool_name", "op": "eq", "value": "WriteFile"},
							{"field": "tool_input.file_path", "op": "glob", "value": "*.go"},
						},
					},
					"action": map[string]any{
						"type":    "command",
						"command": "gofmt -w $TOOL_INPUT_FILE_PATH",
						"timeout": "10s",
					},
					"async": false,
					"once":  false,
				},
			},
		},
	})
	os.WriteFile(cfgPath, content, 0644)

	cfg, err := LoadFromPath(cfgPath)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if !cfg.Hook.IsEnabled() {
		t.Error("Hook.Enabled 应为 true")
	}
	if len(cfg.Hook.Entries) != 1 {
		t.Fatalf("Hook.Entries 长度应为 1, 实际 %d", len(cfg.Hook.Entries))
	}
	e := cfg.Hook.Entries[0]
	if e.Name != "auto-format" {
		t.Errorf("Name 错误: %q", e.Name)
	}
	if e.Event != "post_tool_use" {
		t.Errorf("Event 错误: %q", e.Event)
	}
	if e.Condition == nil {
		t.Error("Condition 应被解析")
	}
	if e.Action.Type != "command" {
		t.Errorf("Action.Type 错误: %q", e.Action.Type)
	}
	if len(e.Action.Raw) == 0 {
		t.Error("Action.Raw 应保留 type-specific 字段原始 JSON")
	}
}

// ptrBool 工具函数：让测试代码写 ptrBool(true) 比 &[]bool{true}[0] 直观。
func ptrBool(b bool) *bool {
	return &b
}
