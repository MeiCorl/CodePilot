// Package web 提供 CodePilot 的 Web 交互层实现。
// 通过 HTTP 协议提供嵌入式静态资源，通过 WebSocket 实现浏览器与 Agent
// 的实时双向通信。HTTP 服务仅监听本机回环地址（127.0.0.1），不向
// 局域网或公网暴露。
package web

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/MeiCorl/CodePilot/src/internal/logger"
)

// DefaultAddr 默认监听地址，仅本机可访问。
// 端口部分使用 0，由操作系统自动分配空闲端口，避免多个 CodePilot
// 进程同时启动时出现端口冲突；Start 完成后通过 Addr() 获取真实端口。
const DefaultAddr = "127.0.0.1:0"

// WSPath WebSocket 端点路径。
const WSPath = "/ws"

//go:embed static
var staticFS embed.FS

// Server 承载 HTTP 静态资源服务与 WebSocket 升级入口。
// 业务消息由 ConnectionManager 内部 Router 接收与分发；
// 业务层通过 Server.Router() 注册 handler。
//
// 字段说明：
//   - addr   监听地址；构造时保存"期望地址"，Start 中 listen 成功后会被
//     刷新为操作系统实际分配的地址（含真实端口），以便上层据此打开浏览器。
//   - ready  服务"已就绪"信号通道；listen 完成且 addr 已刷新后关闭。
//     调用方可通过 Ready() 阻塞等待真实端口可用后再启动浏览器，
//     避免 time.Sleep 这种竞态写法。
type Server struct {
	mu      sync.RWMutex // 保护 addr 在 listen 前后被并发读写
	addr    string
	ready   chan struct{}
	wsMgr   *ConnectionManager
	router  *Router
	httpSrv *http.Server
}

// NewServer 构造 Server 实例；addr 为空时使用 DefaultAddr（随机端口）。
func NewServer(addr string) *Server {
	if addr == "" {
		addr = DefaultAddr
	}
	router := NewRouter()
	return &Server{
		addr:   addr,
		ready:  make(chan struct{}),
		wsMgr:  NewConnectionManager(router),
		router: router,
	}
}

// Addr 返回监听地址。
// Start 完成 net.Listen 之前返回构造时传入的期望地址（可能含 :0 端口）；
// listen 成功之后返回操作系统实际分配的地址（含真实端口）。
// 推荐先 <-Ready() 等待就绪，再调用 Addr() 以保证拿到真实端口。
func (s *Server) Addr() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.addr
}

// Ready 返回一个只读通道，listen 成功并完成 addr 刷新后关闭。
// 调用方可通过 <-server.Ready() 同步等待 server 进入可服务状态。
func (s *Server) Ready() <-chan struct{} {
	return s.ready
}

// ConnectionManager 暴露连接管理器，供业务层做广播/单播。
func (s *Server) ConnectionManager() *ConnectionManager {
	return s.wsMgr
}

// Router 暴露消息路由，业务层在此注册各消息类型的 handler。
func (s *Server) Router() *Router {
	return s.router
}

// Start 启动 HTTP 服务并阻塞，直到 ctx 取消或服务异常退出。
// 端口被占用时返回明确错误信息；监听地址使用 :0（端口为 0）时由
// 操作系统自动分配空闲端口，listen 成功后通过 Addr() 可读取真实地址。
//
// 上层若需在 server 真正可服务后再执行（如打开浏览器），应先
// <-server.Ready()，避免轮询或 time.Sleep 带来的竞态。
func (s *Server) Start(ctx context.Context) error {
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return fmt.Errorf("提取嵌入的 static 目录失败: %w", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(staticSub)))
	mux.HandleFunc(WSPath, s.wsMgr.HandleWS)

	// 先取构造时传入的"期望地址"用于 listen，listen 成功后再回写真实地址。
	wantAddr := s.Addr()
	listener, err := net.Listen("tcp", wantAddr)
	if err != nil {
		if isAddrInUse(err) {
			return fmt.Errorf("端口 %s 已被占用，请检查后重试", wantAddr)
		}
		return fmt.Errorf("启动 Web 服务失败: %w", err)
	}

	// 把 addr 刷新为操作系统实际分配的地址（含真实端口）。
	realAddr := listener.Addr().String()
	s.mu.Lock()
	s.addr = realAddr
	s.mu.Unlock()

	s.httpSrv = &http.Server{
		Addr:              realAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	logger.Info("Web 服务启动",
		zap.String("addr", realAddr),
		zap.String("static_root", "/"),
		zap.String("ws_path", WSPath),
	)

	// 通知上层 server 已可服务（地址已经刷新到 s.addr）。
	close(s.ready)

	errCh := make(chan error, 1)
	go func() {
		if err := s.httpSrv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		logger.Info("Web 服务开始关闭（ctx 取消）")
		return s.Shutdown(context.Background())
	case err, ok := <-errCh:
		if !ok {
			return nil
		}
		if err != nil {
			return fmt.Errorf("Web 服务运行出错: %w", err)
		}
		return nil
	}
}

// Shutdown 优雅关闭 HTTP 服务与所有 WebSocket 连接。
func (s *Server) Shutdown(ctx context.Context) error {
	if s.wsMgr != nil {
		s.wsMgr.CloseAll()
	}
	if s.httpSrv != nil {
		if err := s.httpSrv.Shutdown(ctx); err != nil {
			return fmt.Errorf("关闭 Web 服务失败: %w", err)
		}
	}
	logger.Info("Web 服务已关闭")
	return nil
}

// isAddrInUse 跨平台判断 net.Listen 错误是否为"地址已被占用"。
// 不依赖平台特定的 syscall 常量，通过错误字符串兜底：
//   - Linux/macOS："address already in use" / "bind: address already in use"
//   - Windows："Only one usage of each socket address ... is normally permitted"
func isAddrInUse(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "address already in use") ||
		strings.Contains(msg, "Only one usage of each socket address")
}
