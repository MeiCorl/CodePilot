// Package session 实现会话的本地持久化管理。
//
// 存储模型（append-only JSONL + 按项目分目录）：
//
//	~/.codepilot/sessions/
//	└─ {project_name}/                  # 项目目录 = filepath.Base(workdir)，同名冲突时 basename-<hash>
//	   ├─ .project.json                 # 记录真实 CWD，用于校验/展示
//	   ├─ {session_id}/
//	   │  ├─ meta.json                  # {id, created_at, updated_at, message_count}
//	   │  └─ messages.jsonl             # 每行 1 条 llm.Message，append-only
//	   └─ {session_id}/...
//
// 设计要点：
//   - 单次会话生命周期内 history 纯追加，落盘时只在 messages.jsonl 末尾追加新消息行，
//     不再全量重写（旧的「单文件全量 JSON 覆盖写」模型已废弃）。
//   - {session_id} 一层目录为后续存放工具调用结果等内容预留空间。
//   - 列表 / resume / LoadLatest 的作用域均限定在「当前项目目录」内，跨项目天然隔离。
//   - 旧的 ~/.codepilot/sessions/*.json 文件不被扫描（列表只读项目目录下的子目录），等价于忽略。
package session

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/MeiCorl/CodePilot/src/internal/logger"
	"github.com/MeiCorl/CodePilot/src/llm"
	"go.uber.org/zap"
)

// 落盘文件名常量。
const (
	// metaFileName 为会话元数据文件名（位于 {session_id} 目录下）。
	metaFileName = "meta.json"
	// messagesFileName 为消息日志文件名（append-only JSONL）。
	messagesFileName = "messages.jsonl"
	// projectMetaFileName 为项目目录的归属元数据文件名（记录真实 CWD）。
	projectMetaFileName = ".project.json"
)

// maxScanLineSize 限制单条消息行的最大字节数（1MB）。
// 用于 bufio.Scanner 容错：超大 tool_result 行不至于让整个会话加载失败。
const maxScanLineSize = 1 << 20

// Session 代表一次完整的对话会话。
// 包含会话元信息和所有对话消息。
// 持久化时元信息写入 meta.json、消息逐行写入 messages.jsonl；
// 项目归属由「所在项目目录」承担，故结构体内不冗余存储项目路径字段。
type Session struct {
	// ID 为会话唯一标识（UUID 格式）
	ID string `json:"id"`
	// CreatedAt 为会话创建时间
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt 为会话最后更新时间
	UpdatedAt time.Time `json:"updated_at"`
	// Messages 为对话消息列表，使用 ContentBlock 数组形式
	Messages []llm.Message `json:"messages"`
}

// SessionManager 管理会话的持久化存储，作用域限定在单个项目目录内。
//
// 字段语义：
//   - sessionsRoot：所有项目目录的父目录（~/.codepilot/sessions）
//   - projectName：当前项目对应的目录名（filepath.Base(workdir)，冲突时 basename-<hash>）
//   - projectPath：真实工作目录绝对路径（workdir），用于 .project.json 校验/展示
//   - projectDir：sessionsRoot/projectName，本管理器所有会话操作的根目录
type SessionManager struct {
	sessionsRoot string
	projectName  string
	projectPath  string
	projectDir   string
}

// SessionSummary 是会话的摘要信息，用于列表展示。
// 只包含元信息和预览文本，避免加载完整的消息列表，减少内存开销。
type SessionSummary struct {
	// ID 为会话唯一标识
	ID string `json:"id"`
	// CreatedAt 为会话创建时间
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt 为会话最后更新时间
	UpdatedAt time.Time `json:"updated_at"`
	// MessageCount 为消息数量
	MessageCount int `json:"message_count"`
	// Preview 为首条用户消息的前 80 字符预览，无用户消息时显示 "(空会话)"
	Preview string `json:"preview"`
}

// sessionMeta 为 {session_id}/meta.json 的结构。
// 非导出类型，仅在本包内用于序列化/反序列化。
type sessionMeta struct {
	ID           string    `json:"id"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	MessageCount int       `json:"message_count"`
}

// projectMeta 为项目目录下 .project.json 的结构，记录该目录归属的真实 CWD。
// 同名 basename 冲突时用于区分不同项目，避免误合并会话。
type projectMeta struct {
	// Path 为创建该目录时的工作目录绝对路径
	Path string `json:"path"`
	// Basename 为 filepath.Base(Path)
	Basename string `json:"basename"`
	// CreatedAt 为目录首次创建时间
	CreatedAt time.Time `json:"created_at"`
}

// NewSessionManager 创建会话管理器，按 workdir 定位到对应项目目录。
// sessionsRoot 固定为 ~/.codepilot/sessions；项目目录不存在时自动创建并写入 .project.json。
func NewSessionManager(workdir string) (*SessionManager, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("获取用户主目录失败: %w", err)
	}
	sessionsRoot := filepath.Join(homeDir, ".codepilot", "sessions")
	return newSessionManager(sessionsRoot, workdir)
}

// NewSessionManagerWithDir 使用指定 sessionsRoot 创建会话管理器，供测试与自定义部署使用。
// workdir 用于定位项目目录；项目目录不存在时自动创建。
func NewSessionManagerWithDir(sessionsRoot, workdir string) (*SessionManager, error) {
	return newSessionManager(sessionsRoot, workdir)
}

// newSessionManager 是两个公开构造函数的共用实现。
// 永不返回 fatal：项目目录解析失败时降级为哈希目录 + Warn，保证启动不被阻断。
func newSessionManager(sessionsRoot, workdir string) (*SessionManager, error) {
	// 确保会话根目录存在
	if err := os.MkdirAll(sessionsRoot, 0755); err != nil {
		return nil, fmt.Errorf("创建会话根目录失败: %w", err)
	}

	projectDir, projectName, err := resolveProjectDir(sessionsRoot, workdir)
	if err != nil {
		// 解析失败属于非致命：降级到基于哈希的目录，保证会话功能可用
		logger.Warn("项目目录解析失败，降级使用哈希目录",
			zap.String("workdir", workdir), zap.Error(err))
		projectName = shortHash(workdir)
		projectDir = filepath.Join(sessionsRoot, projectName)
		if mkErr := os.MkdirAll(projectDir, 0755); mkErr != nil {
			return nil, fmt.Errorf("创建降级项目目录失败: %w", mkErr)
		}
		// 降级目录同样写一份 .project.json，便于后续识别归属
		_ = writeProjectMeta(projectDir, workdir)
	}

	return &SessionManager{
		sessionsRoot: sessionsRoot,
		projectName:  projectName,
		projectPath:  workdir,
		projectDir:   projectDir,
	}, nil
}

// resolveProjectDir 根据 workdir 解析出项目目录路径与目录名。
//
// 命名规则与冲突处理：
//  1. 默认目录名 = filepath.Base(workdir)。
//  2. 候选目录不存在 → 创建并写入 .project.json，直接使用 basename。
//  3. 候选目录存在 → 读 .project.json：
//     a. path 与当前 workdir 相同 → 复用（同项目多次启动）。
//     b. path 不同或 .project.json 缺失/损坏 → 视为同名冲突，改用 basename-<sha256(workdir)[:8]>。
func resolveProjectDir(sessionsRoot, workdir string) (projectDir, projectName string, err error) {
	basename := filepath.Base(workdir)
	// basename 异常（根目录、空 workdir 等）直接走哈希兜底
	if !isValidProjectName(basename) {
		basename = shortHash(workdir)
		return createOrReuseHashedDir(sessionsRoot, workdir, basename)
	}

	candidateDir := filepath.Join(sessionsRoot, basename)
	info, statErr := os.Stat(candidateDir)
	if os.IsNotExist(statErr) {
		// 候选目录不存在 → 创建 + 写 .project.json
		if err = os.MkdirAll(candidateDir, 0755); err != nil {
			return "", "", fmt.Errorf("创建项目目录失败: %w", err)
		}
		if err = writeProjectMeta(candidateDir, workdir); err != nil {
			return "", "", err
		}
		return candidateDir, basename, nil
	}
	if statErr != nil {
		return "", "", fmt.Errorf("检查项目目录失败: %w", statErr)
	}
	if !info.IsDir() {
		// 同名文件占位（如旧 .json）→ 哈希兜底
		return createOrReuseHashedDir(sessionsRoot, workdir, basename)
	}

	// 候选目录存在：校验 .project.json 归属
	storedPath, ok := readProjectMeta(candidateDir)
	if ok && normalizePath(storedPath) == normalizePath(workdir) {
		// 同项目复用
		return candidateDir, basename, nil
	}
	// 归属不一致或元数据缺失 → 同名冲突，哈希兜底
	return createOrReuseHashedDir(sessionsRoot, workdir, basename)
}

// createOrReuseHashedDir 创建或复用 basename-<hash> 形式的项目目录。
// 哈希基于 workdir 稳定生成，保证同 workdir 多次启动复用同一哈希目录。
func createOrReuseHashedDir(sessionsRoot, workdir, basename string) (projectDir, projectName string, err error) {
	hashedName := basename + "-" + shortHash(workdir)
	hashedDir := filepath.Join(sessionsRoot, hashedName)
	if err = os.MkdirAll(hashedDir, 0755); err != nil {
		return "", "", fmt.Errorf("创建项目目录(哈希兜底)失败: %w", err)
	}
	// 写/覆盖 .project.json，保证归属记录正确（已存在则更新 path）
	if err = writeProjectMeta(hashedDir, workdir); err != nil {
		return "", "", err
	}
	return hashedDir, hashedName, nil
}

// isValidProjectName 判断目录名是否合法（非空、非路径分隔符、非点号当前目录）。
func isValidProjectName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if name == string(filepath.Separator) {
		return false
	}
	return true
}

// normalizePath 对路径做基础规范化，用于归属比较（去多余分隔符、统一分隔符）。
// 注意：不做大小写归一化，跨平台路径大小写差异不在本期处理范围。
func normalizePath(p string) string {
	return filepath.Clean(p)
}

// writeProjectMeta 写入项目目录下的 .project.json。
func writeProjectMeta(projectDir, projectPath string) error {
	pm := projectMeta{
		Path:      projectPath,
		Basename:  filepath.Base(projectPath),
		CreatedAt: time.Now(),
	}
	data, err := json.MarshalIndent(pm, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化项目元数据失败: %w", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, projectMetaFileName), data, 0644); err != nil {
		return fmt.Errorf("写入项目元数据失败: %w", err)
	}
	return nil
}

// readProjectMeta 读取项目目录下的 .project.json，返回记录的 Path。
// 文件缺失或损坏返回 ok=false（不视为致命错误）。
func readProjectMeta(projectDir string) (storedPath string, ok bool) {
	data, err := os.ReadFile(filepath.Join(projectDir, projectMetaFileName))
	if err != nil {
		return "", false
	}
	var pm projectMeta
	if err := json.Unmarshal(data, &pm); err != nil {
		return "", false
	}
	return pm.Path, true
}

// shortHash 返回字符串的 SHA-256 前 8 位 hex（用于项目目录哈希兜底）。
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:4]) // 4 字节 = 8 hex 字符
}

// ProjectDir 返回会话根目录（sessionsRoot/projectName）。
// 供记忆层上下文压缩子系统（Step 7）在主流程装配时注入 ToolResultStore——
// 工具结果落盘到 <projectDir>/<sessionID>/tool_results/<toolUseID>，与本管理器
// 的会话目录约定对齐。本 getter 只读返回字符串，不暴露内部可变状态。
func (sm *SessionManager) ProjectDir() string {
	return sm.projectDir
}

// CreateNew 创建一个新的空会话（仅在内存中生成 UUID），不立即落盘。
// 首次追加消息时由 AppendMessages 惰性创建 session 目录与文件。
func (sm *SessionManager) CreateNew() *Session {
	now := time.Now()
	return &Session{
		ID:        generateUUID(),
		CreatedAt: now,
		UpdatedAt: now,
		Messages:  make([]llm.Message, 0),
	}
}

// CreateSession 在磁盘上初始化一个会话目录：写 meta.json + 创建空 messages.jsonl。
// 幂等：对已存在的目录重置 meta 为 0 条消息状态（不破坏已有 messages.jsonl 内容）。
// 供需要在追加前显式落盘元数据的场景使用（一般路径走 AppendMessages 的惰性创建）。
func (sm *SessionManager) CreateSession(session *Session) error {
	sessionDir := sm.sessionDirPath(session.ID)
	if err := sm.writeSessionMeta(sessionDir, &sessionMeta{
		ID:           session.ID,
		CreatedAt:    session.CreatedAt,
		UpdatedAt:    session.UpdatedAt,
		MessageCount: 0,
	}); err != nil {
		return err
	}
	// 确保空 messages.jsonl 存在（已存在则保留内容不动）
	msgFile := sm.messagesFilePath(session.ID)
	f, err := os.OpenFile(msgFile, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("创建消息文件失败: %w", err)
	}
	return f.Close()
}

// AppendMessages 把一批消息逐行追加到 messages.jsonl 末尾（append-only）。
//
// 语义：
//   - msgs 为空时直接返回（幂等，避免 OnLoopDone + defer 重复触发产生空写）。
//   - session 目录不存在时惰性创建（建目录 + meta + 空文件）。
//   - 每条消息 json.Marshal 成一行并追加 '\n'，单次 Write 写入保证整行原子性。
//   - 全部消息写入成功后更新 meta（updated_at = now, message_count += len(msgs)）。
//   - 先写消息、再写 meta，保证 meta 记录的 count 不超前于实际落盘的消息数。
func (sm *SessionManager) AppendMessages(sessionID string, msgs []llm.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	sessionDir := sm.sessionDirPath(sessionID)
	// 惰性创建 session 目录与 meta（首次追加消息时落盘）
	if _, err := os.Stat(sessionDir); os.IsNotExist(err) {
		now := time.Now()
		if err := sm.writeSessionMeta(sessionDir, &sessionMeta{
			ID:           sessionID,
			CreatedAt:    now,
			UpdatedAt:    now,
			MessageCount: 0,
		}); err != nil {
			return err
		}
	}
	// 以 O_APPEND 打开，逐条整行写入
	f, err := os.OpenFile(sm.messagesFilePath(sessionID), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("打开消息文件失败: %w", err)
	}
	defer f.Close()

	for _, msg := range msgs {
		line, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("序列化消息失败: %w", err)
		}
		line = append(line, '\n')
		if _, err := f.Write(line); err != nil {
			return fmt.Errorf("写入消息失败: %w", err)
		}
	}
	// 消息写入成功后更新 meta
	return sm.updateSessionMeta(sessionID, func(m *sessionMeta) {
		m.MessageCount += len(msgs)
		m.UpdatedAt = time.Now()
	})
}

// TruncateMessages 清空会话的全部消息（用于 /clear 场景，保留 session_id）。
// 语义：把 messages.jsonl 截断为 0 字节，并把 meta 的 message_count 重置为 0。
// session 目录不存在时惰性创建（等价于一个空会话）。
func (sm *SessionManager) TruncateMessages(sessionID string) error {
	sessionDir := sm.sessionDirPath(sessionID)
	if _, err := os.Stat(sessionDir); os.IsNotExist(err) {
		now := time.Now()
		if err := sm.writeSessionMeta(sessionDir, &sessionMeta{
			ID:           sessionID,
			CreatedAt:    now,
			UpdatedAt:    now,
			MessageCount: 0,
		}); err != nil {
			return err
		}
	}
	msgFile := sm.messagesFilePath(sessionID)
	if err := os.Truncate(msgFile, 0); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("清空消息文件失败: %w", err)
		}
		// 文件不存在：创建一个空文件兜底
		f, ferr := os.OpenFile(msgFile, os.O_CREATE|os.O_WRONLY, 0644)
		if ferr != nil {
			return fmt.Errorf("创建消息文件失败: %w", ferr)
		}
		_ = f.Close()
	}
	return sm.updateSessionMeta(sessionID, func(m *sessionMeta) {
		m.MessageCount = 0
		m.UpdatedAt = time.Now()
	})
}

// Load 从磁盘加载指定 ID 的会话，重建内存 Session。
// 逐行解析 messages.jsonl（跳过损坏行并记 Warn），以实际成功解析的行作为 Messages；
// meta.json 缺失时用零值兜底（不阻塞加载）。
func (sm *SessionManager) Load(sessionID string) (*Session, error) {
	sessionDir := sm.sessionDirPath(sessionID)
	info, err := os.Stat(sessionDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("会话文件不存在: %s", sessionID)
		}
		return nil, fmt.Errorf("读取会话目录失败: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("会话路径不是目录: %s", sessionID)
	}

	messages, err := sm.readMessagesFile(sm.messagesFilePath(sessionID))
	if err != nil {
		return nil, fmt.Errorf("会话文件损坏: %w", err)
	}

	// 读 meta（缺失用零值兜底）
	meta, _, _ := sm.readSessionMeta(sessionID)
	id := sessionID
	var created, updated time.Time
	if meta != nil {
		if meta.ID != "" {
			id = meta.ID
		}
		created = meta.CreatedAt
		updated = meta.UpdatedAt
	}

	return &Session{
		ID:        id,
		CreatedAt: created,
		UpdatedAt: updated,
		Messages:  messages,
	}, nil
}

// readMessagesFile 逐行解析 JSONL 消息文件。
// 损坏行（JSON 解析失败）记 Warn 跳过；文件不存在视为空消息；
// 单行超过 maxScanLineSize 触发 scanner 错误时降级返回已成功解析的消息（不丢失整段会话）。
func (sm *SessionManager) readMessagesFile(path string) ([]llm.Message, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []llm.Message{}, nil
		}
		return nil, err
	}
	defer f.Close()

	messages := make([]llm.Message, 0)
	scanner := bufio.NewScanner(f)
	// 扩容 token 上限到 1MB，防止大 tool_result 行被默认 64KB 上限截断误丢
	scanner.Buffer(make([]byte, 0, 64*1024), maxScanLineSize)

	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var msg llm.Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			logger.Warn("跳过损坏的消息行",
				zap.String("file", path),
				zap.Int("line", lineNo),
				zap.Error(err),
			)
			continue
		}
		messages = append(messages, msg)
	}
	if err := scanner.Err(); err != nil {
		// 降级：返回已成功解析的部分，避免单行超大导致整会话不可读
		logger.Warn("读取消息文件遇到错误，返回已解析的部分",
			zap.String("file", path), zap.Error(err))
	}
	return messages, nil
}

// LoadLatest 加载当前项目下最近更新的会话。
// 扫描项目目录下的子目录（每个子目录是一个 session），按 meta.updated_at 降序取最新。
// 项目目录不存在或无有效会话时返回 nil，无错误。
func (sm *SessionManager) LoadLatest() (*Session, error) {
	entries, err := os.ReadDir(sm.projectDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取会话目录失败: %w", err)
	}

	type candidate struct {
		id        string
		updatedAt time.Time
	}
	var cands []candidate
	for _, entry := range entries {
		if !entry.IsDir() {
			continue // 跳过 .project.json 等非目录文件
		}
		sid := entry.Name()
		meta, ok, _ := sm.readSessionMeta(sid)
		if !ok || meta == nil {
			continue
		}
		cands = append(cands, candidate{id: sid, updatedAt: meta.UpdatedAt})
	}

	if len(cands) == 0 {
		return nil, nil
	}
	sort.Slice(cands, func(i, j int) bool {
		return cands[i].updatedAt.After(cands[j].updatedAt)
	})
	return sm.Load(cands[0].id)
}

// ListSessions 返回当前项目下所有会话的摘要列表，按 UpdatedAt 降序排列。
// 对每个 session 子目录读 meta + 扫描 messages.jsonl 取数量与首条用户消息预览，
// 避免一次性把全部消息载入内存。损坏/缺 meta 的目录跳过并记录日志。
func (sm *SessionManager) ListSessions() ([]SessionSummary, error) {
	return sm.listSessionsSorted(0, false)
}

// ListRecentSessions 返回最近创建的 limit 个会话摘要，按 CreatedAt 降序。
// limit <= 0 时使用默认值 10。
// 适用于「最近会话」表格等只需展示少量最新会话的场景。
func (sm *SessionManager) ListRecentSessions(limit int) ([]SessionSummary, error) {
	if limit <= 0 {
		limit = 10
	}
	return sm.listSessionsSorted(limit, true)
}

// listSessionsSorted 是 ListSessions / ListRecentSessions 的内部实现。
// byCreatedAt=true 时按 CreatedAt 降序，否则按 UpdatedAt 降序；
// limit<=0 表示不限制数量。仅扫描项目目录下的子目录。
func (sm *SessionManager) listSessionsSorted(limit int, byCreatedAt bool) ([]SessionSummary, error) {
	entries, err := os.ReadDir(sm.projectDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []SessionSummary{}, nil
		}
		return nil, fmt.Errorf("读取会话目录失败: %w", err)
	}

	summaries := make([]SessionSummary, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sessionID := entry.Name()

		summary, err := sm.parseSessionSummary(sessionID)
		if err != nil {
			logger.Warn("跳过损坏的会话目录", zap.String("sessionID", sessionID), zap.Error(err))
			continue
		}
		summaries = append(summaries, *summary)
	}

	// 按指定字段降序排列
	if byCreatedAt {
		sort.Slice(summaries, func(i, j int) bool {
			return summaries[i].CreatedAt.After(summaries[j].CreatedAt)
		})
	} else {
		sort.Slice(summaries, func(i, j int) bool {
			return summaries[i].UpdatedAt.After(summaries[j].UpdatedAt)
		})
	}

	// 截取前 limit 条
	if limit > 0 && len(summaries) > limit {
		summaries = summaries[:limit]
	}

	return summaries, nil
}

// parseSessionSummary 轻量解析会话目录：读 meta 取元信息 + 扫描 messages.jsonl 取数量与预览。
// 避免完整反序列化所有消息，仅读 meta 与首条用户消息。
func (sm *SessionManager) parseSessionSummary(sessionID string) (*SessionSummary, error) {
	meta, ok, err := sm.readSessionMeta(sessionID)
	if err != nil {
		return nil, fmt.Errorf("读取会话元数据失败: %w", err)
	}
	if !ok || meta == nil {
		return nil, fmt.Errorf("会话元数据缺失: %s", sessionID)
	}

	messageCount, preview := sm.scanSummary(sm.messagesFilePath(sessionID))

	return &SessionSummary{
		ID:           meta.ID,
		CreatedAt:    meta.CreatedAt,
		UpdatedAt:    meta.UpdatedAt,
		MessageCount: messageCount,
		Preview:      preview,
	}, nil
}

// scanSummary 扫描 messages.jsonl 取消息总数与首条用户消息预览。
// 找到首条 user 预览后仅计数、不再解析内容，避免对大文件做无用解析。
func (sm *SessionManager) scanSummary(path string) (count int, preview string) {
	preview = "(空会话)"
	f, err := os.Open(path)
	if err != nil {
		return 0, preview
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxScanLineSize)

	previewFound := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		count++
		if previewFound {
			continue
		}
		var msg struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal([]byte(line), &msg) != nil {
			continue
		}
		if msg.Role != "user" {
			continue
		}
		// content 为 ContentBlock 数组，取第一个非空 text block 作为预览
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(msg.Content, &blocks) == nil {
			for _, b := range blocks {
				if b.Type == "text" && b.Text != "" {
					preview = truncateText(strings.TrimSpace(b.Text), 80)
					previewFound = true
					break
				}
			}
		}
	}
	return count, preview
}

// Delete 删除指定 ID 的会话目录（含 meta.json 与 messages.jsonl）。
// 目录不存在时返回明确错误。
func (sm *SessionManager) Delete(id string) error {
	sessionDir := sm.sessionDirPath(id)
	if _, err := os.Stat(sessionDir); os.IsNotExist(err) {
		return fmt.Errorf("会话文件不存在: %s", id)
	}
	if err := os.RemoveAll(sessionDir); err != nil {
		return fmt.Errorf("删除会话目录失败: %w", err)
	}
	return nil
}

// ---- 路径辅助 ----

// sessionDirPath 返回会话目录的完整路径（{projectDir}/{sessionID}）。
func (sm *SessionManager) sessionDirPath(sessionID string) string {
	return filepath.Join(sm.projectDir, sessionID)
}

// messagesFilePath 返回会话消息日志文件的完整路径。
func (sm *SessionManager) messagesFilePath(sessionID string) string {
	return filepath.Join(sm.sessionDirPath(sessionID), messagesFileName)
}

// metaFilePath 返回会话元数据文件的完整路径。
func (sm *SessionManager) metaFilePath(sessionID string) string {
	return filepath.Join(sm.sessionDirPath(sessionID), metaFileName)
}

// ---- meta 读写辅助 ----

// readSessionMeta 读取指定会话的 meta.json。
// 返回 (meta, ok, err)：文件缺失时 ok=false 且 err=nil；损坏时返回 err。
func (sm *SessionManager) readSessionMeta(sessionID string) (meta *sessionMeta, ok bool, err error) {
	data, err := os.ReadFile(sm.metaFilePath(sessionID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	var m sessionMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, false, err
	}
	return &m, true, nil
}

// writeSessionMeta 把 meta 写入指定会话目录（必要时创建目录）。
func (sm *SessionManager) writeSessionMeta(sessionDir string, m *sessionMeta) error {
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return fmt.Errorf("创建会话目录失败: %w", err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化会话元数据失败: %w", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, metaFileName), data, 0644); err != nil {
		return fmt.Errorf("写入会话元数据失败: %w", err)
	}
	return nil
}

// updateSessionMeta 读出当前 meta、应用 mutate 变更、再写回。
// meta 不存在时以零值兜底新建。单进程 streamState 已串行化，无需额外加锁。
func (sm *SessionManager) updateSessionMeta(sessionID string, mutate func(*sessionMeta)) error {
	meta, ok, err := sm.readSessionMeta(sessionID)
	if err != nil {
		return fmt.Errorf("读取会话元数据失败: %w", err)
	}
	if !ok || meta == nil {
		now := time.Now()
		meta = &sessionMeta{ID: sessionID, CreatedAt: now, UpdatedAt: now}
	}
	mutate(meta)
	return sm.writeSessionMeta(sm.sessionDirPath(sessionID), meta)
}

// truncateText 截断文本到指定长度，超出部分用省略号替代。
func truncateText(text string, maxLen int) string {
	// 统计 rune 数量而非字节
	runes := []rune(text)
	if len(runes) <= maxLen {
		return text
	}
	return string(runes[:maxLen-3]) + "..."
}

// generateUUID 生成一个 UUID v4。
// 使用 crypto/rand 生成密码学安全的随机字节，格式为 xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx。
func generateUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 失败时回退到基于时间戳的生成
		now := time.Now().UnixNano()
		for i := 0; i < 16; i++ {
			b[i] = byte(now >> (i * 4))
		}
	}
	// 设置版本号 (4) 和变体位
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
