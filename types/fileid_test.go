package types

import (
	"encoding/json"
	"strings"
	"testing"
)

// file_id 的 wire 形状是**扁平**的：{"type":"file","file_id":"file-api-..."}。
// 依据：api.deepseek.com 官方文档「图像理解」+ 真机实测（嵌套 file:{...} 会被端点当空对象）。
func TestMarshalFileIDUsesFlatFileBlock(t *testing.T) {
	cases := []struct {
		name string
		file FilePart
	}{
		{"图片引用", FilePart{Kind: FileKindImage, MimeType: "image/png", FileID: "file-api-abc", Name: "shot.png"}},
		{"文档引用", FilePart{Kind: FileKindDocument, MimeType: "application/pdf", FileID: "file-api-abc", Name: "spec.pdf"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			raw, err := json.Marshal(Message{Role: "user"}.WithText("看一看").WithFiles(testCase.file))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			wire := string(raw)
			for _, want := range []string{`"type":"file"`, `"file_id":"file-api-abc"`} {
				if !strings.Contains(wire, want) {
					t.Fatalf("wire 里应当有 %s，实际 %s", want, wire)
				}
			}
			if strings.Contains(wire, `"file":{`) || strings.Contains(wire, "image_url") {
				t.Fatalf("file_id 引用不该带嵌套载荷或 image_url：%s", wire)
			}
		})
	}
}

func TestFilePartValidateCarrierExclusive(t *testing.T) {
	ok := []FilePart{
		{Kind: FileKindImage, MimeType: "image/png", Data: []byte{1}},
		{Kind: FileKindImage, URL: "https://example.com/a.png"},
		{Kind: FileKindImage, FileID: "file-api-abc"},
		{Kind: FileKindDocument, MimeType: "application/pdf", FileID: "file-api-abc"},
	}
	for i, part := range ok {
		if err := part.Validate(); err != nil {
			t.Errorf("case %d 应当通过：%v", i, err)
		}
	}
	bad := []FilePart{
		{Kind: FileKindImage},
		{Kind: FileKindImage, Data: []byte{1}, FileID: "file-api-abc"},
		{Kind: FileKindImage, URL: "https://example.com/a.png", FileID: "file-api-abc"},
	}
	for i, part := range bad {
		if err := part.Validate(); err == nil {
			t.Errorf("case %d 应当报错：%+v", i, part)
		}
	}
}

// 回读必须保住引用：重发历史消息时靠 FilePart.FileID 把附件带回去，丢了就是静默少一张图。
func TestParseFileIDRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		wire string
	}{
		{"扁平 file_id", `{"role":"user","content":[{"type":"text","text":"hi"},{"type":"file","file_id":"file-api-xyz"}]}`},
		{"嵌套 file.file_id", `{"role":"user","content":[{"type":"file","file":{"file_id":"file-api-xyz","filename":"a.png"}}]}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var msg Message
			if err := json.Unmarshal([]byte(testCase.wire), &msg); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(msg.Files) != 1 || msg.Files[0].FileID != "file-api-xyz" {
				t.Fatalf("file_id 应当被解析成 FilePart.FileID，实际 %+v", msg.Files)
			}
			raw, err := json.Marshal(msg)
			if err != nil {
				t.Fatalf("重新 marshal: %v", err)
			}
			if !strings.Contains(string(raw), `"file_id":"file-api-xyz"`) {
				t.Fatalf("往返后引用丢失：%s", raw)
			}
		})
	}
}
