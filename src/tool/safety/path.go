// Package safety 提供 CodePilot 工具系统的最小化安全兜底：
// 路径沙箱与 Bash 危险命令黑名单。
//
// 设计目标：两道兜底**不可被工具配置关闭**，即使用户在 config.json
// 中设置了任何"禁用安全检查"字段也无效。所有涉及文件读写的工具
// 必须经 ResolveInSandbox，所有 Bash 执行前必须经 CheckBashCommand。
//
// 本文件是 Task 2 阶段的基础实现，覆盖常见路径遍历与显式越界；
// Task 3 会在本文件基础上增强 symlink 解析、路径大小写不敏感等
// 边缘场景，并补充更完善的黑名单规则。
package safety

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrPathOutsideSandbox 路径经规范化后落在 sandbox 之外时返回。
// 工具调用方应原样回传给 LLM，由 LLM 决定是否调整输入。
var ErrPathOutsideSandbox = errors.New("路径沙箱拦截：目标路径不在允许的工作目录之内")

// ErrDangerousCommand 命令命中黑名单时返回。
// 工具调用方应在执行子进程前返回此错误，避免危险命令触及系统。
var ErrDangerousCommand = errors.New("危险命令拦截：命令命中黑名单，拒绝执行")

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
	if !isPathInside(absTarget, absSandbox) {
		return "", fmt.Errorf("%w: %q 不在 %q 之内", ErrPathOutsideSandbox, absTarget, absSandbox)
	}

	// 4. symlink 解析后再次校验（若文件已存在）
	if info, err := os.Lstat(absTarget); err == nil && info.Mode()&os.ModeSymlink != 0 {
		real, err := filepath.EvalSymlinks(absTarget)
		if err != nil {
			return "", fmt.Errorf("解析 symlink 失败: %w", err)
		}
		real = filepath.Clean(real)
		if !isPathInside(real, absSandbox) {
			return "", fmt.Errorf("%w: symlink 目标 %q 落在 sandbox 外", ErrPathOutsideSandbox, real)
		}
		return real, nil
	}

	return absTarget, nil
}

// isPathInside 判断 child 是否落在 parent 之内（包含边界）。
// 在 Windows/macOS 默认文件系统上做大小写不敏感比较，
// Linux 上做大小写敏感比较。
func isPathInside(child, parent string) bool {
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
