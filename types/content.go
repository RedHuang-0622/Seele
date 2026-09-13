package types

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// ─────────────────────────────────────────────
// 多模态内容：随消息下发的图片
// ─────────────────────────────────────────────

// ImagePart 是一条消息随附的一张图片。
//
// 与 Message.Content 的关系是刻意的单向：Content 始终是**文本投影**——历史、
// 上下文压缩、token 估算都只读它，因此不改变既有语义；图片本身放在这里，
// 由 Message 的 wire 投影（MarshalJSON）和各 Provider 策略翻译成对方要求的
// content parts 形态（OpenAI 的 image_url / Anthropic 的 source）。
//
// 字节只活在本地：Data 是原始字节（不含 base64），宽度与高度是限流按 tile
// 计价所需的元数据，两者都不直接出现在 wire 上。
type ImagePart struct {
	// MimeType 是图片内容类型，例如 image/png。
	MimeType string `json:"mime_type,omitempty"`
	// Data 是图片原始字节；为空时按 URL 处理。
	Data []byte `json:"data,omitempty"`
	// URL 是远端图片地址；Data 非空时优先使用字节。
	URL string `json:"url,omitempty"`
	// Width/Height 是像素尺寸，供限流计价与展示；不随 wire 传输。
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
	// Detail 是可选清晰度偏好（low|high|auto），原样透传给 provider。
	Detail string `json:"detail,omitempty"`
	// Name 是溯源名（如截图文件名），仅本地可见。
	Name string `json:"name,omitempty"`
}

// Base64 返回图片字节的标准 base64 编码（不含 data URL 前缀）。
func (p ImagePart) Base64() string { return base64.StdEncoding.EncodeToString(p.Data) }

// DataURL 返回可直接放进 OpenAI image_url 的 data URL；只有远端地址时返回该地址。
func (p ImagePart) DataURL() string {
	if len(p.Data) == 0 {
		return p.URL
	}
	mimeType := p.MimeType
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return "data:" + mimeType + ";base64," + p.Base64()
}

// wireContentPart 是 OpenAI 形态的 content part（Anthropic 策略单独投影）。
type wireContentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *wireImageURL `json:"image_url,omitempty"`
}

type wireImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// messageWire 是 Message 的 wire 形态：content 既可能是字符串（纯文本消息，
// 与历史行为完全一致），也可能是 content parts 数组（随图片的消息）。
type messageWire struct {
	Role             string          `json:"role"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	Content          json.RawMessage `json:"content,omitempty"`
	ToolCalls        []ToolCall      `json:"tool_calls,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
	Name             string          `json:"name,omitempty"`
}

// MarshalJSON 让纯文本消息的 JSON 与历史**逐字节一致**（`"content":"…"`，
// nil 时省略），只有携带图片的消息才展开为 content parts 数组。这样既有
// Provider 适配、缓存与测试都不受影响，多模态只在真正需要时改变 wire 形状。
func (m Message) MarshalJSON() ([]byte, error) {
	content, err := m.marshalContent()
	if err != nil {
		return nil, err
	}
	return json.Marshal(messageWire{
		Role:             m.Role,
		ReasoningContent: m.ReasoningContent,
		Content:          content,
		ToolCalls:        m.ToolCalls,
		ToolCallID:       m.ToolCallID,
		Name:             m.Name,
	})
}

// marshalContent 产出 content 字段：nil（省略）/ 字符串 / content parts 数组。
func (m Message) marshalContent() (json.RawMessage, error) {
	if len(m.Images) == 0 {
		if m.Content == nil {
			return nil, nil
		}
		return json.Marshal(*m.Content)
	}
	parts := make([]wireContentPart, 0, len(m.Images)+1)
	if m.Content != nil && *m.Content != "" {
		parts = append(parts, wireContentPart{Type: "text", Text: *m.Content})
	}
	for i := range m.Images {
		image := m.Images[i]
		parts = append(parts, wireContentPart{
			Type: "image_url",
			ImageURL: &wireImageURL{
				URL:    image.DataURL(),
				Detail: image.Detail,
			},
		})
	}
	return json.Marshal(parts)
}

// UnmarshalJSON 同时接受两种 content 形态：字符串（Provider 响应的常态）与
// content parts 数组（含图的消息）。文本段拼接回 Content，图片段归一为
// ImagePart——OpenAI 的 image_url 与 Anthropic 的 source 都能反解。
//
// 未建模的 part 类型（如 provider 特有的音频段）会被跳过：宁可少读一段，
// 也不该让整条消息解析失败。
func (m *Message) UnmarshalJSON(data []byte) error {
	var wire messageWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	content, images, err := parseWireContent(wire.Content)
	if err != nil {
		return err
	}
	*m = Message{
		Role:             wire.Role,
		ReasoningContent: wire.ReasoningContent,
		Content:          content,
		Images:           images,
		ToolCalls:        wire.ToolCalls,
		ToolCallID:       wire.ToolCallID,
		Name:             wire.Name,
	}
	return nil
}

// parseWireContent 解析 content 字段的两种形态。
func parseWireContent(raw json.RawMessage) (*string, []ImagePart, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil, nil
	}
	switch trimmed[0] {
	case '"':
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return nil, nil, fmt.Errorf("types: decode content string: %w", err)
		}
		return &text, nil, nil
	case '[':
		var rawParts []json.RawMessage
		if err := json.Unmarshal(trimmed, &rawParts); err != nil {
			return nil, nil, fmt.Errorf("types: decode content parts: %w", err)
		}
		var (
			texts  []string
			images []ImagePart
		)
		for i := range rawParts {
			text, image, err := parseWirePart(rawParts[i])
			if err != nil {
				return nil, nil, err
			}
			if text != "" {
				texts = append(texts, text)
			}
			if image != nil {
				images = append(images, *image)
			}
		}
		if len(texts) == 0 {
			return nil, images, nil
		}
		joined := strings.Join(texts, "")
		return &joined, images, nil
	default:
		return nil, nil, fmt.Errorf("types: unsupported content shape: %.32s", trimmed)
	}
}

// parseWirePart 解析单个 content part，返回文本或图片（二者最多一个非空）。
func parseWirePart(raw json.RawMessage) (string, *ImagePart, error) {
	var probe struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL *struct {
			URL    string `json:"url"`
			Detail string `json:"detail"`
		} `json:"image_url"`
		Source *struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
			URL       string `json:"url"`
		} `json:"source"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "", nil, fmt.Errorf("types: decode content part: %w", err)
	}
	switch probe.Type {
	case "text", "input_text":
		return probe.Text, nil, nil
	case "image_url", "input_image":
		if probe.ImageURL == nil {
			return "", nil, fmt.Errorf("types: image part carries no image_url")
		}
		image, err := imageFromURL(probe.ImageURL.URL, probe.ImageURL.Detail)
		return "", image, err
	case "image":
		if probe.Source == nil {
			return "", nil, fmt.Errorf("types: image part carries no source")
		}
		if probe.Source.Type == "url" {
			return "", &ImagePart{URL: probe.Source.URL}, nil
		}
		image, err := decodeDataURL("data:" + probe.Source.MediaType + ";base64," + probe.Source.Data)
		if err != nil {
			return "", nil, err
		}
		return "", image, nil
	default:
		// 未建模的 part 类型：跳过而不是让整条消息解析失败。
		return "", nil, nil
	}
}

// imageFromURL 把 image_url 归一为 ImagePart：data URL 解回字节，远端地址保留。
func imageFromURL(url, detail string) (*ImagePart, error) {
	if strings.HasPrefix(url, "data:") {
		image, err := decodeDataURL(url)
		if err != nil {
			return nil, err
		}
		image.Detail = detail
		return image, nil
	}
	return &ImagePart{URL: url, Detail: detail}, nil
}

// decodeDataURL 解析 data:<mime>;base64,<payload>。
func decodeDataURL(url string) (*ImagePart, error) {
	rest := strings.TrimPrefix(url, "data:")
	meta, payload, found := strings.Cut(rest, ",")
	if !found {
		return nil, fmt.Errorf("types: malformed data URL")
	}
	mimeType, base64Flag, _ := strings.Cut(meta, ";")
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("types: decode data URL payload: %w", err)
	}
	if base64Flag == "" {
		return nil, fmt.Errorf("types: unsupported data URL encoding %q", mimeType)
	}
	return &ImagePart{MimeType: mimeType, Data: decoded}, nil
}

// ─────────────────────────────────────────────
// Message 构造与查询便捷方法
// ─────────────────────────────────────────────

// ImageCount 返回随消息下发的图片数。
func (m Message) ImageCount() int { return len(m.Images) }

// Text 返回文本投影；Content 为 nil 时返回空串。
func (m Message) Text() string {
	if m.Content == nil {
		return ""
	}
	return *m.Content
}

// WithText 返回设置了文本投影的消息副本。
func (m Message) WithText(text string) Message {
	m.Content = &text
	return m
}

// WithImages 返回追加了图片的消息副本；文本投影与服务用量字段原样保留。
func (m Message) WithImages(images ...ImagePart) Message {
	if len(images) == 0 {
		return m
	}
	merged := make([]ImagePart, 0, len(m.Images)+len(images))
	merged = append(merged, m.Images...)
	merged = append(merged, images...)
	m.Images = merged
	return m
}
