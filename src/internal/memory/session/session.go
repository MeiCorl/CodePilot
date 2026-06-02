// Package session 实现会话的本地持久化管理。
// 会话数据以 JSON 文件存储在 ~/.codepilot/sessions/ 目录下，
// 支持会话的创建、保存、加载和自动恢复。
package session

import (
	"crypto/rand"
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

// Session 代表一次完整的对话会话。
// 包含会话元信息和所有对话消息，支持序列化到 JSON 文件。
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

// SessionManager 管理会话的持久化存储。
// 会话文件存储在 ~/.codepilot/sessions/ 目录下，
// 每个会话对应一个 {session_id}.json 文件。
type SessionManager struct {
	// sessionsDir 为会话文件存储目录
	sessionsDir string
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

// NewSessionManager 创建会话管理器。
// 自动创建会话存储目录（如不存在）。
func NewSessionManager() (*SessionManager, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("获取用户主目录失败: %w", err)
	}
	sessionsDir := filepath.Join(homeDir, ".codepilot", "sessions")

	// 目录不存在时自动创建
	if err := os.MkdirAll(sessionsDir, 0755); err != nil {
		return nil, fmt.Errorf("创建会话目录失败: %w", err)
	}

	return &SessionManager{sessionsDir: sessionsDir}, nil
}

// NewSessionManagerWithDir 使用指定目录创建会话管理器，供测试与自定义部署使用。
// 目录不存在时会自动创建。
func NewSessionManagerWithDir(dir string) (*SessionManager, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("创建会话目录失败: %w", err)
	}
	return &SessionManager{sessionsDir: dir}, nil
}

// CreateNew 创建一个新的空会话，生成 UUID 作为标识。
func (sm *SessionManager) CreateNew() *Session {
	now := time.Now()
	return &Session{
		ID:        generateUUID(),
		CreatedAt: now,
		UpdatedAt: now,
		Messages:  make([]llm.Message, 0),
	}
}

// Save 将会话序列化为 JSON 并写入文件。
// 每次保存时更新 UpdatedAt 时间戳。
func (sm *SessionManager) Save(session *Session) error {
	session.UpdatedAt = time.Now()

	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化会话失败: %w", err)
	}

	filePath := sm.sessionFilePath(session.ID)
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return fmt.Errorf("写入会话文件失败: %w", err)
	}

	return nil
}

// Load 从文件加载指定 ID 的会话。
// 文件损坏时返回错误，调用方可据此创建新会话。
func (sm *SessionManager) Load(sessionID string) (*Session, error) {
	filePath := sm.sessionFilePath(sessionID)
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("会话文件不存在: %s", sessionID)
		}
		return nil, fmt.Errorf("读取会话文件失败: %w", err)
	}

	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("会话文件损坏（JSON 解析失败）: %w", err)
	}

	return &session, nil
}

// LoadLatest 加载最近更新的会话。
// 遍历 sessions 目录下所有 JSON 文件，按 UpdatedAt 降序排列，
// 返回最近更新的会话。目录为空时返回 nil，无错误。
func (sm *SessionManager) LoadLatest() (*Session, error) {
	entries, err := os.ReadDir(sm.sessionsDir)
	if err != nil {
		return nil, fmt.Errorf("读取会话目录失败: %w", err)
	}

	// 收集所有有效的会话文件
	var sessions []*Session
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		sessionID := entry.Name()[:len(entry.Name())-len(".json")]
		session, err := sm.Load(sessionID)
		if err != nil {
			// 损坏的会话文件跳过，不阻塞加载
			continue
		}
		sessions = append(sessions, session)
	}

	if len(sessions) == 0 {
		return nil, nil
	}

	// 按 UpdatedAt 降序排列，取最新的
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})

	return sessions[0], nil
}

// ListSessions 返回所有会话的摘要列表，按 UpdatedAt 降序排列。
// 对每个会话文件做轻量解析，仅读取元信息和首条用户消息预览，
// 避免反序列化完整消息列表。损坏的文件跳过并记录日志。
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
// limit<=0 表示不限制数量。
func (sm *SessionManager) listSessionsSorted(limit int, byCreatedAt bool) ([]SessionSummary, error) {
	entries, err := os.ReadDir(sm.sessionsDir)
	if err != nil {
		return nil, fmt.Errorf("读取会话目录失败: %w", err)
	}

	var summaries []SessionSummary
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		sessionID := entry.Name()[:len(entry.Name())-len(".json")]

		summary, err := sm.parseSessionSummary(sessionID)
		if err != nil {
			// 损坏的会话文件跳过，记录日志
			logger.Warn("跳过损坏的会话文件", zap.String("sessionID", sessionID), zap.Error(err))
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

// Delete 删除指定 ID 的会话文件。
// 文件不存在时返回明确错误。
func (sm *SessionManager) Delete(id string) error {
	filePath := sm.sessionFilePath(id)
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return fmt.Errorf("会话文件不存在: %s", id)
	}
	if err := os.Remove(filePath); err != nil {
		return fmt.Errorf("删除会话文件失败: %w", err)
	}
	return nil
}

// parseSessionSummary 轻量解析会话文件，仅提取摘要信息。
// 避免完整反序列化所有消息，仅读取元字段和首条用户消息。
func (sm *SessionManager) parseSessionSummary(sessionID string) (*SessionSummary, error) {
	filePath := sm.sessionFilePath(sessionID)
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("读取会话文件失败: %w", err)
	}

	// 使用轻量解析：仅提取顶层字段和 messages 数组的第一条用户消息
	var raw struct {
		ID        string          `json:"id"`
		CreatedAt time.Time       `json:"created_at"`
		UpdatedAt time.Time       `json:"updated_at"`
		Messages  json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("会话文件损坏: %w", err)
	}

	// 解析消息数组以获取数量和预览
	var messages []json.RawMessage
	_ = json.Unmarshal(raw.Messages, &messages)

	messageCount := len(messages)
	preview := "(空会话)"

	// 查找首条用户消息作为预览
	for _, rawMsg := range messages {
		var msg struct {
			Role    string `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(rawMsg, &msg); err != nil {
			continue
		}
		if msg.Role == "user" {
			// content 可能是 ContentBlock 数组格式
			var blocks []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if err := json.Unmarshal(msg.Content, &blocks); err == nil && len(blocks) > 0 {
				// ContentBlock 数组格式，取第一个 text block
				for _, b := range blocks {
					if b.Type == "text" && b.Text != "" {
						preview = truncateText(strings.TrimSpace(b.Text), 80)
						break
					}
				}
			}
			break
		}
	}

	return &SessionSummary{
		ID:           raw.ID,
		CreatedAt:    raw.CreatedAt,
		UpdatedAt:    raw.UpdatedAt,
		MessageCount: messageCount,
		Preview:      preview,
	}, nil
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

// sessionFilePath 返回会话文件的完整路径。
func (sm *SessionManager) sessionFilePath(sessionID string) string {
	return filepath.Join(sm.sessionsDir, sessionID+".json")
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
