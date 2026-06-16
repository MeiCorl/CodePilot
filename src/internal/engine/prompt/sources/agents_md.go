// Package sources（agents_md.go）实现「AGENTS.md 项目指令」Source。
//
// 加载两级 AGENTS.md：
//   1. 全局：~/.codepilot/AGENTS.md（用户个人偏好，跨项目生效）
//   2. 项目级：<cwd>/AGENTS.md（项目级约定，仅本项目生效）
//
// 合并策略：项目级同名段落**完全覆盖**全局（不做内容合并拼接），
// 不同名段落按各自文件中的出现顺序保留；先列全局独有段、再列项目级独有段。
//
// 文件格式：标准 Markdown，H2（`## name`）作为段落分隔。
// 没有 H2 时整个文件视为一个「通用」段落（空 key），仍可正常加载与合并。
//
// 加载路径可通过 AgentsMDPath 字段在 Env 中覆盖（用于测试与开发者模式）。
//
// 重要：合并后的内容作为 LeadUserMessage（Placement=UserMessage），
// 不进 system 字段，避免与「项目级内容可能很长」相关的注意力稀释。
package sources

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/MeiCorl/CodePilot/src/internal/engine/prompt/template"
	"github.com/MeiCorl/CodePilot/src/internal/engine/prompt/tokens"
	"github.com/MeiCorl/CodePilot/src/internal/logger"
	"go.uber.org/zap"
)

// agentsMDMaxBytes 是单个 AGENTS.md 文件的最大字节数。
// 超过部分截断并打 warning 日志；64KB 足够覆盖绝大多数项目的指令规模。
const agentsMDMaxBytes = 64 * 1024

// agentsMDFileName 是 AGENTS.md 的固定文件名。
const agentsMDFileName = "AGENTS.md"

// agentsMDDirName 是 CodePilot 在用户主目录下的子目录名。
const agentsMDDirName = ".codepilot"

// AgentsMDSource 实现 Source 接口，产出全局 + 项目级 AGENTS.md 合并后的内容。
//
// 该 Source 无内部状态（除依赖 Env.CWD / Env.AgentsMDPath），可并发、可重放。
// Env 字段说明：
//   - CWD: 项目级 AGENTS.md 的查找目录（一般由 handler 设为进程启动目录）
//   - AgentsMDPath: 可选覆盖路径，主要用于测试和「AGENTS.md 实际不在 CWD 根目录」场景
//   - 若两者都为空，Source 会自动调用 os.UserHomeDir() + 拼全局路径，
//     同时 os.Getwd() 获取 CWD 作为项目级路径
type AgentsMDSource struct {
	// HomeDirForTest 用于测试时注入 home 目录；运行时为 ""，由 os.UserHomeDir() 解析。
	// 字段名带 "ForTest" 后缀是 Go 社区惯用约定，明确该字段仅供测试使用；
	// 生产代码不应该触碰它。
	HomeDirForTest string
	// GetwdForTest 用于测试时注入 CWD；运行时为 nil，由 os.Getwd() 解析。
	// 命名同上：仅测试使用。
	GetwdForTest func() (string, error)
}

// NewAgentsMDSource 构造一个 AGENTS.md Source 实例。
// 默认使用 os.UserHomeDir() + os.Getwd() 获取路径。
func NewAgentsMDSource() *AgentsMDSource {
	return &AgentsMDSource{}
}

// Name 实现 Source 接口。
func (s *AgentsMDSource) Name() string { return "agents_md" }

// Assemble 加载两级 AGENTS.md，按 H2 段落合并后输出为 LeadUserMessage。
//
// 输出 Content 格式：
//
//	<project_instructions>
//	## global-section-1
//	<global body>
//	## project-section-A
//	<project body>
//	</project_instructions>
//
// 任一文件缺失时不报错，对应侧视为空；
// 任一文件超 64KB 时截断并打 warning 日志（不影响加载）。
// 任何文件级错误都不向上抛，AGENTS.md 缺失不应阻塞会话启动。
func (s *AgentsMDSource) Assemble(_ context.Context, env Env) (Section, error) {
	globalPath, projectPath, err := s.resolvePaths(env)
	if err != nil {
		// 路径解析失败（如 homeDir 拿不到）→ 降级为空内容
		logger.Warn("agents_md: 解析路径失败", zap.Error(err))
		return Section{
			Name:      "agents_md",
			Content:   "",
			Placement: PlacementUserMessage,
			Tokens:    0,
		}, nil
	}

	// 加载两侧（任一缺失降级为空）
	globalSections := s.loadFile(globalPath)
	projectSections := s.loadFile(projectPath)

	// 合并
	merged := mergeSections(globalSections, projectSections)

	// 渲染为 Markdown 文本
	body := renderSections(merged)

	// 模板变量替换（{{VERSION}}/{{DATE}} 等）
	body = template.Render(body, env)

	// 外层包 <project_instructions> 标签
	final := wrapProjectInstructions(body)

	return Section{
		Name:      "agents_md",
		Content:   final,
		Placement: PlacementUserMessage,
		Tokens:    tokens.Estimate(final),
	}, nil
}

// resolvePaths 计算全局 / 项目级 AGENTS.md 的绝对路径。
// 优先使用 env.AgentsMDPath / env.CWD；缺失时调用 os 包降级。
func (s *AgentsMDSource) resolvePaths(env Env) (globalPath, projectPath string, err error) {
	// 全局路径：<home>/.codepilot/AGENTS.md
	home := s.HomeDirForTest
	if home == "" {
		home, err = os.UserHomeDir()
		if err != nil {
			return "", "", fmt.Errorf("获取 home 目录失败: %w", err)
		}
	}
	globalPath = filepath.Join(home, agentsMDDirName, agentsMDFileName)

	// 项目级路径：<cwd>/AGENTS.md
	cwd := env.CWD
	if cwd == "" {
		getwd := s.GetwdForTest
		if getwd == nil {
			getwd = os.Getwd
		}
		cwd, err = getwd()
		if err != nil {
			return "", "", fmt.Errorf("获取 cwd 失败: %w", err)
		}
	}
	projectPath = filepath.Join(cwd, agentsMDFileName)

	return globalPath, projectPath, nil
}

// section 表示 AGENTS.md 中的一个 H2 段落。
type section struct {
	// name 是 H2 标题文本（去掉 "## " 前缀），如 "code style"
	// 文件无 H2 时 name 为 ""，整段内容存放在 body
	name string
	// body 是该段落正文（不含 H2 标题行）
	body string
	// order 是该段落在源文件中的出现顺序（0-indexed）
	// 合并时用于保持原始文件顺序
	order int
}

// loadFile 读取单个 AGENTS.md 文件，按 H2 解析前先递归展开 @include 引用。
//
// 行为：
//  1. 文件不存在 / 不可读 → 返回 nil（不视为错误）
//  2. 文件超 agentsMDMaxBytes → 截断到该长度，打 warning 日志
//  3. 文件为空 / 只有空白 → 返回 nil
//  4. 无 H2 时整个内容作为单个 name="" 的 section
//  5. 有 H2 时按 H2 切分；H2 之前的「前言」段落作为 name=""
func (s *AgentsMDSource) loadFile(path string) []section {
	data, err := os.ReadFile(path)
	if err != nil {
		// 文件不存在是最常见情况（首次启动 / 项目无 AGENTS.md），不算错误
		if !os.IsNotExist(err) {
			logger.Warn("agents_md: 读取文件失败",
				zap.String("path", path),
				zap.Error(err),
			)
		}
		return nil
	}

	// 大小限制
	if len(data) > agentsMDMaxBytes {
		logger.Warn("agents_md: 文件超过 64KB 限制，已截断",
			zap.String("path", path),
			zap.Int("original_bytes", len(data)),
			zap.Int("truncated_to", agentsMDMaxBytes),
		)
		data = data[:agentsMDMaxBytes]
	}

	content := string(data)
	if strings.TrimSpace(content) == "" {
		return nil
	}

	// @include 展开：把 @path/to/file.md 引用替换为被引用文件的内容。
	// 路径基准 = 当前 AGENTS.md 所在目录，跨文件移动不失效。
	// 失败（不存在/循环/超深/路径逃逸）由 expandIncludes 内部降级为注释占位，
	// 不向上抛错，保持与"AGENTS.md 缺失不阻塞"一致的失败语义。
	content = s.expandIncludes(content, filepath.Dir(path))

	return parseSections(content)
}

// parseSections 把 Markdown 文本按 H2（`## name`）切分为 section 切片。
// 切分规则：
//  1. 每行以 "## " 开头的视为 H2 标题（标题文本 = 去掉 "## " 前缀后 trim）
//  2. 首个 H2 之前的内容（可能是前言/序言）作为一个 name="" 的 section
//  3. H2 之间（包括最后一个 H2 到文件末尾）的内容为该 H2 的 body
func parseSections(content string) []section {
	var sections []section
	var current *section
	order := 0

	scanner := bufio.NewScanner(strings.NewReader(content))
	// 单行最大 1MB（防止恶意超长行撑爆内存）
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		// 匹配 H2 标题：以 "## " 开头（注意不是 "# " 或 "### "）
		// 用 strings.HasPrefix 简单实现，避免引入正则
		const h2Prefix = "## "
		if strings.HasPrefix(line, h2Prefix) {
			// 把当前段（如果有）写入切片
			if current != nil {
				current.body = strings.TrimRight(current.body, "\n")
				sections = append(sections, *current)
			}
			// 开启新段
			name := strings.TrimSpace(strings.TrimPrefix(line, h2Prefix))
			current = &section{name: name, order: order}
			order++
			continue
		}
		// 普通行追加到当前段的 body
		if current == nil {
			// 首个 H2 之前的前言：创建 name="" 段
			current = &section{name: "", order: order}
			order++
		}
		current.body += line + "\n"
	}
	// 收尾：把最后一段写入
	if current != nil {
		current.body = strings.TrimRight(current.body, "\n")
		sections = append(sections, *current)
	}

	// 过滤掉空段（body 完全为空白且 name 为空）
	result := sections[:0]
	for _, sec := range sections {
		if sec.name == "" && strings.TrimSpace(sec.body) == "" {
			continue
		}
		result = append(result, sec)
	}
	return result
}

// mergeSections 按「项目级优先」策略合并两批 section。
//
// 规则：
//  1. 以 name 为 key 建立 map（name 唯一，重复以最后一次出现为准）
//  2. 先放入全局所有段；项目级同名段**完全覆盖**全局段（不拼接 body）
//  3. 项目级独有的段追加到全局段之后
//  4. 全局段中 name="" 的「前言」段特殊处理：项目级前言存在时也整体覆盖
//
// 返回的切片按「全局段（保持全局文件顺序）+ 项目级独有段（保持项目文件顺序）」排序。
func mergeSections(global, project []section) []section {
	if len(global) == 0 && len(project) == 0 {
		return nil
	}
	if len(project) == 0 {
		return global
	}
	if len(global) == 0 {
		return project
	}

	// 用 map 按 name 索引，方便查找覆盖关系
	// 顺序用「全局文件出现顺序」与「项目文件出现顺序」两个独立 slice 维护
	result := make([]section, 0, len(global)+len(project))

	// 先用项目级 section 建立 name → 项目的覆盖关系索引
	projectByName := make(map[string]section, len(project))
	for _, sec := range project {
		projectByName[sec.name] = sec
	}

	// 走一遍全局段：项目级有同名段则用项目级替换，否则保留全局段
	covered := make(map[string]bool, len(project))
	for _, sec := range global {
		if p, ok := projectByName[sec.name]; ok {
			result = append(result, p)
			covered[sec.name] = true
		} else {
			result = append(result, sec)
		}
	}

	// 追加项目级独有的段（按 project 切片中出现的顺序）
	for _, sec := range project {
		if _, ok := covered[sec.name]; ok {
			continue
		}
		// 已经被上面以「项目级替换全局」形式追加过的（name 已 covered）跳过
		// 这里要追加的是「全局没有同名」的项目级段
		// 判断标准：全局中是否出现该 name
		existsInGlobal := false
		for _, g := range global {
			if g.name == sec.name {
				existsInGlobal = true
				break
			}
		}
		if !existsInGlobal {
			result = append(result, sec)
		}
	}
	return result
}

// renderSections 把 section 切片渲染为 Markdown 文本。
// 每个 section 用 `## name\n<body>` 格式输出；name 为空时省略 H2 行。
func renderSections(sections []section) string {
	if len(sections) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, sec := range sections {
		if i > 0 {
			sb.WriteString("\n")
		}
		if sec.name != "" {
			sb.WriteString("## ")
			sb.WriteString(sec.name)
			sb.WriteString("\n")
		}
		sb.WriteString(sec.body)
		if i < len(sections)-1 {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// wrapProjectInstructions 把 Markdown 内容外层包 <project_instructions> 标签。
// 空内容返回空串（避免出现 <project_instructions></project_instructions> 的空壳）。
func wrapProjectInstructions(body string) string {
	if body == "" {
		return ""
	}
	return "<project_instructions>\n" + body + "\n</project_instructions>"
}

// drainLimitedReader 是辅助函数：把 io.LimitReader 读满后的内容返回。
// 实际未被使用，保留作为「未来限制读取大小」的扩展位（现 ReadFile 已用 bytes cap）。
func drainLimitedReader(r io.Reader, n int64) ([]byte, error) {
	limited := io.LimitReader(r, n)
	buf := make([]byte, n)
	total, err := io.ReadFull(limited, buf)
	return buf[:total], err
}
