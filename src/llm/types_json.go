package llm

import (
	"encoding/json"
	"fmt"
)

// contentBlockJSON 是 ContentBlock 的 JSON 序列化中间表示。
// 通过 Type 字段鉴别具体类型，实现接口类型的正确序列化/反序列化。
// 后续扩展 ImageBlock、ToolUseBlock 时，在此结构体中添加对应字段即可。
type contentBlockJSON struct {
	// Type 为内容块类型鉴别字段
	Type ContentBlockType `json:"type"`
	// Text 为文本内容（仅 ContentBlockTypeText 时使用）
	Text string `json:"text,omitempty"`
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
		switch block.Type() {
		case ContentBlockTypeText:
			jm.Content[i] = contentBlockJSON{
				Type: ContentBlockTypeText,
				Text: block.ToText(),
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
		default:
			return fmt.Errorf("不支持的内容块类型: %s", cb.Type)
		}
	}
	return nil
}
