package llm

import (
	"encoding/json"
	"fmt"
)

// contentBlockJSON 是 ContentBlock 的 JSON 序列化中间表示。
// 通过 Type 字段鉴别具体类型，实现接口类型的正确序列化/反序列化。
// 后续扩展 ImageBlock 等新类型时，在此结构体中添加对应字段并更新 Marshal/Unmarshal switch。
type contentBlockJSON struct {
	// Type 为内容块类型鉴别字段
	Type ContentBlockType `json:"type"`
	// Text 为文本内容（仅 ContentBlockTypeText 时使用）
	Text string `json:"text,omitempty"`
	// ID 为工具调用/工具结果的关联 ID
	ID string `json:"id,omitempty"`
	// Name 为被调用的工具名（仅 tool_use 时使用）
	Name string `json:"name,omitempty"`
	// Input 为工具调用参数（仅 tool_use 时使用）
	Input json.RawMessage `json:"input,omitempty"`
	// ToolUseID 关联到对应的 ToolUseBlock.ID（仅 tool_result 时使用）
	ToolUseID string `json:"tool_use_id,omitempty"`
	// IsError 标识工具结果是否为错误（仅 tool_result 时使用）
	// 不使用 omitempty：spec 要求 is_error 字段始终输出，便于下游消费方统一解析
	IsError bool `json:"is_error"`
	// Content 为工具结果文本（仅 tool_result 时使用）
	Content string `json:"content,omitempty"`
}

// MarshalJSON 实现 json.Marshaler 接口。
// 将 ContentBlock 接口数组序列化为带类型鉴别字段的 JSON 数组，
// 确保反序列化时能正确还原具体类型。
func (m Message) MarshalJSON() ([]byte, error) {
	type jsonMsg struct {
		Role    string             `json:"role"`
		Content []contentBlockJSON `json:"content"`
	}
	jm := jsonMsg{
		Role:    string(m.Role),
		Content: make([]contentBlockJSON, len(m.Content)),
	}
	for i, block := range m.Content {
		switch b := block.(type) {
		case *TextBlock:
			jm.Content[i] = contentBlockJSON{
				Type: ContentBlockTypeText,
				Text: b.Text,
			}
		case *ToolUseBlock:
			jm.Content[i] = contentBlockJSON{
				Type:  ContentBlockTypeToolUse,
				ID:    b.ID,
				Name:  b.Name,
				Input: b.Input,
			}
		case *ToolResultBlock:
			jm.Content[i] = contentBlockJSON{
				Type:      ContentBlockTypeToolResult,
				ToolUseID: b.ToolUseID,
				Content:   b.Content,
				IsError:   b.IsError,
			}
		default:
			jm.Content[i] = contentBlockJSON{Type: block.Type()}
		}
	}
	return json.Marshal(jm)
}

// UnmarshalJSON 实现 json.Unmarshaler 接口。
// 读取 type 鉴别字段后，创建对应的具体 ContentBlock 实例。
func (m *Message) UnmarshalJSON(data []byte) error {
	type jsonMsg struct {
		Role    string             `json:"role"`
		Content []contentBlockJSON `json:"content"`
	}
	var jm jsonMsg
	if err := json.Unmarshal(data, &jm); err != nil {
		return err
	}
	m.Role = Role(jm.Role)
	m.Content = make([]ContentBlock, len(jm.Content))
	for i, cb := range jm.Content {
		switch cb.Type {
		case ContentBlockTypeText:
			m.Content[i] = &TextBlock{Text: cb.Text}
		case ContentBlockTypeToolUse:
			m.Content[i] = &ToolUseBlock{
				ID:    cb.ID,
				Name:  cb.Name,
				Input: cb.Input,
			}
		case ContentBlockTypeToolResult:
			m.Content[i] = &ToolResultBlock{
				ToolUseID: cb.ToolUseID,
				Content:   cb.Content,
				IsError:   cb.IsError,
			}
		default:
			return fmt.Errorf("不支持的内容块类型: %s", cb.Type)
		}
	}
	return nil
}
