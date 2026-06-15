package web

import (
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"github.com/MeiCorl/CodePilot/src/internal/logger"
)

// upgrader WebSocket 升级器，配置 CheckOrigin 防止跨站劫持。
// 由于服务仅监听 127.0.0.1，恶意站点无法直接连入；但 Origin 校验
// 仍作为纵深防御，仅允许 Origin 与 Host 同源或非浏览器客户端
// （curl/wscat 等不发 Origin 头）连接。
var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		// 非浏览器客户端（无 Origin 头）允许通过
		if origin == "" {
			return true
		}
		// 浏览器客户端：要求 Origin 主机部分与请求 Host 一致
		return origin == "http://"+r.Host || origin == "https://"+r.Host
	},
}

// ConnectionManager 管理所有活跃的 WebSocket 连接。
// 内部使用 sync.RWMutex 保护连接集合的并发访问：写操作用 Lock，
// 读/遍历操作用 RLock。
//
// AllClosed 事件：每当活跃连接数从 1 变 0 时，会通过 AllClosed()
// 返回的 channel 发送一次非阻塞信号。供 main 在浏览器关闭时驱动
// 进程自动退出；信号是 buffered（容量 1），多次连发只保留最新一次，
// 避免触发方被背压阻塞。
type ConnectionManager struct {
	mu          sync.RWMutex
	conns       map[*websocket.Conn]struct{}
	router      *Router
	allClosedCh chan struct{} // 每次活跃连接数 1→0 时发送一次信号
}

// NewConnectionManager 构造连接管理器。
// router 为 nil 时使用空路由（收到任何业务消息都会被识别为未知类型）。
func NewConnectionManager(router *Router) *ConnectionManager {
	if router == nil {
		router = NewRouter()
	}
	return &ConnectionManager{
		conns:       make(map[*websocket.Conn]struct{}),
		router:      router,
		allClosedCh: make(chan struct{}, 1),
	}
}

// Router 返回内部 Router，供业务层注册消息 handler。
func (m *ConnectionManager) Router() *Router {
	return m.router
}

// Add 注册一个新连接到管理器。
func (m *ConnectionManager) Add(conn *websocket.Conn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.conns[conn] = struct{}{}
	logger.Info("WebSocket 连接已建立",
		zap.String("remote", conn.RemoteAddr().String()),
		zap.Int("total", len(m.conns)),
	)
}

// Remove 注销一个连接（若存在），并关闭底层连接。
// 注销后若活跃连接数首次降为 0，会通过 AllClosed channel 发送一次
// 非阻塞信号，供 main 在浏览器关闭时驱动自动退出。
func (m *ConnectionManager) Remove(conn *websocket.Conn) {
	m.mu.Lock()
	if _, ok := m.conns[conn]; ok {
		delete(m.conns, conn)
		_ = conn.Close()
	}
	allClosed := len(m.conns) == 0
	m.mu.Unlock()

	logger.Info("WebSocket 连接已断开", zap.Int("total", len(m.conns)))

	if allClosed {
		m.signalAllClosed()
	}
}

// signalAllClosed 非阻塞地往 AllClosed channel 发送一次信号。
// 抽出来便于 Remove 与 CloseAll 复用同一份逻辑。
func (m *ConnectionManager) signalAllClosed() {
	m.mu.RLock()
	ch := m.allClosedCh
	m.mu.RUnlock()
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
		// 已有未消费的信号，丢弃本次以避免阻塞 Remove 流程
	}
}

// AllClosed 返回"所有连接都已断开"事件通道。
// 该 channel 不会被关闭（ConnectionManager 整个生命周期里都会反复
// 触发该事件），仅作为信号通道使用：每次活跃连接数 1→0 时发送一次。
// main 在浏览器关闭时基于该信号做防抖（N 秒内无新连接则退出进程）。
func (m *ConnectionManager) AllClosed() <-chan struct{} {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.allClosedCh
}

// Count 返回当前活跃连接数。
func (m *ConnectionManager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.conns)
}

// Snapshot 返回当前所有活跃连接的切片副本。
// 在 RLock 下拷贝后立即释放锁，调用方可在锁外安全遍历。
//
// [Why] 供需要"逐个走 Handler.sendMessage(writeMu)"的推送场景使用——
// 例如 MCP 后台初始化完成后的 BroadcastMCPStatus。这里刻意返回连接指针供
// 调用方自行走 sendMessage，而不是直接调裸 conn.WriteMessage（即不走 Broadcast）：
// 后者绕过 Handler.writeMu，会与 runStream 的流式写竞争，触发 gorilla
// "concurrent write to websocket connection" panic。
func (m *ConnectionManager) Snapshot() []*websocket.Conn {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*websocket.Conn, 0, len(m.conns))
	for c := range m.conns {
		out = append(out, c)
	}
	return out
}

// Broadcast 向所有连接广播一条文本消息。
// 单个连接写入失败仅记录日志，不中断其他连接的广播。
// Task 3/4 接入 Router 后将由业务层调用。
func (m *ConnectionManager) Broadcast(data []byte) {
	m.mu.RLock()
	conns := make([]*websocket.Conn, 0, len(m.conns))
	for c := range m.conns {
		conns = append(conns, c)
	}
	m.mu.RUnlock()

	for _, c := range conns {
		if err := c.WriteMessage(websocket.TextMessage, data); err != nil {
			logger.Warn("WebSocket 广播失败",
				zap.String("remote", c.RemoteAddr().String()),
				zap.Error(err),
			)
		}
	}
}

// Send 向单个连接发送一条文本消息。
func (m *ConnectionManager) Send(conn *websocket.Conn, data []byte) error {
	return conn.WriteMessage(websocket.TextMessage, data)
}

// CloseAll 关闭所有活跃连接。
// 关闭后若连接集合清空，会通过 AllClosed channel 发送一次信号，
// 与 Remove 路径保持一致。
func (m *ConnectionManager) CloseAll() {
	m.mu.Lock()
	for c := range m.conns {
		_ = c.Close()
	}
	m.conns = make(map[*websocket.Conn]struct{})
	m.mu.Unlock()

	m.signalAllClosed()
}

// HandleWS 处理 WebSocket 握手并启动读循环。
// 读循环内由 Router 负责消息编解码与路由分发；连接断开时自动清理。
func (m *ConnectionManager) HandleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Warn("WebSocket 升级失败", zap.Error(err))
		return
	}
	m.Add(conn)

	go func() {
		defer m.Remove(conn)
		m.router.HandleLoop(conn)
	}()
}
