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

// FilePart 是一条消息随附的一张图片。
//
// 与 Message.Content 的关系是刻意的单向：Content 始终是**文本投影**——历史、
// 上下文压缩、token 估算都只读它，因此不改变既有语义；图片本身放在这里，
// 由 Message 的 wire 投影（MarshalJSON）和各 Provider 策略翻译成对方要求的
// content parts 形态（OpenAI 的 image_url / Anthropic 的 source）。
//
// 字节只活在本地：Data 是原始字节（不含 base64），宽度与高度是限流按 tile
// 计价所需的元数据，两者都不直接出现在 wire 上。
// FileKind 是附件在**发射层**的种类：它决定发哪种内容块、走哪条校验与计价。
//
// 三家 provider 的线上形状印证了这个划分：Gemini 的 Part 是统一的
// （inline_data / file_data + mime_type），而 OpenAI（image_url vs file/input_file）
// 与 Anthropic（image vs document）是**硬类型判别**——种类传错是 400，不是降级。
// 所以区分必须显式落在 Kind 上，而载体形状保持一个（见 FilePart）。
type FileKind string

const (
	// FileKindImage 是图像附件（jpeg/png/gif/webp 等）。
	FileKindImage FileKind = "image"
	// FileKindDocument 是文档附件（PDF/纯文本等，具体白名单由 provider 决定）。
	FileKindDocument FileKind = "document"
)

// EffectiveKind 返回附件的有效种类：Kind 为空时按 MIME 推断（image/* → 图像，
// 其余 → 文档）。推断只是兜底，知道种类就应当写 Kind。
func (p FilePart) EffectiveKind() FileKind {
	if p.Kind != "" {
		return p.Kind
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(p.MimeType)), "image/") {
		return FileKindImage
	}
	return FileKindDocument
}

// IsImage 判断附件是否按图像发射。
func (p FilePart) IsImage() bool { return p.EffectiveKind() == FileKindImage }

// Validate 检查种类、MIME 与载荷是否自洽，供发射层在请求前拦下必然被拒的附件
// （种类与 MIME 打架、既无字节又无地址）。数值型限制（像素/字节/页数）由 provider
// 与限额层各自持有，这里只做形状检查。
func (p FilePart) Validate() error {
	mimeType := strings.ToLower(strings.TrimSpace(p.MimeType))
	switch kind := p.EffectiveKind(); kind {
	case FileKindImage:
		if mimeType != "" && !strings.HasPrefix(mimeType, "image/") {
			return fmt.Errorf("types: image part carries mime type %q", p.MimeType)
		}
	case FileKindDocument:
		if strings.HasPrefix(mimeType, "image/") {
			return fmt.Errorf("types: document part carries image mime type %q; use FileKindImage", p.MimeType)
		}
	default:
		return fmt.Errorf("types: unknown file kind %q", kind)
	}
	if len(p.Data) == 0 && p.URL == "" {
		return fmt.Errorf("types: file part carries neither bytes nor url")
	}
	return nil
}

type FilePart struct {
	// Kind 是附件的种类（图片 / 文档）：决定发射哪种内容块；留空时按 MimeType 推断。
	Kind FileKind `json:"kind,omitempty"`
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
func (p FilePart) Base64() string { return base64.StdEncoding.EncodeToString(p.Data) }

// DataURL 返回可直接放进 OpenAI image_url 的 data URL；只有远端地址时返回该地址。
func (p FilePart) DataURL() string {
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
	File     *wireFile     `json:"file,omitempty"`
}

// wireFile 是 OpenAI 形态的文档 part 载荷（Chat Completions 的 file.file_data）。
//
// 端点能力差异（实测 api.deepseek.com / deepseek-v4-flash，裁决记录与复跑命令见
// seelex seelebridge/attachment_live_smoke_test.go）：
//
//   - 官方 OpenAI 的 file part 是**嵌套**形状（file:{file_data,filename}），支持 PDF；
//   - 该兼容端点读的是**扁平**字段（file_data / filename / file_id 直接挂在 part 上），
//     嵌套形状会被当成空对象，回 400 "file must have a file_id or file_data"；
//   - 形状即便对了，它的 file 通道也只收 webp/png/jpeg/gif（错误原文列了白名单），
//     内部图片数组说明它其实是个图片通道：PDF / text 一律 400；
//   - /files 上传（唯一支持 purpose=user_data）同样只收图片，file_id 引用是通的；
//   - Anthropic 兼容端点的 document block 是 200 却内容不进模型。
//
// 结论：「能不能发文档」是端点能力问题，该由能力声明/门控决定；没有文档通道时应当降级
// （文本内联，见 seelex seelebridge/attachment），而不是把「形状写对了」当成「能发」。
type wireFile struct {
	FileData string `json:"file_data,omitempty"`
	Filename string `json:"filename,omitempty"`
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
	if len(m.Files) == 0 {
		if m.Content == nil {
			return nil, nil
		}
		return json.Marshal(*m.Content)
	}
	parts := make([]wireContentPart, 0, len(m.Files)+1)
	if m.Content != nil && *m.Content != "" {
		parts = append(parts, wireContentPart{Type: "text", Text: *m.Content})
	}
	for i := range m.Files {
		file := m.Files[i]
		switch file.EffectiveKind() {
		case FileKindImage:
			parts = append(parts, wireContentPart{
				Type:     "image_url",
				ImageURL: &wireImageURL{URL: file.DataURL(), Detail: file.Detail},
			})
		case FileKindDocument:
			parts = append(parts, wireContentPart{
				Type: "file",
				File: &wireFile{FileData: file.DataURL(), Filename: file.Name},
			})
		}
	}
	return json.Marshal(parts)
}

// UnmarshalJSON 同时接受两种 content 形态：字符串（Provider 响应的常态）与
// content parts 数组（含图的消息）。文本段拼接回 Content，图片段归一为
// FilePart——OpenAI 的 image_url 与 Anthropic 的 source 都能反解。
//
// 未建模的 part 类型（如 provider 特有的音频段）会被跳过：宁可少读一段，
// 也不该让整条消息解析失败。
func (m *Message) UnmarshalJSON(data []byte) error {
	var wire messageWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	content, files, err := parseWireContent(wire.Content)
	if err != nil {
		return err
	}
	*m = Message{
		Role:             wire.Role,
		ReasoningContent: wire.ReasoningContent,
		Content:          content,
		Files:            files,
		ToolCalls:        wire.ToolCalls,
		ToolCallID:       wire.ToolCallID,
		Name:             wire.Name,
	}
	return nil
}

// parseWireContent 解析 content 字段的两种形态。
func parseWireContent(raw json.RawMessage) (*string, []FilePart, error) {
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
			texts []string
			files []FilePart
		)
		for i := range rawParts {
			text, file, err := parseWirePart(rawParts[i])
			if err != nil {
				return nil, nil, err
			}
			if text != "" {
				texts = append(texts, text)
			}
			if file != nil {
				files = append(files, *file)
			}
		}
		if len(texts) == 0 {
			return nil, files, nil
		}
		joined := strings.Join(texts, "")
		return &joined, files, nil
	default:
		return nil, nil, fmt.Errorf("types: unsupported content shape: %.32s", trimmed)
	}
}

// parseWirePart 解析单个 content part，返回文本或图片（二者最多一个非空）。
func parseWirePart(raw json.RawMessage) (string, *FilePart, error) {
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
		File *struct {
			FileData string `json:"file_data"`
			FileID   string `json:"file_id"`
			Filename string `json:"filename"`
		} `json:"file"`
		FileData string `json:"file_data"`
		FileURL  string `json:"file_url"`
		Filename string `json:"filename"`
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
		file, err := fileFromURL(probe.ImageURL.URL, probe.ImageURL.Detail, FileKindImage)
		return "", file, err
	case "image", "document":
		if probe.Source == nil {
			return "", nil, fmt.Errorf("types: file part carries no source")
		}
		kind := FileKindImage
		if probe.Type == "document" {
			kind = FileKindDocument
		}
		if probe.Source.Type == "url" {
			return "", &FilePart{Kind: kind, URL: probe.Source.URL}, nil
		}
		file, err := decodeDataURL("data:"+probe.Source.MediaType+";base64,"+probe.Source.Data, kind)
		if err != nil {
			return "", nil, err
		}
		return "", file, nil
	case "file", "input_file":
		// OpenAI：Chat Completions 把载荷放在 file.file_data，Responses 的
		// input_file 放在 part 顶层（file_data / file_url / filename）。
		data, filename := probe.FileData, probe.Filename
		if probe.File != nil {
			data, filename = probe.File.FileData, probe.File.Filename
		}
		if strings.HasPrefix(data, "data:") {
			file, err := decodeDataURL(data, FileKindDocument)
			if err != nil {
				return "", nil, err
			}
			if file.Name == "" {
				file.Name = filename
			}
			return "", file, nil
		}
		if probe.FileURL != "" {
			return "", &FilePart{Kind: FileKindDocument, URL: probe.FileURL, Name: filename}, nil
		}
		// 只有 file_id 时本地拿不到字节，模型无法据此重建附件：跳过而不是报错。
		return "", nil, nil
	default:
		// 未建模的 part 类型：跳过而不是让整条消息解析失败。
		return "", nil, nil
	}
}

// imageFromURL 把 image_url 归一为 FilePart：data URL 解回字节，远端地址保留。
func fileFromURL(url, detail string, kind FileKind) (*FilePart, error) {
	if strings.HasPrefix(url, "data:") {
		file, err := decodeDataURL(url, kind)
		if err != nil {
			return nil, err
		}
		file.Detail = detail
		return file, nil
	}
	return &FilePart{Kind: kind, URL: url, Detail: detail}, nil
}

// decodeDataURL 解析 data:<mime>;base64,<payload>。
func decodeDataURL(url string, kind FileKind) (*FilePart, error) {
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
	return &FilePart{Kind: kind, MimeType: mimeType, Data: decoded}, nil
}

// ─────────────────────────────────────────────
// Message 构造与查询便捷方法
// ─────────────────────────────────────────────

// ImageCount 返回随消息下发的图片数。
func (m Message) FileCount() int { return len(m.Files) }

// ImageCount 返回消息里按图像发射的附件数（文档不计入）。
func (m Message) ImageCount() int {
	count := 0
	for i := range m.Files {
		if m.Files[i].IsImage() {
			count++
		}
	}
	return count
}

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

// WithFiles 返回追加了图片的消息副本；文本投影与服务用量字段原样保留。
func (m Message) WithFiles(files ...FilePart) Message {
	if len(files) == 0 {
		return m
	}
	merged := make([]FilePart, 0, len(m.Files)+len(files))
	merged = append(merged, m.Files...)
	merged = append(merged, files...)
	m.Files = merged
	return m
}
