// Package session 核心实现：单 server Session 生命周期管理与 RPC 调度。
//
// 并发模型：
//   - 每个 Session 启动时由调用方调用 Start 拉起后台 recvLoop goroutine
//   - 业务侧并发调用 Initialize / ListTools / CallTool 各自挂入 pending map
//   - recvLoop 持续从 Transport 读消息，按 id 投递到对应等待方
//
// 错误传播路径：
//   - ctx 取消       → 该请求立刻返回 ctx.Err()，pending 清理
//   - Transport 断开 → recvLoop 关闭所有 pending errCh，调用方收到 ErrSessionClosed
//   - Session.Close   → 同上，并额外把 Session 标记为关闭态
//   - server 错误     → 该请求收到 *RPCError，其他 pending 不受影响
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/MeiCorl/CodePilot/src/internal/mcp/jsonrpc"
	"github.com/MeiCorl/CodePilot/src/internal/mcp/reconnect"
	"github.com/MeiCorl/CodePilot/src/internal/mcp/transport"
)

// HealthState 描述 Session 当前与远端的连接健康度。
//
// 当前 Task 4 仅使用 Healthy；Reconnecting / Unhealthy 由 Task 7 重连策略维护。
// 用 atomic.Int32 读写以避免锁开销。
type HealthState int32

const (
	// HealthHealthy 正常状态：Transport 健康、握手已完成。
	HealthHealthy HealthState = iota
	// HealthReconnecting 正在尝试重连（Task 7 使用）。
	HealthReconnecting
	// HealthUnhealthy 永久不可用，需重启 CodePilot（Task 7 使用）。
	HealthUnhealthy
)

// pendingCall 是单个 in-flight 请求的等待槽位。
//
// 业务侧 goroutine 在 request() 中 select 同时监听：
//   - respCh：recvLoop 投递的 Response
//   - errCh ：ctx 取消 / transport 断开 / session 关闭
//
// 二者择一消费：respCh 与 errCh 不会同时投递。
type pendingCall struct {
	respCh chan *jsonrpc.Response
	errCh  chan error
}

// Session 是单个 MCP server 的高层会话。
//
// 生命周期：
//   - 构造：NewSession(name, transport)
//   - 启动：Start(ctx) 启动 recvLoop（必须在调用 RPC 前）
//   - 握手：Initialize → NotifyInitialized
//   - 使用：ListTools / CallTool
//   - 关闭：Close()
type Session struct {
	name      string
	transport transport.Transport
	logger    *zap.Logger

	// idGen 全局唯一 id 生成器（jsonrpc.NewIDGenerator 内部用 atomic counter）。
	idGen *jsonrpc.IDGenerator

	// pendingMu 保护 pending map 与 closeCh 上的关闭广播。
	pendingMu sync.Mutex
	pending   map[string]*pendingCall

	// closed 用 atomic.Bool 走快速路径：避免每次 RPC 都抢锁。
	closed atomic.Bool

	// recvDone 在 recvLoop 退出时 close；Close() 等待该信号。
	recvDone chan struct{}

	// health 当前健康状态（Task 4 仅维护 HealthHealthy）。
	health atomic.Int32

	// toolsCacheMu 保护 toolsCached / toolsCachedAt 的并发访问。
	//
	// 设计要点：tools/list 的远端调用相对昂贵（一次完整 JSON-RPC 往返），
	// 且远端工具列表在 server 进程生命周期内基本稳定，60s TTL 既能避免
	// 频繁拉取、又能在 server 端动态新增工具时合理刷新。
	toolsCacheMu  sync.Mutex
	toolsCached   []MCPTool // 上次 ListTools 成功返回的快照
	toolsCachedAt time.Time // 缓存命中起始时间（time.Time 零值视为未缓存）

	// reconnectFactory 重建 transport 的工厂函数（Task 7 重连策略）。
	//
	// Session 自身不知道如何构造 stdio / http transport：stdio 需要重新
	// spawn 子进程，http 需要重建 HTTP client。构造时由调用方注入工厂，
	// EnsureHealthy 断开时调用该工厂获取新 transport。
	//
	// nil 时 EnsureHealthy 收到断开会立即返回 error（无重连能力）。
	reconnectFactory func() (transport.Transport, error)

	// reconnectBackoff 退避节奏（默认 1s/3s/9s，可被 SessionOption 覆盖）。
	//
	// nil 时 EnsureHealthy 内部 fallback 到 reconnect.NewDefaultBackoff()。
	reconnectBackoff *reconnect.Backoff

	// reconnectMu 保护「重连流程」的串行化：防止多个 goroutine 同时触发
	// EnsureHealthy 时并发重建 transport。EnsureHealthy 在持锁期间切换
	// healthState 与 transport，避免与 request() 抢锁的复杂度。
	reconnectMu sync.Mutex

	// startCtx 启动时使用的 context（用于重连时重启 recvLoop）。
	//
	// 缓存而非每次都从外部传入的原因：调用方（Pool）在启动 Session 时
	// 传入的 ctx 可能在重连时已取消；保留原 ctx 让重连继续使用同一生命周期。
	startCtx context.Context
}

// ToolsCacheTTL 是 ListToolsCached 的默认缓存时长。
//
// spec 明确要求 60s：在 Agent Loop 高频会话刷新场景下大幅减少
// 远端 server 压力；超过 TTL 后下次调用走真实 RPC 并重置缓存。
const ToolsCacheTTL = 60 * time.Second

// NewSession 构造一个未启动的 Session。
//
// 注意：构造后必须调用 Start 才会启动 recvLoop，否则后续 RPC 会因
// 没有消费者而永久挂起。
//
// 该函数保留为无选项的便捷入口；如需配置重连工厂或自定义退避节奏，
// 使用 NewSessionWithOptions。
func NewSession(name string, t transport.Transport, logger *zap.Logger) *Session {
	return NewSessionWithOptions(name, t, logger)
}

// SessionOption 是 Session 的可选配置（functional options 模式）。
//
// 用法：
//
//	sess := session.NewSessionWithOptions(name, t, logger,
//	    session.WithReconnectFactory(factory),
//	    session.WithReconnectBackoff(customBackoff),
//	)
type SessionOption func(*Session)

// WithReconnectFactory 注入 transport 重建工厂。
//
// 工厂在 EnsureHealthy 触发时调用：返回的 transport 必须未 Connect，
// 由 EnsureHealthy 内部负责 Connect + 重新握手。
//
// 传 nil 不会修改现有工厂（防御性：避免误清空）。
func WithReconnectFactory(factory func() (transport.Transport, error)) SessionOption {
	return func(s *Session) {
		if factory != nil {
			s.reconnectFactory = factory
		}
	}
}

// WithReconnectBackoff 注入自定义退避节奏。
//
// 传 nil 不会修改现有退避器（防御性）；空 intervals 时回退到默认 1s/3s/9s。
func WithReconnectBackoff(b *reconnect.Backoff) SessionOption {
	return func(s *Session) {
		if b != nil {
			s.reconnectBackoff = b
		}
	}
}

// NewSessionWithOptions 构造一个带可选配置的未启动 Session。
//
// 流程：
//  1. 设置必填字段
//  2. 依次应用 opts
//  3. reconnectBackoff 仍为 nil 时 fallback 到默认 1s/3s/9s
func NewSessionWithOptions(name string, t transport.Transport, logger *zap.Logger, opts ...SessionOption) *Session {
	if logger == nil {
		logger = zap.NewNop()
	}
	s := &Session{
		name:      name,
		transport: t,
		logger:    logger.With(zap.String("mcp_server", name)),
		idGen:     jsonrpc.NewIDGenerator(),
		pending:   make(map[string]*pendingCall),
		recvDone:  make(chan struct{}),
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.reconnectBackoff == nil {
		s.reconnectBackoff = reconnect.NewDefaultBackoff()
	}
	return s
}

// Name 返回 server 名称（用于日志 / 状态栏展示）。
func (s *Session) Name() string { return s.name }

// Health 返回当前健康状态。
func (s *Session) Health() HealthState {
	return HealthState(s.health.Load())
}

// markHealth 原子设置健康状态（仅 Session 内部使用）。
func (s *Session) markHealth(h HealthState) { s.health.Store(int32(h)) }

// markReconnectingOnExit 在 recvLoop 退出时调用。
//
// 行为：
//   - 若 Session 整体已 Close → 不修改 healthState（正常关闭路径）
//   - 若 Session 仍处于 healthy → 切换为 reconnecting（异常断开路径）
//   - 若 Session 已经是 reconnecting / unhealthy → 不修改
//
// 通过 CAS 保护「从 healthy 切换到 reconnecting」的原子性：
// 多个并发失败路径同时调用本方法时，只有第一个把 healthy 改为 reconnecting。
func (s *Session) markReconnectingOnExit() {
	if s.closed.Load() {
		s.logger.Debug("markReconnectingOnExit skip: closed")
		return
	}
	if s.health.CompareAndSwap(int32(HealthHealthy), int32(HealthReconnecting)) {
		s.logger.Info("healthState: healthy -> reconnecting")
	}
}

// Start 启动后台 recvLoop goroutine。
//
// 该方法立即返回，不会等待 transport 实际可用；调用方应通过
// transport.Connect / Initialize 的成功与否判断底层是否就绪。
//
// 重启语义（Task 7 重连）：
//   - 首次调用：缓存 ctx、分配 recvDone、启动 recvLoop
//   - 后续调用：若 recvLoop 仍在跑（recvDone 未关闭）→ 幂等返回
//   - 后续调用：若 recvLoop 已退出（recvDone 已关闭）→ 分配新 recvDone 并重启
//     此路径专门给 EnsureHealthy 重连后使用
//
// Session 整体 Close 后（s.closed=true）Start 立即返回，不再启动。
func (s *Session) Start(ctx context.Context) {
	if s.closed.Load() {
		return
	}
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()

	// 已关闭的 Session 不再启动
	if isClosed(s.recvDone) {
		// 区分「整体已 Close」和「重连后 recvLoop 退出」
		// 整体 Close 时 s.closed=true，已在函数入口拦截
		// 走到这里说明是重连路径：分配新 recvDone 并启动
		s.recvDone = make(chan struct{})
		s.startCtx = ctx
		go s.recvLoop(ctx)
		return
	}

	// 首次启动：缓存 ctx 供重连时复用
	if s.startCtx == nil {
		s.startCtx = ctx
		go s.recvLoop(ctx)
		return
	}

	// 后续调用：recvLoop 还在跑（recvDone 未关闭），幂等返回
}

// recvLoop 后台 goroutine：从 transport 持续读取消息并按 id 投递给等待方。
//
// 退出条件：
//   - transport.Recv 返回 io.EOF（对端正常关闭）
//   - transport.Recv 返回 ctx.Err()（ctx 取消）
//   - transport.Recv 返回其他错误（异常断开）
//
// 退出时关闭所有 pending errCh，让所有等待方立即收到错误。
//
// 退出语义（Task 7 重连）：
//   - 若 Session 整体已 Close（s.closed=true）→ 不修改 healthState
//   - 若 Session 未 Close（transport 异常断开）→ 设置 healthState=reconnecting
//     下次 EnsureHealthy 触发 lazy 重连
func (s *Session) recvLoop(ctx context.Context) {
	defer close(s.recvDone)

	for {
		// 检测 ctx 取消：在 Recv 阻塞前先快速判断一次
		if err := ctx.Err(); err != nil {
			s.failAllPending(err)
			s.markReconnectingOnExit()
			return
		}

		data, err := s.transport.Recv()
		if err != nil {
			// 任意错误都视为 transport 不可用 → 关闭所有 pending
			// 包括正常关闭（io.EOF）与异常断开（wrapped IO error）
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				s.failAllPending(err)
			} else {
				s.failAllPending(fmt.Errorf("mcp: transport 断开: %w", err))
			}
			// Task 7：异常断开时主动标记 reconnecting（仅在 Session 未 Close 时）
			s.markReconnectingOnExit()
			return
		}

		// 反序列化消息
		msg, err := jsonrpc.UnmarshalMessage(data)
		if err != nil {
			// 单条畸形消息：仅记日志，不影响其他 pending
			s.logger.Warn("收到畸形 JSON-RPC 消息，已忽略",
				zap.ByteString("raw", data),
				zap.Error(err))
			continue
		}

		// 当前 Session 只关心 Response；server-initiated notification 留待后续
		resp, ok := msg.(*jsonrpc.Response)
		if !ok {
			s.logger.Debug("忽略非 Response 消息",
				zap.String("kind", fmt.Sprintf("%T", msg)))
			continue
		}

		// 按 id 投递
		s.deliverResponse(resp)
	}
}

// deliverResponse 把 Response 投递给对应 pendingCall。
//
// 未找到对应 pending（id 不在 map）记 warn 级别日志：通常是请求已超时
// 被清理，但响应才到达；属于正常情况而非协议错误。
func (s *Session) deliverResponse(resp *jsonrpc.Response) {
	s.pendingMu.Lock()
	pc, ok := s.pending[resp.ID]
	if ok {
		delete(s.pending, resp.ID)
	}
	s.pendingMu.Unlock()

	if !ok {
		s.logger.Warn("收到无主响应（可能已超时清理）",
			zap.String("id", resp.ID))
		return
	}

	// 非阻塞投递：respCh 容量 1，若调用方已退出（极少见）则丢弃
	select {
	case pc.respCh <- resp:
	default:
		s.logger.Warn("pendingCall 接收通道已满，丢弃响应",
			zap.String("id", resp.ID))
	}
}

// failAllPending 关闭所有 pending 的 errCh，广播一个共同错误。
// recvLoop 退出前必调用，让所有等待方立刻唤醒。
func (s *Session) failAllPending(cause error) {
	s.pendingMu.Lock()
	pending := s.pending
	s.pending = make(map[string]*pendingCall)
	s.pendingMu.Unlock()

	for _, pc := range pending {
		select {
		case pc.errCh <- cause:
		default:
		}
	}
	if len(pending) > 0 {
		s.logger.Warn("recvLoop 退出，关闭所有 pending 请求",
			zap.Int("count", len(pending)),
			zap.Error(cause))
	}
}

// request 内部通用 RPC 入口：发请求 → 等响应。
//
// 行为约定：
//   - 同步返回 server 的 result（json.RawMessage）；server 返回 error 字段时返回 *RPCError
//   - ctx 取消立即返回 ctx.Err()
//   - transport 断开立即返回 transport 相关错误
//   - Session 已 Close 立即返回 ErrSessionClosed
//
// 调用方负责把 result 二次反序列化为目标类型。
//
// skipEnsure=true 跳过 EnsureHealthy 步骤，仅供 EnsureHealthy 内部（doReconnect
// → tryRebuildAndHandshake → Initialize）使用，避免重入 deadlock。
func (s *Session) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return s.requestInternal(ctx, method, params, false)
}

func (s *Session) requestInternal(ctx context.Context, method string, params any, skipEnsure bool) (json.RawMessage, error) {
	if s.closed.Load() {
		return nil, ErrSessionClosed
	}

	// Task 7：保证 transport 健康，必要时触发 lazy 重连
	// skipEnsure=true 时跳过此步（重连路径内部调用，reconnectMu 已持有）
	if !skipEnsure {
		if err := s.EnsureHealthy(ctx); err != nil {
			return nil, err
		}
	}

	// 序列化 params（如有）
	var rawParams json.RawMessage
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("mcp: 序列化 params 失败: %w", err)
		}
		rawParams = data
	}

	// 构造 Request
	id := s.idGen.Next()
	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		ID:      id,
		Method:  method,
		Params:  rawParams,
	}
	payload, err := jsonrpc.MarshalRequest(req)
	if err != nil {
		return nil, fmt.Errorf("mcp: 序列化 Request 失败: %w", err)
	}

	// 挂 pending：必须在 Send 之前完成，避免响应先到达
	pc := &pendingCall{
		respCh: make(chan *jsonrpc.Response, 1),
		errCh:  make(chan error, 1),
	}
	s.pendingMu.Lock()
	if s.closed.Load() {
		s.pendingMu.Unlock()
		return nil, ErrSessionClosed
	}
	s.pending[id] = pc
	s.pendingMu.Unlock()

	// 清理：函数返回前把 pending 从 map 中移除（防止响应延迟到达时找不到 pending）
	defer func() {
		s.pendingMu.Lock()
		delete(s.pending, id)
		s.pendingMu.Unlock()
	}()

	// 发送请求
	if err := s.transport.Send(payload); err != nil {
		return nil, fmt.Errorf("mcp: 发送 %s 失败: %w", method, err)
	}

	// 等待：resp / err / ctx
	select {
	case resp := <-pc.respCh:
		if resp.Error != nil {
			return nil, newRPCError(resp.Error.Code, resp.Error.Message, resp.Error.Data)
		}
		if len(resp.Result) == 0 {
			return nil, ErrNoResponse
		}
		return resp.Result, nil
	case err := <-pc.errCh:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// notify 发送无 id 的 Notification（不需要等待响应）。
//
// 常用于 notifications/initialized、notifications/cancelled 等。
// 当前 MCP 协议中 client 主动发的通知（initialized / cancelled / progress）
// 暂不需要 params；如未来需要，可在 params 非 nil 时序列化。
func (s *Session) notify(ctx context.Context, method string, params any) error {
	return s.notifyInternal(ctx, method, params, false)
}

// notifyNoEnsure 是 notify 的「跳过 EnsureHealthy」变体，专供 EnsureHealthy
// 内部使用（tryRebuildAndHandshake → NotifyInitialized），避免 EnsureHealthy
// 在 reconnectMu 持锁状态下重入。
func (s *Session) notifyNoEnsure(ctx context.Context, method string, params any) error {
	return s.notifyInternal(ctx, method, params, true)
}

func (s *Session) notifyInternal(ctx context.Context, method string, params any, skipEnsure bool) error {
	if s.closed.Load() {
		return ErrSessionClosed
	}

	// Task 7：保证 transport 健康，必要时触发 lazy 重连
	// skipEnsure=true 时跳过（重连路径内部调用，reconnectMu 已持有）
	if !skipEnsure {
		if err := s.EnsureHealthy(ctx); err != nil {
			return err
		}
	}

	if params != nil {
		// 当前未使用：保留入口以便未来扩展（如 notifications/cancelled 带 requestId）
		_ = params
	}

	n := &jsonrpc.Notification{
		JSONRPC: jsonrpc.Version,
		Method:  method,
	}
	payload, err := jsonrpc.MarshalNotification(n)
	if err != nil {
		return fmt.Errorf("mcp: 序列化 Notification 失败: %w", err)
	}
	if err := s.transport.Send(payload); err != nil {
		return fmt.Errorf("mcp: 发送 %s 失败: %w", method, err)
	}
	// ctx 仅用于检测调用方是否已放弃
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// ============================================================================
// 重连策略（Task 7）
// ============================================================================

// ErrReconnectNoFactory Session 缺少 transport 重建工厂时 EnsureHealthy 返回。
//
// 触发场景：构造 Session 时未通过 WithReconnectFactory 注入工厂，transport
// 断开后 EnsureHealthy 无法重建 → 立即报错。
// 修复方法：构造时调用 WithReconnectFactory(func() (Transport, error) { ... })。
var ErrReconnectNoFactory = errors.New("mcp: 未注入 reconnect 工厂，无法重建 transport")

// EnsureHealthy 保证 Session 处于可通信状态，必要时触发 lazy 重连。
//
// 行为：
//   - healthState==healthy → 立即返回 nil
//   - healthState==unhealthy → 立即返回 ErrServerUnhealthy（需重启 CodePilot）
//   - healthState==reconnecting → 进入重连循环：
//     1. Close 旧 transport
//     2. 调用 reconnectFactory 构造新 transport（若失败则按 backoff 重试）
//     3. Connect + Initialize + NotifyInitialized（失败按 backoff 重试）
//     4. 成功：markHealth(healthy)、失效 tools 缓存、return nil
//     5. 失败：sleep backoff(attempt)、attempt++、继续
//     6. 超过 maxAttempts：markHealth(unhealthy)、return ErrExhausted
//
// 并发安全：内部用 reconnectMu 串行化，多个 goroutine 同时调 EnsureHealthy
// 只有一个会执行重连循环，其余会等待并直接复用最终结果。
//
// 业务侧接入：request() / notify() 调用前自动调一次 EnsureHealthy，断开后
// 第一次 RPC 会触发重连，重连成功后该 RPC 正常执行；重连失败时该 RPC 返回
// ErrServerUnhealthy。
func (s *Session) EnsureHealthy(ctx context.Context) error {
	if s.closed.Load() {
		return ErrSessionClosed
	}

	// 快速路径：直接读 atomic 健康状态，避免无谓抢锁
	if s.Health() == HealthHealthy {
		return nil
	}

	// 永久不可用
	if s.Health() == HealthUnhealthy {
		return ErrServerUnhealthy
	}

	// 重连路径：加锁串行化
	s.reconnectMu.Lock()
	defer s.reconnectMu.Unlock()

	// 双重检查：持锁后再判一次（前面的快速路径可能已切换）
	switch s.Health() {
	case HealthHealthy:
		return nil
	case HealthUnhealthy:
		return ErrServerUnhealthy
	}

	if s.reconnectFactory == nil {
		s.markHealth(HealthUnhealthy)
		s.logger.Error("无法重连：未注入 reconnect 工厂",
			zap.String("server", s.name))
		return ErrReconnectNoFactory
	}

	// doReconnect 在重试耗尽时返回 reconnect.ErrExhausted，但 healthState 已被
	// 置为 unhealthy；外部语义上等同于 ErrServerUnhealthy。
	if err := s.doReconnect(ctx); err != nil {
		if errors.Is(err, reconnect.ErrExhausted) {
			return ErrServerUnhealthy
		}
		return err
	}
	return nil
}

// doReconnect 在 reconnectMu 持锁状态下执行重连循环。
//
// 约定：调用前 healthState 已是 reconnecting；调用成功后置为 healthy，
// 失败后置为 unhealthy 并返回对应错误。
func (s *Session) doReconnect(ctx context.Context) error {
	// 关闭旧 transport：清理 stdio 子进程 / HTTP 连接
	// 失败也允许继续：旧 transport 已断，重连本来就是替换
	if err := s.transport.Close(); err != nil {
		s.logger.Debug("关闭旧 transport 失败（忽略）",
			zap.String("server", s.name),
			zap.Error(err))
	}

	// 失效 tools 缓存：远端 server 可能已重启，工具列表可能变化
	s.InvalidateToolsCache()

	// 清空 pending：旧 recvLoop 已退出时已 failAllPending 过；此处防御性
	s.pendingMu.Lock()
	s.pending = make(map[string]*pendingCall)
	s.pendingMu.Unlock()

	maxAttempts := s.reconnectBackoff.MaxAttempts()
	var lastErr error
	for attempt := 0; ; attempt++ {
		// 越界检查
		if attempt >= maxAttempts {
			s.markHealth(HealthUnhealthy)
			s.logger.Warn("重连次数耗尽，server 标记为 unhealthy",
				zap.String("server", s.name),
				zap.Int("attempts", attempt),
				zap.Error(lastErr))
			return reconnect.ErrExhausted
		}

		// 尝试重建（构造 + Connect + Initialize + NotifyInitialized）
		if err := s.tryRebuildAndHandshake(ctx); err != nil {
			lastErr = err
			s.logger.Warn("重连失败，将按 backoff 重试",
				zap.String("server", s.name),
				zap.Int("attempt", attempt+1),
				zap.Int("max_attempts", maxAttempts),
				zap.Error(err))

			// sleep backoff(attempt)；ctx 取消立即返回
			delay, ok := s.reconnectBackoff.NextDelay(attempt)
			if !ok {
				// 理论上不会到这里（attempt < maxAttempts 时 NextDelay 总返回 ok=true）
				continue
			}
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				s.markHealth(HealthUnhealthy)
				return ctx.Err()
			}
			continue
		}

		// 成功
		s.markHealth(HealthHealthy)
		s.logger.Info("重连成功",
			zap.String("server", s.name),
			zap.Int("attempt", attempt+1))
		return nil
	}
}

// tryRebuildAndHandshake 单次重连尝试：工厂构造新 transport → Connect →
// Initialize + NotifyInitialized。
//
// 失败时负责回滚（关闭新 transport），不修改 healthState。
func (s *Session) tryRebuildAndHandshake(ctx context.Context) error {
	// 1. 工厂构造新 transport（未 Connect）
	newTransport, err := s.reconnectFactory()
	if err != nil {
		return fmt.Errorf("工厂构造 transport 失败: %w", err)
	}
	if newTransport == nil {
		return fmt.Errorf("工厂返回 nil transport")
	}

	// 2. Connect 新 transport
	if err := newTransport.Connect(ctx); err != nil {
		_ = newTransport.Close()
		return fmt.Errorf("新 transport Connect 失败: %w", err)
	}

	// 3. 替换 transport + 重启 recvLoop
	s.transport = newTransport
	s.startCtx = ctx
	s.Start(ctx)

	// 4. 重新 Initialize（必须：server 重启后需要重新握手）
	//
	// 走 requestInternal(skipEnsure=true) 而非 s.Initialize()，避免 EnsureHealthy
	// 重入导致 reconnectMu 死锁（EnsureHealthy → doReconnect → tryRebuildAndHandshake
	// → Initialize → request → EnsureHealthy 会再次尝试加锁）。
	hsCtx, cancel := context.WithTimeout(ctx, DefaultHandshakeTimeout)
	defer cancel()
	initParams := InitializeParams{
		ProtocolVersion: ProtocolVersion,
		Capabilities:    map[string]any{},
		ClientInfo: map[string]string{
			"name":    ClientName,
			"version": ClientVersion,
		},
	}
	raw, err := s.requestInternal(hsCtx, "initialize", initParams, true)
	if err != nil {
		return fmt.Errorf("重新 Initialize 失败: %w", err)
	}
	var initResult InitializeResult
	if err := json.Unmarshal(raw, &initResult); err != nil {
		return fmt.Errorf("重新 Initialize 响应解析失败: %w", err)
	}

	// 5. NotifyInitialized（失败也允许继续，与 Pool.RegisterAndStart 一致）
	if err := s.notifyNoEnsure(hsCtx, "notifications/initialized", nil); err != nil {
		s.logger.Warn("重新发送 notifications/initialized 失败",
			zap.String("server", s.name),
			zap.Error(err))
	}

	return nil
}

// ============================================================================
// 高层方法
// ============================================================================

// InitializeParams 是 initialize 请求的 params 结构。
type InitializeParams struct {
	ProtocolVersion string                 `json:"protocolVersion"`
	Capabilities    map[string]any         `json:"capabilities"`
	ClientInfo      map[string]string      `json:"clientInfo"`
}

// Initialize 发送 initialize 请求并返回 server capabilities / serverInfo。
//
// 必须在 ListTools / CallTool 之前调用，且与 NotifyInitialized 配对使用。
func (s *Session) Initialize(ctx context.Context) (*InitializeResult, error) {
	params := InitializeParams{
		ProtocolVersion: ProtocolVersion,
		Capabilities:    map[string]any{}, // 当前 client 不声明任何 capability
		ClientInfo: map[string]string{
			"name":    ClientName,
			"version": ClientVersion,
		},
	}
	raw, err := s.request(ctx, "initialize", params)
	if err != nil {
		return nil, err
	}
	var result InitializeResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("mcp: 反序列化 initialize 响应失败: %w", err)
	}
	result.Raw = raw
	return &result, nil
}

// NotifyInitialized 发送 notifications/initialized 通知。
//
// MCP 规范要求：client 收到 initialize 响应后必须发送该通知，
// 否则 server 可能不会响应后续 tools/list 等请求。
func (s *Session) NotifyInitialized(ctx context.Context) error {
	return s.notify(ctx, "notifications/initialized", nil)
}

// ListTools 拉取 server 暴露的工具列表。
//
// 实现注意：
//   - 当前 MCP 规范未强制分页，但保留 params.cursor 字段以备未来扩展
//   - 客户端不传 cursor：默认从第一页开始
func (s *Session) ListTools(ctx context.Context) ([]MCPTool, error) {
	raw, err := s.request(ctx, "tools/list", struct {
		Cursor string `json:"cursor,omitempty"`
	}{})
	if err != nil {
		return nil, err
	}
	// MCP tools/list 响应结构：{ "tools": [...], "nextCursor": "..." }
	var resp struct {
		Tools      []MCPTool `json:"tools"`
		NextCursor string    `json:"nextCursor,omitempty"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("mcp: 反序列化 tools/list 响应失败: %w", err)
	}
	return resp.Tools, nil
}

// ListToolsCached 带 60s TTL 缓存的 tools/list。
//
// 行为：
//   - 命中缓存（距上次成功 < ToolsCacheTTL）→ 直接返回缓存快照，不发 RPC
//   - 缓存过期或为空 → 调用 ListTools；成功后更新缓存；失败不污染既有缓存
//   - 返回的切片是缓存的"逻辑快照"（浅拷贝顶层切片，元素仍指向同一 MCPTool）；
//     调用方不应原地修改返回的元素以免影响后续命中
//
// 适用场景：Agent Loop 每轮会话刷新远端工具列表时调用本方法；启动期
// 握手已经预热过一次 ListTools，运行期 60s 内重复调用直接命中。
func (s *Session) ListToolsCached(ctx context.Context) ([]MCPTool, error) {
	// 快速路径：命中缓存
	s.toolsCacheMu.Lock()
	if !s.toolsCachedAt.IsZero() && time.Since(s.toolsCachedAt) < ToolsCacheTTL {
		// 浅拷贝顶层切片，避免外部 append 改动 cache
		cached := make([]MCPTool, len(s.toolsCached))
		copy(cached, s.toolsCached)
		s.toolsCacheMu.Unlock()
		return cached, nil
	}
	s.toolsCacheMu.Unlock()

	// 慢路径：拉取并刷新缓存
	tools, err := s.ListTools(ctx)
	if err != nil {
		// 失败：保留旧缓存（让调用方按 ListTools 的错误处理决定是否降级）
		return nil, err
	}
	s.toolsCacheMu.Lock()
	s.toolsCached = tools
	s.toolsCachedAt = time.Now()
	s.toolsCacheMu.Unlock()

	// 返回独立副本，避免外部修改影响缓存
	out := make([]MCPTool, len(tools))
	copy(out, tools)
	return out, nil
}

// InvalidateToolsCache 主动失效 tools/list 缓存。
//
// 使用场景：Task 7 重连成功后远端 server 可能已重启，需丢弃旧缓存
// 强制下次 ListToolsCached 走真实 RPC；其他正常情况无需调用。
func (s *Session) InvalidateToolsCache() {
	s.toolsCacheMu.Lock()
	s.toolsCached = nil
	s.toolsCachedAt = time.Time{}
	s.toolsCacheMu.Unlock()
}

// CallToolParams 是 tools/call 请求的 params 结构。
type CallToolParams struct {
	// Name 远端 server 内部的工具名（不含 mcp__ 前缀）。
	Name string `json:"name"`
	// Arguments 工具入参，序列化为 JSON 对象；nil 时序列化为 null。
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// CallTool 调用远端工具并返回 result。
//
// 注意 IsError=true 的情形：MCP 规范允许 server 在 result 层面表达
// 工具执行失败（如 echo("") 触发参数校验），client 端不视为 RPC 错误
// 而视为业务失败，调用方应在 tool adapter 层处理。
func (s *Session) CallTool(ctx context.Context, name string, arguments json.RawMessage) (*MCPCallResult, error) {
	raw, err := s.request(ctx, "tools/call", CallToolParams{
		Name:      name,
		Arguments: arguments,
	})
	if err != nil {
		return nil, err
	}
	var result MCPCallResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("mcp: 反序列化 tools/call 响应失败: %w", err)
	}
	return &result, nil
}

// Close 关闭 Session。
//
// 行为：
//  1. 标记 Session 为关闭态（后续 RPC 立即返回 ErrSessionClosed）
//  2. 关闭底层 Transport（stdin EOF / HTTP 句柄释放）
//  3. 唤醒所有 pending 请求（errCh 收到 ErrSessionClosed）
//  4. 等待 recvLoop goroutine 退出（带 5s 兜底超时）
//
// 多次调用幂等。
func (s *Session) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil // 已关闭
	}

	// 关闭 transport：触发 recvLoop 退出
	if err := s.transport.Close(); err != nil {
		s.logger.Warn("关闭 transport 失败", zap.Error(err))
	}

	// 兜底唤醒所有 pending：transport 关闭后 recvLoop 会自行 fail 所有 pending，
	// 但若 transport.Close 不触发 Recv 返回（理论上 stdio / http 都会），需要保险
	s.failAllPending(ErrSessionClosed)

	// 等 recvLoop 退出（最多 5s 兜底，避免 Close 永久阻塞）
	select {
	case <-s.recvDone:
	case <-time.After(5 * time.Second):
		s.logger.Warn("recvLoop 未在 5s 内退出，强制返回")
	}

	// 清空 pending（防御：若 recvLoop 因 ctx 取消未走 failAllPending 兜底）
	s.pendingMu.Lock()
	s.pending = make(map[string]*pendingCall)
	s.pendingMu.Unlock()

	return nil
}

// isClosed 判断通道是否已关闭（recvDone）。
//
// 用于 Start 中判断 Session 是否已关闭（避免向已关闭 Session 启动 goroutine）。
// 单纯判断 closed 字段不够：Start 在 pendingMu 锁内访问 closed 会与 Close 死锁。
func isClosed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
