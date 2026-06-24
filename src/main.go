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
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/MeiCorl/CodePilot/src/internal/command/slash"
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
	"github.com/MeiCorl/CodePilot/src/internal/skill"
	skilladapter "github.com/MeiCorl/CodePilot/src/internal/skill/adapter"
	skillsources "github.com/MeiCorl/CodePilot/src/internal/skill/sources"
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

// ---- Slash 命令注册表适配器（Step 9.1 Task 6） ----
//
// [Why 需要 adapter] web.Handler 通过 SlashCommandProvider 接口（List + OnChange）消费
// slash 命令清单；*slash.Registry 本身提供的方法签名（List 返回 []SlashCommand 接口切片、
// OnChange 入参 func()）与 web.SlashCommandProvider 接口（List 返回 []SlashCommandEntry
// 具体切片、OnChange 入参 func()）**形状不同**——web.SlashCommandEntry 是 handler 包内
// 私有 struct，slash.Registry 自然不能直接返回该类型。因此在 main.go 顶层做一次薄包装：
//   - adapter 持有 *slash.Registry 引用
//   - List() 把 []SlashCommand 转 []SlashCommandEntry（5 字段投影）
//   - OnChange() 透传 registry.OnChange
//
// 这样 web 包只知道"拿到的是 web.SlashCommandEntry 列表"，不感知 slash 包的存在，
// 维持 spec.md 中"web → slash 单向依赖"的方向约束（slash.builtin 持 *web.Handler
// 引用但 web 包不 import slash 包）。

// slashAdapter 把 *slash.Registry 适配为 web.SlashCommandProvider 接口。
// 单一职责：字段投影 + 回调透传，零业务逻辑。
type slashAdapter struct {
	registry *slash.Registry
}

// newSlashAdapter 构造一个把 *slash.Registry 适配为 web.SlashCommandProvider 的实例。
// 参数：
//   - registry：slash 命令注册中心指针；为 nil 时 List 返回空切片、OnChange 为 no-op
//
// 返回值：*slashAdapter 指针，满足 web.SlashCommandProvider 接口约束。
func newSlashAdapter(registry *slash.Registry) *slashAdapter {
	return &slashAdapter{registry: registry}
}

// List 返回当前所有已注册命令的 web.SlashCommandEntry 投影（按 Registry 注册顺序）。
// 实现 web.SlashCommandProvider.List 签名要求。
func (a *slashAdapter) List() []web.SlashCommandEntry {
	if a.registry == nil {
		return nil
	}
	cmds := a.registry.List()
	if len(cmds) == 0 {
		return []web.SlashCommandEntry{}
	}
	entries := make([]web.SlashCommandEntry, 0, len(cmds))
	for _, c := range cmds {
		entries = append(entries, web.SlashCommandEntry{
			Name:        c.Name(),
			Description: c.Description(),
			NeedsArg:    c.NeedsArg(),
			ArgHint:     c.ArgHint(),
			Category:    c.Category(),
		})
	}
	return entries
}

// OnChange 透传 slash.Registry.OnChange；handler 注入后注册一个"命令清单变化"回调，
// 用于在 Step 10 Skill 动态注册场景下推 slash_commands_updated。
func (a *slashAdapter) OnChange(fn func()) {
	if a.registry == nil {
		return
	}
	a.registry.OnChange(fn)
}

// ---- Skill 注册表适配器（Step 10 Task 6） ----
//
// [Why 需要 adapter] web.Handler 通过 SkillProvider 接口（List + ListBySource）
// 消费 Skill 清单；*skill.Registry 暴露的 List/ListBySource 返回 *skill.Skill，
// 包含 Source 枚举（int）等内部细节，web 包不能直接依赖（避免 import cycle
// 与分层倒挂）。因此在 main.go 顶层做一次薄包装：
//   - adapter 持有 *skill.Registry 引用
//   - List() 把 []*skill.Skill 投影为 []web.SkillEntry（4 字段投影）
//   - ListBySource(source) 按 source 字符串过滤后投影
//
// web 包只知道"拿到的是 web.SkillEntry 列表"，不感知 skill 包的存在。
// 与 slashAdapter 的设计模式完全一致：web → 单向消费上层数据，避免 web → skill
// 反向 import 链路。

// skillProviderAdapter 把 *skill.Registry 适配为 web.SkillProvider 接口。
// 单一职责：字段投影（*skill.Skill → web.SkillEntry），零业务逻辑。
type skillProviderAdapter struct {
	registry *skill.Registry
}

// newSkillProviderAdapter 构造一个把 *skill.Registry 适配为 web.SkillProvider 的实例。
// 参数：
//   - registry：Skill 注册中心指针；为 nil 时 List / ListBySource 返回空切片
//
// 返回值：*skillProviderAdapter 指针，满足 web.SkillProvider 接口约束。
func newSkillProviderAdapter(registry *skill.Registry) *skillProviderAdapter {
	return &skillProviderAdapter{registry: registry}
}

// skillToEntry 把 *skill.Skill 投影为 web.SkillEntry（4 字段）。
// Source 字段走 skill.Source.String() 字符串投影（与前端 SkillsListPayload
// 的三档数组对应：project / user / builtin）。
// registry 为 nil 时返回 nil 切片；ListBySource source 不识别时同样返回 nil。
func (a *skillProviderAdapter) skillToEntry(s *skill.Skill) web.SkillEntry {
	if s == nil {
		return web.SkillEntry{}
	}
	return web.SkillEntry{
		Name:        s.Name,
		Description: s.Description,
		Source:      s.Source.String(),
		Path:        s.RootPath,
	}
}

// List 返回所有已加载 Skill 的扁平投影列表（按 Registry 注册顺序）。
// 实际按 Source 顺序（项目级 → 用户级 → 内置级）排列，由 Registry.List 内部保证。
// 实现 web.SkillProvider.List 签名要求。registry 为 nil 时返回 nil 切片。
func (a *skillProviderAdapter) List() []web.SkillEntry {
	if a.registry == nil {
		return nil
	}
	skills := a.registry.List()
	if len(skills) == 0 {
		return []web.SkillEntry{}
	}
	out := make([]web.SkillEntry, 0, len(skills))
	for _, s := range skills {
		out = append(out, a.skillToEntry(s))
	}
	return out
}

// ListBySource 按 source 字符串（"project" / "user" / "builtin"）返回该档下的
// Skill 投影列表，按 Registry 注册顺序。未识别的 source 返回 nil 切片。
// 实现 web.SkillProvider.ListBySource 签名要求。
func (a *skillProviderAdapter) ListBySource(source string) []web.SkillEntry {
	if a.registry == nil {
		return nil
	}
	var src skill.Source
	switch source {
	case "project":
		src = skill.SourceProject
	case "user":
		src = skill.SourceUser
	case "builtin":
		src = skill.SourceBuiltin
	default:
		// 防御性：handler 端约定只传 "project" / "user" / "builtin"；
		// 收到其他值时返回 nil（不暴露给前端错误状态，list_skills payload
		// 退化为单档空数组，前端 tab 列表为空）。
		return nil
	}
	skills := a.registry.ListBySource(src)
	if len(skills) == 0 {
		return []web.SkillEntry{}
	}
	out := make([]web.SkillEntry, 0, len(skills))
	for _, s := range skills {
		out = append(out, a.skillToEntry(s))
	}
	return out
}

// ---- Skill 注入适配器（Step 10 Task 7） ----
//
// [Why 需要 adapter] Skill 适配器包定义的 LeadMessageInjector 接口（InjectLeadUserMessage
// 方法）要求把 Skill 完整内容 + 可选 <user_args> 段写入 *ConversationManager.leadUserMessage，
// 但 adapter 包不能直接 import engine/conversation（避免反向依赖）。*web.Handler
// 是唯一直接持有 *ConversationManager 的层（assembleSP 在内部直接访问 h.conv），
// Step 10 Task 7 在 web 包内增加 Handler.InjectLeadUserMessage 导出方法包装
// h.conv.SetLeadUserMessage；main.go 顶层把 *web.Handler 适配为 LeadMessageInjector：
//   - leadInjectorAdapter 持有 *web.Handler 引用；
//   - InjectLeadUserMessage 转发到 handler.InjectLeadUserMessage。
//
// 这样 Skill 适配器（slash 子包）只依赖接口、不感知 conversation 包或 web 包
// 的具体实现（adapter 看到的接口只声明 InjectLeadUserMessage 方法，main.go
// 顶层实现负责 wire）。维持 spec.md「slash 不依赖 web/conversation」的边界约束。
type leadInjectorAdapter struct {
	h *web.Handler
}

// newLeadInjectorAdapter 构造一个把 *web.Handler 适配为
// skilladapter.LeadMessageInjector 的实例。
// 参数：
//   - h：web.Handler 指针；为 nil 时 InjectLeadUserMessage 直接返回 nil（降级）
func newLeadInjectorAdapter(h *web.Handler) *leadInjectorAdapter {
	return &leadInjectorAdapter{h: h}
}

// InjectLeadUserMessage 把 content（含 Skill 完整正文与 <user_args> 段）写入
// 对话管理器的 leadUserMessage 字段，由 GetContext 在窗口派生结果前拼到 messages 最前。
//
// [Why 写入 leadUserMessage] 与 spec §B.3 对齐：用户触发 /<skill> 时 Skill 完整内容
// 应作为 LeadUserMessage 注入到下一轮 user 消息头部，LLM 端据此理解 Skill 工作流。
// leadUserMessage 字段是会话级一次性注入（Step 4 Task 5 已实现），由 prompt.Builder
// 在每次会话启动时重置。
func (a *leadInjectorAdapter) InjectLeadUserMessage(content, _ string) error {
	if a == nil || a.h == nil {
		return nil
	}
	return a.h.InjectLeadUserMessage(content)
}

// buildSkillRoots 计算 Skill 系统的三类路径：项目根（cwd）、用户根（homeDir）、
// 可执行文件根（execDir）。与 buildMemoryRoots 风格同构——主流程与配置无关的
// 路径计算统一集中在 main.go 顶层，便于测试和复用。
//
// [Why 单独函数] 后续 Step 11 / Step 12 接入时也可以复用同一组路径，避免
// 各子系统对 homeDir / execDir 解析逻辑各自实现导致漂移。
func buildSkillRoots(toolWorkdir string) (workdir, homeDir, execDir string) {
	workdir = toolWorkdir
	if home, err := os.UserHomeDir(); err == nil {
		homeDir = home
	}
	if exe, err := os.Executable(); err == nil {
		execDir = filepath.Dir(exe)
	} else {
		// fallback:取当前工作目录
		execDir = workdir
	}
	return workdir, homeDir, execDir
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

	// 6.4 Step 10 Task 7：Skill 系统装配（按 spec §A §B §C §D 全链路接入）。
	//
	// 装配顺序:
	//   1) 解析 workdir / homeDir / execDir 三类根路径
	//   2) cfg.Skill.Enabled=true → 调 skill.LoadAll 扫描三档目录并构造 *skill.Registry
	//      - ErrSkillConflict: 启动期致命错误,记录日志并退出进程(spec §A.4)
	//      - LoadIssue: 单 Skill 解析失败,记录 warn 后继续(spec §A.5)
	//   3) use_skill 工具注册到 tool.Registry(LLM 主动调用入口)
	//   4) Skill → SlashCommand 适配器批量注册到 slash.Registry(/<skill> 触发)
	//   5) SkillsIndexSource 注入 prompt.Builder 末尾(渐进式披露索引)
	//
	// cfg.Skill.Enabled=false 时**完全跳过** Skill 加载:
	//   - 不调 LoadAll
	//   - 不注册 use_skill 工具
	//   - 不注入 SkillsIndexSource
	//   - 不注册 Skill 类 slash 命令
	// 但 /skills client 命令仍注册(前端拉到空列表,空状态展示)。
	skillWorkdir, skillHomeDir, skillExecDir := buildSkillRoots(toolWorkdir)
	var skillReg *skill.Registry
	if cfg.Skill.IsEnabled() {
		var issues []skill.LoadIssue
		var loadErr error
		skillReg, issues, loadErr = skill.LoadAll(skillWorkdir, skillHomeDir, skillExecDir, cfg.Skill.MaxSkillSizeBytes)
		if loadErr != nil {
			// 同级同名冲突等致命错误 → 启动期退出(spec §A.4)
			// 错误信息应含冲突 Skill 名称与源,fmt.Fprintln 给用户清晰提示
			fmt.Fprintln(os.Stderr, "[error] Skill 加载失败:", loadErr)
			return fmt.Errorf("skill 加载失败: %w", loadErr)
		}
		// 单 Skill 解析失败等 warn 级问题 → 记录日志后继续(spec §A.5)
		for _, iss := range issues {
			logger.Warn("skill 加载问题",
				zap.String("path", iss.Path),
				zap.String("source", iss.Source.String()),
				zap.Error(iss.Err))
		}
		logger.Info("Skill 系统就绪",
			zap.Int("count", skillReg.Count()),
			zap.String("workdir", skillWorkdir),
			zap.String("home_dir", skillHomeDir),
			zap.String("exec_dir", skillExecDir),
			zap.Int("max_skill_size_bytes", cfg.Skill.MaxSkillSizeBytes),
		)
	} else {
		logger.Info("Skill 系统已关闭（skill.enabled=false），跳过加载/工具/命令/SP 注入")
	}

	// use_skill 工具注册(skillReg != nil 时)。
	// 走 toolRegistry.Register(同名走 "use_skill" 字符串)而不是 MustRegister,
	// 因为同名校验失败时只记录 warn 不阻断启动(理论不会发生冲突,5 个内置工具
	// 不含 use_skill)。工具由 Skill 系统独占时使用,无副作用(只读)。
	if skillReg != nil {
		if err := toolRegistry.Register(skilladapter.NewUseSkillTool(skillReg)); err != nil {
			logger.Warn("use_skill 工具注册失败", zap.Error(err))
		}
	}

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

	// System Prompt Builder：5 个 Source 按注册顺序产出内容：
	//   - static:       5 个硬编码子模块（角色/行为/代码质量/工具/安全）
	//   - environment:  OS + CWD + Git 状态
	//   - agents_md:    全局 + 项目级 AGENTS.md 合并
	//   - memory:       自动记忆索引注入（Step 8，读两级 MEMORY.md → LeadUserMessage）
	//   - skills_index: Skill 渐进式披露索引注入（Step 10，name+description+source 三档分组）
	//     cfg.Skill.Enabled=false 或 skillReg==nil 时**不**注册该 Source
	//     (Builder 末端按条件追加,符合 Task 7 装配规范)
	promptSources := []sources.Source{
		sources.NewStaticSource(),
		sources.NewEnvironmentSource(),
		sources.NewAgentsMDSource(),
		sources.NewMemoryIndexSource(memoryStore, sources.MemoryIndexOptions{
			Enabled:  memEnabled,
			MaxLines: cfg.Memory.IndexMaxLines,
			MaxBytes: cfg.Memory.IndexMaxBytes,
		}),
	}
	if skillReg != nil {
		promptSources = append(promptSources, skillsources.NewSkillsIndexSource(skillReg))
	}
	promptBuilder := prompt.NewBuilder(promptSources...)

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

	// Step 9.1：装配 slash 命令注册表（Task 6 接入）。
	// 三步走：
	//   1) NewRegistry() 构造空注册中心；
	//   2) RegisterBuiltin 一站式注册 6 条内置命令（/new、/sessions、/resume、/clear、
	//      /compact、/dump）；任意一条注册失败直接 fatal 退出（启动时注册失败不可恢复）。
	//   3) 通过 newSlashAdapter 把 *slash.Registry 适配成 web.SlashCommandProvider
	//      注入 handler——web 包不直接 import slash 包，避免 web → slash → web 循环依赖。
	//
	// [Why 不在 main.go 顶层 import slash adapter] spec 要求"包边界清晰，slash 不
	// 依赖 web"；adapter 写在 main.go 顶层是更轻的位置，零新增包。
	slashRegistry := slash.NewRegistry()
	if err := slash.RegisterBuiltin(slashRegistry, handler); err != nil {
		return fmt.Errorf("注册 slash 内置命令失败: %w", err)
	}
	// Step 10 Task 6：注册 /skills client 类命令（纯前端消费，Execute 占位）。
	// 失败不致命（极小概率），记录后继续——/skills 模态框不可用不影响主流程。
	// 与 cfg.Skill.Enabled 解耦：无论 Skill 启用与否，/skills 命令都注册（前端拉空列表展示）。
	if err := slashRegistry.Register(&skilladapter.SkillsListCmd{}); err != nil {
		logger.Warn("注册 /skills 命令失败", zap.Error(err))
	}
	// Step 10 Task 7：Skill → SlashCommand 适配器批量注册。
	// skillReg==nil 时(cfg.Skill.Enabled=false 或 0 Skill)跳过——等价"未启用 Skill"。
	if skillReg != nil {
		// leadInjectorAdapter 包装 *web.Handler 暴露给 slash 适配层
		// 作为 LeadMessageInjector；与 slash 包不直接 import conversation 的设计保持一致。
		leadInjector := newLeadInjectorAdapter(handler)
		if errs := skilladapter.RegisterAll(slashRegistry, skillReg.List(), leadInjector); len(errs) > 0 {
			for _, e := range errs {
				logger.Warn("Skill slash 命令注册失败", zap.Error(e))
			}
		}
	}
	handler.SetSlashRegistry(newSlashAdapter(slashRegistry))
	logger.Info("slash 命令注册表就绪",
		zap.Int("count", slashRegistry.Count()),
	)

	// Step 10 Task 6/7：注入 SkillProvider（list_skills 协议的数据源）。
	// Task 7 把 skill.LoadAll(...) 返回的 *skill.Registry 通过 newSkillProviderAdapter
	// 包装后注入；skillReg==nil 时(handler 在 skillProvider 为 nil 时回推三档空数组,
	// 前端 /skills 模态框展示「暂无 Skill」空状态),与零 Skill 启动场景一致。
	handler.SetSkillProvider(newSkillProviderAdapter(skillReg))
	if skillReg != nil {
		logger.Info("Skill 列表提供器已就绪",
			zap.Int("count", skillReg.Count()),
		)
	} else {
		logger.Info("Skill 列表提供器已就绪（无 Skill 加载,/skills 显示空状态）")
	}

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

	// Step 9.1 Task 6：注册 ws onOpen 主动推送 slash_commands 回调。
	// 在 SetConnMgr 之后立即注册——connMgr.onOpenHook 在新连接 Add 完成后同步触发，
	// 调用 PushSlashCommandsOnOpen 推 slash_commands 消息，前端无需主动拉取即可拿到清单。
	// [Why 此处注册] ConnectionManager 已经构造完毕（NewServer 内部已完成），
	// SetOnOpenHook 任何时机调用都生效；但放在 SetConnMgr 紧邻位置保持装配顺序统一。
	//
	// [Why 用 PushSlashCommandsOnOpen 而非直接引用 onWSOpenSlash] onWSOpenSlash 是
	// handler 包内未导出方法（Step 9.1 Task 4 设计），不同包无法直接引用；
	// PushSlashCommandsOnOpen 是其导出别名，方法值可直接作为 SetOnOpenHook 入参，
	// 避免 lambda 闭包 + 错误日志样板代码。
	server.ConnectionManager().SetOnOpenHook(handler.PushSlashCommandsOnOpen)

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
