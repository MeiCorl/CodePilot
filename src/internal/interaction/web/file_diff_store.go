package web

import (
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/MeiCorl/CodePilot/src/internal/logger"
	"github.com/MeiCorl/CodePilot/src/internal/tool"
)

// FileDiffMaxBytes 是单条 FileDiff 允许保存的最大字节数（Before+After 合计）。
// 超过该阈值的 diff 会被丢弃，避免 WriteFile/EditFile 处理大文件时撑爆内存。
// 阈值参考：约 2 MB 文本对应的代码量约 5-6 万行，远超单次工具调用的常见体量。
const FileDiffMaxBytes = 2 * 1024 * 1024

// FileDiff 描述一次更新类工具调用（WriteFile / EditFile）对单个文件造成的改动。
//
// 数据生命周期：仅在进程内存中保存，不随 Session JSON 持久化；进程退出即丢失。
// 设计动机：旧会话重启后用户点击「查看改动」会得到 not_found 提示，符合 spec 中
// "diff 仅内存" 的决策，避免大幅膨胀 session JSON。
//
// 与 tool.FileDiffEntry 的关系：Entry 是工具侧写入的最小数据单元（路径 + 改动
// 前后内容），FileDiff 在 Entry 基础上补齐 web 侧元数据（Language 推导、UpdatedAt）。
// Set 负责把 Entry 转成 FileDiff，Get 返回 FileDiff 供前端使用。
type FileDiff struct {
	// ToolUseID 是 Anthropic 协议中的 tool_use_id，作为该条 diff 的唯一主键。
	// 同一 tool_use_id 的 Set 调用为覆盖语义（仅最后一次生效）。
	ToolUseID string
	// FilePath 是被改动的文件绝对路径（已通过沙箱 resolve）。
	FilePath string
	// Before 是改动前的文件内容；新建文件场景下为 ""。
	Before string
	// After 是改动后的文件内容。
	After string
	// Language 是按文件后缀识别出的 highlight.js 语言标识（如 "go" / "json"）；
	// 未识别时为 ""，前端会回退到纯文本渲染。
	Language string
	// UpdatedAt 是写入时间，用于将来可能的 LRU 淘汰（当前不启用）。
	UpdatedAt time.Time
}

// FileDiffStore 是 FileDiff 的进程内存储。
//
// 并发模型：sync.RWMutex 保护 map。读多写少（每次 WriteFile/EditFile 执行后
// 写一次，浏览器每次点击「查看改动」读一次），RWMutex 收益明显。
//
// 实现 tool.FileDiffSink interface：由内置工具注入为本 Store 即可让 WriteFile /
// EditFile 自动把每次改动写入该 map，无需 web 包反向依赖 builtin。
type FileDiffStore struct {
	mu    sync.RWMutex
	items map[string]FileDiff
}

// 编译期断言：*FileDiffStore 满足 tool.FileDiffSink。
// 若 Set 签名变动，编译期即可发现，不依赖运行时注册检查。
var _ tool.FileDiffSink = (*FileDiffStore)(nil)

// NewFileDiffStore 构造一个空的 FileDiffStore。
func NewFileDiffStore() *FileDiffStore {
	return &FileDiffStore{
		items: make(map[string]FileDiff),
	}
}

// Set 写入一条 diff。
//
// 返回值：true 表示写入成功；false 表示因超过 FileDiffMaxBytes 而被拒绝（已 warn 日志）。
// 拒绝时不写入 map，调用方不应再 Get。
// toolUseID 为空字符串时直接返回 false 并 warn（无效主键）。
//
// Set 同时实现 tool.FileDiffSink interface：接收最小化的 tool.FileDiffEntry，
// 内部按 FilePath 推导 Language 并填充 UpdatedAt 后存入 map。
func (s *FileDiffStore) Set(toolUseID string, entry tool.FileDiffEntry) bool {
	if s == nil {
		return false
	}
	if toolUseID == "" {
		logger.Warn("FileDiffStore.Set 拒绝：toolUseID 为空")
		return false
	}

	size := len(entry.Before) + len(entry.After)
	if size > FileDiffMaxBytes {
		logger.Warn("FileDiffStore.Set 拒绝：超过容量上限",
			zap.String("tool_use_id", toolUseID),
			zap.String("file_path", entry.FilePath),
			zap.Int("size_bytes", size),
			zap.Int("max_bytes", FileDiffMaxBytes),
		)
		return false
	}

	diff := FileDiff{
		ToolUseID: toolUseID,
		FilePath:  entry.FilePath,
		Before:    entry.Before,
		After:     entry.After,
		Language:  DetectLanguage(entry.FilePath),
		UpdatedAt: time.Now(),
	}

	s.mu.Lock()
	s.items[toolUseID] = diff
	s.mu.Unlock()
	return true
}

// Get 按 toolUseID 读取一条 diff。
//
// 返回值：found 表示是否存在；不存在时不返回零值。
func (s *FileDiffStore) Get(toolUseID string) (FileDiff, bool) {
	if s == nil {
		return FileDiff{}, false
	}
	s.mu.RLock()
	diff, ok := s.items[toolUseID]
	s.mu.RUnlock()
	return diff, ok
}

// Delete 删除一条 diff；不存在时为 no-op。
func (s *FileDiffStore) Delete(toolUseID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.items, toolUseID)
	s.mu.Unlock()
}

// Len 返回当前存储的条目数（用于测试与监控）。
func (s *FileDiffStore) Len() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.items)
}

// DetectLanguage 按文件后缀映射到 highlight.js 语言标识。
//
// 覆盖范围与 Step 1.2 的 highlight 主题保持一致；未识别时返回 ""，
// 前端会回退到纯文本渲染（不传 language 给 hljs）。
// 该函数是无状态纯函数，被 Store 与工具侧共用，避免映射表分散。
func DetectLanguage(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go":
		return "go"
	case ".md", ".markdown":
		return "markdown"
	case ".json":
		return "json"
	case ".xml":
		return "xml"
	case ".py":
		return "python"
	case ".ts":
		return "typescript"
	case ".js":
		return "javascript"
	case ".css":
		return "css"
	case ".yaml", ".yml":
		return "yaml"
	case ".html", ".htm":
		return "xml" // hljs 内置 html，但映射为 xml 标签着色更通用
	case ".sql":
		return "sql"
	case ".sh", ".bash":
		return "bash"
	default:
		return ""
	}
}
