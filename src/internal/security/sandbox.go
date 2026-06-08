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
	if path == "" {
		return "", fmt.Errorf("%w: 路径为空", ErrPathOutsideSandbox)
	}
	if sandboxDir == "" {
		return "", fmt.Errorf("sandboxDir 不能为空")
	}

	// 1. 规范化 sandboxDir
	absSandbox, err := filepath.Abs(sandboxDir)
	if err != nil {
		return "", fmt.Errorf("解析 sandboxDir 失败: %w", err)
	}
	absSandbox = filepath.Clean(absSandbox)

	// 2. 规范化目标路径
	absTarget := path
	if !filepath.IsAbs(absTarget) {
		absTarget = filepath.Join(absSandbox, absTarget)
	}
	absTarget = filepath.Clean(absTarget)

	// 3. 路径前缀校验（大小写不敏感：Windows 文件系统不区分大小写）
	if !IsPathInside(absTarget, absSandbox) {
		return "", fmt.Errorf("%w: %q 不在 %q 之内", ErrPathOutsideSandbox, absTarget, absSandbox)
	}

	// 4. symlink 解析后再次校验（若文件已存在）
	if info, err := os.Lstat(absTarget); err == nil && info.Mode()&os.ModeSymlink != 0 {
		real, err := filepath.EvalSymlinks(absTarget)
		if err != nil {
			return "", fmt.Errorf("解析 symlink 失败: %w", err)
		}
		real = filepath.Clean(real)
		if !IsPathInside(real, absSandbox) {
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
