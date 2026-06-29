// Package hookcontext — $VAR_NAME 变量插值实现(spec §E 末段)。
package hookcontext

import "strings"

var varNameFirstSet [128]bool

var varNameRestSet [128]bool

func init() {
	for c := byte('A'); c <= 'Z'; c++ {
		varNameFirstSet[c] = true
	}
	varNameFirstSet['_'] = true

	for c := byte('A'); c <= 'Z'; c++ {
		varNameRestSet[c] = true
	}
	varNameRestSet['_'] = true
	for c := byte('0'); c <= '9'; c++ {
		varNameRestSet[c] = true
	}
	varNameRestSet['.'] = true
}

// Interpolate 把 template 中所有 $VAR_NAME 占位符替换为 vars 对应值。
//
// 语法规则(spec §E):
//   - 标识符首字符:[A-Z_];
//   - 标识符后续:[A-Z0-9_];含 '.' 用于嵌套字段(如 $TOOL_INPUT.COMMAND);
//   - $$ 转义:渲染为字面 $;
//   - token 内含小写 / 非 ASCII:整段字面保留 $(只识别大写+下划线+数字+点);
//   - 变量未定义时替换为空字符串。
func Interpolate(template string, vars map[string]string) string {
	if template == "" {
		return ""
	}
	if !strings.ContainsRune(template, '$') {
		return template
	}

	var sb strings.Builder
	sb.Grow(len(template))

	i := 0
	for i < len(template) {
		c := template[i]
		if c != '$' {
			sb.WriteByte(c)
			i++
			continue
		}

		next := i + 1
		if next >= len(template) {
			sb.WriteByte('$')
			i++
			continue
		}

		peek := template[next]
		if peek == '$' {
			sb.WriteByte('$')
			i += 2
			continue
		}
		if !isVarNameStart(peek) {
			sb.WriteByte('$')
			i++
			continue
		}

		start := next
		j := next + 1
		illegalAt := -1
		for j < len(template) {
			cj := template[j]
			if cj < 128 && varNameRestSet[cj] {
				j++
				continue
			}
			if cj >= 128 {
				illegalAt = j
				break
			}
			if cj >= 'a' && cj <= 'z' {
				illegalAt = j
				break
			}
			break
		}
		if illegalAt >= 0 {
			sb.WriteByte('$')
			i = next
			continue
		}
		name := template[start:j]
		if val, ok := lookupVar(vars, name); ok {
			sb.WriteString(val)
		}
		i = j
	}

	return sb.String()
}

func isVarNameStart(c byte) bool {
	if c >= 128 {
		return false
	}
	if c == '_' {
		return true
	}
	return c >= 'A' && c <= 'Z'
}

func lookupVar(vars map[string]string, name string) (string, bool) {
	if vars == nil {
		return "", false
	}
	v, ok := vars[name]
	return v, ok
}
