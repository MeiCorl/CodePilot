// Package security 提供 CodePilot 的安全层，包括：
//   - 路径沙箱（ResolveInSandbox / IsPathOutsideSandbox）
//   - Bash 危险命令黑名单（CheckBashCommand）
//   - 权限检查器（Checker / Interceptor）
//   - 人在回路确认（HITLCallback）
//
// 本文件迁移自原 tool/safety/path.go，新增 IsPathOutsideSandbox 查询函数。
// 路径沙箱作为硬兜底层，不可被配置关闭或绕过，所有涉及文件读写的工具
// 必须经 ResolveInSandbox 校验。
package security

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrPathOutsideSandbox 路径经规范化后落在 sandboxDir 之外时返回。
// 工具调用方应原样回传给 LLM，由 LLM 决定是否调整输入。
var ErrPathOutsideSandbox = errors.New("路径沙箱拦截：目标路径不在允许的工作目录之内")

// ResolveInSandbox 将用户传入的路径解析为绝对路径，并校验其落在
// sandboxDir 之内。
//
// 等价于 ResolveInSandboxWithRoots(path, sandboxDir, nil)——不附带任何
// 附加只读根。保留本函数以维持既有调用点与测试零改动；附加根能力
// （Step 8 记忆系统）请直接使用 ResolveInSandboxWithRoots。
//
// 行为：
//  1. 相对路径：基于 sandboxDir 解析；
//  2. 绝对路径：直接使用；
//  3. 通过 filepath.Clean 规范化；
//  4. 校验规范化后路径必须以 sandboxDir 开头；
//  5. 对已存在的 symlink，使用 filepath.EvalSymlinks 解析真实路径
//     后再次校验（防止通过软链绕过）。
//
// 不存在且需要被创建的目标（如 WriteFile）允许通过基础检查，
// 由调用方在创建后再做权限/范围校验。
func ResolveInSandbox(path, sandboxDir string) (string, error) {
	return ResolveInSandboxWithRoots(path, sandboxDir, nil)
}

// ResolveInSandboxWithRoots 在 ResolveInSandbox 基础上额外允许一组「附加只读根」。
//
// [Why] Step 8 记忆系统要求 LLM 能经 ReadFile/Glob/Grep 读取
// ~/.codepilot/memory 与 <cwd>/.codepilot/memory 下的记忆文件——这些目录
// 通常落在工作目录之外，原 ResolveInSandbox 会一律拒绝。本函数在不破坏
// 既有沙箱语义的前提下，把附加根纳入合法可读范围，形成
// "working_directory ∪ 附加根" 的可读集合。
//
// 与 ResolveInSandbox 的唯一差异：规范化后的目标路径落在 sandboxDir 或
// 任一 extraRoot 之内即视为合法；symlink 解析后的真实路径同样必须落在
// 某个根内（防止通过软链逃逸到附加根之外）。extraRoots 为空（或全为空串）
// 时行为与 ResolveInSandbox 完全等价。
//
// [安全语义] 本函数仅放宽「路径合法性」的硬兜底；是否真正可读仍由权限层
// permission.Decide 决定——IsPathOutsideSandbox（权限层用）不感知附加根，
// 跨 working_directory 的用户级 memory 读取在权限层仍标记为越界，由
// mode + allow/deny/ask 规则共同决策（Strict 走 Deny/Ask，Default 宽松）。
// 即「沙箱放行 ≠ 权限绕过」，两层职责分离形成纵深防御。
func ResolveInSandboxWithRoots(path, sandboxDir string, extraRoots []string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%w: 路径为空", ErrPathOutsideSandbox)
	}
	if sandboxDir == "" {
		return "", fmt.Errorf("sandboxDir 不能为空")
	}

	// 1. 规范化 sandboxDir，并收集候选根集合（sandboxDir 永远是第一个根）。
	//    附加根做同样的 Abs+Clean 规范化；非法/空根静默跳过，不中断主流程。
	absSandbox, err := filepath.Abs(sandboxDir)
	if err != nil {
		return "", fmt.Errorf("解析 sandboxDir 失败: %w", err)
	}
	absSandbox = filepath.Clean(absSandbox)
	roots := []string{absSandbox}
	for _, r := range extraRoots {
		if r == "" {
			continue
		}
		ar, err := filepath.Abs(r)
		if err != nil {
			// 单个附加根解析失败不影响整体，跳过该根即可。
			continue
		}
		roots = append(roots, filepath.Clean(ar))
	}

	// 2. 规范化目标路径（相对路径基于 sandboxDir 解析，与原语义一致）
	absTarget := path
	if !filepath.IsAbs(absTarget) {
		absTarget = filepath.Join(absSandbox, absTarget)
	}
	absTarget = filepath.Clean(absTarget)

	// 3. 范围校验：目标必须落在 sandboxDir 或某个附加根之内（跨平台大小写处理见 IsPathInsideAnyRoot）
	if !IsPathInsideAnyRoot(absTarget, roots) {
		return "", fmt.Errorf("%w: %q 不在允许的工作目录之内", ErrPathOutsideSandbox, absTarget)
	}

	// 4. symlink 解析后再次校验（若文件已存在）：真实路径同样必须落在某个根内，
	//    防止通过附加根目录内的软链逃逸到根外敏感区域。
	if info, err := os.Lstat(absTarget); err == nil && info.Mode()&os.ModeSymlink != 0 {
		real, err := filepath.EvalSymlinks(absTarget)
		if err != nil {
			return "", fmt.Errorf("解析 symlink 失败: %w", err)
		}
		real = filepath.Clean(real)
		if !IsPathInsideAnyRoot(real, roots) {
			return "", fmt.Errorf("%w: symlink 目标 %q 落在 sandbox 外", ErrPathOutsideSandbox, real)
		}
		return real, nil
	}

	return absTarget, nil
}

// IsPathOutsideSandbox 查询路径是否落在 sandboxDir 之外。
//
// 与 ResolveInSandbox 使用相同的规范化逻辑，但不拒绝，仅返回布尔值。
// 供拦截器在权限检查时判断路径是否在工作目录范围外，结合档位决定
// 是 Ask / Deny 还是 Allow。
//
// [故意不感知附加只读根] 本函数只认 sandboxDir（工作目录），不感知
// ResolveInSandboxWithRoots 的 extraRoots。这是有意的职责分离：沙箱层
// （ResolveInSandboxWithRoots）决定「路径是否合法可碰」，权限层
// （IsPathOutsideSandbox + mode 兜底）决定「碰之前要不要问用户」。因此
// 即便 ~/.codepilot/memory 已被沙箱附加根放行，跨工作目录读取它在此处
// 仍返回 true（越界），由权限模式决定 Ask/Deny/Allow——满足 Step 8
// 「沙箱放行 ≠ 权限绕过」的双层语义。
//
// 返回值：
//   - (true, nil): 路径越界
//   - (false, nil): 路径在范围内
//   - (false, error): 规范化过程出错
func IsPathOutsideSandbox(path, sandboxDir string) (bool, error) {
	if path == "" || sandboxDir == "" {
		return false, nil
	}

	absSandbox, err := filepath.Abs(sandboxDir)
	if err != nil {
		return false, fmt.Errorf("解析 sandboxDir 失败: %w", err)
	}
	absSandbox = filepath.Clean(absSandbox)

	absTarget := path
	if !filepath.IsAbs(absTarget) {
		absTarget = filepath.Join(absSandbox, absTarget)
	}
	absTarget = filepath.Clean(absTarget)

	if !IsPathInside(absTarget, absSandbox) {
		return true, nil
	}

	// 对已存在的 symlink 也要检查真实路径
	if info, err := os.Lstat(absTarget); err == nil && info.Mode()&os.ModeSymlink != 0 {
		real, err := filepath.EvalSymlinks(absTarget)
		if err != nil {
			return false, fmt.Errorf("解析 symlink 失败: %w", err)
		}
		real = filepath.Clean(real)
		if !IsPathInside(real, absSandbox) {
			return true, nil
		}
	}

	return false, nil
}

// IsPathInside 判断 child 是否落在 parent 之内（包含边界）。
// 在 Windows/macOS 默认文件系统上做大小写不敏感比较，
// Linux 上做大小写敏感比较。
//
// 导出此函数供 builtin 工具的 symlink 兜底使用：Glob/Grep 工具在 walk
// 过程中对 symlink 调用 EvalSymlinks 后用本函数判断是否在 sandbox 内。
func IsPathInside(child, parent string) bool {
	child = filepath.Clean(child)
	parent = filepath.Clean(parent)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		child = strings.ToLower(child)
		parent = strings.ToLower(parent)
	}
	if child == parent {
		return true
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	// Rel 返回以 ".." 开头的相对路径说明在 parent 之外
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// IsPathInsideAnyRoot 判断 path 是否落在 roots 中任一根目录之内（含边界）。
//
// 供 ResolveInSandboxWithRoots 的附加只读根机制复用：把 sandboxDir 与各
// extraRoot 一起作为候选根，命中任一即视为合法。空串根会被忽略（防御非法输入）。
// 跨平台大小写语义由 IsPathInside 统一承担（Windows/macOS 不敏感、Linux 敏感）。
func IsPathInsideAnyRoot(path string, roots []string) bool {
	for _, root := range roots {
		if root == "" {
			continue
		}
		if IsPathInside(path, root) {
			return true
		}
	}
	return false
}
