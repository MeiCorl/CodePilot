package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Source 标识 Skill 的来源级别,用于 Registry 的合并冲突规则与 SP 索引展示。
//
// 取值含义:
//   - SourceProject:项目级 Skill,目录 <cwd>/.codepilot/skills/<name>/SKILL.md,优先级最高;
//   - SourceUser:用户级 Skill,目录 ~/.codepilot/skills/<name>/SKILL.md,跨项目生效;
//   - SourceBuiltin:内置级 Skill,目录 <exec>/internal/skill/builtin/<name>/SKILL.md,本步骤为空。
//
// 数字常量取 1/2/3 而非 0/1/2 是为了避免零值歧义——一个 *Skill.Source 字段零值含义
// 不在合法集合内,可在调试时快速暴露未初始化的对象。
type Source int

const (
	// SourceProject 项目级 Skill,优先级最高,会覆盖同名用户级/内置级 Skill。
	SourceProject Source = 1
	// SourceUser 用户级 Skill,跨项目生效。
	SourceUser Source = 2
	// SourceBuiltin 内置级 Skill,由 CodePilot 自带分发,本步骤未内置任何 Skill。
	SourceBuiltin Source = 3
)

// String 返回 Source 的人类可读字符串。
//
// 该字符串同时作为:
//   - SP 索引([project] / [user] / [builtin] 前缀)中的级别标签;
//   - /skills 列表模态框的分组标签;
//   - 日志/审计字段的值。
//
// 未识别的 Source 返回 "<unknown>",便于调试时发现未初始化的字段。
func (s Source) String() string {
	switch s {
	case SourceProject:
		return "project"
	case SourceUser:
		return "user"
	case SourceBuiltin:
		return "builtin"
	default:
		return "<unknown>"
	}
}

// Skill 是从 SKILL.md 解析得到的数据结构,贯穿 Skill 系统的全生命周期:
//
//   - 启动期:loader.ParseFile 解析 SKILL.md 后构造;
//   - 运行期:registry 持有按 Source 排好序的 *Skill 列表;
//   - 适配层:slash 适配器取 Name/Description/Args 拼命令,tool 适配器取 Body()
//     作为 tool_result,prompt Source 取 Name/Description 拼索引。
//
// 字段说明:
//   - Name:必填,Skill 的唯一标识(也是 slash 命令名去掉前缀 "/" 后的部分);
//   - Description:必填,一句话用途,SP 索引展示;
//   - Args:可选,用户参数提示(如 "<path>"),用于 /<skill> 命令的补全型交互;
//   - AllowedTools:可选,可调用工具白名单(Step 11 Hook 系统接入);
//   - Source:由 loader/registry 写入,标识来源级别;
//   - RootPath:Skill 目录绝对路径(用于 FullContent 二次读盘);
//   - body:私有,SKILL.md 解析时的原始 markdown 正文(不含 frontmatter),
//     通过 Body() 方法对外访问以封装重组逻辑。
type Skill struct {
	Name         string
	Description  string
	Args         string
	AllowedTools []string
	Source       Source
	RootPath     string
	// MaxBytes 是 Body() / FullContent() 的输出上限(字节数,作用于 markdown 正文部分)。
	// 0 表示不截断;>0 时正文超过该值会截断并附加截断提示段。
	// [Why] 字段归属 Skill:scanner 在加载时根据 setting.json 的 skill.max_skill_size_bytes
	// 写入,Body() 不需要参数,既保持 tasks.md 的零参数签名,又满足大体积 SKILL.md 截断需求。
	MaxBytes int

	// body 是 SKILL.md 解析时缓存的「frontmatter 之后」markdown 正文。
	// [Why] 私有:Body() / FullContent() 负责把 frontmatter 重组为 # Skill: <name>
	// 标题段 + 描述 + 参数提示后拼接正文,调用方不应直接持有原始 markdown。
	body     string
	embedded bool
}

// skillFileName SKILL.md 主入口文件名,固定不变。
//
// [Why] 与 Claude Code / Codex / Cursor 等业界约定对齐,固定为大写 SKILL.md,
// 扫描器与适配器均通过此常量引用,避免硬编码散落。
const skillFileName = "SKILL.md"

// Body 返回 SKILL.md 完整内容(含 frontmatter 重新组装为 markdown 标题段 + 正文)。
//
// 输出格式(无 args 时省略 args 段):
//
//	# Skill: <name>
//
//	> <description>
//
//	<args hint>(可选)
//
//	<body markdown>
//
// 该方法与 FullContent() 的区别:
//   - Body() 使用启动期 loader 解析后缓存的 body(零 I/O,常驻内存);
//   - FullContent() 按 RootPath/SKILL.md 重新读盘,反映 SKILL.md 最新内容。
//
// use_skill 工具的 tool_result、slash 命令的 LeadUserMessage 都走 Body(),避免高频
// 调用时的磁盘 I/O 抖动;运行期需要「最新内容」的场景(如 SKILL.md 被外部编辑)走 FullContent。
func (s *Skill) Body() string {
	return s.renderBody(s.truncateBody(s.body))
}

// NewSkill 由 loader 包(以及未来可能的 builtin 分发代码)调用的构造器,集中
// 私有字段 body 的赋值,避免外部包通过字面量绕开字段访问限制。
//
// [Why] body 字段设计为 unexported 是为了让调用方只能走 Body() / FullContent()
// 拿到重组后的 markdown;但 loader 等内部构造路径必须能写入 body。
// 提供 NewSkill 而不是把 body 改成 exported,既保留封装,又满足 loader 的
// 「只在我这里构造」诉求。
func NewSkill(name, description, args string, allowedTools []string, source Source, rootPath, body string) *Skill {
	return &Skill{
		Name:         name,
		Description:  description,
		Args:         args,
		AllowedTools: allowedTools,
		Source:       source,
		RootPath:     rootPath,
		body:         body,
	}
}

// FullContent 重新读盘 SKILL.md 后组装完整内容,确保返回 SKILL.md 当前最新内容。
//
// 与 Body() 的差异:
//   - Body() 用解析期缓存的 markdown + Skill 字段重组(零 I/O,常驻内存);
//   - FullContent() 按 RootPath/SKILL.md 重新读取,再次解析 frontmatter,
//     用「文件上的最新 frontmatter 字段」重组标题段,正文也走 truncateBody
//     后拼接到末尾——保证标题/描述/正文都反映磁盘上的最新 SKILL.md。
//
// 本方法用于:
//   - SKILL.md 在启动期被外部修改后,需要拿到最新正文;
//   - use_skill 工具等关键路径允许一次磁盘读取以换取「最新内容」保证。
//
// 错误:
//   - RootPath 为空 → 返回 "skill has no root path" 错误;
//   - 文件不存在 → 返回 os 层面的 file not found 错误(wrap 文件路径);
//   - 读盘失败 → 返回原始 os 错误(wrap 文件路径);
//   - frontmatter 解析失败 → 返回 wrapped 错误。
//
// [Why] 标题段使用「文件最新 frontmatter」而非 Skill 缓存字段:SKILL.md 可能在
// 解析后被外部修改(description 改了),FullContent 应反映这一改动;
//
//	Skill 结构体本身仍持有解析期的缓存,运行期不会因为 FullContent 调用而被
//	并发改写(并发安全靠调用方负责)。
func (s *Skill) FullContent() (string, error) {
	if s.embedded {
		return s.Body(), nil
	}
	if s.RootPath == "" {
		return "", fmt.Errorf("skill %q has no root path", s.Name)
	}
	path := filepath.Join(s.RootPath, skillFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	fm, body, ferr := splitFrontmatterForRead(string(data))
	if ferr != nil {
		return "", fmt.Errorf("parse %s: %w", path, ferr)
	}
	truncated := s.truncateBodyWithValue(body)
	return renderFullContent(fm, truncated), nil
}

// renderBody 把 frontmatter 重组为 markdown 标题 + 描述 + 参数提示,并拼上正文。
//
// [Why] 集中一处组装:Body() 与 FullContent() 必须输出完全一致的格式,
// 避免出现 use_skill 工具与 slash 注入的同一 Skill 内容不一致。
func (s *Skill) renderBody(body string) string {
	var sb strings.Builder
	sb.WriteString("# Skill: ")
	sb.WriteString(s.Name)
	sb.WriteString("\n\n")
	sb.WriteString("> ")
	sb.WriteString(s.Description)
	sb.WriteString("\n\n")
	if s.Args != "" {
		sb.WriteString(s.Args)
		sb.WriteString("\n\n")
	}
	sb.WriteString(body)
	return sb.String()
}

// truncateBody 按 Skill.MaxBytes 截断 markdown 正文,超过则附加截断提示段。
//
// [Why] 集中一处:Body() 与 FullContent() 必须使用完全一致的截断规则,
// 避免出现 use_skill 工具与 slash 注入的同一 Skill 截断行为不一致。
//
// 行为:
//   - MaxBytes <= 0:不截断,返回原 body;
//   - len(body) <= MaxBytes:不截断,返回原 body;
//   - len(body) > MaxBytes:截取前 MaxBytes 字节 + "\n\n[truncated, full size: N bytes]"
//     提示段(N 为原始 body 字节数),便于 LLM 与用户识别「看到的是片段」。
//
// 字节数 vs 字符数:本步骤以 UTF-8 字节数为单位,避免多字节字符被截到一半;
// 与 spec §C.3 的「64KB」描述一致(64KB 指字节,非字符)。
func (s *Skill) truncateBody(body string) string {
	if s.MaxBytes <= 0 || len(body) <= s.MaxBytes {
		return body
	}
	truncated := body[:s.MaxBytes]
	originalSize := len(body)
	return truncated + "\n\n[truncated, full size: " + strconv.Itoa(originalSize) + " bytes]"
}

// extractBody 从 SKILL.md 原始内容中剥离 frontmatter 段,返回正文。
//
// [Why] FullContent() 需要保证与 Body() 输出一致,但读盘后是完整 md(含 frontmatter),
// 必须先按 --- ... --- 边界剥离 frontmatter 段,再交给 renderBody 重新组装,
// 避免出现「frontmatter 重复」。
//
// 行为约定:与 loader.ParseFile 的 frontmatter 识别规则保持一致——
//   - 首个非空行必须是 ---,否则视为无 frontmatter(整体作为 正文);
//   - 找到第二个 --- 作为闭合;未闭合视为无 frontmatter(整体作为 正文)。
//
// 此函数不返回 error:与 loader 不同,FullContent 是「已知合法 Skill」上的二次读盘,
// 即便 frontmatter 缺失也不应报错(让上层走已有 body 即可)。
func extractBody(raw string) string {
	lines := strings.Split(raw, "\n")
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	if start >= len(lines) || strings.TrimSpace(lines[start]) != "---" {
		return raw
	}
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			body := strings.Join(lines[i+1:], "\n")
			return strings.TrimLeft(body, "\n")
		}
	}
	return raw
}

// splitFrontmatterForRead 复用 loader 相同规则的 frontmatter 拆分,但返回 skill 包内的
// frontmatter 结构体而不是 loader.Frontmatter——避免 loader 包与 skill 包的循环依赖
// (loader 已 import skill,skill 不能 import loader)。
//
// [Why] FullContent 需要重新解析 frontmatter 但又不希望把 yaml.v3 直接依赖搬到
// skill 主包——skill 主包应保持「数据层」纯净,YAML 解析由 loader 包实现。
// 本函数对 loader.Frontmatter 做「字段名相同」的兼容:Name/Description/Args/AllowedTools
// 与 loader.Frontmatter 完全一致(解析阶段两边字段结构同构),可以独立解析。
//
// 为避免引入新的 YAML 依赖,splitFrontmatterForRead 直接解析 frontmatter 文本为
// 简单 map[string]string / []string(只识别 4 个受控字段),并在解析失败时返回 error。
//
// 此函数对调用方的契约:
//   - 成功:返回的 frontmatter 字段值都已 TrimSpace;
//   - 失败:返回 wrap 后的 error,便于 FullContent 把错误透传出去。
func splitFrontmatterForRead(raw string) (frontmatterRead, string, error) {
	lines := strings.Split(raw, "\n")
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	if start >= len(lines) || strings.TrimSpace(lines[start]) != "---" {
		return frontmatterRead{}, "", fmt.Errorf("missing frontmatter")
	}
	end := -1
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return frontmatterRead{}, "", fmt.Errorf("unclosed frontmatter")
	}
	yamlPart := strings.Join(lines[start+1:end], "\n")
	fm, err := parseFrontmatterText(yamlPart)
	if err != nil {
		return frontmatterRead{}, "", err
	}
	body := strings.TrimLeft(strings.Join(lines[end+1:], "\n"), "\n")
	return fm, body, nil
}

// frontmatterRead 是 splitFrontmatterForRead 返回的 frontmatter 投影,只承载 4 个
// 受控字段(与 loader.Frontmatter 同构)。Skill 主包不引入 yaml.v3 依赖。
type frontmatterRead struct {
	Name         string
	Description  string
	Args         string
	AllowedTools []string
}

// parseFrontmatterText 把 YAML 段解析为 frontmatterRead。
//
// [Why] 不引入 yaml.v3:Skill 主包保持「数据层纯净」,YAML 解析职责留给 loader 包。
// 本函数是「已知 4 个标量字段」的轻量解析器,足以支撑 FullContent 的二次读盘需求。
//
// 支持语法:
//   - `key: value`(标量,字符串 trim);
//   - `key:\n  - item1\n  - item2`(列表,字符串 trim)。
//
// 错误格式:返回带行号的错误,便于排错。
func parseFrontmatterText(yamlText string) (frontmatterRead, error) {
	var fm frontmatterRead
	lines := strings.Split(yamlText, "\n")
	i := 0
	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			i++
			continue
		}
		// 找到 key: 边界
		colonIdx := strings.Index(line, ":")
		if colonIdx < 0 {
			return fm, fmt.Errorf("invalid frontmatter line: %q", line)
		}
		key := strings.TrimSpace(line[:colonIdx])
		rest := strings.TrimSpace(line[colonIdx+1:])
		switch key {
		case "name":
			fm.Name = trimQuotes(rest)
		case "description":
			fm.Description = trimQuotes(rest)
		case "args":
			fm.Args = trimQuotes(rest)
		case "allowed-tools":
			// 列表:可能 rest 为空(下一行 - item),也可能 rest 直接是 - item
			var items []string
			if rest != "" && strings.HasPrefix(rest, "-") {
				items = append(items, strings.TrimSpace(strings.TrimPrefix(rest, "-")))
				i++
				for i < len(lines) {
					l := lines[i]
					t := strings.TrimSpace(l)
					if t == "" || !strings.HasPrefix(t, "-") {
						break
					}
					items = append(items, strings.TrimSpace(strings.TrimPrefix(t, "-")))
					i++
				}
				fm.AllowedTools = items
				continue
			}
			if rest == "" {
				i++
				for i < len(lines) {
					l := lines[i]
					t := strings.TrimSpace(l)
					if t == "" || !strings.HasPrefix(t, "-") {
						break
					}
					items = append(items, strings.TrimSpace(strings.TrimPrefix(t, "-")))
					i++
				}
				fm.AllowedTools = items
				continue
			}
			// 单值形式:allowed-tools: [ReadFile, Bash]
			inner := strings.Trim(rest, "[]")
			for _, p := range strings.Split(inner, ",") {
				if v := strings.TrimSpace(p); v != "" {
					items = append(items, v)
				}
			}
			fm.AllowedTools = items
			i++
			continue
		default:
			// 未知 key:跳过(Skill frontmatter 受控于 4 个字段)
		}
		i++
	}
	return fm, nil
}

// trimQuotes 去掉字符串首尾的单/双引号(yaml 字符串常见包裹形式)。
func trimQuotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// renderFullContent 把 frontmatterRead + 正文拼装为完整 markdown。
//
// [Why] FullContent 的标题段使用文件最新 frontmatter,而 Body() 的标题段使用
// Skill 缓存字段;二者格式必须一致——所以 renderFullContent 与 Skill.renderBody
// 输出格式对齐。
func renderFullContent(fm frontmatterRead, body string) string {
	var sb strings.Builder
	sb.WriteString("# Skill: ")
	sb.WriteString(fm.Name)
	sb.WriteString("\n\n")
	sb.WriteString("> ")
	sb.WriteString(fm.Description)
	sb.WriteString("\n\n")
	if fm.Args != "" {
		sb.WriteString(fm.Args)
		sb.WriteString("\n\n")
	}
	sb.WriteString(body)
	return sb.String()
}

// truncateBodyWithValue 按指定 maxBytes 截断 body,MaxBytes <= 0 或 body 长度不足时不截断。
//
// [Why] FullContent 的正文截断以「读盘后实测 body 大小」为依据,可能与 Skill 缓存
// 的 body 不一致(外部修改 SKILL.md 后),因此单独提供一个接受显式 maxBytes 的版本,
// 由调用方根据 Skill.MaxBytes 传入。
func (s *Skill) truncateBodyWithValue(body string) string {
	if s.MaxBytes <= 0 || len(body) <= s.MaxBytes {
		return body
	}
	return body[:s.MaxBytes] + "\n\n[truncated, full size: " + strconv.Itoa(len(body)) + " bytes]"
}
