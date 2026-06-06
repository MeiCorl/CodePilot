package template

import "testing"

// TestRender_Empty 验证空 text 返回空串。
func TestRender_Empty(t *testing.T) {
	if got := Render("", Env{}); got != "" {
		t.Errorf("空 text 应返回空串，得到 %q", got)
	}
}

// TestRender_NoPlaceholder 验证不含 {{ 的 text 原样返回（快速路径）。
func TestRender_NoPlaceholder(t *testing.T) {
	in := "hello world, no placeholders here"
	if got := Render(in, Env{}); got != in {
		t.Errorf("无占位符应原样返回\n  in:  %q\n  got: %q", in, got)
	}
}

// TestRender_AllSupportedVars 验证 6 个支持的占位符全部正确替换。
func TestRender_AllSupportedVars(t *testing.T) {
	env := Env{
		OS:      "linux",
		CWD:     "/home/user/proj",
		Date:    "2026-06-06",
		Version: "1.0.5",
		GitStatus: GitStatus{
			Branch: "master",
			Dirty:  true,
		},
	}
	in := "OS={{OS}} CWD={{CWD}} BR={{GIT_BRANCH}} D={{GIT_DIRTY}} DATE={{DATE}} V={{VERSION}}"
	want := "OS=linux CWD=/home/user/proj BR=master D=dirty DATE=2026-06-06 V=1.0.5"
	if got := Render(in, env); got != want {
		t.Errorf("全量替换不符\n  in:   %q\n  want: %q\n  got:  %q", in, want, got)
	}
}

// TestRender_UnknownVarPreserved 验证未知占位符原样保留。
func TestRender_UnknownVarPreserved(t *testing.T) {
	in := "OS={{OS}} FOO={{FOO}}"
	env := Env{OS: "linux"}
	want := "OS=linux FOO={{FOO}}"
	if got := Render(in, env); got != want {
		t.Errorf("未知变量应原样保留\n  want: %q\n  got:  %q", want, got)
	}
}

// TestRender_LowercaseVarPreserved 验证小写形式视为未知（不区分大小写处理）。
func TestRender_LowercaseVarPreserved(t *testing.T) {
	in := "{{os}} {{Os}}"
	env := Env{OS: "linux"}
	want := "{{os}} {{Os}}"
	if got := Render(in, env); got != want {
		t.Errorf("小写占位符应原样保留\n  want: %q\n  got:  %q", want, got)
	}
}

// TestRender_EmptyGitBranchFallback 验证非 git 仓库时 GIT_BRANCH 返回可读字符串。
func TestRender_EmptyGitBranchFallback(t *testing.T) {
	in := "BR={{GIT_BRANCH}}"
	env := Env{} // GitStatus 为零值
	want := "BR=not a git repository"
	if got := Render(in, env); got != want {
		t.Errorf("空 branch 应得 'not a git repository'\n  want: %q\n  got:  %q", want, got)
	}
}

// TestRender_EmptyVersionFallback 验证 Version 为空时返回 "dev"。
func TestRender_EmptyVersionFallback(t *testing.T) {
	in := "V={{VERSION}}"
	env := Env{}
	want := "V=dev"
	if got := Render(in, env); got != want {
		t.Errorf("空 version 应得 'dev'\n  want: %q\n  got:  %q", want, got)
	}
}

// TestRender_DirtyFalseVsTrue 验证 GIT_DIRTY 在 clean 与 dirty 下的两种输出。
func TestRender_DirtyFalseVsTrue(t *testing.T) {
	cases := []struct {
		name  string
		dirty bool
		want  string
	}{
		{"clean", false, "STATUS=clean"},
		{"dirty", true, "STATUS=dirty"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := Env{GitStatus: GitStatus{Dirty: c.dirty}}
			if got := Render("STATUS={{GIT_DIRTY}}", env); got != c.want {
				t.Errorf("want %q, got %q", c.want, got)
			}
		})
	}
}

// TestRender_MultipleOccurrences 验证同一占位符多次出现全部替换。
func TestRender_MultipleOccurrences(t *testing.T) {
	in := "{{OS}}-{{OS}}-{{OS}}"
	env := Env{OS: "linux"}
	want := "linux-linux-linux"
	if got := Render(in, env); got != want {
		t.Errorf("多次出现应全部替换\n  want: %q\n  got:  %q", want, got)
	}
}

// TestRender_UnclosedPlaceholder 验证未闭合的占位符（{{ 但无 }}）原样保留。
func TestRender_UnclosedPlaceholder(t *testing.T) {
	in := "before-{{OS-and-no-close-after"
	env := Env{OS: "linux"}
	want := "before-{{OS-and-no-close-after"
	if got := Render(in, env); got != want {
		t.Errorf("未闭合占位符应原样保留\n  want: %q\n  got:  %q", want, got)
	}
}

// TestRender_VariableFollowedByMoreText 验证占位符后跟其他文本时正确处理。
func TestRender_VariableFollowedByMoreText(t *testing.T) {
	in := "{{OS}} and then more text"
	env := Env{OS: "linux"}
	want := "linux and then more text"
	if got := Render(in, env); got != want {
		t.Errorf("占位符后跟文本时处理错误\n  want: %q\n  got:  %q", want, got)
	}
}
