// dump.go 实现 `/dump` 斜杠命令的会话快照导出能力。
//
// 职责：把「当前会话内存中的完整历史上下文 + System Prompt 快照」格式化为
// 两份文件并原子写入会话目录：
//   - dump.json：结构化 JSON，供程序/工具消费（可直接被 json.Unmarshal 还原）
//   - dump.md  ：人类可读 Markdown，供人工阅读与排查
//
// [为什么归属 web 包] dump 是交互层（Web）触发的一次性只读导出，与
// file_diff_store.go 同级定位——格式化与写盘逻辑内聚在一个文件，handler
// 仅负责取数据（sp / 历史副本 / 会话目录）并调用本模块。零侵入下层包，
// 不修改 llm / memory / engine。
//
// [并发一致性] 本模块的所有函数都是纯函数（基于入参生成字节串/写文件，
// 不持有可变共享状态），真正的「快照一致性」由 handler 侧的 streamState
// 抢占保证——handleDump 在 tryAcquire 成功后才调用本模块，此时无并发
// runStream 改写 history，传入的 messages 是稳定副本。

package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MeiCorl/CodePilot/src/llm"
)

// dumpFileJSON / dumpFileMD 为写盘的固定文件名。
// 采用固定名而非时间戳：每次导出覆盖最新快照，路径稳定可记忆，与会话目录
// 下 codepilot.log / tool_results/ 的「按会话单实例」归档风格一致。
const (
	dumpFileJSON = "dump.json"
	dumpFileMD   = "dump.md"
)

// dumpSessionMeta 是 dump.json 顶层「session」字段的承载结构，描述会话元信息。
// 仅取与归档/排查相关的三个字段，不暴露 Session 内部完整结构。
type dumpSessionMeta struct {
	// ID 为会话唯一标识（UUID 格式）。
	ID string `json:"id"`
	// CreatedAt 为会话创建时间。
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt 为会话最后更新时间。
	UpdatedAt time.Time `json:"updated_at"`
}

// dumpSystemBlock 对应 System Prompt 进入 system 字段的一段内容。
// 与 llm.SystemBlock 同构；独立定义以避免向前端协议层强加 llm 包的细节语义。
type dumpSystemBlock struct {
	// Text 为该段 system 内容原文。
	Text string `json:"text"`
	// Cacheable 表示该段是否被 Anthropic 协议层标记为 cache 命中区。
	Cacheable bool `json:"cacheable"`
}

// dumpSystemPrompt 是 dump.json 中「system_prompt」字段的承载结构，
// 完整描述当前会话组装出的 System Prompt 快照。
type dumpSystemPrompt struct {
	// SystemBlocks 为进入 LLM 请求 system 字段的各段内容，顺序与 Source 注册顺序一致。
	SystemBlocks []dumpSystemBlock `json:"system_blocks"`
	// LeadUserMessage 为合并后的首条 user 消息（通常含 AGENTS.md 合并结果 + 自动记忆）。
	LeadUserMessage string `json:"lead_user_message,omitempty"`
	// Stats 记录每个 Source 贡献的 token 数。
	Stats []SPSourceStat `json:"stats,omitempty"`
	// TotalTokens 为所有 Source 产出 token 的累加值。
	TotalTokens int `json:"total_tokens"`
}

// sessionDump 是 dump.json 的完整顶层结构。
// 字段顺序即 JSON 输出顺序：导出时间 → 会话元信息 → System Prompt → 对话历史。
type sessionDump struct {
	// ExportedAt 为导出动作发生的时间（由 handler 传入，避免格式化层自行取时间）。
	ExportedAt time.Time `json:"exported_at"`
	// Session 为会话元信息。
	Session dumpSessionMeta `json:"session"`
	// SystemPrompt 为当前会话的 System Prompt 快照。
	SystemPrompt dumpSystemPrompt `json:"system_prompt"`
	// Messages 为当前会话内存中的完整历史消息（不含 leadUserMessage，因后者已在
	// SystemPrompt 段单独导出，避免重复）。直接复用 llm.Message 的 MarshalJSON，
	// 自带 type 鉴别字段，反序列化时能正确还原 ContentBlock 具体类型。
	Messages []llm.Message `json:"messages"`
}

// buildSessionDump 把分散的数据源组装成一份 sessionDump。
//
// 入参：
//   - sessionID/CreatedAt/UpdatedAt：当前会话元信息（来自 h.current）
//   - sp：System Prompt 快照（来自 h.sp，调用方在持 h.mu 临界区内复制）
//   - messages：历史副本（来自 h.conv.AllMessages()，已是独立副本）
//   - now：导出时间戳（由 handler 传入）
//
// 返回的 sessionDump 可分别喂给 buildDumpJSON / buildDumpMarkdown。
func buildSessionDump(
	sessionID string,
	createdAt, updatedAt time.Time,
	sp llm.SystemPrompt,
	messages []llm.Message,
	now time.Time,
) sessionDump {
	// SystemBlocks 从 llm.SystemPrompt 转为本模块独立类型（剥离 Cacheable 之外的隐式语义）。
	blocks := make([]dumpSystemBlock, 0, len(sp.SystemBlocks))
	for _, b := range sp.SystemBlocks {
		blocks = append(blocks, dumpSystemBlock{Text: b.Text, Cacheable: b.Cacheable})
	}
	// Stats 同构转换，复用 protocol.go 已有的 SPSourceStat（与 DevExportSPPayload 一致）。
	stats := make([]SPSourceStat, 0, len(sp.Stats))
	for _, s := range sp.Stats {
		stats = append(stats, SPSourceStat{Name: s.Name, Tokens: s.Tokens})
	}
	return sessionDump{
		ExportedAt: now,
		Session: dumpSessionMeta{
			ID:        sessionID,
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
		},
		SystemPrompt: dumpSystemPrompt{
			SystemBlocks:    blocks,
			LeadUserMessage: sp.LeadUserMessage,
			Stats:           stats,
			TotalTokens:     sp.TotalTokens,
		},
		Messages: messages,
	}
}

// buildDumpJSON 把 sessionDump 序列化为带缩进的 JSON 字节串。
// 使用 MarshalIndent 保证人工可读（dump.json 也常被人直接打开排查）。
func buildDumpJSON(sd sessionDump) ([]byte, error) {
	return json.MarshalIndent(sd, "", "  ")
}

// buildDumpMarkdown 把 sessionDump 渲染为人类可读的 Markdown 字符串。
//
// 布局：头部元信息 → System Prompt 段（各 block + lead + stats 表格）→ 对话历史段。
// ContentBlock 按类型分别渲染（而非统一调 ToText），以便在 Markdown 里清晰区分
// 文本 / 工具调用 / 工具结果，并把 tool_use 的 input 以围栏代码块格式化输出。
func buildDumpMarkdown(sd sessionDump) string {
	var b strings.Builder

	// ---- 头部：标题 + 导出时间 + 会话元信息 ----
	fmt.Fprintf(&b, "# CodePilot 会话导出\n\n")
	fmt.Fprintf(&b, "- 导出时间：%s\n", sd.ExportedAt.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "- 会话 ID：%s\n", sd.Session.ID)
	fmt.Fprintf(&b, "- 创建时间：%s\n", sd.Session.CreatedAt.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "- 更新时间：%s\n", sd.Session.UpdatedAt.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "- 历史消息数：%d\n\n", len(sd.Messages))

	// ---- System Prompt 段 ----
	b.WriteString("## System Prompt\n\n")
	fmt.Fprintf(&b, "- 总 token 估算：%d\n", sd.SystemPrompt.TotalTokens)
	b.WriteString("\n")
	if len(sd.SystemPrompt.SystemBlocks) > 0 {
		for i, blk := range sd.SystemPrompt.SystemBlocks {
			cacheMark := ""
			if blk.Cacheable {
				// 标注可缓存段，便于排查 Anthropic prompt cache 切片是否符合预期。
				cacheMark = " *(cacheable)*"
			}
			fmt.Fprintf(&b, "### System Block #%d%s\n\n", i+1, cacheMark)
			b.WriteString(blk.Text)
			b.WriteString("\n\n")
		}
	} else {
		b.WriteString("> （无 system 段内容）\n\n")
	}
	if sd.SystemPrompt.LeadUserMessage != "" {
		b.WriteString("### Lead User Message\n\n")
		b.WriteString(sd.SystemPrompt.LeadUserMessage)
		b.WriteString("\n\n")
	}
	if len(sd.SystemPrompt.Stats) > 0 {
		b.WriteString("### Source Stats\n\n")
		b.WriteString("| Source | Tokens |\n")
		b.WriteString("| --- | --- |\n")
		for _, s := range sd.SystemPrompt.Stats {
			fmt.Fprintf(&b, "| %s | %d |\n", s.Name, s.Tokens)
		}
		b.WriteString("\n")
	}

	// ---- 对话历史段 ----
	b.WriteString("## 对话历史\n\n")
	if len(sd.Messages) == 0 {
		b.WriteString("> （空会话，无历史消息）\n")
		return b.String()
	}
	for i, msg := range sd.Messages {
		fmt.Fprintf(&b, "### [%d] %s\n\n", i+1, string(msg.Role))
		if len(msg.Content) == 0 {
			b.WriteString("> （空内容）\n\n")
			continue
		}
		renderContentBlocksMD(&b, msg.Content)
		b.WriteString("\n")
	}
	return b.String()
}

// renderContentBlocksMD 把一条消息的所有 ContentBlock 渲染进 Markdown 写入器。
// 按类型分支：文本 → 原文；工具调用 → 名称+ID+input 围栏；工具结果 → ID+内容（错误标注）。
func renderContentBlocksMD(b *strings.Builder, blocks []llm.ContentBlock) {
	for _, block := range blocks {
		switch cb := block.(type) {
		case *llm.TextBlock:
			b.WriteString(cb.Text)
			b.WriteString("\n\n")
		case *llm.ToolUseBlock:
			fmt.Fprintf(b, "**tool_use** `%s` (id=%s)\n\n", cb.Name, cb.ID)
			// input 已是 json.RawMessage，尝试格式化美化输出；失败则原样输出原始字节。
			inputStr := prettyJSONRaw(cb.Input)
			b.WriteString("```json\n")
			b.WriteString(inputStr)
			b.WriteString("\n```\n\n")
		case *llm.ToolResultBlock:
			errMark := ""
			if cb.IsError {
				errMark = " [error]"
			}
			fmt.Fprintf(b, "**tool_result**%s (tool_use_id=%s)\n\n", errMark, cb.ToolUseID)
			b.WriteString("```\n")
			b.WriteString(cb.Content)
			b.WriteString("\n```\n\n")
		default:
			// 兜底：未知类型用其 ToText 文本表示，保证不丢内容。
			fmt.Fprintf(b, "%s\n\n", block.ToText())
		}
	}
}

// prettyJSONRaw 把 json.RawMessage 美化为带缩进的字符串；无法解析时回退原样输出。
// 工具调用 input 通常是合法 JSON 对象，美化后更易在 Markdown 中阅读。
func prettyJSONRaw(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	// json.Indent 要求 *bytes.Buffer 入参，用其做纯格式化（不解析到具体类型，保留原始数值/结构）。
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		// 格式化失败（理论上 input 应为合法 JSON）：退化为去掉首尾空白的原样文本。
		return strings.TrimSpace(string(raw))
	}
	return buf.String()
}

// writeDumpFiles 把 sessionDump 渲染为两份文件并原子写入 dir 目录。
//
// [原子写] 每份文件都先写 `<name>.tmp` 再 os.Rename 到目标名——避免崩溃/并发
// 场景下出现半成品文件被其它读者读到。os.Rename 在同一目录内是原子的
// （Windows 同卷同样保证），覆盖既有文件也安全（固定名每次覆盖的语义由此实现）。
//
// 返回写入的 JSON / MD 绝对路径与可能的错误。dir 不存在时返回错误（调用方
// 传入的是会话目录，正常情况下 AppendMessages 首次追加时已惰性创建）。
func writeDumpFiles(dir string, sd sessionDump) (jsonPath, mdPath string, err error) {
	jsonBytes, err := buildDumpJSON(sd)
	if err != nil {
		return "", "", fmt.Errorf("渲染 dump.json 失败: %w", err)
	}
	mdStr := buildDumpMarkdown(sd)

	jsonPath = filepath.Join(dir, dumpFileJSON)
	mdPath = filepath.Join(dir, dumpFileMD)

	if err := atomicWriteText(jsonPath, jsonBytes); err != nil {
		return "", "", fmt.Errorf("写入 dump.json 失败: %w", err)
	}
	if err := atomicWriteText(mdPath, []byte(mdStr)); err != nil {
		return "", "", fmt.Errorf("写入 dump.md 失败: %w", err)
	}
	return jsonPath, mdPath, nil
}

// atomicWriteText 以「写临时文件 + rename」方式原子覆盖目标文件。
// data 为最终要落盘的字节内容（UTF-8 文本，文件权限 0644，与会话 messages.jsonl 一致）。
func atomicWriteText(path string, data []byte) error {
	tmp := path + ".tmp"
	// 0644：owner 可读写、其余只读，与会话目录内其它归档文件权限对齐。
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
