// Package session 的一部分：多 server 连接池。
//
// Pool 的职责：
//   - 启动阶段：并发把多个 server 拉起，单 server 失败不影响其他
//   - 运行阶段：按 name 复用 Session 实例（连接池复用，无重连）
//   - 状态查询：HealthyNames / Unhealthy 供 WebUI 状态栏使用
//
// 与 Task 7 重连策略的关系：
//   - Task 5 阶段只关注「启动时建连失败」→ 标记 unhealthy
//   - 运行期 transport 断开由 Session.healthState 跟踪，Task 7 会接入重连
//
// 与 Task 8 配置接入的关系：
//   - ServerConfig 是 Pool 内部的轻量描述，不依赖 mcp/config 包
//   - Task 8 的 mcp/config.BuildTransports 会构造 Transport 后转成 ServerConfig 喂给 Pool
package session

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"github.com/MeiCorl/CodePilot/src/internal/mcp/transport"
)

// DefaultHandshakeTimeout 单 server 握手的默认超时。
//
// 30s 与 setting.json 的 tool_execution_timeout_seconds 默认值对齐。
// spec 显式提及默认 30s，便于 stdio / http 共用同一值。
const DefaultHandshakeTimeout = 30 * time.Second

// ServerConfig 是 Pool 的输入描述：单个 server 启动所需的最小信息。
//
// 与 Task 8 即将引入的 MCPServerConfig（解析自 setting.json）解耦：
// Pool 不关心配置来源，只接受「name + 构造好的 Transport + 超时」三要素。
type ServerConfig struct {
	// Name server 唯一标识（用于查找 / 日志 / WebUI 状态栏）。
	Name string
	// Transport 必须已经构造好（stdio / http）但**未 Connect**，
	// Pool 内部负责调用 Connect + Initialize。
	Transport transport.Transport
	// Timeout 单 server 握手超时（Connect + Initialize + ListTools 总耗时）。
	// 0 表示使用 DefaultHandshakeTimeout。
	Timeout time.Duration
	// ReconnectFactory 注入到 Session 的 transport 重建工厂。
	// 非 nil 时 Pool.RegisterAndStart 内部用 session.WithReconnectFactory 包装。
	// Task 7 之前可以传 nil（无重连能力）；Task 8 接入主流程后通常由
	// mcp/config.BuildTransports 注入。
	ReconnectFactory func() (transport.Transport, error)
}

// unhealthyRecord 记录一个启动失败的 server 及其原因。
type unhealthyRecord struct {
	Reason string
	At     time.Time
}

// Pool 是多 server Session 的容器。
//
// 线程安全：所有方法均可并发调用。
// 生命周期：NewPool → InitializeAll（可多次调用追加 server）→ CloseAll。
type Pool struct {
	logger *zap.Logger

	mu        sync.RWMutex
	sessions  map[string]*Session        // 握手成功的 server
	unhealthy map[string]*unhealthyRecord // 启动失败的 server

	closed atomic.Bool
	// initializing 标记当前是否正处于 InitializeAll 执行期间。
	// 供 WebUI 状态栏展示 MCP "连接中…" loading 态：主流程把 MCP 初始化放到
	// 后台 goroutine 后，前端通过 mcp_status.Loading 字段拿到该状态。
	// [Why] 用 atomic.Bool 而非 mu：与 closed 同风格，InitializeAll 的
	// 入口/出口 Store，WebUI 层高频只读 Load，避免与 sessions/unhealthy 的
	// 读写锁竞争。零值 false = 未开始初始化，语义自洽。
	initializing atomic.Bool
}

// Initializing 返回当前是否处于 InitializeAll 执行期间。
// 供 WebUI 区分三元状态：mcpPool==nil → 未启用 MCP；
// !=nil && Initializing()==true → 后台连接中；!=nil && Initializing()==false → 已就绪/已失败。
func (p *Pool) Initializing() bool {
	return p.initializing.Load()
}

// NewPool 构造一个空的 Pool。
// 需随后调用 InitializeAll 注入 server 配置。
func NewPool(logger *zap.Logger) *Pool {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Pool{
		logger:    logger.With(zap.String("component", "mcp_pool")),
		sessions:  make(map[string]*Session),
		unhealthy: make(map[string]*unhealthyRecord),
	}
}

// RegisterAndStart 同步拉起单个 server：Connect → Initialize → NotifyInitialized → ListTools。
//
// 行为：
//   - 成功：注册到 sessions map，返回 nil
//   - 失败：清理 transport，记 unhealthy，返回错误
//   - 重复 name：若已存在则直接返回现有 Session（幂等）
//
// 该方法可在 InitializeAll 内部并发调用，也支持外部单 server 调用。
func (p *Pool) RegisterAndStart(ctx context.Context, cfg ServerConfig) (*Session, error) {
	if p.closed.Load() {
		return nil, ErrSessionClosed
	}
	if cfg.Name == "" {
		return nil, fmt.Errorf("mcp pool: server name 不能为空")
	}
	if cfg.Transport == nil {
		return nil, fmt.Errorf("mcp pool: server %q Transport 不能为空", cfg.Name)
	}

	// 幂等：已存在则直接返回
	p.mu.RLock()
	if existing, ok := p.sessions[cfg.Name]; ok {
		p.mu.RUnlock()
		return existing, nil
	}
	p.mu.RUnlock()

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = DefaultHandshakeTimeout
	}
	handshakeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Step 1: Connect
	if err := cfg.Transport.Connect(handshakeCtx); err != nil {
		p.markUnhealthy(cfg.Name, fmt.Sprintf("Connect 失败: %v", err))
		return nil, fmt.Errorf("mcp pool: server %q Connect 失败: %w", cfg.Name, err)
	}

	// Step 2: NewSession + Start
	//
	// Start 必须用 context.Background() 而不是 handshakeCtx：
	// RegisterAndStart 内部的 defer cancel() 会在函数返回时立即取消 handshakeCtx，
	// 导致 Session.recvLoop 在第一次循环就因 ctx.Err() 退出，业务侧所有 pending
	// 全部 failAllPending 唤醒返回 transport 断开错误。
	//
	// Step 8 接入：构造 Session 时按需注入 ReconnectFactory，让 EnsureHealthy
	// 在 transport 断开时能按原配置重建 transport 并按 backoff 重试。
	var sessOpts []SessionOption
	if cfg.ReconnectFactory != nil {
		sessOpts = append(sessOpts, WithReconnectFactory(cfg.ReconnectFactory))
	}
	var sess *Session
	if len(sessOpts) > 0 {
		sess = NewSessionWithOptions(cfg.Name, cfg.Transport, p.logger, sessOpts...)
	} else {
		sess = NewSession(cfg.Name, cfg.Transport, p.logger)
	}
	sess.Start(context.Background())

	// Step 3: Initialize
	if _, err := sess.Initialize(handshakeCtx); err != nil {
		_ = sess.Close()
		p.markUnhealthy(cfg.Name, fmt.Sprintf("Initialize 失败: %v", err))
		return nil, fmt.Errorf("mcp pool: server %q Initialize 失败: %w", cfg.Name, err)
	}

	// Step 4: NotifyInitialized（失败也允许继续，部分 server 不严格校验）
	if err := sess.NotifyInitialized(handshakeCtx); err != nil {
		p.logger.Warn("发送 notifications/initialized 失败",
			zap.String("server", cfg.Name),
			zap.Error(err))
	}

	// Step 5: ListTools（预热：失败也允许继续，注册时再重试）
	if _, err := sess.ListTools(handshakeCtx); err != nil {
		p.logger.Warn("ListTools 预热失败",
			zap.String("server", cfg.Name),
			zap.Error(err))
	}

	// 注册到 sessions（清理 unhealthy 标记）
	p.mu.Lock()
	if existing, ok := p.sessions[cfg.Name]; ok {
		// 极端并发：两个 goroutine 同时到这里，保留先到的
		p.mu.Unlock()
		_ = sess.Close()
		return existing, nil
	}
	p.sessions[cfg.Name] = sess
	delete(p.unhealthy, cfg.Name)
	p.mu.Unlock()

	p.logger.Info("MCP server 握手成功",
		zap.String("server", cfg.Name))
	return sess, nil
}

// markUnhealthy 记录一个 server 启动失败。
func (p *Pool) markUnhealthy(name, reason string) {
	p.mu.Lock()
	p.unhealthy[name] = &unhealthyRecord{
		Reason: reason,
		At:     time.Now(),
	}
	p.mu.Unlock()
	p.logger.Warn("MCP server 标记为 unhealthy",
		zap.String("server", name),
		zap.String("reason", reason))
}

// InitializeAll 并发启动所有配置的 server。
//
// 设计要点：
//   - 用 errgroup 驱动并发，但内部 swallow 错误（每个 server 单独 try）
//   - 整体不返回 error：单个 server 失败仅记 unhealthy + log.Warn
//   - 重复 name：后启动的覆盖先启动的（合理处理配置变更场景）
//
// 调用时机：CodePilot 启动时由 main.go 调用一次；运行期如需热加载，
// 可再次调用（Pool 不限制调用次数）。
func (p *Pool) InitializeAll(ctx context.Context, configs []ServerConfig) error {
	if p.closed.Load() {
		return ErrSessionClosed
	}
	if len(configs) == 0 {
		return nil
	}

	// 标记进入初始化期，defer 保证无论成功/失败/panic 都复位为 false。
	// WebUI 的 buildMCPStatusPayload 据此输出 loading payload，初始化完成后
	// 即便有 server 不健康，loading 也必须变 false（避免前端永久卡在"连接中"）。
	p.initializing.Store(true)
	defer p.initializing.Store(false)

	var eg errgroup.Group
	for _, cfg := range configs {
		cfg := cfg // 循环变量捕获
		eg.Go(func() error {
			if _, err := p.RegisterAndStart(ctx, cfg); err != nil {
				// 失败已在 RegisterAndStart 内部记 unhealthy + log，
				// 这里不向上抛（保持 InitializeAll "整体不返回错误" 契约）
				p.logger.Debug("server 启动失败（不影响其他）",
					zap.String("server", cfg.Name),
					zap.Error(err))
			}
			return nil
		})
	}
	// eg.Wait() 在所有 goroutine 返回 nil 时也返回 nil
	_ = eg.Wait()
	return nil
}

// Get 按 server 名称查找 Session。
// 未注册（name 不存在或该 server 启动失败）返回 (nil, false)。
func (p *Pool) Get(name string) (*Session, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	s, ok := p.sessions[name]
	return s, ok
}

// Names 返回所有已知 server 名称（含 unhealthy）。
// 主要用于 WebUI 状态栏与诊断日志。
func (p *Pool) Names() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, 0, len(p.sessions)+len(p.unhealthy))
	for name := range p.sessions {
		out = append(out, name)
	}
	for name := range p.unhealthy {
		out = append(out, name)
	}
	return out
}

// HealthyNames 返回所有健康 server 名称。
//
// 判定依据：sessions map 中存在即视为 healthy（启动时握手成功）。
// 运行期 transport 断开由 Session.healthState 跟踪，由调用方按需查询。
func (p *Pool) HealthyNames() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, 0, len(p.sessions))
	for name := range p.sessions {
		out = append(out, name)
	}
	return out
}

// Unhealthy 返回启动失败 server 的 name → reason 映射。
// 调用方不应修改返回值（map 浅拷贝仅 copy key/value 指针）。
func (p *Pool) Unhealthy() map[string]string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.unhealthy) == 0 {
		return nil
	}
	out := make(map[string]string, len(p.unhealthy))
	for name, rec := range p.unhealthy {
		out[name] = rec.Reason
	}
	return out
}

// CloseAll 优雅关闭所有 Session。
//
// 行为：
//   - 标记 Pool 为 closed，后续 RegisterAndStart / InitializeAll 立即返回错误
//   - 并发关闭所有 sessions map 中的 Session
//   - 清空 sessions / unhealthy map
//
// 多次调用幂等。
func (p *Pool) CloseAll(_ context.Context) error {
	if !p.closed.CompareAndSwap(false, true) {
		return nil
	}

	p.mu.Lock()
	sessions := make([]*Session, 0, len(p.sessions))
	for _, s := range p.sessions {
		sessions = append(sessions, s)
	}
	p.sessions = make(map[string]*Session)
	p.unhealthy = make(map[string]*unhealthyRecord)
	p.mu.Unlock()

	// 并发关闭
	var wg sync.WaitGroup
	for _, s := range sessions {
		wg.Add(1)
		go func(sess *Session) {
			defer wg.Done()
			if err := sess.Close(); err != nil {
				p.logger.Warn("关闭 session 失败",
					zap.String("server", sess.Name()),
					zap.Error(err))
			}
		}(s)
	}
	wg.Wait()
	return nil
}
