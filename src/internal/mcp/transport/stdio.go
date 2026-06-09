package transport

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// StdioConfig 是 stdio Transport 的配置。
type StdioConfig struct {
	// Command 要启动的可执行文件路径（必填）。
	Command string
	// Args 传给子进程的命令行参数。
	Args []string
	// Env 额外注入到子进程的环境变量（在父进程 os.Environ() 基础上覆盖同名键）。
	Env map[string]string
	// Workdir 子进程工作目录，空字符串继承父进程。
	Workdir string
	// Stderr 子进程 stderr 流向，nil 时丢弃（避免污染父进程 stderr）。
	Stderr io.Writer
	// CloseTimeout Close 等待子进程优雅退出的最大时间，0 表示 5s。
	CloseTimeout time.Duration
	// MaxLineBytes 单条 JSONL 消息的最大字节数，0 表示 4MB。
	// 超过此值的行会被 scanner 报错丢弃，防止恶意 server 发送超长行耗尽内存。
	MaxLineBytes int
}

// stdioTransport 通过 os/exec 启动本地子进程，
// stdin 写 JSONL、stdout 读 JSONL、stderr 可选重定向。
//
// 并发模型：
//   - 内部 sync.Mutex 保护 cmd / stdin / alive / closed 等字段
//   - Recv 启动短命 goroutine 跑 scanner.Scan()，避免 scanner 阻塞时与 Close 死锁
//   - 后台 goroutine 等待子进程退出，触发 done 通道让 Recv 立即返回 EOF
type stdioTransport struct {
	cfg StdioConfig

	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	scanner  *bufio.Scanner
	done     chan struct{} // 子进程退出信号
	alive    bool
	closed   bool
	closeErr error
}

// NewStdio 构造 stdio Transport。需调用 Connect 建立连接。
func NewStdio(cfg StdioConfig) *stdioTransport {
	if cfg.CloseTimeout == 0 {
		cfg.CloseTimeout = 5 * time.Second
	}
	if cfg.MaxLineBytes == 0 {
		cfg.MaxLineBytes = 4 * 1024 * 1024
	}
	return &stdioTransport{cfg: cfg}
}

// Connect 启动子进程并就绪 stdin/stdout。
//
// 流程：
//  1. 构造 *exec.Cmd，合并 Env
//  2. 拿 stdin / stdout pipe
//  3. cmd.Start() 启动子进程
//  4. 启动后台 goroutine 等待子进程退出并更新 alive 状态
//
// 失败时返回错误，Transport 仍处于未连接状态（不破坏既有内部状态）。
func (t *stdioTransport) Connect(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.alive {
		return nil
	}
	if t.cfg.Command == "" {
		return fmt.Errorf("stdio: Command 不能为空")
	}

	cmd := exec.CommandContext(context.Background(), t.cfg.Command, t.cfg.Args...)
	cmd.Dir = t.cfg.Workdir
	if len(t.cfg.Env) > 0 {
		// 复制父进程环境变量，再覆盖/追加用户指定项
		env := make([]string, 0, len(os.Environ())+len(t.cfg.Env))
		env = append(env, os.Environ()...)
		for k, v := range t.cfg.Env {
			env = append(env, k+"="+v)
		}
		cmd.Env = env
	}
	if t.cfg.Stderr != nil {
		cmd.Stderr = t.cfg.Stderr
	} else {
		cmd.Stderr = io.Discard
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdio: 获取 stdin 失败: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("stdio: 获取 stdout 失败: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return fmt.Errorf("stdio: 启动子进程失败: %w", err)
	}

	// 配置 scanner：增大 buffer 以支持 MCP 长消息（如 listTools 返回大量工具）
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), t.cfg.MaxLineBytes)

	t.cmd = cmd
	t.stdin = stdin
	t.scanner = scanner
	t.done = make(chan struct{})
	t.alive = true
	t.closed = false
	t.closeErr = nil

	// 后台 goroutine：等待子进程退出，更新 alive 与 closeErr
	go func() {
		waitErr := cmd.Wait()
		t.mu.Lock()
		t.alive = false
		t.closeErr = waitErr
		t.mu.Unlock()
		close(t.done)
	}()
	return nil
}

// Send 写入单行 JSON + '\n'。
//
// 行为：
//   - Transport 已 Close → 返回 ErrClosed
//   - 未 Connect 或子进程已死 → 返回 ErrNotConnected
//   - 写失败 → 标记 alive=false，返回包装错误
func (t *stdioTransport) Send(msg []byte) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return ErrClosed
	}
	stdin, alive := t.stdin, t.alive
	t.mu.Unlock()
	if stdin == nil || !alive {
		return ErrNotConnected
	}
	// 追加 '\n' 形成 JSONL
	line := make([]byte, 0, len(msg)+1)
	line = append(line, msg...)
	line = append(line, '\n')
	if _, err := stdin.Write(line); err != nil {
		t.markDead()
		return fmt.Errorf("stdio: 写入失败: %w", err)
	}
	return nil
}

// Recv 阻塞读取下一条 JSONL（不含末尾换行）。
//
// 实现：
//   - 启动短命 goroutine 跑 scanner.Scan()（scanner 自身是阻塞 IO）
//   - select 等待扫描结果 / 子进程退出信号
//   - 子进程退出时立即返回 io.EOF，避免 Recv 与 Close 死锁
func (t *stdioTransport) Recv() ([]byte, error) {
	t.mu.Lock()
	scanner := t.scanner
	done := t.done
	t.mu.Unlock()
	if scanner == nil {
		return nil, ErrNotConnected
	}

	type scanResult struct {
		data []byte
		err  error
	}
	resCh := make(chan scanResult, 1)
	go func() {
		if scanner.Scan() {
			// 复制 bytes 避免 scanner 内部 buffer 复用导致 race
			data := make([]byte, len(scanner.Bytes()))
			copy(data, scanner.Bytes())
			resCh <- scanResult{data: data}
			return
		}
		// Scan 返回 false：要么 err 非 nil（畸形行 / 超长），要么正常 EOF
		err := scanner.Err()
		if err == nil {
			err = io.EOF
		}
		resCh <- scanResult{err: err}
	}()

	select {
	case r := <-resCh:
		return r.data, r.err
	case <-done:
		// 子进程已退出，scanner 不会再有新数据；返回 EOF 唤醒调用方
		return nil, io.EOF
	}
}

// Close 优雅关闭 Transport：先关 stdin 触发子进程 EOF，超时后强杀。
// 多次调用幂等，返回第一次 Close 的错误。
func (t *stdioTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		err := t.closeErr
		t.mu.Unlock()
		return err
	}
	t.closed = true
	stdin := t.stdin
	cmd := t.cmd
	done := t.done
	t.mu.Unlock()

	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd == nil || done == nil {
		return nil
	}
	// 等待子进程退出或超时
	select {
	case <-done:
		t.mu.Lock()
		err := t.closeErr
		t.mu.Unlock()
		return err
	case <-time.After(t.cfg.CloseTimeout):
		// 超时强杀，避免僵尸子进程
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			<-done // 等彻底退出后回收资源
		}
		return nil
	}
}

// IsAlive 查询当前连接是否健康。
func (t *stdioTransport) IsAlive() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.alive && !t.closed
}

// markDead 在写入失败时主动标记 alive=false，让后续 Send/Recv 立即失败。
func (t *stdioTransport) markDead() {
	t.mu.Lock()
	t.alive = false
	t.mu.Unlock()
}
