// agents_md_include.go 实现 AGENTS.md 的 @include 展开能力。
//
// 设计动机：
// AGENTS.md 是项目级约定文档，但实际项目里约定常常分散在多个文件
// （如 docs/style.md、docs/testing.md）。把全部内容塞进单一 AGENTS.md
// 既难维护也违反单一职责。让 AGENTS.md 通过 @path 引用其他 .md 文件，
// 把"项目规约的目录结构"作为 System Prompt 的一部分，是行业主流做法
// （Claude Code 即支持 @filename 自动展开）。
//
// 安全模型（Why 4 条防线缺一不可）：
//  1. 路径沙箱：被引用文件必须落在 baseDir（CWD 或 AGENTS.md 所在目录）内，
//     拒绝 ../ 逃逸与绝对路径，与 security.IsPathInside 同款约束；
//  2. 循环检测：用 map 追踪已访问绝对路径，命中即停止，避免 A→B→A 死循环；
//  3. 深度上限：递归深度 maxIncludeDepth（5），与 Claude Code 对齐；
//  4. 大小限制：被引用文件也走 agentsMDMaxBytes（64KB）截断，防止
//     递归 include 把一个 1MB 大文件塞进上下文撑爆缓存。
//
// 失败语义：
// 与现有"AGENTS.md 文件缺失不阻塞"风格保持一致——任何失败都用
// <!-- @path: 原因 --> 注释占位，不向上抛错、不阻塞会话启动。
// 这样"写错一个 @ 引用"不会让整个项目用不了 CodePilot。
//
// 引用语法：
//   @relative/path/file.md
// 规则：
//   - 必须以 .md 结尾（避免误吞 .txt/.json 等）
//   - 路径分隔符统一 /（在 Windows 下由 filepath 内部转 \）
//   - 不支持 @绝对路径 或 @~/xxx（避免与现有路径沙箱风格冲突）
//   - 路径允许字母数字 _ - . /
//
// 来源标注：
// 展开后用一行 HTML 注释 <!-- included from PATH --> 包裹，
// 让 LLM 与用户都能追溯"这段文本来自哪个被引用文件"。
package sources

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.uber.org/zap"

	"github.com/MeiCorl/CodePilot/src/internal/logger"
)

// maxIncludeDepth 限制 @include 的递归展开深度，与 Claude Code 默认对齐。
// 超出后插入 <!-- included from PATH: depth exceeded --> 占位，避免无限递归。
const maxIncludeDepth = 5

// includePattern 匹配 @relative/path.md 的所有出现。
// 规则：@ 后跟 1+ 合法路径字符 + 必须以 .md 结尾 + 单词边界。
// (?m) 多行模式让 ^ $ 匹配行首行尾；不强制整行匹配，允许行内夹杂。
var includePattern = regexp.MustCompile(`@([A-Za-z0-9_\-./]+\.md)\b`)

// includeMarkerFormat 是展开后在被引用内容前的标注行。
// 用 HTML 注释（<!-- ... -->）而非 Markdown 块，理由：
//   - HTML 注释不会被 LLM 当成"指令"误执行
//   - 渲染工具（marked.js）默认不显示
//   - 调试时 grep 容易定位
const includeMarkerFormat = "<!-- included from %s -->\n"

// includeErrorMarkerFormat 是失败占位的标注行（含原因）。
// 不抛错、不阻塞，失败原因显式化便于用户在 LLM 上下文里看到。
const includeErrorMarkerFormat = "<!-- @%s: %s -->\n"

// includeContext 持有单次 Assemble 调用的展开上下文（不可复用，无状态副本）。
//
// 为什么用 struct 而非全局 map：
//   - 全局 map 跨 Assemble 串扰，且无法并发（不同 session 同时启动会撞车）
//   - 每次 Assemble 重新构造 context，结束后随 GC 释放，零状态泄漏
type includeContext struct {
	// baseDir 是 @include 路径解析的基准目录。
	// 由 loadFile 传入（项目级 / 全局各自传各自的 AGENTS.md 所在目录）。
	baseDir string
	// visited 追踪本次展开链上已访问的绝对路径，用于循环检测。
	// key 是 filepath.Clean 后的绝对路径，命中即视为循环。
	visited map[string]bool
	// depth 当前递归深度（0 = 顶层 AGENTS.md 自身）。
	depth int
}

// expandIncludes 是包内公开入口，递归展开 content 中的所有 @include 引用。
//
// 调用时机：在 loadFile 之后、parseSections 之前。
// baseDir 是该 content 所在文件目录（用于解析 @ 相对路径）。
//
// 返回值：展开后的最终文本（含来源标注 / 失败占位）。
func (s *AgentsMDSource) expandIncludes(content, baseDir string) string {
	ctx := &includeContext{
		baseDir: baseDir,
		visited: map[string]bool{},
		depth:   0,
	}
	return ctx.expand(content)
}

// expand 是递归主体，处理 content 中的所有 @include 引用。
//
// 实现说明：
//   - 用 includePattern.FindAllStringSubmatchIndex 一次性扫描所有匹配位置
//   - 按位置切片拼接：prefix + replacement + suffix
//   - replacement 来自被引用文件的 expand 递归调用结果
//   - 行内非 @include 文本原样保留
func (c *includeContext) expand(content string) string {
	matches := includePattern.FindAllStringSubmatchIndex(content, -1)
	if len(matches) == 0 {
		return content
	}

	var sb strings.Builder
	// cursor 维护已处理到的字节位置，初始 0
	cursor := 0
	for _, m := range matches {
		// m[0:2] 是整个 @xxx.md 匹配位置
		// m[2:4] 是捕获组（去掉 @ 的路径）的位置
		matchStart, matchEnd := m[0], m[1]
		pathStart, pathEnd := m[2], m[3]
		rawPath := content[pathStart:pathEnd]

		// 拼接匹配前的原文
		sb.WriteString(content[cursor:matchStart])

		// 解析 + 安全校验 + 读取 + 递归展开
		replacement := c.resolveAndLoad(rawPath)

		sb.WriteString(replacement)
		cursor = matchEnd
	}
	// 拼接最后一段尾部原文
	sb.WriteString(content[cursor:])
	return sb.String()
}

// resolveAndLoad 是单条 @include 引用的处理入口。
//
// 流程：路径解析 → 安全校验 → 循环检测 → 读取 → 递归展开。
// 任何一步失败都返回 includeErrorMarkerFormat 占位，不向上抛错。
func (c *includeContext) resolveAndLoad(rawPath string) string {
	// 1. 路径解析：相对 baseDir
	resolved, err := resolveIncludePath(rawPath, c.baseDir)
	if err != nil {
		return fmt.Sprintf(includeErrorMarkerFormat, rawPath, err.Error())
	}

	// 2. 路径安全校验：必须在 baseDir 内（防 ../ 逃逸与绝对路径）
	//    resolveIncludePath 已做基础校验，这里再防一次极端 case
	if err := assertInsideBase(resolved, c.baseDir); err != nil {
		return fmt.Sprintf(includeErrorMarkerFormat, rawPath, err.Error())
	}

	// 3. 循环检测：命中 visited 即视为循环
	cleanResolved := filepath.Clean(resolved)
	if c.visited[cleanResolved] {
		return fmt.Sprintf(includeErrorMarkerFormat, rawPath, "circular include")
	}

	// 4. 深度限制
	if c.depth >= maxIncludeDepth {
		return fmt.Sprintf(includeErrorMarkerFormat, rawPath, "max include depth exceeded")
	}

	// 5. 读取文件（缺失不抛错）
	data, err := os.ReadFile(cleanResolved)
	if err != nil {
		// 不存在的文件 = 注释占位，常见场景（笔误）
		return fmt.Sprintf(includeErrorMarkerFormat, rawPath, "file not found")
	}

	// 6. 大小截断（与 AGENTS.md 64KB 限制一致）
	if len(data) > agentsMDMaxBytes {
		logger.Warn("agents_md: 被引用的文件超过 64KB 限制，已截断",
			zap.String("path", rawPath),
			zap.Int("original_bytes", len(data)),
			zap.Int("truncated_to", agentsMDMaxBytes),
		)
		data = data[:agentsMDMaxBytes]
	}

	// 7. 标记当前路径为已访问，递归展开，结束后取消标记
	c.visited[cleanResolved] = true
	c.depth++
	defer func() {
		c.depth--
		delete(c.visited, cleanResolved)
	}()

	expanded := c.expand(string(data))

	// 8. 标注来源（用源路径作为标识，便于 LLM 追溯）
	marker := fmt.Sprintf(includeMarkerFormat, rawPath)
	return marker + expanded
}

// resolveIncludePath 把 @ 后的原始路径解析为绝对路径。
//
// 规则：
//   - 拒绝绝对路径（POSIX 风格 / 开头、Windows 盘符 : 开头、filepath.IsAbs）
//   - 拒绝 .. 路径段（防路径遍历）
//   - 其它路径：相对 baseDir 解析后 Clean
//
// 错误信息以简洁可读为主，会被嵌入到 includeErrorMarkerFormat。
func resolveIncludePath(rawPath, baseDir string) (string, error) {
	// 拒绝 POSIX 绝对路径（以 / 开头）。
	// 关键：不能用 filepath.IsAbs 单独判断——Windows 下它认为 "/etc/secret.md"
	// 是相对当前盘符的相对路径，会拼出 F:\xxx\project\etc\secret.md，
	// 落进 baseDir 后被 ReadFile → "file not found"，绕过安全检查。
	if strings.HasPrefix(rawPath, "/") {
		return "", fmt.Errorf("absolute path not allowed")
	}
	// Windows 盘符绝对路径：C:\ D:/ 等
	if len(rawPath) >= 2 && rawPath[1] == ':' {
		return "", fmt.Errorf("drive-letter path not allowed")
	}
	// filepath.IsAbs 兜底（覆盖 Linux 下的 /xxx、macOS 下的 /Users/xxx 等）
	if filepath.IsAbs(rawPath) {
		return "", fmt.Errorf("absolute path not allowed")
	}
	// 拒绝 .. 路径段（防路径遍历）
	if rawPath == ".." || strings.HasPrefix(rawPath, "../") ||
		strings.Contains(rawPath, "/../") || strings.HasPrefix(rawPath, "./../") {
		return "", fmt.Errorf("path traversal not allowed")
	}

	// 相对 baseDir 解析
	resolved := filepath.Join(baseDir, rawPath)
	resolved = filepath.Clean(resolved)
	return resolved, nil
}

// assertInsideBase 校验 resolved 路径必须落在 baseDir 之内。
//
// 复用 security.IsPathInside 不行（依赖方向：security 不应被 prompt 反向依赖），
// 故在此写简化版。语义：resolved == baseDir 或以 baseDir/ 开头（按 OS 路径分隔符）。
//
// 注意：这里**不**处理 symlink 跨目录（与 AGENTS.md 加载一致，
// resolvePaths 已经对 CWD EvalSymlinks；include 文件留作未来增强位）。
func assertInsideBase(resolved, baseDir string) error {
	cleanResolved := filepath.Clean(resolved)
	cleanBase := filepath.Clean(baseDir)
	rel, err := filepath.Rel(cleanBase, cleanResolved)
	if err != nil {
		return fmt.Errorf("path outside base: %v", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path outside base")
	}
	return nil
}
