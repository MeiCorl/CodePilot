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
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/MeiCorl/CodePilot/src/internal/config"
	"github.com/MeiCorl/CodePilot/src/internal/engine/conversation"
	"github.com/MeiCorl/CodePilot/src/internal/engine/prompt"
	"github.com/MeiCorl/CodePilot/src/internal/engine/prompt/sources"
	"github.com/MeiCorl/CodePilot/src/internal/interaction/web"
	"github.com/MeiCorl/CodePilot/src/internal/logger"
	"github.com/MeiCorl/CodePilot/src/internal/memory/session"
	"github.com/MeiCorl/CodePilot/src/internal/runtime/console"
	"github.com/MeiCorl/CodePilot/src/internal/security"
	"github.com/MeiCorl/CodePilot/src/llm"
	"github.com/MeiCorl/CodePilot/src/internal/tool"
	// import 触发 builtin 包的 init()，将 5 个内置工具以 cwd + 30s 兜底
	// 注册到 tool.DefaultRegistry()；main 随后按 cfg 调
	// builtin.RegisterWithOptions 用 cfg 中的工作目录/超时覆盖默认实例。
	"github.com/MeiCorl/CodePilot/src/internal/tool/builtin"
)

const (
	// defaultMaxRounds 滑动窗口默认保留的最大对话轮数。
	// 50 轮 ≈ 100 条消息，足以覆盖大多数会话；Step 7 上下文管理将替换为完整策略。
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

// run 是主流程入口；返回 error 表示启动或运行过程中发生不可恢复错误。
// 拆出独立函数便于在测试中调用（虽然 step1.1 暂未引入 main 测试）。
func run() error {
	// 1. 初始化文件日志
	if err := logger.Init(); err != nil {
		fmt.Fprintf(os.Stderr, "[warning] 日志初始化失败，将不写文件日志: %v\n", err)
	}
	defer logger.Sync()
	defer logger.Close()

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

	// 4. 创建会话管理器（内部自动加载最近会话）
	sessMgr, err := session.NewSessionManager()
	if err != nil {
		return fmt.Errorf("创建会话管理器失败: %w", err)
	}

	// 5. 获取启动时所在工作目录，顶栏展示用
	workdir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("获取当前工作目录失败: %w", err)
	}
	logger.Info("工作目录", zap.String("workdir", workdir))

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
	toolHandler.RegisterMiddleware(security.SandboxMiddleware(toolWorkdir, checker))

	logger.Info("权限系统就绪",
		zap.String("mode", string(checker.Mode())),
		zap.Int("rules", checker.RuleCount()),
	)

	// 7. 构造 System Prompt Builder（Step 4：分层 SP 体系）。
	// 4 个 Source 按注册顺序产出内容：
	//   - static:       5 个硬编码子模块（角色/行为/代码质量/工具/安全）
	//   - environment:  OS + CWD + Git 状态
	//   - agents_md:    全局 + 项目级 AGENTS.md 合并
	//   - memory:       自动记忆（Step 8 接入；当前为 NoopMemoryProvider）
	promptBuilder := prompt.NewBuilder(
		sources.NewStaticSource(),
		sources.NewEnvironmentSource(),
		sources.NewAgentsMDSource(),
		sources.NewMemorySource(sources.NewNoopMemoryProvider(), nil),
	)

	// 8. 构造 Handler / Server
	// 使用 DefaultAddr（127.0.0.1:0）让 OS 自动分配端口，
	// 这样多个项目下可同时启动多个 CodePilot 进程互不冲突。
	handler := web.NewHandler(provider, sessMgr, cfg, defaultMaxRounds, promptBuilder, cfg.ContextWindowSize, workdir, toolRegistry, toolHandler, fileDiffStore)
	// 注入权限拦截器 + HITL 回调（Handler 内部实现 WebSocket 交互）
	handler.SetInterceptor(interceptor, checker)
	server := web.NewServer(web.DefaultAddr)
	handler.Register(server.Router())

	// 9. 异步启动 Web 服务
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

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
	logger.Info("CodePilot 已退出",
		zap.String("current_session", handler.CurrentSessionID()),
	)
	return nil
}
