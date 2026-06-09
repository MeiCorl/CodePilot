// Package adapter 的批量注册入口。
//
// 本文件提供 RegisterAll：遍历 Pool 中所有健康 server，对每个 server 的
// 远端工具构造 adapterTool 并写入 tool.Registry，使内置工具/远端工具在
// Agent Loop 看来完全一致。
//
// 设计要点：
//   - 使用 Session.ListToolsCached：启动期与运行期共享 60s TTL 缓存，避免
//     重复拉取；缓存内部由 session 包统一维护
//   - 单 server 失败仅记日志，不影响其他 server 注册（与 Pool.InitializeAll
//     的"失败隔离"语义一致）
//   - 工具命名冲突（同 server 出现重名）→ 跳过后者并记 warn；跨 server 因前缀
//     不同天然不冲突
//   - PoolView 接口抽取，使本函数可同时支持真实 Pool 与 mock，便于测试
package adapter

import (
	"context"
	"errors"
	"fmt"

	"go.uber.org/zap"

	"github.com/MeiCorl/CodePilot/src/internal/mcp/session"
	"github.com/MeiCorl/CodePilot/src/internal/tool"
)

// PoolView 是 RegisterAll 依赖的 Pool 最小子集接口。
//
// 仅声明本步骤需要的方法：HealthyNames + Get，便于 mock 测试。
// 真实 *session.Pool 自动满足该接口（编译期由下方 _ 断言保证）。
type PoolView interface {
	// HealthyNames 返回所有握手成功且当前可用的 server 名列表。
	HealthyNames() []string
	// Get 按 server 名查找 Session；未注册返回 (nil, false)。
	Get(name string) (*session.Session, bool)
}

// 编译期保证 *session.Pool 实现 PoolView，避免接口漂移。
var _ PoolView = (*session.Pool)(nil)

// RegisterStats 是 RegisterAll 的执行统计，便于上层日志与状态栏展示。
//
// 字段说明：
//   - ServersProcessed 实际处理过的 server 数量（来自 HealthyNames）
//   - ToolsRegistered  成功注册到 tool.Registry 的工具总数
//   - PerServer        每个 server 注册的工具数（key=server 名）
//   - SkippedDuplicate 重名跳过的工具数（按工具名计）
//   - Errors           处理过程中累积的非致命错误（每条已记日志，调用方可忽略）
type RegisterStats struct {
	ServersProcessed int
	ToolsRegistered  int
	PerServer        map[string]int
	SkippedDuplicate int
	Errors           []error
}

// RegisterAll 把 Pool 中所有健康 server 的远端工具批量注册到 reg。
//
// 行为：
//   - 遍历 pool.HealthyNames()，逐个 server 通过 ListToolsCached 拉工具
//   - 每个 MCPTool 构造 adapterTool，写入 reg.Register；重名跳过
//   - 任一 server 失败仅记 warn，不阻塞其他 server
//   - 返回 RegisterStats 供调用方观测；error 仅在 pool 为 nil 时返回
//
// 参数：
//   - ctx     传递给 ListToolsCached，调用方可用于设置全局超时
//   - pool    服务于 PoolView 接口（运行期一般是 *session.Pool 单例）
//   - reg     目标 tool.Registry（运行期一般是 tool.DefaultRegistry()）
//   - logger  可选 zap.Logger；nil 时使用 NopLogger
func RegisterAll(ctx context.Context, pool PoolView, reg *tool.Registry, logger *zap.Logger) (*RegisterStats, error) {
	if pool == nil {
		return nil, errors.New("mcp adapter: pool 不能为空")
	}
	if reg == nil {
		return nil, errors.New("mcp adapter: registry 不能为空")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	log := logger.With(zap.String("component", "mcp_adapter"))

	names := pool.HealthyNames()
	stats := &RegisterStats{
		PerServer: make(map[string]int, len(names)),
	}

	for _, serverName := range names {
		stats.ServersProcessed++
		sess, ok := pool.Get(serverName)
		if !ok || sess == nil {
			// 极少见：HealthyNames 返回后并发被移除；记录但不致命
			log.Warn("server 在 HealthyNames 与 Get 之间被移除，跳过",
				zap.String("server", serverName))
			continue
		}

		tools, err := sess.ListToolsCached(ctx)
		if err != nil {
			log.Warn("拉取远端工具列表失败，跳过该 server",
				zap.String("server", serverName),
				zap.Error(err))
			stats.Errors = append(stats.Errors, fmt.Errorf("server %q ListTools: %w", serverName, err))
			continue
		}

		registered := registerServerTools(serverName, tools, sess, reg, log, stats)
		stats.PerServer[serverName] = registered
		log.Info("MCP server 工具批量注册完成",
			zap.String("server", serverName),
			zap.Int("registered", registered),
			zap.Int("remote_total", len(tools)))
	}

	log.Info("所有 MCP server 工具注册结束",
		zap.Int("servers_processed", stats.ServersProcessed),
		zap.Int("tools_registered", stats.ToolsRegistered),
		zap.Int("skipped_duplicate", stats.SkippedDuplicate),
		zap.Int("errors", len(stats.Errors)))

	return stats, nil
}

// registerServerTools 把单个 server 的工具列表注册到 Registry。
//
// 返回值：实际成功注册的工具数（不含重名跳过）。
// 重名处理：触发 *tool.ErrToolAlreadyRegistered 时仅记 warn 并累加 SkippedDuplicate；
// 不阻断同 server 后续工具的注册。
func registerServerTools(
	serverName string,
	tools []session.MCPTool,
	caller SessionCaller,
	reg *tool.Registry,
	log *zap.Logger,
	stats *RegisterStats,
) int {
	registered := 0
	for _, mcpTool := range tools {
		adapter, err := NewAdapterTool(serverName, mcpTool, caller)
		if err != nil {
			// 远端 tool 字段不合规（如 name 为空）：跳过单条，不影响其他
			log.Warn("构造 adapterTool 失败，跳过",
				zap.String("server", serverName),
				zap.String("remote_tool", mcpTool.Name),
				zap.Error(err))
			stats.Errors = append(stats.Errors, fmt.Errorf("server %q tool %q: %w", serverName, mcpTool.Name, err))
			continue
		}

		if err := reg.Register(adapter); err != nil {
			var dup *tool.ErrToolAlreadyRegistered
			if errors.As(err, &dup) {
				log.Warn("工具命名冲突，跳过",
					zap.String("server", serverName),
					zap.String("tool", adapter.Name()))
				stats.SkippedDuplicate++
				continue
			}
			// 其他注册错误：记日志后继续（如 Name 为空，理论上不可能）
			log.Warn("注册工具失败，跳过",
				zap.String("server", serverName),
				zap.String("tool", adapter.Name()),
				zap.Error(err))
			stats.Errors = append(stats.Errors, fmt.Errorf("register %q: %w", adapter.Name(), err))
			continue
		}
		registered++
		stats.ToolsRegistered++
	}
	return registered
}
