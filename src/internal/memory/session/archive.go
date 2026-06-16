// archive.go 提供会话历史的「原文归档」与「活跃历史重写」能力，专供第二层摘要压缩使用。
//
// 背景：第二层摘要压缩会把较早的原文历史摘要化，内存与 messages.jsonl 中的活跃视图
// 替换为「摘要 + 近期原文」。这带来两个持久化需求，本文件各提供一个方法：
//
//   - ArchiveMessages：把【被压缩掉的早期原文】追加写入 history_archive.jsonl，作为
//     可追溯的原文备份（摘要不可逆，原文一旦从活跃视图移除就只能在归档里找回）。
//   - RewriteActiveMessages：把【压缩后的活跃历史】（摘要 + 近期原文）全量覆盖写到
//     messages.jsonl，使持久化与内存一致——恢复会话时加载到的就是「摘要 + 近期原文」，
//     不会对同一段历史重复摘要。
//
// 【对 append-only 模型的破坏与约束】
// session 包的整体持久化模型是 append-only（messages.jsonl 只追加不重写，见 session.go
// 包注释）。RewriteActiveMessages 是【唯一打破】该模型的路径——它对 messages.jsonl 做
// 全量覆盖写。这是一次受控的低频重组事件：
//   - 仅由第二层摘要压缩调用（频率极低，仅在历史逼近窗口上限时）；
//   - 采用「写临时文件 + rename 覆盖」的原子写法，避免写到一半崩溃导致 jsonl 损坏
//     （高可用：崩溃时要么是旧版完整 jsonl、要么是新版完整 jsonl，不会是半截损坏文件）；
//   - 其余所有路径（正常对话追加、/clear 截断）仍走 append-only，不受影响。
//
// ArchiveMessages 仍是 append-only（与 messages.jsonl 同格式逐行 JSON 追加），
// 不破坏既有模型。

package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/MeiCorl/CodePilot/src/llm"
)

// historyArchiveFileName 为被压缩掉的早期原文归档文件名（append-only JSONL）。
// 位于 {session_id} 目录下，与 messages.jsonl 同级。
const historyArchiveFileName = "history_archive.jsonl"

// archiveFilePath 返回会话历史归档文件的完整路径（{projectDir}/{sessionID}/history_archive.jsonl）。
func (sm *SessionManager) archiveFilePath(sessionID string) string {
	return filepath.Join(sm.sessionDirPath(sessionID), historyArchiveFileName)
}

// ArchiveMessages 把一批【被压缩掉的早期原文】消息追加写入 history_archive.jsonl。
//
// 语义：
//   - append-only：以 O_APPEND 打开（不存在则创建），逐条整行 JSON 追加，不覆盖已有归档。
//     多次调用（如摘要失败重试）会产生重复副本——归档是「原文备份」，重复副本不影响
//     正确性（仅用于追溯，不参与 LLM 上下文）。
//   - msgs 为空时直接返回（幂等，避免空写）。
//   - 会话目录不存在时惰性创建（与 AppendMessages 一致）。
//   - 序列化复用 llm.Message 的 MarshalJSON（带 type 鉴别字段），与 messages.jsonl
//     完全同构，便于后续用同一解析逻辑还原。
func (sm *SessionManager) ArchiveMessages(sessionID string, msgs []llm.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	sessionDir := sm.sessionDirPath(sessionID)
	// 惰性创建会话目录（压缩可能发生在会话目录仅由内存存在、尚未完整落盘的边界场景）。
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return fmt.Errorf("创建会话目录失败: %w", err)
	}
	f, err := os.OpenFile(sm.archiveFilePath(sessionID), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("打开归档文件失败: %w", err)
	}
	defer f.Close()

	for _, msg := range msgs {
		line, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("序列化归档消息失败: %w", err)
		}
		line = append(line, '\n')
		if _, err := f.Write(line); err != nil {
			return fmt.Errorf("写入归档失败: %w", err)
		}
	}
	return nil
}

// RewriteActiveMessages 把【压缩后的活跃历史】（摘要 + 近期原文）全量覆盖写到 messages.jsonl。
//
// 这是 session 包中【唯一非 append-only】的写入路径，仅由第二层摘要压缩调用（低频）。
// 用途：压缩后让持久化与内存一致，使得会话恢复时加载到的就是「摘要 + 近期原文」，
// 不会对同一段已摘要化的历史重复摘要。
//
// 原子写法（高可用）：
//  1. 先把全部消息逐行写入临时文件 <messages.jsonl>.tmp；
//  2. 全部写入成功后 os.Rename 覆盖 messages.jsonl。
//
// 这样任意时刻崩溃，磁盘上的 messages.jsonl 要么是完整的旧版、要么是完整的新版，
// 不会出现写到一半的损坏文件（半截 JSON 行会让整个会话加载失败）。写入过程任一步失败
// 都会清理临时文件并返回 err，不影响原 messages.jsonl。
//
// 写入成功后同步更新 meta 的 message_count 为新历史长度（CreatedAt 保留）。
func (sm *SessionManager) RewriteActiveMessages(sessionID string, msgs []llm.Message) error {
	sessionDir := sm.sessionDirPath(sessionID)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return fmt.Errorf("创建会话目录失败: %w", err)
	}

	msgFile := sm.messagesFilePath(sessionID)
	tmpFile := msgFile + ".tmp"

	// 清理可能残留的临时文件，避免被旧内容污染。
	_ = os.Remove(tmpFile)

	f, err := os.OpenFile(tmpFile, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("打开临时文件失败: %w", err)
	}

	// 写入失败统一清理临时文件后返回，保证不留半成品。
	cleanupAndClose := func(writeErr error) error {
		_ = f.Close()
		_ = os.Remove(tmpFile)
		return writeErr
	}

	for _, msg := range msgs {
		line, err := json.Marshal(msg)
		if err != nil {
			return cleanupAndClose(fmt.Errorf("序列化活跃消息失败: %w", err))
		}
		line = append(line, '\n')
		if _, err := f.Write(line); err != nil {
			return cleanupAndClose(fmt.Errorf("写入临时文件失败: %w", err))
		}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}

	// 原子覆盖：临时文件 → messages.jsonl。
	// Windows 上 os.Rename 通过 MoveFileEx(MOVEFILE_REPLACE_EXISTING) 实现覆盖；
	// POSIX 上 rename 原子替换。单进程串行写入，不存在并发占用冲突。
	if err := os.Rename(tmpFile, msgFile); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("覆盖 messages.jsonl 失败: %w", err)
	}

	// 同步 meta：message_count 反映新历史长度，CreatedAt 保留（updateSessionMeta 读改写）。
	return sm.updateSessionMeta(sessionID, func(m *sessionMeta) {
		m.MessageCount = len(msgs)
		m.UpdatedAt = time.Now()
	})
}
