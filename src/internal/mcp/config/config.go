// Package config 把 setting.json 的 mcp.servers 段转换为 Pool 可消费的
// session.ServerConfig。
//
// 设计要点:
//   - 与 internal/config 解耦:本包不依赖 security/tool/llm 等,只依赖
//     internal/config + internal/mcp/{transport,session},便于单独测试
//   - BuildTransports 在启动期一次性把 setting 段转成 map[name]transport.Transport,
//     单 server 失败仅记日志并跳过(返回的 map 中不包含该 server)
//   - 同时输出 reconnectFactory(用于 Session 注入),确保重连时按相同参数
//     重建 transport;factory 内部捕获原 ServerConfig,允许 stdio 子进程
//     在断开后被新进程替换
package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/MeiCorl/CodePilot/src/internal/config"
	"github.com/MeiCorl/CodePilot/src/internal/mcp/session"
	"github.com/MeiCorl/CodePilot/src/internal/mcp/transport"
)

// DefaultHandshakeTimeout 是单 server 握手的默认超时(秒)。
//
// 与 session.DefaultHandshakeTimeout (30s) 保持一致;此处提供秒级常量是
// 为 setting.json 提供易读默认值。
const DefaultHandshakeTimeout = 30 * time.Second

// DefaultListToolsCacheTTL 是 tools/list 缓存默认 TTL(秒)。
//
// 与 session.ToolsCacheTTL (60s) 一致;此处提供秒级常量便于 setting.json 配置。
const DefaultListToolsCacheTTL = 60 * time.Second

// BuildResult 是 BuildTransports 的输出。
//
// 字段说明:
//   - PoolConfigs:    启动期需要立即建连的 server 列表,直接喂给 Pool.InitializeAll
//   - ReconnectFactory: 每个 server 配套的 transport 重建工厂,注入到 Session.WithReconnectFactory
//   - Skipped:        因 disabled 或 type 非法被跳过的 server 名称与原因(用于日志/HITL 提示)
type BuildResult struct {
	// PoolConfigs 启动期需要建连的 server 列表。
	PoolConfigs []session.ServerConfig
	// ReconnectFactory 按 server 名索引的 transport 重建工厂。
	// 调用方在构造 Session 时通过 session.WithReconnectFactory 注入。
	ReconnectFactory map[string]func() (transport.Transport, error)
	// Skipped 被跳过的 server(name → reason);不为 nil 即代表有 server 被跳过,
	// 主流程在状态栏用 warning 展示。
	Skipped map[string]string
}

// BuildTransports 把 cfg.MCP.Servers 转换为 Pool 可消费的 ServerConfig 列表与
// reconnect 工厂映射。
//
// 行为:
//   - 遍历 cfg.MCP.Servers,跳过 Disabled=true 的条目(记入 Skipped,原因 "disabled")
//   - Type=stdio → 构造 *transport.stdioTransport,Stderr 重定向到 io.Discard
//   - Type=http  → 构造 *transport.httpTransport,Headers 逐条加为自定义请求头
//     约定:Authorization 头会被自动转换为 WithBearerToken,避免与 basic auth 冲突
//   - 单 server 构造失败仅记日志并跳过,不影响其他 server
//   - 返回的 BuildResult 全部字段都非 nil(空 slice/map 也保留),调用方 nil-safe
//
// 并发安全:本函数是纯转换,无共享状态;不触发任何 IO。
func BuildTransports(cfg *config.Config, logger *zap.Logger) *BuildResult {
	if logger == nil {
		logger = zap.NewNop()
	}
	log := logger.With(zap.String("component", "mcp_config"))

	result := &BuildResult{
		PoolConfigs:      make([]session.ServerConfig, 0, len(cfg.MCP.Servers)),
		ReconnectFactory: make(map[string]func() (transport.Transport, error)),
		Skipped:          make(map[string]string),
	}

	for _, s := range cfg.MCP.Servers {
		if s.Disabled {
			result.Skipped[s.Name] = "disabled"
			log.Info("MCP server 已禁用,跳过",
				zap.String("server", s.Name),
			)
			continue
		}

		// 基础合法性再次校验(理论上 config.validateMCP 已把关,此处防御)
		if err := validateServer(s); err != nil {
			result.Skipped[s.Name] = err.Error()
			log.Warn("MCP server 配置不合法,跳过",
				zap.String("server", s.Name),
				zap.Error(err),
			)
			continue
		}

		// 构造工厂(闭包捕获 ServerConfig,重连时复用)
		factory := makeFactory(s, log)

		// 首次构造 transport 失败也不阻塞:工厂在重连时再试一次
		trans, err := factory()
		if err != nil {
			result.Skipped[s.Name] = fmt.Sprintf("构造 transport 失败: %v", err)
			log.Warn("MCP server 首次构造 transport 失败,标记 unhealthy",
				zap.String("server", s.Name),
				zap.Error(err),
			)
			// 仍然保留 factory,允许下次调用(如 ListToolsCached 触发的 lazy 重连)再试
		}

		timeout := DefaultHandshakeTimeout
		if s.Timeout > 0 {
			timeout = time.Duration(s.Timeout) * time.Second
		}

		// 注:trans 可能在工厂首次调用时失败(此时为 nil),Pool.RegisterAndStart
		// 会返回错误并标记 unhealthy;此处仍把 ServerConfig 加进 PoolConfigs,
		// 让 Pool 有机会记录"尝试过但失败"。
		result.PoolConfigs = append(result.PoolConfigs, session.ServerConfig{
			Name:      s.Name,
			Transport: trans,
			Timeout:   timeout,
		})
		result.ReconnectFactory[s.Name] = factory
	}

	log.Info("MCP server 配置解析完成",
		zap.Int("total", len(cfg.MCP.Servers)),
		zap.Int("active", len(result.PoolConfigs)),
		zap.Int("skipped", len(result.Skipped)),
	)
	return result
}

// makeFactory 构造 transport 重建工厂。
//
// 工厂闭包捕获 ServerConfig,每次调用都重新构造 transport 实例(未 Connect),
// 由调用方(Pool 或 Session.EnsureHealthy)负责后续 Connect + 握手。
//
// 这样设计的好处:stdio 子进程崩溃后,工厂会按原 config 重新 spawn 新进程;
// HTTP 端点短暂不可用时,工厂会按原 URL/Headers 重新建立客户端。
func makeFactory(s config.MCPServerConfig, log *zap.Logger) func() (transport.Transport, error) {
	return func() (transport.Transport, error) {
		switch strings.ToLower(s.Type) {
		case "stdio":
			cfg := transport.StdioConfig{
				Command: s.Command,
				Args:    s.Args,
				Env:     s.Env,
				// Stderr 丢弃,避免污染 CodePilot 进程的 stderr
				// 如未来需要诊断,可在 mcp.servers 段额外提供 stderr 路径字段
			}
			return transport.NewStdio(cfg), nil
		case "http":
			return transport.NewHTTP(s.URL, applyHTTPOptions(s.Headers, s.Timeout)...), nil
		default:
			return nil, fmt.Errorf("不支持的传输类型: %s", s.Type)
		}
	}
}

// applyHTTPOptions 把 Headers/Timeout 应用到 httpTransport。
//
// 约定:
//   - "Authorization" 头(不区分大小写) → 调 WithBearerToken(去掉前缀 "Bearer ")
//   - 其他头 → 调 WithHTTPHeader 追加
//   - Timeout → 调 WithHTTPTimeout 设置
//
// 返回值是已构造好的 transport.Option 列表,供外部 NewHTTP 一次性传入,
// 避免在 factory 内调 NewHTTP 后再追加 option(本步骤 *httpTransport 不可见)。
func applyHTTPOptions(headers map[string]string, timeoutSec int) []transport.Option {
	var opts []transport.Option
	for k, v := range headers {
		if strings.EqualFold(k, "Authorization") {
			// 兼容 "Bearer xxx" / "Basic xxx" / 裸 token 等多种写法;
			// 这里一律用 WithBearerToken("Bearer <v>") 处理(若 v 已含 "Bearer " 则原样)
			token := strings.TrimSpace(v)
			if token != "" {
				opts = append(opts, transport.WithBearerToken(token))
			}
			continue
		}
		opts = append(opts, transport.WithHTTPHeader(k, v))
	}
	if timeoutSec > 0 {
		opts = append(opts, transport.WithHTTPTimeout(time.Duration(timeoutSec)*time.Second))
	}
	return opts
}

// validateServer 对单条 server 声明做最小合法性校验。
//
// 与 config.validateMCP 重复一层防御(后者已被 main 启动流程调用);此处
// 兜底,允许 BuildTransports 被独立测试调用。
func validateServer(s config.MCPServerConfig) error {
	if strings.TrimSpace(s.Name) == "" {
		return errors.New("server name 不能为空")
	}
	switch strings.ToLower(s.Type) {
	case "stdio":
		if strings.TrimSpace(s.Command) == "" {
			return errors.New("stdio 类型必须填写 command")
		}
	case "http":
		if strings.TrimSpace(s.URL) == "" {
			return errors.New("http 类型必须填写 url")
		}
	default:
		return fmt.Errorf("不支持的传输类型: %q", s.Type)
	}
	return nil
}
