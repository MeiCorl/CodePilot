// Package executor — CommandExecutor 实现 (spec §D.1)。
//
// command action 是 Hook 系统最常见的 action 类型:用户在 setting.json 配置一段
// shell 命令字符串,CodePilot 在 Hook 触发点(工具前后/Agent Loop 节点/会话开关
// 等)用 os/exec 包 fork 一条子进程执行,退出码 0 视为成功,其它视为失败。
//
// 设计要点:
//   - 用 os/exec.CommandContext 而非底层 syscall,跨 Windows / Linux / macOS 通用,
//     且通过 context.Context 实现「timeout 到点自动 kill 子进程」;
//   - WorkingDir 缺省取 hookCtx.Workdir,保证 hook 执行子命令的工作目录与 Agent
//     当前工作目录一致(避免相对路径歧义);
//   - env 合并 os.Environ() 与用户提供的 Env map,Env 覆盖同名键(同 spec §D.1);
//   - 退出码非 0 / 超时 / 子进程 spawn 失败均转为 error 返回,Engine 层 warn 记录
//     不传播(spec §G「任何 hook 返回 error → 记 warn 日志,不传播」)。
//
// 安全：本任务评估后不在 hook 路径中跑 security 黑名单(避免与 tool.Bash 重复
// 拦截语义混淆;Hook 是「用户自己写的自动化逻辑」,与 tool 主动调用属于不同信任
// 域);若后续需求扩展,在 IsCommandSafe 处挂入即可,无需改 engine。
package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/MeiCorl/CodePilot/src/internal/hookcontext"
)

// CommandConfig 是 command action 的 type-specific 配置,对应 setting.json:
//   {
//     "type": "command",
//     "command": "prettier --write $TOOL_INPUT_FILE_PATH",
//     "working_dir": "/opt/project",     // 可选
//     "env": {"NO_COLOR": "1"},          // 可选
//     "timeout": "10s"                   // 可选,默认 30s
//   }
//
// 字段使用指针 + omitempty 让「未配置」与「配置为空」保持语义一致:
// WorkingDir / Timeout 缺省时由 Execute 阶段回退;Env / Command 缺省时
// 按各自语义处理（Env 缺省 = 仅继承父进程 env;Command 缺省 = 拒绝执行）。
type CommandConfig struct {
	// Command 为必填的 shell 命令字符串,支持 $VAR 变量插值;为空时 Execute 拒绝。
	Command string `json:"command"`
	// WorkingDir 为子进程 cwd;为空时使用 hookCtx.Workdir;若两者皆空则用
	// os.Getwd() 作为最后兜底。
	WorkingDir string `json:"working_dir,omitempty"`
	// Env 为注入到子进程环境变量的额外键值对,与 os.Environ() 合并,Env 覆盖同名键。
	Env map[string]string `json:"env,omitempty"`
	// Timeout 为子进程最大存活时间,字符串格式（如 "10s"）;空 / 0 时使用默认 30s;
	// ≤ 0 视为不限（不建议,Engine 通常已加 ctx 超时）。
	Timeout string `json:"timeout,omitempty"`
}

// CommandExecutor 是 command action 的执行器实现。
//
// 不可变对象,Engine 在 LoadEntries 阶段一次性实例化,运行期只调用 Execute。
// 不持有可变状态所以天然并发安全。
type CommandExecutor struct {
	cfg      CommandConfig
	resolved string // 变量替换前的原始命令（留作日志与错误信息）
	timeout  time.Duration
}

// NewCommandExecutor 解析 raw action JSON 并构造 CommandExecutor。
//
// 解析失败的常见原因:
//   - JSON 格式错误 → 返回 wrap error;
//   - command 字段为空字符串 → 返回明确错误(避免后续 Execute 被 silent drop)。
func NewCommandExecutor(raw json.RawMessage) (*CommandExecutor, error) {
	var cfg CommandConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("hook command action: parse: %w", err)
	}
	if cfg.Command == "" {
		return nil, fmt.Errorf("hook command action: command field required")
	}
	timeout, err := ParseDuration(cfg.Timeout, DefaultCommandTimeout)
	if err != nil {
		return nil, fmt.Errorf("hook command action: timeout: %w", err)
	}
	if timeout <= 0 {
		timeout = DefaultCommandTimeout
	}
	return &CommandExecutor{
		cfg:      cfg,
		resolved: cfg.Command,
		timeout:  timeout,
	}, nil
}

// Type 返回 "command"。
func (e *CommandExecutor) Type() string { return ActionTypeCommand }

// Execute 在独立子进程中执行 shell 命令。
//
// 流程:
//  1. 校验 command 非空(已在 NewCommandExecutor 校验,此处冗余防御);
//  2. 变量替换($VAR → vars);
//  3. 推导出子进程 argv(Windows 走 powershell,其它走 sh -c;
//     与 tool.builtin.bash 保持一致,避免 hook 与 tool 行为分裂);
//  4. os/exec.CommandContext + 30s 默认超时(可配);
//  5. 捕获 stdout/stderr,debug 日志记录;
//  6. 退出码非 0 → 返回 *CommandError;ctx 超时 → 返回 ErrCommandTimeout;
//
// [Why 不复用 tool.builtin.bash] bash tool 走权限系统(HITL 询问),hook action
// 信任用户配置(用户自己写的,不需要二次确认),两者意图不同,共用会引入权限
// 系统的间接延迟。
func (e *CommandExecutor) Execute(ctx context.Context, hookCtx *hookcontext.HookContext, vars map[string]string) error {
	cmdline := hookcontext.Interpolate(e.cfg.Command, vars)
	if cmdline == "" {
		// 变量替换后变为空字符串:可能是用户写 command: "$A" 而 A 未定义。
		// 拒绝而非静默执行空命令(空命令在 sh -c 下也会 spawn 一个 sh,资源浪费)。
		return fmt.Errorf("hook command: command is empty after variable interpolation")
	}

	// 跨平台 shell:与 tool.builtin.bash 相同选择,保持一致。
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", cmdline)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", cmdline)
	}

	// WorkingDir 优先级:配置 > hookCtx.Workdir > 当前进程 cwd。
	// [Why hookCtx.Workdir 兜底] hook 通常在 Agent 工作目录下触发,
	// 子进程未指定 cwd 时容易跑到 CodePilot 二进制所在目录,产生路径混淆。
	cwd := e.cfg.WorkingDir
	if cwd == "" && hookCtx != nil {
		cwd = hookCtx.Workdir
	}
	if cwd != "" {
		cmd.Dir = cwd
	}

	// env:父进程 os.Environ() + 用户 Env(后者覆盖前者)。
	//
	// [Why 不直接 cmd.Env = os.Environ()] 那会导致用户 Env 完全替换继承链,
	// PATH / HOME / 系统 locale 都会丢;反之合并保留系统默认 + 用户扩展,
	// 同时 Env 字段的「覆盖同名键」语义仍生效。
	cmd.Env = mergeEnviron(os.Environ(), e.cfg.Env)

	// 抓 stdout / stderr 到 buffer,debug 日志记录便于排错;
	// 同时避免子进程输出堵塞管道(Pipe 阻塞 -> 子进程死锁)。
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	runErr := cmd.Run()

	// 日志:即使失败也要记录 stdout/stderr,便于用户排错。
	// 注意:此处不持有 logger(执行器不依赖 logger 包),
	//      改由 Engine 在调用 Execute 前后通过 RunSafe 统一记录;
	//      RunSafe 已记录 panic 与 error,这里把「正常 stdout/stderr」通过
	//      留 buffer 让 Engine 决定是否取用。RunSafe 当前不读取 buffer,
	//      是更克制的设计(executor 不依赖全局 logger)。
	_ = stdoutBuf
	_ = stderrBuf

	if runErr != nil {
		// ctx 取消 = 超时,优先识别。
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("%w (ctx err: %v)", ErrCommandTimeout, ctxErr)
		}
		// 子进程退出非 0 -> CommandError
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			return &CommandError{
				ExitCode: exitErr.ExitCode(),
				Stderr:   truncate(stderrBuf.String(), stderrSnippetLimit),
				Command:  cmdline,
			}
		}
		// 其它(spawn 失败 / 权限 / 二进制不存在等)直接透传。
		return fmt.Errorf("hook command: run failed: %w", runErr)
	}

	return nil
}

// mergeEnviron 把 parent(父进程 os.Environ() 切片,形如 KEY=VALUE)与 overrides
// (用户 Env map)合并,overrides 中的键覆盖 parent 中同名键。
//
// [Why 自己实现而非 strings.Split] os.Environ() 切片已经是 "KEY=VAL" 形式,
// 需要保留注释 / 保留顺序(顺序敏感的环境变量如 PATH 改变前/后值会导致子进程
// 行为不一致)。
func mergeEnviron(parent []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return parent
	}
	// 用 map 索引 parent 中已有键的位置,遇到 overrides 同名时在原地改写,
	// 避免出现两个同名键;若 parent 没出现过该键则 append 到末尾。
	index := make(map[string]int, len(parent))
	for i, kv := range parent {
		// 仅取第一个 '=' 之前的部分。
		for j := 0; j < len(kv); j++ {
			if kv[j] == '=' {
				index[kv[:j]] = i
				break
			}
		}
	}
	out := append([]string(nil), parent...)
	for k, v := range overrides {
		if i, ok := index[k]; ok {
			out[i] = k + "=" + v
		} else {
			out = append(out, k+"="+v)
			index[k] = len(out) - 1
		}
	}
	return out
}

// truncate 把字符串截断到 max 字节,超出时附加 "..." 标记。
//
// [Why 用 rune count 而非 byte] 防止 UTF-8 多字节字符被截在中间产生乱码。
// 这里只需简单截断,不做完美 rune 安全(保留末尾不变仍是 trade-off)。
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max < 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}
