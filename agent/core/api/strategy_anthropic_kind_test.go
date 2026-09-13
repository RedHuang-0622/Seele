package api

import (
	"encoding/json"
	"testing"

	"github.com/RedHuang-0622/Seele/types"
)

// TestAnthropicFileBlocksByKind 钉住 Anthropic 侧的按种类投影：图片走 image block，
// 文档走 document block，两者共用同一套 source（base64 / url）规则；既无字节也无
// 地址的附件被跳过，而不是给 provider 发一个空 source。
func TestAnthropicFileBlocksByKind(t *testing.T) {
	files := []types.FilePart{
		{Kind: types.FileKindImage, MimeType: "image/png", Data: []byte{1, 2, 3}},
		{Kind: types.FileKindDocument, MimeType: "application/pdf", Data: []byte{4, 5, 6}},
		{Kind: types.FileKindDocument, URL: "https://example.com/spec.pdf"},
		{Kind: types.FileKindImage, MimeType: "image/png"},
	}
	encoded, err := json.Marshal(anthropicFileBlocks(files))
	if err != nil {
		t.Fatalf("marshal blocks: %v", err)
	}
	want := `[{"source":{"data":"AQID","media_type":"image/png","type":"base64"},"type":"image"},` +
		`{"source":{"data":"BAUG","media_type":"application/pdf","type":"base64"},"type":"document"},` +
		`{"source":{"type":"url","url":"https://example.com/spec.pdf"},"type":"document"}]`
	var got, expected any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("decode got: %v", err)
	}
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatalf("decode want: %v", err)
	}
	reGot, _ := json.Marshal(got)
	reWant, _ := json.Marshal(expected)
	if string(reGot) != string(reWant) {
		t.Fatalf("blocks 不符\n got=%s\nwant=%s", reGot, reWant)
	}

	// toolResultContent 走同一条路径：带附件的工具结果产出 blocks，而不是字符串。
	message := types.Message{Role: "tool", ToolCallID: "c1"}.WithText("看屏幕").WithFiles(files[0])
	if _, isString := toolResultContent(message).(string); isString {
		t.Fatal("带附件的 tool_result 不应退化成字符串 content")
	}
}
