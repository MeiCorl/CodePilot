// tool_result_store.go 实现工具结果的落盘子系统，服务于第一层「轻量预防」压缩。
//
// 背景：ReadFile / Grep / Bash 等工具的完整输出往往是上下文体积的大头，且每个
// block 在后续每一轮请求里都会被原样重发——既浪费 token 预算，又拖累每次请求的
// 成本与延迟。Step 7 第一层压缩的策略是：单个工具结果超过阈值时，把完整结果落盘
// 到会话目录下，内存中仅保留「头部截断预览 + 文件路径尾注」，并告知 LLM 需要准确
// 结果时可用 ReadFile 重新读取（见 preview.go / light_compactor.go）。
//
// 本文件只负责「落盘」这一件事——纯 IO 子系统，不感知压缩阈值与预览逻辑（那些是
// LightCompactor 的职责）。这样拆分是为了高内聚：本子系统可独立测试，且后续若要
// 把工具结果改存到其它后端（如外部对象存储）只需替换本文件实现，不触碰压缩编排。
//
// 落盘位置约定（与 session 包的目录结构对齐）：
//
//	<projectDir>/<sessionID>/tool_results/<toolUseID>
//
// 其中 <projectDir>/<sessionID>/ 正是 session 包 SessionManager 的会话目录
// （见 session.go 包注释「{session_id} 一层目录为后续存放工具调用结果等内容预留空间」），
// tool_results/ 是其下的专用子目录。projectDir 由 Task 7 主流程装配时从
// SessionManager 注入（与本结构体持有的字符串保持同一值），故本文件刻意不依赖
// session 包，避免记忆层内的反向耦合。
//
// 关键设计：
//   - 幂等：同一 (sessionID, toolUseID) 第二次写入直接 skipped，不重写文件。配合
//     LightCompactor 的「in-place 预览替换」与确定性阈值判断，保证某个工具结果一旦
//     被决定落盘，则后续整个会话每轮都保持预览态——prompt cache 前缀稳定，命中率不抖动。
//   - 并发安全：用 O_CREATE|O_EXCL 原子创建，多个 goroutine 同时 Save 同一 id 时只有
//     一方真正写入，其余得到「已存在」语义返回 skipped，无 panic、无重复写。
//   - 防路径逃逸：sessionID / toolUseID 经 isSafeName 校验，拒绝含路径分隔符或为
//     "."/".." 的名字，杜绝构造恶意 id 写到会话目录之外（工具结果可能含代码/密钥/日志，
//     视为敏感数据，受现有路径沙箱同等约束）。
//   - 失败可重试：写入内容失败时清理已创建的半成品文件，避免遗留空壳被后续 Save 误判
//     为「已存盘」而永久跳过。

package context

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// toolResultsDirName 为会话目录下存放工具结果归档的子目录名。
const toolResultsDirName = "tool_results"

// ToolResultStore 是工具结果的落盘子系统。
//
// 持有 projectDir（会话根目录，与 SessionManager.projectDir 同一值）作为所有会话
// 目录的父目录。无状态成员——所有路径由 (sessionID, toolUseID) 现场计算，天然线程安全
// （同一 store 可被多个 goroutine 并发使用，文件系统自身的并发语义由 O_EXCL 兜底）。
type ToolResultStore struct {
	// projectDir 为会话根目录（<sessionsRoot>/<projectName>），所有会话目录的父目录。
	// 由主流程装配时注入，与 SessionManager.projectDir 指向同一物理目录。
	projectDir string
}

// NewToolResultStore 创建一个工具结果存盘器。
//
// projectDir 为会话根目录（即 SessionManager.projectDir）。本构造不做目录存在性检查
// （惰性创建留到 Save 时按需 MkdirAll），允许在会话目录尚未建立时即构造 store。
func NewToolResultStore(projectDir string) *ToolResultStore {
	return &ToolResultStore{projectDir: projectDir}
}

// Path 返回工具结果归档文件的预期完整路径。
//
// 纯字符串拼接，不触发任何 IO——即使文件尚未落盘也能给出路径，供预览尾注引用
// （LightCompactor 生成「完整结果已存盘于 <路径>」提示时调用）。调用方需保证
// sessionID / toolUseID 合法（本方法不校验，校验在 Save / Exists 入口进行）。
func (s *ToolResultStore) Path(sessionID, toolUseID string) string {
	return filepath.Join(s.projectDir, sessionID, toolResultsDirName, toolUseID)
}

// Exists 查询指定工具结果是否已落盘。
//
// 触发一次 os.Stat（轻量 IO）。名字非法时直接返回 false（不产生 IO，也不报错——
// 查询语义上「非法名字必然不存在」）。用于 LightCompactor 判定「该 block 是否已是
// 预览态」的辅助依据，避免对已落盘结果重复处理。
func (s *ToolResultStore) Exists(sessionID, toolUseID string) bool {
	if !isSafeName(sessionID) || !isSafeName(toolUseID) {
		return false
	}
	_, err := os.Stat(s.Path(sessionID, toolUseID))
	return err == nil
}

// Save 把工具结果内容落盘，幂等且并发安全。
//
// 语义与返回值：
//   - 文件已存在（含并发竞态下被其它 goroutine 抢先创建）：skipped=true，不重写，
//     err=nil。filePath 始终指向预期路径（无论是否实际写入）。
//   - 文件不存在且创建+写入成功：skipped=false，err=nil。
//   - 名字非法（路径逃逸风险）：skipped=false，返回 err，不做任何 IO。
//   - 目录创建 / 文件打开 / 写入失败：skipped=false，返回 err（写入失败会清理半成品）。
//
// 幂等与并发的实现：先 os.Stat 快速判定已存在（避免无谓的 OpenFile）；不存在时用
// O_CREATE|O_EXCL|O_WRONLY 打开——O_EXCL 保证同一路径只有一个 goroutine 创建成功，
// 竞态失败方收到 os.IsExist，统一按「已存在 → skipped」处理。
//
// 内容原样写入（纯文本，即工具结果的 Content 字符串），不做 JSON 包裹，便于 LLM
// 重读时 ReadFile 直接得到可读文本而非转义后的 JSON。
func (s *ToolResultStore) Save(sessionID, toolUseID, content string) (filePath string, skipped bool, err error) {
	// 防路径逃逸：拒绝含路径分隔符或为 "."/".." 的名字，避免恶意/异常 id 写到会话目录之外
	if !isSafeName(sessionID) {
		return "", false, fmt.Errorf("非法 sessionID（含路径分隔符或为保留名）: %q", sessionID)
	}
	if !isSafeName(toolUseID) {
		return "", false, fmt.Errorf("非法 toolUseID（含路径分隔符或为保留名）: %q", toolUseID)
	}

	filePath = s.Path(sessionID, toolUseID)

	// 快速路径：已存在则直接跳过（幂等的核心，避免对已落盘结果重复 IO）。
	if _, statErr := os.Stat(filePath); statErr == nil {
		return filePath, true, nil
	}

	// 确保父目录存在（会话目录可能尚未建立，惰性创建）。
	dir := filepath.Dir(filePath)
	if mkErr := os.MkdirAll(dir, 0755); mkErr != nil {
		return "", false, fmt.Errorf("创建工具结果目录失败 (%s): %w", dir, mkErr)
	}

	// O_CREATE|O_EXCL 原子创建：并发下仅一方成功，失败方按「已存在」处理。
	f, openErr := os.OpenFile(filePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if openErr != nil {
		if os.IsExist(openErr) {
			// 与另一 goroutine 的竞态：对方已抢先创建，视为已存在。
			return filePath, true, nil
		}
		return "", false, fmt.Errorf("创建工具结果文件失败 (%s): %w", filePath, openErr)
	}

	// 写入失败时清理半成品文件：否则下次 Save 会 Stat 命中而永久 skipped，
	// 导致该工具结果永远以空壳形式存在（内容丢失却看似已存盘）。
	if _, writeErr := f.WriteString(content); writeErr != nil {
		_ = f.Close()
		_ = os.Remove(filePath) // 忽略清理错误：原写入错误更值得上报
		return "", false, fmt.Errorf("写入工具结果失败 (%s): %w", filePath, writeErr)
	}
	if closeErr := f.Close(); closeErr != nil {
		// Close 失败同样可能留下不完整文件，清理之以保证可重试。
		_ = os.Remove(filePath)
		return "", false, fmt.Errorf("关闭工具结果文件失败 (%s): %w", filePath, closeErr)
	}
	return filePath, false, nil
}

// isSafeName 校验名字可作为文件名 / 目录名段使用。
//
// 拒绝：空串、"."、".."、含任意路径分隔符（/ 或 \，覆盖跨平台）的名字。
// 合法的 sessionID（UUID，含 "-"）与 toolUseID（如 "toolu_xxx" / "call_xxx"，含 "_"）
// 均不含路径分隔符，不会被误拒。
//
// 注意：不依赖 filepath.Base 做清洗——因为 filepath.Base("..") 仍返回 ".."，
// 无法防御保留名；显式枚举拒绝项最稳妥。
func isSafeName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	// 同时检查正反斜杠，兼容 Windows（\）与 POSIX（/）；其它平台其一为分隔符即视为越界。
	if strings.ContainsAny(name, `/\`) {
		return false
	}
	return true
}
