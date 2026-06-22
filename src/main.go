// Package main 是 CodePilot 终端 AI Coding Agent 的程序入口。
//
// 启动链路：
//  1. 初始化文件日志（失败不阻塞主流程）
//  2. 加载 ~/.codepilot/setting.json 配置
//  3. 按配置构造 LLM Provider
//  4. 创建会话管理器（自动恢复最近一个会话）
//  5. 启动 HTTP + WebSocket 服务，监听本机回环地址；端口由操作系统
//     自动分配（127.0.0.1:0），支持同时启动多个 CodePilot 进程
//  6. 通过 server.Ready() 等待 listen 完成后，再以真实端口调用系统
//     默认浏览器打开交互页面（失败仅警告）
//  7. 浏览器成功打开后，自动隐藏 Windows 终端窗口（其他平台 no-op），
//     让 CodePilot 在后台静默运行
//  8. 阻塞等待以下任一触发以进入退出流程：
//     - SIGINT / SIGTERM 信号（终端 Ctrl+C）
//     - Web 服务运行异常
//     - 浏览器窗口关闭后等待 browserExitGracePeriod 无新连接恢复
//
// 退出流程：取消 runCtx → Server.Start 内部完成 Shutdown →
// 关闭 WebSocket 连接、关闭文件日志、返回进程退出码 0。
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/MeiCorl/CodePilot/src/internal/config"
	"github.com/MeiCorl/CodePilot/src/internal/engine/conversation"
	"github.com/MeiCorl/CodePilot/src/internal/engine/prompt"
	"github.com/MeiCorl/CodePilot/src/internal/engine/prompt/sources"
	"github.com/MeiCorl/CodePilot/src/internal/interaction/web"
	"github.com/MeiCorl/CodePilot/src/internal/logger"
	"github.com/MeiCorl/CodePilot/src/internal/mcp/adapter"
	mcpconfig "github.com/MeiCorl/CodePilot/src/internal/mcp/config"
	"github.com/MeiCorl/CodePilot/src/internal/mcp/session"
	"github.com/MeiCorl/CodePilot/src/internal/memory/autolearn"
	memctx "github.com/MeiCorl/CodePilot/src/internal/memory/context"
	memsession "github.com/MeiCorl/CodePilot/src/internal/memory/session"
	"github.com/MeiCorl/CodePilot/src/internal/runtime/console"
	"github.com/MeiCorl/CodePilot/src/internal/security"
	"github.com/MeiCorl/CodePilot/src/internal/tool"
	"github.com/MeiCorl/CodePilot/src/llm"
	// import 触发 builtin 包的 init()，将 5 个内置工具以 cwd + 30s 兜底
	// 注册到 tool.DefaultRegistry()；main 随后按 cfg 调
	// builtin.RegisterWithOptions 用 cfg 中的工作目录/超时覆盖默认实例。
	"github.com/MeiCorl/CodePilot/src/internal/tool/builtin"
)

const (
	// defaultMaxRounds 为兼容旧构造链保留的历史参数；当前不再用于裁剪上下文。
	defaultMaxRounds = 50

	// browserExitGracePeriod 浏览器关闭后等待新连接恢复的宽限期。
	// 浏览器刷新或暂时断网时 WebSocket 都会断开，但很快会重连；
	// 超过该宽限期仍无新连接则认定用户已关闭浏览器窗口，触发进程退出。
	browserExitGracePeriod = 5 * time.Second
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "[error]", err)
		os.Exit(1)
	}
}

// buildMemoryRoots 计算记忆系统的用户级与项目级根目录（绝对路径）。
//
// [Why 单一入口] 这是记忆子系统路径的唯一计算入口——autolearn.Store 落盘目录、
// SandboxMiddleware 附加只读根（Step 8 Task 3）、memory 索引注入 Source 读取来源、
// Reviewer 写入目标全部从此处取根，保证「记忆实际目录」「沙箱放行范围」「注入来源」
// 三者同源不漂移。homeDir 取不到或为空时 userRoot 返回空（仅用项目级，降级不阻塞
// 启动）；toolWorkdir 为空时 projectRoot 返回空。
func buildMemoryRoots(toolWorkdir string) (userRoot, projectRoot string) {
	if homeDir, err := os.UserHomeDir(); err == nil && homeDir != "" {
		userRoot = autolearn.UserMemoryRoot(homeDir)
	}
	projectRoot = autolearn.ProjectMemoryRoot(toolWorkdir)
	return userRoot, projectRoot
}

// buildMemoryReadRoots 计算记忆系统的「附加只读根」，供沙箱放行读取类工具
// 访问工作目录之外的记忆文件（Step 8 Task 3）。
//
// 基于 buildMemoryRoots 派生，过滤空串根（避免把相对路径误当合法绝对根）。
func buildMemoryReadRoots(toolWorkdir string) []string {
	userRoot, projectRoot := buildMemoryRoots(toolWorkdir)
	var roots []string
	if userRoot != "" {
		roots = append(roots, userRoot)
	}
	if projectRoot != "" {
		roots = append(roots, projectRoot)
	}
	return roots
}

// run 是主流程入口；返回 error 表示启动或运行过程中发生不可恢复错误。
// 拆出独立函数便于在测试中调用（虽然 step1.1 暂未引入 main 测试）。
func run() error {
	// 1. 初始化文件日志
	if err := logger.Init(); err != nil {
		fmt.Fprintf(os.Stderr, "[warning] 日志初始化失败，将不写文件日志: %v\n", err)
	}
	defer logger.Sync()
	defer logger.Close()
	// 关闭所有会话级 logger 并释放其文件句柄。利用 defer LIFO：本行最后注册故最先执行，
	// 确保会话 logger 先各自 Sync+Close，再执行上面的全局 Close/Sync。
	defer logger.CloseAllSessions()

	// 2. 加载配置
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger.Info("配置加载完成",
		zap.String("provider", cfg.Provider),
		zap.String("model", cfg.Model),
	)

	// 3. 初始化 LLM Provider
	provider, err := llm.NewProvider(cfg)
	if err != nil {
		return fmt.Errorf("初始化 LLM Provider 失败: %w", err)
	}

	// 4. 获取启动时所在工作目录（供会话管理器按项目分目录、顶栏展示用）。
	//    必须先于会话管理器构造：项目目录由 filepath.Base(workdir) 决定。
	workdir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("获取当前工作目录失败: %w", err)
	}
	logger.Info("工作目录", zap.String("workdir", workdir))

	// 5. 创建会话管理器（按当前项目分目录，内部自动加载本项目最近会话）
	sessMgr, err := memsession.NewSessionManager(workdir)
	if err != nil {
		return fmt.Errorf("创建会话管理器失败: %w", err)
	}

	// 6. 工具系统配置：把 cfg 中的工作目录/超时/白名单实际注入到工具层。
	// builtin 包的 init() 已用 cwd + 30s 兜底注册；此处用 cfg 显式覆盖，
	// 保证 cfg.Tools.Enabled / ToolWorkingDirectory / ToolExecutionTimeoutSeconds
	// 字段真正生效。toolWorkingDir 为空时回退到进程 cwd。
	toolRegistry := tool.DefaultRegistry()
	toolWorkdir := cfg.ToolWorkingDirectory
	if toolWorkdir == "" {
		toolWorkdir = workdir
	}
	bashTimeout := time.Duration(cfg.ToolExecutionTimeoutSeconds) * time.Second
	builtin.RegisterWithOptions(toolRegistry, toolWorkdir, bashTimeout)
	toolHandler := conversation.NewToolHandler(toolRegistry, bashTimeout, toolWorkdir)

	// Step 1.4：构造 FileDiffStore 注入 WriteFile/EditFile 工具，
	// 供前端「查看改动」按钮按需拉取文件 diff。
	// store 在 main 顶层构造（不放在 Handler 内部），方便按 Register 名直接
	// 取出 WriteFile/EditFile 工具实例调 SetDiffSink。
	fileDiffStore := web.NewFileDiffStore()
	if wfTool, ok := toolRegistry.Get(builtin.WriteFileName); ok {
		if wf, ok := wfTool.(*builtin.WriteFileTool); ok {
			wf.SetDiffSink(fileDiffStore)
		}
	}
	if efTool, ok := toolRegistry.Get(builtin.EditFileName); ok {
		if ef, ok := efTool.(*builtin.EditFileTool); ok {
			ef.SetDiffSink(fileDiffStore)
		}
	}
	logger.Info("工具系统就绪",
		zap.Int("count", toolRegistry.Count()),
		zap.Strings("enabled", toolRegistry.EnabledNames(cfg.Tools.Enabled)),
		zap.String("sandbox_dir", toolWorkdir),
		zap.Duration("execution_timeout", bashTimeout),
	)

	// 6.5 Step 5：权限系统构造。
	// 加载全局 + 项目级配置 → 合并策略 → 创建 Checker → 创建 Interceptor。
	// HITL Callback 由 Handler 层注入（Handler 持有 WebSocket 连接），
	// 此处先传 nil，后续通过 Handler.SetInterceptor 完成注入。
	policy := security.LoadPermissions(cfg, nil)
	checker := security.NewChecker(policy, toolWorkdir)
	interceptor := security.NewInterceptor(checker, nil)
	toolHandler.SetInterceptor(interceptor)

	// 注册路径沙箱 Middleware：在 ToolHandler 的权限拦截之后、工具 Execute
	// 之前运行，对所有路径类工具做统一沙箱解析；解析结果通过 ctx 注入
	// PathResolver 传给工具，工具侧不再自行调 ResolveInSandbox。
	// MCP 工具只要在 security.PathTools 注册即可零成本继承此保护。
	// 注入 checker 作为 PathRuleProvider，使越界但被"永久/本会话允许"的
	// 路径（目录级 glob 规则）在 Middleware 层也得到放行，避免双层防护
	// 对已授权路径的"硬兜底误杀"。
	//
	// Step 8 Task 3：注入「记忆目录附加只读根」，使读取类工具（ReadFile/
	// Glob/Grep）能读取工作目录之外的记忆文件。根来源与 Store 落盘目录同源
	// （autolearn.UserMemoryRoot / ProjectMemoryRoot），保证沙箱放行范围与记忆
	// 实际目录一致。仅 PermRead 工具生效（写入类工具不能直接改 memory，纵深防御）；
	// 沙箱放行仅解除路径限制，权限层 permission.Decide 仍照常按 mode 决策
	// （跨 workdir 读取用户级 memory 在 Strict 模式仍走 Ask/Deny）。
	memoryReadRoots := buildMemoryReadRoots(toolWorkdir)
	toolHandler.RegisterMiddleware(security.SandboxMiddleware(
		toolWorkdir, checker, security.WithReadRoots(memoryReadRoots),
	))

	logger.Info("权限系统就绪",
		zap.String("mode", string(checker.Mode())),
		zap.Int("rules", checker.RuleCount()),
	)

	// 6.6 Step 8：MCP 客户端启动（纯构造阶段）。
	// 从 cfg.MCP.Servers 解析 transports + 工厂 → 构造 Pool → 注入 reconnect 工厂。
	// 本段只做"无 IO 的纯内存构造"（BuildTransports / NewPool 均不触发网络/子进程），
	// 真正的握手 + 工具注册（InitializeAll / RegisterAll）放到下方后台 goroutine，
	// [Why] 避免阻塞 WebUI 启动——stdio spawn / HTTP 建连 / JSON-RPC 握手可能耗时数秒。
	//
	// mcpBuild 上提声明到 if 块外，供下方后台 goroutine 捕获 PoolConfigs。
	// 整个 mcp 段为可选项，cfg.MCP.Servers 为空时跳过整段。
	var mcpPool *session.Pool
	var mcpBuild *mcpconfig.BuildResult
	if len(cfg.MCP.Servers) > 0 {
		mcpBuild = mcpconfig.BuildTransports(cfg, logger.L())
		if len(mcpBuild.PoolConfigs) > 0 {
			// 给每个 ServerConfig 注入 reconnect 工厂
			for i := range mcpBuild.PoolConfigs {
				if f, ok := mcpBuild.ReconnectFactory[mcpBuild.PoolConfigs[i].Name]; ok {
					mcpBuild.PoolConfigs[i].ReconnectFactory = f
				}
			}
			// 仅构造空 Pool，立即注入 Handler 让前端能拿到 loading 态；
			// 握手 + 注册在下方后台 goroutine 完成。
			mcpPool = session.NewPool(logger.L())
		}
		if len(mcpBuild.Skipped) > 0 {
			logger.Warn("MCP server 被跳过",
				zap.Int("count", len(mcpBuild.Skipped)),
			)
			for name, reason := range mcpBuild.Skipped {
				logger.Warn("  - skipped",
					zap.String("server", name),
					zap.String("reason", reason),
				)
			}
		}
	}

	// 7. 构造记忆子系统（Step 8）+ System Prompt Builder（Step 4）。
	//
	// 记忆子系统：路径来源 buildMemoryRoots（与沙箱附加只读根同源）。Store 作为记忆
	// 持久化底座，同时供【索引注入 Source】（会话启动读两级 MEMORY.md → LeadUserMessage）
	// 与【后台回顾 Reviewer】（每轮 AgentLoop 结束异步写记忆 + 刷索引）共用同一实例，
	// 避免两份根计算漂移。memory.enabled=false 时仍构造两者——Source 的 Assemble 与
	// Reviewer 的 OnLoopDone 内部按 Enabled 短路（Task 6 三层短路：config.IsEnabled →
	// Source → Reviewer），降级为无记忆状态，不阻塞启动。
	memUserRoot, memProjectRoot := buildMemoryRoots(toolWorkdir)
	memoryStore := autolearn.NewStore(memUserRoot, memProjectRoot)
	memEnabled := cfg.Memory.IsEnabled()
	memoryReviewer := autolearn.NewReviewer(provider, memoryStore, autolearn.ReviewerConfig{
		Enabled:       memEnabled,
		ReviewTimeout: 60 * time.Second, // 固定 60s，首版不纳入 setting.json（spec Out of Scope）
	})
	logger.Info("记忆系统就绪",
		zap.Bool("enabled", memEnabled),
		zap.String("user_root", memUserRoot),
		zap.String("project_root", memProjectRoot),
	)

	// System Prompt Builder：4 个 Source 按注册顺序产出内容：
	//   - static:       5 个硬编码子模块（角色/行为/代码质量/工具/安全）
	//   - environment:  OS + CWD + Git 状态
	//   - agents_md:    全局 + 项目级 AGENTS.md 合并
	//   - memory:       自动记忆索引注入（Step 8，读两级 MEMORY.md → LeadUserMessage）
	promptBuilder := prompt.NewBuilder(
		sources.NewStaticSource(),
		sources.NewEnvironmentSource(),
		sources.NewAgentsMDSource(),
		sources.NewMemoryIndexSource(memoryStore, sources.MemoryIndexOptions{
			Enabled:  memEnabled,
			MaxLines: cfg.Memory.IndexMaxLines,
			MaxBytes: cfg.Memory.IndexMaxBytes,
		}),
	)

	// 8. 构造 Handler / Server
	// 使用 DefaultAddr（127.0.0.1:0）让 OS 自动分配端口，
	// 这样多个项目下可同时启动多个 CodePilot 进程互不冲突。
	handler := web.NewHandler(provider, sessMgr, cfg, defaultMaxRounds, promptBuilder, cfg.ContextWindowSize, workdir, toolRegistry, toolHandler, fileDiffStore)
	// 注入权限拦截器 + HITL 回调（Handler 内部实现 WebSocket 交互）
	handler.SetInterceptor(interceptor, checker)
	// Step 8:把 MCP pool 注入 Handler,让 mcp_status 推送 + 远端工具 server 解析生效
	handler.SetMCPPool(mcpPool)
	// Step 8：注入自动记忆回顾器，让每轮 AgentLoop 结束后异步回顾本轮对话、沉淀记忆。
	handler.SetReviewer(memoryReviewer)

	// Step 7：装配上下文压缩子系统。
	// ToolResultStore 无条件构造并注入 Handler——/clear 需据此清理落盘的工具结果归档，
	// 与压缩总开关解耦：即便 compaction 关闭，残留的 tool_results 也能被 /clear 清掉
	// （NewToolResultStore 纯内存无 IO，上提无开销）。
	toolResultStore := memctx.NewToolResultStore(sessMgr.ProjectDir())
	handler.SetToolResultStore(toolResultStore)

	// 压缩协调器（可选，压缩总开关关闭时跳过）。
	// 链路：LightCompactor + SummaryCompactor（archiver 用 sessMgr，*SessionManager 天然
	// 满足 HistoryArchiver 接口）→ Compactor 协调器。通过 handler.SetCompactor 注入
	// （同时转发给 ConversationManager 使每轮自动压缩生效）。
	// enabled=false 时仅关闭自动压缩，不再启用滑动窗口裁剪。
	if cfg.Compaction.IsEnabled() {
		lightCompactor := memctx.NewLightCompactor(toolResultStore, cfg.Compaction)
		summaryCompactor := memctx.NewSummaryCompactor(sessMgr, cfg.Compaction)
		compactor := memctx.NewCompactor(lightCompactor, summaryCompactor, cfg.Compaction)
		handler.SetCompactor(compactor)
		logger.Info("上下文压缩系统就绪",
			zap.String("projectDir", sessMgr.ProjectDir()),
			zap.Int("toolResultThreshold", cfg.Compaction.ToolResultThreshold),
			zap.Int("autoTriggerMargin", cfg.Compaction.AutoTriggerMargin),
			zap.Int("breakerThreshold", cfg.Compaction.BreakerThreshold),
		)
	} else {
		logger.Info("上下文压缩已关闭（compaction.enabled=false），将发送完整活跃历史")
	}

	server := web.NewServer(web.DefaultAddr)
	handler.Register(server.Router())
	// 注入 ConnectionManager：让 MCP 后台初始化就绪后能向所有活跃连接广播 mcp_status。
	// NewServer 构造时 wsMgr 已就绪，此处可立即取用。
	handler.SetConnMgr(server.ConnectionManager())

	// 9. 异步启动 Web 服务
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 9.1 MCP 后台异步初始化（不阻塞 WebUI）。
	// InitializeAll（spawn 子进程 / HTTP 建连 / JSON-RPC 握手）+ RegisterAll（拉取远端工具）
	// 都有 IO，放后台让浏览器先弹出可用。就绪后把工具注入 registry 并广播 mcp_status。
	//
	// [取消语义] InitializeAll/RegisterAll 用各自的独立 timeout ctx，不直接接 runCtx：
	// 避免用户正常退出（runCtx cancel）时强制截断正在握手的 server、留下僵尸 stdio 子进程。
	// 退出语义由"阶段间 runCtx 检查 + 退出流程的 mcpPool.CloseAll 兜底"三段式保证。
	// [recover] 防止 MCP 初始化 panic 拖垮整个进程。
	if mcpPool != nil && mcpBuild != nil && len(mcpBuild.PoolConfigs) > 0 {
		poolConfigs := mcpBuild.PoolConfigs
		go func() {
			defer func() {
				if r := recover(); r != nil {
					logger.Error("MCP 后台初始化 panic，已恢复",
						zap.Any("panic", r),
						zap.String("stack", string(debug.Stack())),
					)
				}
			}()

			// 1. InitializeAll：并发拉起所有 server（单 server 失败仅记 unhealthy，不阻断其他）
			initCtx, initCancel := context.WithTimeout(context.Background(), 60*time.Second)
			if err := mcpPool.InitializeAll(initCtx, poolConfigs); err != nil {
				logger.Warn("MCP pool 初始化返回错误（部分 server 可能未就绪）", zap.Error(err))
			}
			initCancel()
			healthy := mcpPool.HealthyNames()
			logger.Info("MCP pool 启动完成",
				zap.Int("healthy", len(healthy)),
				zap.Strings("healthy_names", healthy),
				zap.Int("unhealthy", len(mcpBuild.Skipped)+len(mcpPool.Unhealthy())),
			)

			// 2. runCtx 检查点：进程正在退出则不再注册工具、不再广播
			if runCtx.Err() != nil {
				return
			}

			// 3. RegisterAll：批量把远端工具注册到 tool.Registry（晚加载——本轮 runStream
			//    若已发出快照则不含新工具，下一轮 user_input 自动包含）
			regCtx, regCancel := context.WithTimeout(context.Background(), 15*time.Second)
			stats, regErr := adapter.RegisterAll(regCtx, mcpPool, toolRegistry, logger.L())
			regCancel()
			if regErr != nil {
				logger.Warn("MCP 工具注册失败", zap.Error(regErr))
			} else if stats != nil {
				logger.Info("MCP 工具注册完成",
					zap.Int("tools", stats.ToolsRegistered),
					zap.Int("servers", stats.ServersProcessed),
					zap.Int("skipped", stats.SkippedDuplicate),
				)
			}

			// 4. 推送真实状态（Initializing() 此刻已复位为 false，loading 由 true 翻 false）
			if runCtx.Err() == nil {
				handler.BroadcastMCPStatus()
			}
		}()
	}

	serverErrCh := make(chan error, 1)
	go func() {
		if err := server.Start(runCtx); err != nil {
			serverErrCh <- err
		}
		close(serverErrCh)
	}()

	// 10. 等待 server 真正完成 listen 后，再用真实端口打开浏览器。
	// Ready 与 serverErrCh 同时监听：listen 失败时不会一直阻塞。
	select {
	case <-server.Ready():
		openURL := "http://" + server.Addr()
		if err := web.OpenURL(openURL); err != nil {
			fmt.Fprintf(os.Stderr, "[warning] 无法自动打开浏览器，请手动访问 %s: %v\n", openURL, err)
			logger.Warn("自动打开浏览器失败", zap.String("url", openURL), zap.Error(err))
		} else {
			fmt.Fprintf(os.Stdout, "[info] CodePilot 已启动，访问地址：%s\n", openURL)
			logger.Info("已请求打开浏览器", zap.String("url", openURL))
		}
	case err := <-serverErrCh:
		// listen 还没成功 server 就已退出（典型场景：端口被占用等）。
		if err != nil {
			return fmt.Errorf("Web 服务启动失败: %w", err)
		}
		return nil
	}

	// 11. 浏览器已弹出，隐藏 Windows 终端窗口，让 CodePilot 在后台静默运行。
	// 非 Windows 平台 / 进程无控制台时该调用为 no-op，不会报错。
	// 若有需要查看日志，用户可在启动后通过任务管理器打开控制台或查看文件日志。
	if console.Visible() {
		console.Hide()
		logger.Info("已隐藏终端窗口，CodePilot 在后台运行")
	}

	// 12. 启动后台 goroutine 监听"浏览器关闭"事件：所有 WebSocket
	// 断开后等一个宽限期，期间若有新连接恢复则重置 timer；
	// 宽限期到仍无新连接则向主 select 发送 trigger 触发退出。
	// 这样既能容忍浏览器刷新/暂时断网，也能在用户真正关闭窗口时自动退出。
	allClosedTrigger := make(chan struct{}, 1)
	go func() {
		ch := server.ConnectionManager().AllClosed()
		for {
			// 等待一次"活跃连接数 1→0"事件
			<-ch
			timer := time.NewTimer(browserExitGracePeriod)
			// 宽限期内若再次收到断开事件（重连后又断开），重置 timer
			select {
			case <-ch:
				timer.Stop()
				continue
			case <-timer.C:
				select {
				case allClosedTrigger <- struct{}{}:
				default:
				}
				return
			}
		}
	}()

	// 13. 等待信号或服务异常
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	select {
	case sig := <-sigCh:
		logger.Info("收到退出信号，开始优雅退出", zap.String("signal", sig.String()))
	case err := <-serverErrCh:
		if err != nil {
			return fmt.Errorf("Web 服务运行出错: %w", err)
		}
		// server 自然结束（一般由 ctx 取消触发），正常退出
		return nil
	case <-allClosedTrigger:
		logger.Info("浏览器窗口已关闭，宽限期内无新连接，自动退出",
			zap.Duration("grace", browserExitGracePeriod),
		)
	}

	// 14. 触发 server 关闭并等待 goroutine 退出
	cancel()
	if err := <-serverErrCh; err != nil {
		logger.Warn("Web 服务退出时返回错误", zap.Error(err))
	}
	// 关闭 MCP pool：优雅回收 stdio 子进程 / 关闭 HTTP 连接、停掉各 Session 的 recvLoop。
	// [Why] 此前靠进程退出隐式回收，可能导致 stdio 子进程短暂残留；异步初始化后，
	// 后台 goroutine 若仍卡在 InitializeAll 握手中，CloseAll 关 transport 可使其快速返回。
	// CloseAll 幂等（atomic CAS），且其 ctx 参数被忽略，直接传 background。
	if mcpPool != nil {
		mcpPool.CloseAll(context.Background())
	}
	logger.Info("CodePilot 已退出",
		zap.String("current_session", handler.CurrentSessionID()),
	)
	return nil
}
