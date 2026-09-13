package types

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

// pngBytes / pdfBytes 是任意载荷：形状测试只关心编码与种类，不关心内容。
var (
	pngBytes = []byte{0x89, 'P', 'N', 'G'}
	pdfBytes = []byte{'%', 'P', 'D', 'F'}
)

func compactJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var buffer bytes.Buffer
	if err := json.Compact(&buffer, encoded); err != nil {
		t.Fatalf("compact: %v", err)
	}
	return buffer.String()
}

// TestMessageWireShapeByKind 钉住每种 Kind 的 OpenAI 形态发射：图片走 image_url，
// 文档走 file.file_data，顺序按 Files 原序（这是「一个载体」相对两个平行字段的收益）。
func TestMessageWireShapeByKind(t *testing.T) {
	cases := []struct {
		name string
		msg  Message
		want string
	}{
		{
			name: "纯文本消息保持字符串 content",
			msg:  Message{Role: "user"}.WithText("hello"),
			want: `{"role":"user","content":"hello"}`,
		},
		{
			name: "图片附件",
			msg: Message{Role: "user"}.WithText("看图").WithFiles(FilePart{
				Kind: FileKindImage, MimeType: "image/png", Data: pngBytes, Detail: "high",
			}),
			want: `{"role":"user","content":[{"type":"text","text":"看图"},{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw==","detail":"high"}}]}`,
		},
		{
			name: "文档附件",
			msg: Message{Role: "user"}.WithText("读文档").WithFiles(FilePart{
				Kind: FileKindDocument, MimeType: "application/pdf", Data: pdfBytes, Name: "spec.pdf",
			}),
			want: `{"role":"user","content":[{"type":"text","text":"读文档"},{"type":"file","file":{"file_data":"data:application/pdf;base64,JVBERg==","filename":"spec.pdf"}}]}`,
		},
		{
			name: "图片与文档同一条消息，按插入顺序",
			msg: Message{Role: "user"}.WithFiles(
				FilePart{Kind: FileKindImage, MimeType: "image/png", Data: pngBytes},
				FilePart{Kind: FileKindDocument, MimeType: "application/pdf", Data: pdfBytes, Name: "spec.pdf"},
			),
			want: `{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw=="}},{"type":"file","file":{"file_data":"data:application/pdf;base64,JVBERg==","filename":"spec.pdf"}}]}`,
		},
		{
			name: "Kind 留空时按 MIME 推断",
			msg: Message{Role: "user"}.WithFiles(
				FilePart{MimeType: "image/jpeg", Data: pngBytes},
				FilePart{MimeType: "application/pdf", Data: pdfBytes},
			),
			want: `{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,iVBORw=="}},{"type":"file","file":{"file_data":"data:application/pdf;base64,JVBERg=="}}]}`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := compactJSON(t, testCase.msg); got != testCase.want {
				t.Fatalf("wire 形状不符\n got=%s\nwant=%s", got, testCase.want)
			}
		})
	}
}

// TestMessageUnmarshalKinds 钉住三种 provider 形状都能反解成带 Kind 的载体：
// OpenAI 的 image_url / file（含 Responses 的顶层 input_file）、Anthropic 的
// image / document。只有 file_id 的情况拿不到字节，跳过而不是报错。
func TestMessageUnmarshalKinds(t *testing.T) {
	cases := []struct {
		name string
		wire string
		want []FilePart
	}{
		{
			name: "OpenAI image_url data URL",
			wire: `{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw==","detail":"low"}}]}`,
			want: []FilePart{{Kind: FileKindImage, MimeType: "image/png", Data: pngBytes, Detail: "low"}},
		},
		{
			name: "OpenAI image_url 远端地址",
			wire: `{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}`,
			want: []FilePart{{Kind: FileKindImage, URL: "https://example.com/a.png"}},
		},
		{
			name: "OpenAI file（Chat Completions）",
			wire: `{"role":"user","content":[{"type":"file","file":{"file_data":"data:application/pdf;base64,JVBERg==","filename":"spec.pdf"}}]}`,
			want: []FilePart{{Kind: FileKindDocument, MimeType: "application/pdf", Data: pdfBytes, Name: "spec.pdf"}},
		},
		{
			name: "OpenAI input_file（Responses，顶层字段）",
			wire: `{"role":"user","content":[{"type":"input_file","file_data":"data:text/plain;base64,aGk=","filename":"note.txt"}]}`,
			want: []FilePart{{Kind: FileKindDocument, MimeType: "text/plain", Data: []byte("hi"), Name: "note.txt"}},
		},
		{
			name: "Anthropic image block",
			wire: `{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw=="}}]}`,
			want: []FilePart{{Kind: FileKindImage, MimeType: "image/png", Data: pngBytes}},
		},
		{
			name: "Anthropic document block（url source）",
			wire: `{"role":"user","content":[{"type":"document","source":{"type":"url","url":"https://example.com/spec.pdf"}}]}`,
			want: []FilePart{{Kind: FileKindDocument, URL: "https://example.com/spec.pdf"}},
		},
		{
			name: "只有 file_id 时保住引用（重发历史要靠它把附件带回去）",
			wire: `{"role":"user","content":[{"type":"input_file","file_id":"file-123"}]}`,
			want: []FilePart{{Kind: FileKindDocument, FileID: "file-123"}},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var message Message
			if err := json.Unmarshal([]byte(testCase.wire), &message); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if !reflect.DeepEqual(message.Files, testCase.want) {
				t.Fatalf("反解结果不符\n got=%+v\nwant=%+v", message.Files, testCase.want)
			}
		})
	}
}

// TestFilePartKindAndValidate 钉住种类推断与形状校验：发射层据此在请求前拦下必然 400
// 的附件（种类与 MIME 打架、既无字节又无地址）。
func TestFilePartKindAndValidate(t *testing.T) {
	cases := []struct {
		name     string
		part     FilePart
		wantKind FileKind
		wantErr  bool
	}{
		{"空 Kind + image/png 推断为图像", FilePart{MimeType: "image/png", Data: pngBytes}, FileKindImage, false},
		{"空 Kind + application/pdf 推断为文档", FilePart{MimeType: "application/pdf", Data: pdfBytes}, FileKindDocument, false},
		{"显式文档 + text/plain", FilePart{Kind: FileKindDocument, MimeType: "text/plain", Data: pdfBytes}, FileKindDocument, false},
		{"图像 Kind 与 MIME 打架", FilePart{Kind: FileKindImage, MimeType: "application/pdf", Data: pdfBytes}, FileKindImage, true},
		{"文档 Kind 与图片 MIME 打架", FilePart{Kind: FileKindDocument, MimeType: "image/png", Data: pngBytes}, FileKindDocument, true},
		{"既无字节又无地址", FilePart{Kind: FileKindImage, MimeType: "image/png"}, FileKindImage, true},
		{"未知种类", FilePart{Kind: FileKind("audio"), MimeType: "audio/wav", Data: pdfBytes}, FileKind("audio"), true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.part.EffectiveKind(); got != testCase.wantKind {
				t.Fatalf("EffectiveKind = %q, want %q", got, testCase.wantKind)
			}
			err := testCase.part.Validate()
			if testCase.wantErr && err == nil {
				t.Fatal("Validate 应报错却通过了")
			}
			if !testCase.wantErr && err != nil {
				t.Fatalf("Validate 意外报错: %v", err)
			}
		})
	}
}

// TestMessageFileCountsByKind 钉住计数语义：FileCount 数全部附件，ImageCount 只数图像，
// 这样限额层（按 tile 计价）不会被文档污染。
func TestMessageFileCountsByKind(t *testing.T) {
	message := Message{Role: "user"}.WithFiles(
		FilePart{Kind: FileKindImage, MimeType: "image/png", Data: pngBytes},
		FilePart{Kind: FileKindDocument, MimeType: "application/pdf", Data: pdfBytes},
		FilePart{MimeType: "image/jpeg", Data: pngBytes},
	)
	if got := message.FileCount(); got != 3 {
		t.Fatalf("FileCount = %d, want 3", got)
	}
	if got := message.ImageCount(); got != 2 {
		t.Fatalf("ImageCount = %d, want 2（文档不计入）", got)
	}
}
