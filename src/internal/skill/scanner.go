package skill

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"

	"github.com/MeiCorl/CodePilot/src/internal/logger"
)

// 三档目录相对根路径常量(spec §A.1):
//   - 项目级:<cwd>/.codepilot/skills(复数,spec §A.1 强调「skills 复数」);
//   - 用户级:~/.codepilot/skills(复数,跨项目生效);
//   - 内置级:<execDir>/internal/skill/builtin(预留扩展点)。
//
// [Why] builtinDirName 直接在主包内:避免 skill → builtin → skill 循环依赖
// (若 builtin 包实现扫描并 import skill.Skill 来构造对象)。本包已自带
// frontmatter 解析逻辑(parseFrontmatterLocal),完全自包含。
const (
	projectSkillsDir = ".codepilot/skills"
	userSkillsDir    = ".codepilot/skills"
	builtinRelPath   = "internal/skill/builtin"
)

// LoadIssue 记录 Skill 加载过程中的非致命问题(spec §A.5 加载失败隔离)。
//
// 字段:
//
//	- Path:触发问题的 SKILL.md 路径或目录路径(若可定位);
//	- Err:具体错误(frontmatter 解析失败 / 目录不可读等);
//	- Source:问题 Skill 的来源级别,用于日志分组展示。
//
// 用途:Scanner 收集这些 issue 后返回上层(main.go / handler),由调用方决定
// 是否记录 warn 日志并继续启动。本类型不属于 fatal error,Registry 仍可继续构造。
type LoadIssue struct {
	Path   string
	Err    error
	Source Source
}

// Error 返回 issue 的可读字符串,便于日志/CLI 输出。
func (i LoadIssue) Error() string {
	return fmt.Sprintf("[%s] %s: %v", i.Source.String(), i.Path, i.Err)
}

// LoadAll 是 Skill 系统的顶层入口,执行三档(内置 → 用户 → 项目)扫描与合并注册。
//
// 调用方式(main.go 启动期):
//
//	reg, issues, err := skill.LoadAll(workdir, homeDir, execDir, maxBytes)
//	if err != nil { /* 同级同名冲突,应退出进程 */ }
//	for _, iss := range issues { logger.Warn(...) }
//
// 参数:
//
//	- workdir:项目根目录(等于 os.Getwd() 或 .codepilot/setting.json 中的 working_directory);
//	- homeDir:用户主目录(= os.UserHomeDir());
//	- execDir:CodePilot 可执行文件所在目录;
//	- maxBytes:Body() / FullContent() 的正文截断上限(<=0 表示不截断);
//
// 返回值:
//
//	- *Registry:合并后的注册表;始终返回非 nil(即便三档全空也返回空 Registry);
//	- []LoadIssue:加载过程中遇到的非致命问题(单个 SKILL.md 解析失败、目录不可读);
//	  调用方可遍历记录 warn 日志;即使非空也不视为错误;
//	- error:仅在「同级别同名冲突」时返回非 nil,Registry 与 issues 仍携带部分状态供调试;
//
// 加载顺序与冲突规则(spec §A.4):
//  1. 内置级 → 扫描 execDir/internal/skill/builtin/ 子目录 → parseFrontmatterLocal → Register;
//  2. 用户级 → 扫描 homeDir/.codepilot/skills/ 子目录 → parseFrontmatterLocal → Register;
//  3. 项目级 → 扫描 workdir/.codepilot/skills/ 子目录 → parseFrontmatterLocal → Register;
//     (项目级最后到,silent skip 可覆盖用户级同名)
//
// 目录缺失处理:任一目录不存在 → 静默跳过,不报错,不记 issue。
//
// 单 Skill 解析失败:记 LoadIssue + warn 日志,继续加载其他 Skill。
//
// 同级同名冲突:记 LoadIssue + 立即返回 *ErrSkillConflict,Registry 状态部分填充
// (已 Register 的 Skill 保留)。
//
// [Why] 扫描 + 解析完全在 skill 主包内实现(不调用 loader.ParseFile):
// skill 主包不能 import skill/loader(loader 已 import skill,会形成循环)。
// parseFrontmatterLocal 是 loader.ParseFile 的行为等价的内联实现,
// 保证 Task 2 范围内的端到端闭环(目录扫描 + 解析 + 注册三档合并)。
// 后续若需要将 scanner 独立成子包(如 skill/scanner),再把 loader.ParseFile
// 通过接口注入的方式替换回去。
func LoadAll(workdir, homeDir, execDir string, maxBytes int) (*Registry, []LoadIssue, error) {
	reg := NewRegistry()
	var issues []LoadIssue

	// 1. 内置级(本步骤始终空目录,保留扩展点)
	if err := scanLevel(reg, filepath.Join(execDir, builtinRelPath), SourceBuiltin, maxBytes, &issues); err != nil {
		return reg, issues, err
	}

	// 2. 用户级
	if err := scanLevel(reg, filepath.Join(homeDir, userSkillsDir), SourceUser, maxBytes, &issues); err != nil {
		return reg, issues, err
	}

	// 3. 项目级(最后注册,silent skip 可覆盖用户级同名)
	if err := scanLevel(reg, filepath.Join(workdir, projectSkillsDir), SourceProject, maxBytes, &issues); err != nil {
		return reg, issues, err
	}

	return reg, issues, nil
}

// scanLevel 扫描指定根目录下所有子目录的 SKILL.md,逐个 Register。
//
// 入参:
//
//	- rootDir:扫描根目录(如 <workdir>/.codepilot/skills);
//	- src:该级 Skill 的 Source 标识(SourceUser / SourceProject / SourceBuiltin);
//	- maxBytes:Body 截断上限;
//	- issues:非致命问题收集指针(解析失败的 Skill append 进去);
//
// 返回值:
//
//	- nil:目录缺失 或 全部 Skill 加载完成(可能伴随 issues);
//	- *ErrSkillConflict:同级别同名冲突(记录 issue 后立即返回);
//
// 目录不存在:os.Stat 返回 os.ErrNotExist → 静默 return nil,不记 issue。
//
// 子目录处理:
//  1. 必须是目录(跳过普通文件);
//  2. 该目录下存在 SKILL.md → 调 parseFrontmatterLocal;
//  3. 解析成功 → 构造 *Skill(Source=src, MaxBytes=maxBytes),Register;
//  4. 解析失败 → 记 issue + warn 日志,继续下一个子目录(spec §A.5)。
//
// [Why] 顺序保持:os.ReadDir 返回的目录顺序按文件名排序(Go 1.16+ 保证),
// 同级内 Skill 注册顺序确定,便于测试断言。
func scanLevel(reg *Registry, rootDir string, src Source, maxBytes int, issues *[]LoadIssue) error {
	// 目录不存在:静默跳过(spec §A.5 + §Out-of-Scope)
	if _, err := os.Stat(rootDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		// 其他错误(权限等):记 issue 但不阻断
		*issues = append(*issues, LoadIssue{
			Path:   rootDir,
			Err:    fmt.Errorf("stat skill dir: %w", err),
			Source: src,
		})
		logger.L().Warn("skill 扫描目录失败",
			zap.String("path", rootDir),
			zap.String("source", src.String()),
			zap.Error(err))
		return nil
	}

	entries, err := os.ReadDir(rootDir)
	if err != nil {
		*issues = append(*issues, LoadIssue{
			Path:   rootDir,
			Err:    fmt.Errorf("read skill dir: %w", err),
			Source: src,
		})
		logger.L().Warn("skill 扫描目录失败",
			zap.String("path", rootDir),
			zap.String("source", src.String()),
			zap.Error(err))
		return nil
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillDir := filepath.Join(rootDir, entry.Name())
		skillPath := filepath.Join(skillDir, "SKILL.md")

		// SKILL.md 不存在:跳过,不记 issue(空目录合法,spec §A.1 子目录类型之一)
		if _, ferr := os.Stat(skillPath); ferr != nil {
			if errors.Is(ferr, os.ErrNotExist) {
				continue
			}
			*issues = append(*issues, LoadIssue{
				Path:   skillPath,
				Err:    fmt.Errorf("stat SKILL.md: %w", ferr),
				Source: src,
			})
			logger.L().Warn("skill SKILL.md 不可访问",
				zap.String("path", skillPath),
				zap.String("source", src.String()),
				zap.Error(ferr))
			continue
		}

		s, perr := parseFrontmatterLocal(skillPath)
		if perr != nil {
			// 单个 Skill 解析失败 → issue + warn + 继续(spec §A.5)
			*issues = append(*issues, LoadIssue{
				Path:   skillPath,
				Err:    perr,
				Source: src,
			})
			logger.L().Warn("skill 解析失败",
				zap.String("path", skillPath),
				zap.String("source", src.String()),
				zap.Error(perr))
			continue
		}

		// 覆盖 Source / MaxBytes(scanner 决定)
		s.Source = src
		if maxBytes > 0 {
			s.MaxBytes = maxBytes
		}

		if rerr := reg.Register(s); rerr != nil {
			// 同级别同名冲突 → 记 issue + 立即返回(不可恢复,启动期应退出)
			var conflict *ErrSkillConflict
			if errors.As(rerr, &conflict) {
				*issues = append(*issues, LoadIssue{
					Path:   skillPath,
					Err:    conflict,
					Source: src,
				})
				logger.L().Error("skill 同名冲突",
					zap.String("name", conflict.Name),
					zap.String("existing_source", conflict.ExistingSource.String()),
					zap.String("path", skillPath))
				return conflict
			}
			// 其他 Register error(参数非法等):记 issue 但继续(理论上不应发生)
			*issues = append(*issues, LoadIssue{
				Path:   skillPath,
				Err:    rerr,
				Source: src,
			})
			logger.L().Warn("skill 注册失败",
				zap.String("path", skillPath),
				zap.String("source", src.String()),
				zap.Error(rerr))
			continue
		}
	}
	return nil
}

// parseFrontmatterLocal 在 skill 主包内解析 SKILL.md,行为等价于 loader.ParseFile。
//
// [Why] 内联而非 import loader:loader 包已 import skill(用于 NewSkill 构造),
// 若 skill 主包再 import loader 会形成循环。本函数复用 skill 包已有的
// splitFrontmatterForRead / parseFrontmatterText / frontmatterRead,组合出
// 与 loader.ParseFile 行为一致的 *Skill 构造流程:
//
//	读盘 → splitFrontmatterForRead → trimSpace + 校验 name/description → NewSkill
//
// 入参 path 为 SKILL.md 的绝对路径。
//
// 错误:
//   - 文件不存在 → wrap 路径返回,可 errors.Is(err, os.ErrNotExist);
//   - frontmatter 段缺失/未闭合 → ErrMissingFrontmatter 本地等价物;
//   - YAML 语法错(行内 ":" 缺失)→ ErrYAML 本地等价物;
//   - 必填字段缺失 → ErrMissingField 本地等价物;
//
// 与 loader.ParseFile 的差异:
//   - Source 字段:loader 默认填 SourceProject,本函数不预设(scanner 后续覆盖);
//   - 错误类型:本函数返回本地定义的同名 struct(避免循环依赖),errors.As 仍可识别。
func parseFrontmatterLocal(path string) (*Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	fm, body, err := splitFrontmatterForRead(string(data))
	if err != nil {
		// splitFrontmatterForRead 在缺 frontmatter / 未闭合时返回的是 fmt.Errorf
		// 普通错误,不带结构化类型;此处统一包装为 *ErrMissingFrontmatter 以便上层
		// errors.As 识别。
		msg := err.Error()
		if strings.Contains(msg, "missing frontmatter") || strings.Contains(msg, "unclosed frontmatter") {
			return nil, &localErrMissingFrontmatter{Path: path}
		}
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	// 校验必填字段
	if strings.TrimSpace(fm.Name) == "" {
		return nil, &localErrMissingField{Path: path, Field: "name"}
	}
	if strings.TrimSpace(fm.Description) == "" {
		return nil, &localErrMissingField{Path: path, Field: "description"}
	}
	// 构造 Skill:Source 留零值,scanner 会覆盖为正确的 Source
	return NewSkill(
		fm.Name,
		fm.Description,
		fm.Args,
		fm.AllowedTools,
		Source(0), // 由 scanner 覆盖
		filepath.Dir(path),
		body,
	), nil
}

// 本地错误类型定义(避免循环依赖 loader 包的同名 struct)。
type localErrMissingFrontmatter struct {
	Path string
}

func (e *localErrMissingFrontmatter) Error() string {
	return fmt.Sprintf("parse %s: missing frontmatter (expected --- ... --- at top of file)", e.Path)
}

type localErrMissingField struct {
	Path  string
	Field string
}

func (e *localErrMissingField) Error() string {
	return fmt.Sprintf("parse %s: %s is required", e.Path, e.Field)
}
