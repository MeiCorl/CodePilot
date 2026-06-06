// Package template（render.go）实现 System Prompt 的模板变量渲染。
//
// 支持的占位符：
//   - {{OS}}          runtime.GOOS（"windows"/"linux"/"darwin"）
//   - {{CWD}}         Env.CWD（已 resolve 真实路径）
//   - {{GIT_BRANCH}}  Env.GitStatus.Branch，非 git 仓库时为 "not a git repository"
//   - {{GIT_DIRTY}}   Env.GitStatus.Dirty 的可读字符串（"dirty"/"clean"）
//   - {{DATE}}        Env.Date（"YYYY-MM-DD"）
//   - {{VERSION}}     Env.Version（来自 build flag，缺省 "dev"）
//
// 安全约束：模板变量**不接受外部输入**，仅从 Env 读取，
// Env 来自会话启动时采集的内部状态，不会被用户/工具篡改，
// 因此无需担心 XSS 注入或字符串拼接攻击。
package template

import "strings"

// supportedVars 是 Render 支持的全部占位符集合，
// 用于校验：未识别的占位符保持原样（不替换、不报错），
// 这样后续新增变量时旧模板可平滑过渡。
var supportedVars = map[string]bool{
	"OS":         true,
	"CWD":        true,
	"GIT_BRANCH": true,
	"GIT_DIRTY":  true,
	"DATE":       true,
	"VERSION":    true,
}

// Render 把 text 中所有 {{VAR}} 占位符替换为 Env 中对应字段的可读字符串。
//
// 行为约定：
//  1. 未知占位符保持原样（如 {{FOO}} → {{FOO}}），便于后续扩展
//  2. 同一占位符多次出现全部替换
//  3. 占位符必须大写（supportedVars 仅识别大写形式），小写形式视为未知保留
//  4. 不接受外部输入：仅从 env 读取，杜绝注入
//  5. 空 text 直接返回 ""（不分配 Builder）
//
// 该函数是纯函数（无副作用、不读全局状态），可被并发调用。
func Render(text string, env Env) string {
	if text == "" {
		return ""
	}
	// 快速路径：text 中不含 "{{" 时直接返回原串
	if !strings.Contains(text, "{{") {
		return text
	}

	var sb strings.Builder
	sb.Grow(len(text))
	i := 0
	for i < len(text) {
		// 寻找下一个 "{{"
		start := strings.Index(text[i:], "{{")
		if start == -1 {
			sb.WriteString(text[i:])
			break
		}
		// 写出 "{{" 之前的内容
		sb.WriteString(text[i : i+start])
		i += start + 2 // 跳过 "{{"

		// 寻找对应的 "}}"
		end := strings.Index(text[i:], "}}")
		if end == -1 {
			// 没有闭合的占位符，原样写出剩余内容
			sb.WriteString(text[i-2:])
			break
		}
		varName := text[i : i+end]
		i += end + 2 // 跳过 "}}"

		// 查表替换；未知占位符原样写出
		if value, ok := lookupVar(varName, env); ok {
			sb.WriteString(value)
		} else {
			sb.WriteString("{{")
			sb.WriteString(varName)
			sb.WriteString("}}")
		}
	}
	return sb.String()
}

// lookupVar 把 Env 字段映射为占位符的可读字符串值。
// 返回 (value, true) 表示该占位符已知；返回 ("", false) 表示未知。
func lookupVar(name string, env Env) (string, bool) {
	if !supportedVars[name] {
		return "", false
	}
	switch name {
	case "OS":
		return env.OS, true
	case "CWD":
		return env.CWD, true
	case "GIT_BRANCH":
		if env.GitStatus.Branch == "" {
			return "not a git repository", true
		}
		return env.GitStatus.Branch, true
	case "GIT_DIRTY":
		return boolToDirty(env.GitStatus.Dirty), true
	case "DATE":
		return env.Date, true
	case "VERSION":
		if env.Version == "" {
			return "dev", true
		}
		return env.Version, true
	}
	return "", false
}

// boolToDirty 把 bool 转为可读字符串。
func boolToDirty(b bool) string {
	if b {
		return "dirty"
	}
	return "clean"
}
