package safety

import (
	"fmt"
	"regexp"
	"strings"
)

// bashRule 描述一条黑名单规则。
// pattern 为正则；reason 为命中时返回的可读原因。
type bashRule struct {
	pattern *regexp.Regexp
	reason  string
}

// bashBlacklist 是所有危险命令规则的集合。
// 规则设计原则：宁可漏过边缘 case 也不误杀正常命令。
// 复杂的上下文相关匹配（如反引号替换、$() 嵌套）由
// Task 3 通过更严格的解析器补充。
var bashBlacklist = []bashRule{
	// 递归删除根目录或家目录
	{
		pattern: regexp.MustCompile(`\brm\s+(-\w*[rRfF]\w*\s+)*(/\*?|~|\$\{?HOME\}?)`),
		reason:  "禁止递归删除根目录或家目录",
	},
	// 磁盘格式化
	{
		pattern: regexp.MustCompile(`\bmkfs(\.\w+)?\s+`),
		reason:  "禁止执行 mkfs 格式化磁盘",
	},
	// 系统关机/重启
	{
		pattern: regexp.MustCompile(`\b(shutdown|reboot|halt|poweroff)\b`),
		reason:  "禁止执行关机/重启命令",
	},
	// 切换运行级别
	{
		pattern: regexp.MustCompile(`\binit\s+[0-6]\b`),
		reason:  "禁止切换系统运行级别",
	},
	// dd 写入块设备
	{
		pattern: regexp.MustCompile(`\bdd\b[^\n]*\bof=/dev/`),
		reason:  "禁止 dd 写入块设备",
	},
	// 重定向到块设备
	{
		pattern: regexp.MustCompile(`>\s*/dev/sd[a-z]`),
		reason:  "禁止重定向到块设备",
	},
	// fork bomb
	{
		pattern: regexp.MustCompile(`:\(\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;\s*:`),
		reason:  "禁止执行 fork bomb",
	},
	// chmod 777 根目录
	{
		pattern: regexp.MustCompile(`\bchmod\s+(-\w+\s+)*777\s+/`),
		reason:  "禁止对根目录设置 777 权限",
	},
}

// CheckBashCommand 检查命令字符串是否命中黑名单。
//
// 命中返回 *DangerousCommandError，携带具体原因；
// 未命中返回 nil。
//
// 注意：本检查**仅**是字符串级别的正则匹配，复杂的 shell 注入
// 场景（如 base64 编码命令）无法被本层防御，必须由上游 LLM prompt
// 规范与权限系统（Step 5）配合。
func CheckBashCommand(command string) error {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return fmt.Errorf("%w: 命令为空", ErrDangerousCommand)
	}
	for _, rule := range bashBlacklist {
		if rule.pattern.MatchString(cmd) {
			return &DangerousCommandError{Command: command, Reason: rule.reason}
		}
	}
	return nil
}

// DangerousCommandError 携带具体拦截原因的详细错误。
type DangerousCommandError struct {
	Command string
	Reason  string
}

// Error 实现 error 接口。
func (e *DangerousCommandError) Error() string {
	return fmt.Sprintf("%s: %s", ErrDangerousCommand.Error(), e.Reason)
}

// Unwrap 支持 errors.Is(err, ErrDangerousCommand) 判定。
func (e *DangerousCommandError) Unwrap() error { return ErrDangerousCommand }
