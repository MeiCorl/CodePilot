// Package loader 提供 SKILL.md 文件的解析能力,把磁盘上的 SKILL.md 内容
// 转换为 *skill.Skill 数据结构,是 Skill 数据流入的入口。
//
// 设计原则:
//   - 只关心「单文件 → Skill」的解析,不涉及目录扫描、跨文件合并、冲突规则;
//   - 错误信息必须含文件路径,便于 scanner/registry 上层定位;
//   - 不依赖 skill 包的内部私有字段,只通过 Skill 公开字段构造;
//   - 不持有任何全局状态,纯函数式解析,易于测试。
package loader

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/MeiCorl/CodePilot/src/internal/skill"
)

// Frontmatter 是 SKILL.md 的 YAML frontmatter 结构定义。
//
// [Why] 与 spec §A.3 对齐——必填 name / description;可选 args / allowed-tools。
// yaml tag 与 spec 中 SKILL.md 字段命名保持一致(allowed-tools 用连字符),
// 便于用户手写 SKILL.md 时无需关注 yaml 转义。
type Frontmatter struct {
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	Args         string   `yaml:"args,omitempty"`
	AllowedTools []string `yaml:"allowed-tools,omitempty"`
}

// frontmatterMarker frontmatter 段边界标记。
//
// [Why] 显式声明为常量,避免散落的字符串字面量;与 SKILL.md 实际写法对齐。
const frontmatterMarker = "---"

// ErrMissingFrontmatter 表示 SKILL.md 缺少 frontmatter 段。
//
// 调用方(scanner)拿到此 error 应记录 warn 并跳过该 Skill,而非终止整个加载流程。
type ErrMissingFrontmatter struct {
	Path string
}

func (e *ErrMissingFrontmatter) Error() string {
	return fmt.Sprintf("parse %s: missing frontmatter (expected --- ... --- at top of file)", e.Path)
}

// ErrMissingField 表示 frontmatter 中缺必填字段(name 或 description)。
type ErrMissingField struct {
	Path  string
	Field string
}

func (e *ErrMissingField) Error() string {
	return fmt.Sprintf("parse %s: %s is required", e.Path, e.Field)
}

// ErrYAML 表示 YAML 语法错误,wrap 了 yaml.v3 的原始错误便于排错。
type ErrYAML struct {
	Path string
	Err  error
}

func (e *ErrYAML) Error() string {
	return fmt.Sprintf("parse %s: yaml parse error: %v", e.Path, e.Err)
}

func (e *ErrYAML) Unwrap() error {
	return e.Err
}

// ParseFile 读取并解析 SKILL.md,返回构造好的 *skill.Skill。
//
// 行为:
//   - 文件不存在 → 返回 os 层面错误(wrap 文件路径),供上层区分「文件缺失」与「解析失败」;
//   - 文件内容空 → 视为缺 frontmatter;
//   - 缺 frontmatter 段 → 返回 *ErrMissingFrontmatter;
//   - YAML 语法错误 → 返回 *ErrYAML(unwrap 后是 yaml.v3 原始错误);
//   - 必填字段缺失 → 返回 *ErrMissingField(字段名准确: name / description);
//   - 合法 → 返回 *Skill{Source: SourceProject, RootPath: filepath.Dir(path), body: rawMarkdown}。
//
// [Why] Source 在 ParseFile 中默认填 SourceProject:loader 阶段不区分目录来源,
// 调用方(scanner)负责在注册到 registry 时按目录覆盖 Source 字段。这样保证
// loader 与 source 决策解耦,易于测试。
func ParseFile(path string) (*skill.Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		// 文件不存在 / 权限错误等都直接 wrap 路径返回,不做类型包装——
		// scanner 拿到 error 后用 errors.Is(err, os.ErrNotExist) 区分「缺失」即可。
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	raw := string(data)
	fm, body, err := splitFrontmatter(raw, path)
	if err != nil {
		return nil, err
	}

	if err := validateFrontmatter(fm, path); err != nil {
		return nil, err
	}

	s := skill.NewSkill(
		fm.Name,
		fm.Description,
		fm.Args,
		fm.AllowedTools,
		skill.SourceProject, // 默认值,scanner 会按目录覆盖
		filepath.Dir(path),
		body,
	)
	return s, nil
}

// splitFrontmatter 把 SKILL.md 内容切成 (frontmatter 结构体, 正文 markdown)。
//
// 行为约定:
//   - 首个非空行必须以 --- 开头,否则视为无 frontmatter(返回 *ErrMissingFrontmatter);
//   - 找到第二个 --- 作为闭合;未闭合返回 *ErrMissingFrontmatter;
//   - 闭合标记后到文件末尾之间为正文,trim 掉首部空行以避免与 # Skill: 标题之间出现连续空行;
//   - frontmatter 段交给 yaml.Unmarshal 解析,YAML 错误返回 *ErrYAML。
//
// [Why] 把「拆分 + 解析」集中在一处:body 段不参与 YAML,避免长正文/特殊字符
// 干扰 YAML 解析;同时 frontmatter 段是受控字段(4 个标量),解析失败能精确定位。
func splitFrontmatter(raw string, path string) (Frontmatter, string, error) {
	lines := strings.Split(raw, "\n")

	// 跳过开头空行
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	if start >= len(lines) || strings.TrimSpace(strings.TrimPrefix(lines[start], "\ufeff")) != frontmatterMarker {
		return Frontmatter{}, "", &ErrMissingFrontmatter{Path: path}
	}

	// 找闭合 ---
	end := -1
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == frontmatterMarker {
			end = i
			break
		}
	}
	if end == -1 {
		// 未闭合视为缺 frontmatter,与「无 frontmatter」统一行为
		return Frontmatter{}, "", &ErrMissingFrontmatter{Path: path}
	}

	yamlPart := strings.Join(lines[start+1:end], "\n")
	var fm Frontmatter
	if err := yaml.Unmarshal([]byte(yamlPart), &fm); err != nil {
		return Frontmatter{}, "", &ErrYAML{Path: path, Err: err}
	}

	body := strings.Join(lines[end+1:], "\n")
	body = strings.TrimLeft(body, "\n")
	return fm, body, nil
}

// validateFrontmatter 校验必填字段 name / description 非空。
//
// [Why] 集中校验而非在各调用点判断:保证错误信息一致(都含路径与字段名),
// 同时避免 Skill 对象持有空 Name / 空 Description 进入运行期。
func validateFrontmatter(fm Frontmatter, path string) error {
	if strings.TrimSpace(fm.Name) == "" {
		return &ErrMissingField{Path: path, Field: "name"}
	}
	if strings.TrimSpace(fm.Description) == "" {
		return &ErrMissingField{Path: path, Field: "description"}
	}
	return nil
}
