package skill

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"

	"github.com/MeiCorl/CodePilot/src/internal/logger"
	skillbuiltin "github.com/MeiCorl/CodePilot/src/internal/skill/builtin"
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
//   - Path:触发问题的 SKILL.md 路径或目录路径(若可定位);
//   - Err:具体错误(frontmatter 解析失败 / 目录不可读等);
//   - Source:问题 Skill 的来源级别,用于日志分组展示。
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
//   - workdir:项目根目录(等于 os.Getwd() 或 .codepilot/setting.json 中的 working_directory);
//   - homeDir:用户主目录(= os.UserHomeDir());
//   - execDir:CodePilot 可执行文件所在目录;
//   - maxBytes:Body() / FullContent() 的正文截断上限(<=0 表示不截断);
//
// 返回值:
//
//   - *Registry:合并后的注册表;始终返回非 nil(即便三档全空也返回空 Registry);
//   - []LoadIssue:加载过程中遇到的非致命问题(单个 SKILL.md 解析失败、目录不可读);
//     调用方可遍历记录 warn 日志;即使非空也不视为错误;
//   - error:仅在「同级别同名冲突」时返回非 nil,Registry 与 issues 仍携带部分状态供调试;
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

	// 1. 内置级:三段式 fallback,保证任意路径启动都能拿到内置 Skill。
	//    1) embedded 路径:编译时 //go:embed 嵌入到 binary 内部的 SKILL.md,
	//       启动期从 embeddedFS 读取——只要 binary 编译时源码目录有 SKILL.md,
	//       任意启动路径都能拿到。
	//    2) exeDir 路径(legacy):Makefile / build.ps1 把 SKILL.md 复制到 binary
	//       旁边的 <execDir>/internal/skill/builtin/。仅在 dist 启动时有效。
	//    3) workdir-relative src 路径(新增 fallback):从 workdir 向上找
	//       src/internal/skill/builtin(项目标准 layout),支持在项目根 / 子目录
	//       直接启动 binary,或 binary 被复制到非 dist 路径时仍能加载内置 Skill。
	//    三段是「或」关系,任一段成功即可,后续段用 SkipDuplicateSameSource 跳过
	//    重复条目。
	if err := scanEmbeddedBuiltins(reg, maxBytes, &issues); err != nil {
		return reg, issues, err
	}
	if err := scanLevelWithOptions(reg, filepath.Join(execDir, builtinRelPath), SourceBuiltin, maxBytes, &issues, scanOptions{SkipDuplicateSameSource: true}); err != nil {
		return reg, issues, err
	}
	if srcBuiltin := findSrcBuiltinFallback(workdir); srcBuiltin != "" {
		if err := scanLevelWithOptions(reg, srcBuiltin, SourceBuiltin, maxBytes, &issues, scanOptions{SkipDuplicateSameSource: true}); err != nil {
			return reg, issues, err
		}
	}

	// 2. 用户级
	if err := scanLevel(reg, filepath.Join(homeDir, userSkillsDir), SourceUser, maxBytes, &issues); err != nil {
		return reg, issues, err
	}

	// 3. 项目级(最后注册,silent skip 可覆盖用户级同名)
	if err := scanLevel(reg, filepath.Join(workdir, projectSkillsDir), SourceProject, maxBytes, &issues); err != nil {
		return reg, issues, err
	}

	// 4. 启动期可观测性:如果三段 fallback 都没加载到任何内置 Skill,显式 warn。
	//    典型场景:binary 编译时 SKILL.md 不在源码目录(嵌入为空)+ binary 被复制
	//    到非 dist 路径(exeDir 路径下没有副本)+ 不在项目根或项目无 src 目录(workdir
	//    fallback 也找不到)。此时 /skills 模态框的内置级 tab 为空,用户能据此
	//    日志定位是「重新 make build」还是「路径布局异常」。
	//    [Why] 之前是静默 return,用户看到空 tab 完全无法定位原因。
	if len(reg.ListBySource(SourceBuiltin)) == 0 {
		logger.L().Warn("skill 内置级加载为空(embedded / exeDir / src fallback 全部未命中)",
			zap.String("workdir", workdir),
			zap.String("exec_dir", execDir),
			zap.String("exeDir_scan_path", filepath.Join(execDir, builtinRelPath)),
		)
	}

	return reg, issues, nil
}

// findSrcBuiltinFallback 从 workdir 向上查找 src/internal/skill/builtin 目录,
//作为「内置 Skill」第三段 fallback 路径,用于支持以下场景:
//
//  1. binary 在 dist 路径编译并启动(典型 release 模式):exeDir 路径有 SKILL.md
//     副本,本函数不参与(返回 "");
//  2. binary 在 dist 路径编译,但被复制到非 dist 路径启动(用户实际报 bug 的
//     场景):exeDir 路径找不到,本函数从 workdir 向上找到项目根的 src/ 兜底;
//  3. 用户从项目根或子目录直接启动 binary(workdir 包含项目根):exeDir 路径失败,
//     本函数兜底;
//
// 路径搜索策略:
//
//   - 起点:workdir(可能为绝对路径或相对路径,先 abs 化);
//   - 逐级向上查 <dir>/src/internal/skill/builtin 目录,直到文件系统根;
//   - 找到第一个**存在且至少含一个 SKILL.md 子目录**的目录即返回;
//   - 全程找不到返回 ""(LoadAll 据此跳过第三段 fallback)。
//
// 防御性约束:
//
//   - workdir 为空时直接返回 "",不去猜测;
//   - 找到目录后必须 ReadDir 验证至少有一个子目录含 SKILL.md,避免把空目录
//     当 fallback 命中继续走 scanLevelWithOptions(后者会扫 0 个 skill,日志
//     噪音大)。
//
// [Why 不在 builtin 包用 runtime.Caller 拿源码路径] runtime.Caller(0) 在编译为
// binary 后返回的虚拟路径不会指向真实文件系统,无法用作 fallback 路径;只有
// 「workdir 向上找 src/」这种基于约定的查找在 dev/release 两种模式下都能用。
func findSrcBuiltinFallback(workdir string) string {
	if workdir == "" {
		return ""
	}
	absWD, err := filepath.Abs(workdir)
	if err != nil {
		return ""
	}
	cur := absWD
	for i := 0; i < 16; i++ { // 最多向上 16 级,够覆盖所有项目布局
		candidate := filepath.Join(cur, "src", "internal", "skill", "builtin")
		if info, serr := os.Stat(candidate); serr == nil && info.IsDir() {
			if hasBuiltinSkillMD(candidate) {
				return candidate
			}
		}
		parent := filepath.Dir(cur)
		if parent == cur { // 已到根(filepath.Dir 已返回自身说明已到驱动器根)
			return ""
		}
		cur = parent
	}
	return ""
}

// hasBuiltinSkillMD 校验 candidate 目录下是否至少存在一个子目录含 SKILL.md。
//
// [Why 单独函数] findSrcBuiltinFallback 要避免「目录存在但没有 SKILL.md」时
// 误命中——例如某些项目里 src/internal/skill/builtin 目录里只是 README.md
// 占位,没有实际 Skill 资源;返回 true 后 LoadAll 会走 scanLevelWithOptions,
// 扫 0 个 skill 还会让 warn 日志消失,反而误导排查。
func hasBuiltinSkillMD(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		_, ferr := os.Stat(filepath.Join(dir, e.Name(), skillFileName))
		if ferr == nil {
			return true
		}
	}
	return false
}

// scanLevel 扫描指定根目录下所有子目录的 SKILL.md,逐个 Register。
//
// 入参:
//
//   - rootDir:扫描根目录(如 <workdir>/.codepilot/skills);
//   - src:该级 Skill 的 Source 标识(SourceUser / SourceProject / SourceBuiltin);
//   - maxBytes:Body 截断上限;
//   - issues:非致命问题收集指针(解析失败的 Skill append 进去);
//
// 返回值:
//
//   - nil:目录缺失 或 全部 Skill 加载完成(可能伴随 issues);
//   - *ErrSkillConflict:同级别同名冲突(记录 issue 后立即返回);
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
type scanOptions struct {
	SkipDuplicateSameSource bool
}

func scanEmbeddedBuiltins(reg *Registry, maxBytes int, issues *[]LoadIssue) error {
	entries, err := skillbuiltin.Embedded()
	if err != nil {
		*issues = append(*issues, LoadIssue{
			Path:   "embedded://internal/skill/builtin",
			Err:    fmt.Errorf("read embedded built-in skills: %w", err),
			Source: SourceBuiltin,
		})
		return nil
	}
	for _, entry := range entries {
		displayPath := "embedded://internal/skill/builtin/" + entry.Path
		s, perr := parseFrontmatterString(displayPath, entry.Content)
		if perr != nil {
			*issues = append(*issues, LoadIssue{Path: displayPath, Err: perr, Source: SourceBuiltin})
			logger.L().Warn("skill built-in parse failed", zap.String("path", displayPath), zap.Error(perr))
			continue
		}
		s.Source = SourceBuiltin
		s.RootPath = strings.TrimSuffix(displayPath, "/"+skillFileName)
		s.embedded = true
		if maxBytes > 0 {
			s.MaxBytes = maxBytes
		}
		if rerr := reg.Register(s); rerr != nil {
			var conflict *ErrSkillConflict
			if errors.As(rerr, &conflict) {
				*issues = append(*issues, LoadIssue{Path: displayPath, Err: conflict, Source: SourceBuiltin})
				logger.L().Error("skill built-in name conflict", zap.String("name", conflict.Name), zap.String("path", displayPath))
				return conflict
			}
			*issues = append(*issues, LoadIssue{Path: displayPath, Err: rerr, Source: SourceBuiltin})
			logger.L().Warn("skill built-in register failed", zap.String("path", displayPath), zap.Error(rerr))
		}
	}
	return nil
}

func scanLevel(reg *Registry, rootDir string, src Source, maxBytes int, issues *[]LoadIssue) error {
	return scanLevelWithOptions(reg, rootDir, src, maxBytes, issues, scanOptions{})
}

func scanLevelWithOptions(reg *Registry, rootDir string, src Source, maxBytes int, issues *[]LoadIssue, opts scanOptions) error {
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
		if opts.SkipDuplicateSameSource {
			if existing, ok := reg.Get(s.Name); ok && existing != nil && existing.Source == src {
				continue
			}
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
	return parseFrontmatterString(path, string(data))
}

func parseFrontmatterString(path, raw string) (*Skill, error) {
	fm, body, err := splitFrontmatterForRead(raw)
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "missing frontmatter") || strings.Contains(msg, "unclosed frontmatter") {
			return nil, &localErrMissingFrontmatter{Path: path}
		}
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if strings.TrimSpace(fm.Name) == "" {
		return nil, &localErrMissingField{Path: path, Field: "name"}
	}
	if strings.TrimSpace(fm.Description) == "" {
		return nil, &localErrMissingField{Path: path, Field: "description"}
	}
	return NewSkill(
		fm.Name,
		fm.Description,
		fm.Args,
		fm.AllowedTools,
		Source(0),
		filepath.Dir(path),
		body,
	), nil
}

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
