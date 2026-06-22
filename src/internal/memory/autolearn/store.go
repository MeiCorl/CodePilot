// store.go 实现自动学习记忆的文件持久化抽象。
//
// 它是 Source（Task 2，读索引注入上下文）与 Reviewer（Task 5，写记忆文件 + 刷索引）
// 的共同底座，本身不感知 LLM、不感知 prompt——纯 IO 子系统。
//
// 关键设计：
//   - 分级存储：用户级（~/.codepilot/memory/）与项目级（<cwd>/.codepilot/memory/）
//     两个独立根，由 NewStore 显式注入路径（主流程计算 home/cwd 后传入，便于测试注入
//     临时目录，与 AgentsMDSource 的 HomeDirForTest 思路一致）。
//   - 原子写：记忆文件与 MEMORY.md 均走「写临时文件 + os.Rename」覆盖，任意时刻崩溃
//     磁盘上要么是完整的旧版、要么是完整的新版，不会出现半截损坏文件（高可用）。
//   - 路径逃逸双重防护：
//     1) normalizeSlug：写入前把 LLM 产出的任意 slug 规范化为 [a-z0-9-]，从源头消除
//        路径分隔符与危险字符；
//     2) isSafeSlug：读 / 解析 / 索引渲染时用严格正则二次校验，拒绝任何含分隔符、
//        大写、连续/首尾连字符、超长的 slug，防止手动篡改索引构造逃逸路径。
//   - 失败可重试：所有写方法返回 error 由上层（reviewer）决定降级；store 自身在
//     少数关键点（索引文件损坏无法解析）打 warn 日志。
//   - 增量读-改-写原子性：UpsertIndexEntry / RemoveIndexEntry 用 mu 保护整体
//     「读索引 → 改 → 原子重写」，避免并发回顾互相覆盖丢条目；WriteMemory /
//     RewriteIndex 自身已是原子写，无需加锁。

package autolearn

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/MeiCorl/CodePilot/src/internal/logger"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

const (
	// indexFileName MEMORY.md 索引文件名（用户级与项目级各自一份）。
	indexFileName = "MEMORY.md"
	// memoryFileExt 单条记忆文件后缀。
	memoryFileExt = ".md"
	// maxSlugLen slug 最大长度（字符数），超长截断。48 兼顾语义化与文件名可读。
	maxSlugLen = 48
	// tmpSuffix 原子写的临时文件后缀。
	tmpSuffix = ".tmp"

	// codepilotDirName CodePilot 用户配置根目录名，与 config/logger/session 等
	// 包的 "~/.codepilot" 约定一致（项目内无统一常量，各包各自拼接，此处同惯例）。
	codepilotDirName = ".codepilot"
	// memoryDirName memory 子目录名，拼在 codepilotDirName 之下。
	memoryDirName = "memory"
)

// indexLineRe 匹配单行索引：`- [type](slug.md)——summary`。
//
// 捕获组：1=type（小写字母+下划线）、2=slug（小写字母数字+连字符）、3=summary（行尾任意）。
// 分隔符为中文双破折号 ——（U+2014 U+2014），与用户规约一致。
// 行首允许空白，兼容人工编辑时的缩进。
var indexLineRe = regexp.MustCompile(`^\s*-\s*\[([a-z_]+)\]\(([a-z0-9-]+)\.md\)——(.*)$`)

// safeSlugRe 合法 slug 的严格正则：小写字母/数字段，段间用单个连字符连接，
// 不允许首尾连字符、连续连字符、大写、分隔符、点号等。
var safeSlugRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Store 是自动学习记忆的文件持久化抽象。
//
// 持有用户级与项目级两个根目录，无业务状态。所有路径由 (scope, slug) 现场计算，
// 文件系统层面的并发安全由「原子写 + mu 保护增量读改写」共同保证。
type Store struct {
	// userRoot 用户级记忆根目录：~/.codepilot/memory/。
	userRoot string
	// projectRoot 项目级记忆根目录：<cwd>/.codepilot/memory/。
	projectRoot string
	// mu 保护增量读-改-写操作（UpsertIndexEntry / RemoveIndexEntry）的原子性，
	// 避免并发回顾各自读到旧索引后覆盖丢条目。WriteMemory / RewriteIndex 自身
	// 原子写，无需持锁；ReadIndex 只读，无需持锁。
	mu sync.Mutex
}

// NewStore 创建一个记忆存储器。
//
// userRoot / projectRoot 分别为用户级与项目级记忆根目录的绝对路径，由主流程
// 计算后注入（便于测试用 t.TempDir() 注入隔离路径）。本构造不做目录存在性检查——
// 惰性创建留到首次写入时按需 MkdirAll，允许在记忆目录尚未建立时即构造 store。
func NewStore(userRoot, projectRoot string) *Store {
	return &Store{
		userRoot:    userRoot,
		projectRoot: projectRoot,
	}
}

// RootFor 返回指定存储域的记忆根目录。
// 未知 scope 一律按项目级处理（与 ScopeOf 对非法类型的兜底一致）。
func (s *Store) RootFor(scope StorageScope) string {
	if scope == ScopeUser {
		return s.userRoot
	}
	return s.projectRoot
}

// UserMemoryRoot 返回用户级记忆根目录 <homeDir>/.codepilot/memory。
//
// [Why] 供主流程统一计算 memory 根——SandboxMiddleware 的附加只读根
// （Step 8 Task 3）与 Store 构造（Step 8 Task 7）都从此处取根，保证
// 「沙箱放行范围」与「记忆实际落盘目录」语义同源，不会因两处各算一份
// 而漂移。homeDir 为空时返回空串，调用方应据此跳过该根，避免把相对路径
// ".codepilot/memory" 当成合法绝对根误放行。
func UserMemoryRoot(homeDir string) string {
	if homeDir == "" {
		return ""
	}
	return filepath.Join(homeDir, codepilotDirName, memoryDirName)
}

// ProjectMemoryRoot 返回项目级记忆根目录 <cwd>/.codepilot/memory。
// cwd 为空时返回空串（同 UserMemoryRoot 的防御语义）。
func ProjectMemoryRoot(cwd string) string {
	if cwd == "" {
		return ""
	}
	return filepath.Join(cwd, codepilotDirName, memoryDirName)
}

// indexPath 返回指定域 MEMORY.md 的完整路径。
func (s *Store) indexPath(scope StorageScope) string {
	return filepath.Join(s.RootFor(scope), indexFileName)
}

// memoryPath 返回指定域某条记忆文件的完整路径。slug 应已通过 isSafeSlug 校验。
func (s *Store) memoryPath(scope StorageScope, slug string) string {
	return filepath.Join(s.RootFor(scope), slug+memoryFileExt)
}

// ---- 索引读写 ----

// ReadIndex 读取指定域的 MEMORY.md，解析为索引条目切片。
//
// 语义：
//   - 文件不存在（首次启动 / 该域无记忆）：返回 (nil, nil)，不报错
//   - 文件存在但解析失败的行（格式错误、未知类型、非法 slug）：静默跳过，不整体报错
//   - 读 IO 错误：返回 error
//
// 跳过非法行的容错设计 [Why]：MEMORY.md 可被用户手动编辑，部分行格式不规范不应
// 导致整个记忆子系统不可用；非法 slug 被跳过也杜绝了「索引被篡改构造路径逃逸」。
func (s *Store) ReadIndex(scope StorageScope) ([]IndexEntry, error) {
	data, err := os.ReadFile(s.indexPath(scope))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取记忆索引失败 (%s): %w", s.indexPath(scope), err)
	}
	return parseIndex(string(data)), nil
}

// parseIndex 解析 MEMORY.md 文本为索引条目。
// 逐行匹配 indexLineRe；非法行（分块标题、注释、格式错误、未知类型、非法 slug）跳过。
func parseIndex(content string) []IndexEntry {
	var entries []IndexEntry
	for _, line := range strings.Split(content, "\n") {
		m := indexLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		t := MemoryType(m[1])
		slug := m[2]
		summary := strings.TrimSpace(m[3])
		// 类型与 slug 双校验：拒绝未知类型标签与路径逃逸 slug，不污染索引视图。
		if !IsValidType(t) || !isSafeSlug(slug) {
			logger.Warn("autolearn: 跳过非法索引行",
				zap.String("line", line),
				zap.String("type", string(t)),
				zap.String("slug", slug),
			)
			continue
		}
		entries = append(entries, IndexEntry{Type: t, Slug: slug, Summary: summary})
	}
	return entries
}

// RewriteIndex 全量重写指定域的 MEMORY.md（按 4 类分块渲染），原子覆盖。
//
// 语义：
//   - 目录惰性创建；
//   - 非法条目（未知类型 / 非法 slug）在渲染时被过滤，不写入索引；
//   - 全部条目均被过滤或为空时，写入空索引文件（不删除文件，保持存在性可预测）。
func (s *Store) RewriteIndex(scope StorageScope, entries []IndexEntry) error {
	root := s.RootFor(scope)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("创建记忆目录失败 (%s): %w", root, err)
	}
	content := renderIndex(entries)
	path := s.indexPath(scope)
	if err := atomicWriteFile(path, []byte(content)); err != nil {
		return fmt.Errorf("写入记忆索引失败 (%s): %w", path, err)
	}
	return nil
}

// renderIndex 把索引条目按 4 类分块渲染为 MEMORY.md 文本。
//
// 格式：
//
//	## user_preference
//	- [user_preference](indent-style.md)——使用4个空格代替TAB
//
//	## reference
//	- [reference](db.md)——DB使用手册
//
// 空 bucket（无条目的类型）不渲染标题，保持索引紧凑。
// 非法条目（未知类型 / 非法 slug）被静默过滤，保证落盘索引只含可被安全回读的条目。
func renderIndex(entries []IndexEntry) string {
	buckets := make(map[MemoryType][]IndexEntry, len(memoryTypeOrder))
	for _, e := range entries {
		if !IsValidType(e.Type) || !isSafeSlug(e.Slug) {
			logger.Warn("autolearn: 渲染索引时过滤非法条目",
				zap.String("type", string(e.Type)),
				zap.String("slug", e.Slug),
			)
			continue
		}
		buckets[e.Type] = append(buckets[e.Type], e)
	}

	var sb strings.Builder
	for _, t := range memoryTypeOrder {
		es := buckets[t]
		if len(es) == 0 {
			continue
		}
		sb.WriteString("## ")
		sb.WriteString(string(t))
		sb.WriteByte('\n')
		for _, e := range es {
			sb.WriteString("- [")
			sb.WriteString(string(e.Type))
			sb.WriteString("](")
			sb.WriteString(e.Slug)
			sb.WriteString(".md)——")
			sb.WriteString(e.Summary)
			sb.WriteByte('\n')
		}
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n") + "\n"
}

// RenderEntries 是 renderIndex 的公开包装：把一组索引条目按 4 类分块渲染为文本片段。
//
// [Why] 供 sources 层（memory 索引注入 Source）等上层复用同一套渲染逻辑，避免在不同
// 包重复实现而出现格式漂移。非法条目（未知类型 / 非法 slug）会被过滤；全部非法或为空
// 时返回空串（调用方据此判断是否跳过注入）。
func RenderEntries(entries []IndexEntry) string {
	return renderIndex(entries)
}

// UpsertIndexEntry 增量更新指定域的索引：按 slug 去重，存在则更新（类型 + 简介），
// 不存在则追加，最后全量重写。
//
// [Why] 用 mu 保护整体「读 → 改 → 原子重写」：避免两个并发回顾各自读到旧索引、
// 改完先后重写导致一方的新增条目被另一方覆盖丢失。WriteMemory / RewriteIndex
// 自身已是原子写，无需持锁；本方法的锁仅保证读改写序列不被并发打断。
// 调用方（reviewer）的 per-session 串行是额外的上层保障。
func (s *Store) UpsertIndexEntry(scope StorageScope, entry IndexEntry) error {
	if !IsValidType(entry.Type) || !isSafeSlug(entry.Slug) {
		return fmt.Errorf("非法索引条目（类型或 slug 不合法）: type=%q slug=%q", entry.Type, entry.Slug)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.ReadIndex(scope)
	if err != nil {
		return err
	}
	found := false
	for i, e := range entries {
		if e.Slug == entry.Slug {
			entries[i] = entry
			found = true
			break
		}
	}
	if !found {
		entries = append(entries, entry)
	}
	return s.RewriteIndex(scope, entries)
}

// RemoveIndexEntry 从指定域索引中删除某 slug 对应的条目（全量重写）。
// 同样在 mu 保护下完成读改写。条目不存在视为已删除，不报错（幂等）。
func (s *Store) RemoveIndexEntry(scope StorageScope, slug string) error {
	if !isSafeSlug(slug) {
		return fmt.Errorf("非法记忆 slug: %q", slug)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.ReadIndex(scope)
	if err != nil {
		return err
	}
	filtered := make([]IndexEntry, 0, len(entries))
	for _, e := range entries {
		if e.Slug != slug {
			filtered = append(filtered, e)
		}
	}
	return s.RewriteIndex(scope, filtered)
}

// ---- 记忆文件读写 ----

// WriteMemory 写一条记忆文件（YAML frontmatter + 正文），原子覆盖同名旧文件。
//
// 语义：
//   - slug 经 normalizeSlug 规范化（容忍 LLM 产出的大小写/空格/下划线/分隔符），
//     规范化后仍不合法（如空串）则返回 error，不写入；
//   - 类型非法返回 error；
//   - 目录惰性创建；写入走原子 tmp+rename，覆盖同名旧文件不留半成品；
//   - 返回写入文件的绝对路径。
//
// 注意：本方法不维护 MEMORY.md 索引——调用方（reviewer）在写完记忆后需另行调用
// UpsertIndexEntry 刷新索引行，两步解耦便于在「仅更新记忆内容但简介不变」等场景
// 灵活组合。
func (s *Store) WriteMemory(scope StorageScope, m Memory) (string, error) {
	slug := normalizeSlug(m.Slug)
	if !isSafeSlug(slug) {
		return "", fmt.Errorf("非法记忆 slug（规范化后仍不合法）: %q", m.Slug)
	}
	if !IsValidType(m.Type) {
		return "", fmt.Errorf("非法记忆类型: %q", m.Type)
	}

	root := s.RootFor(scope)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("创建记忆目录失败 (%s): %w", root, err)
	}

	data, err := renderMemoryFile(m)
	if err != nil {
		return "", err
	}
	path := s.memoryPath(scope, slug)
	if err := atomicWriteFile(path, data); err != nil {
		return "", fmt.Errorf("写入记忆文件失败 (%s): %w", path, err)
	}
	return path, nil
}

// ReadMemory 读取指定域某条记忆文件全文，解析为 Memory。
//
// 语义：
//   - slug 必须通过 isSafeSlug 严格校验（不经 normalize），含分隔符/大写等一律拒绝，
//     杜绝「索引被篡改后构造逃逸路径读取会话目录外文件」；
//   - 文件缺失返回 error（与 ReadIndex 的「缺失视为空」不同：读取具体记忆时缺失是异常）；
//   - frontmatter 解析失败返回 error；无 frontmatter 的文件整体作为 Content 返回。
func (s *Store) ReadMemory(scope StorageScope, slug string) (*Memory, error) {
	if !isSafeSlug(slug) {
		return nil, fmt.Errorf("非法记忆 slug: %q", slug)
	}
	data, err := os.ReadFile(s.memoryPath(scope, slug))
	if err != nil {
		return nil, fmt.Errorf("读取记忆文件失败 (%s): %w", s.memoryPath(scope, slug), err)
	}
	m, err := parseMemoryFile(string(data))
	if err != nil {
		return nil, err
	}
	m.Slug = slug
	return m, nil
}

// DeleteMemory 删除指定域某条记忆文件。幂等：文件不存在视为已删除，不报错。
// 本方法不维护索引——调用方需另行调用 RemoveIndexEntry 清理索引行。
func (s *Store) DeleteMemory(scope StorageScope, slug string) error {
	if !isSafeSlug(slug) {
		return fmt.Errorf("非法记忆 slug: %q", slug)
	}
	if err := os.Remove(s.memoryPath(scope, slug)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除记忆文件失败 (%s): %w", s.memoryPath(scope, slug), err)
	}
	return nil
}

// ---- 渲染与解析辅助 ----

// renderMemoryFile 把 Memory 渲染为 md 文件字节内容：frontmatter + 正文。
//
// [Why] 仅对 Frontmatter（4 个受控标量）做 yaml.Marshal，正文 Content 原样拼接在
// frontmatter 闭合标记之后——正文不经过 YAML 转义，长文本/特殊字符不会破坏格式。
func renderMemoryFile(m Memory) ([]byte, error) {
	yamlBytes, err := yaml.Marshal(m.Frontmatter)
	if err != nil {
		return nil, fmt.Errorf("序列化记忆 frontmatter 失败: %w", err)
	}
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.Write(yamlBytes)
	sb.WriteString("---\n\n")
	// 去掉正文开头多余空行，与 frontmatter 后的单空行分隔配合，避免出现连续空行。
	sb.WriteString(strings.TrimLeft(m.Content, "\n"))
	return []byte(sb.String()), nil
}

// parseMemoryFile 解析 md 文件内容为 Memory：提取首个 --- ... --- 块为 frontmatter，
// 其后为正文。无 frontmatter（首个非空行不是 ---）或未闭合时，整体作为 Content 降级返回。
func parseMemoryFile(content string) (*Memory, error) {
	lines := strings.Split(content, "\n")
	// 找到首个非空行作为 frontmatter 起始判定
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	if start >= len(lines) || strings.TrimSpace(lines[start]) != "---" {
		// 无 frontmatter，整体作为正文（兼容无格式纯文本记忆）
		return &Memory{Content: content}, nil
	}
	// 找闭合 ---
	end := -1
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		// 未闭合，整体作为正文降级
		return &Memory{Content: content}, nil
	}

	yamlPart := strings.Join(lines[start+1:end], "\n")
	var fm Frontmatter
	if err := yaml.Unmarshal([]byte(yamlPart), &fm); err != nil {
		return nil, fmt.Errorf("解析记忆 frontmatter 失败: %w", err)
	}
	body := strings.Join(lines[end+1:], "\n")
	body = strings.TrimLeft(body, "\n") // 去掉 frontmatter 闭合后的空行分隔
	return &Memory{Frontmatter: fm, Content: body}, nil
}

// normalizeSlug 把任意输入规范化为合法 slug（小写字母/数字 + 单连字符）。
//
// 规则：转小写 → 非 [a-z0-9] 字符替换为连字符 → 合并连续连字符 → 去首尾连字符 → 截断到上限。
// [Why] 回顾 LLM 产出的 slug 可能含大写、空格、下划线甚至分隔符，这里从源头清洗，
// 配合 isSafeSlug 保证最终落盘文件名不含路径分隔符，杜绝写入逃逸。
func normalizeSlug(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	s = b.String()
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = strings.Trim(s, "-")
	if len(s) > maxSlugLen {
		s = strings.Trim(s[:maxSlugLen], "-")
	}
	return s
}

// isSafeSlug 严格校验 slug 是否合法（仅 [a-z0-9] 段 + 单连字符，非空，不超长）。
// 用于读取/解析/索引渲染时的二次防护，拒绝任何含路径分隔符、大写、首尾/连续连字符的 slug。
func isSafeSlug(slug string) bool {
	if slug == "" || len(slug) > maxSlugLen {
		return false
	}
	return safeSlugRe.MatchString(slug)
}

// atomicWriteFile 以「写临时文件 + rename」原子覆盖目标文件。
// 参照 interaction/web/dump.go 的 atomicWriteText 与 session/archive.go 的原子写法：
// 任意时刻崩溃，目标文件要么是完整的旧版、要么是完整的新版，不会半截损坏。
func atomicWriteFile(path string, data []byte) error {
	tmp := path + tmpSuffix
	// 0644：owner 可读写、其余只读，与会话目录内其它归档文件权限对齐。
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	// Windows 上 os.Rename 通过 MoveFileEx(MOVEFILE_REPLACE_EXISTING) 实现覆盖；
	// POSIX 上 rename 原子替换。本进程内 store 的写操作受 mu / 上层串行约束，无并发占用。
	return os.Rename(tmp, path)
}
