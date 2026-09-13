package types

import (
	"encoding/json"
	"testing"
)

func TestMessageTextOnlyWireShapeUnchanged(t *testing.T) {
	text := "hi"
	data, err := json.Marshal(Message{Role: "user", Content: &text})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if got, want := string(data), `{"role":"user","content":"hi"}`; got != want {
		t.Fatalf("wire = %s, want %s", got, want)
	}
}

func TestMessageNilContentOmitted(t *testing.T) {
	data, err := json.Marshal(Message{Role: "assistant"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if got, want := string(data), `{"role":"assistant"}`; got != want {
		t.Fatalf("wire = %s, want %s", got, want)
	}
}

func TestMessageEmptyContentStillEmitted(t *testing.T) {
	empty := ""
	data, err := json.Marshal(Message{Role: "user", Content: &empty})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if got, want := string(data), `{"role":"user","content":""}`; got != want {
		t.Fatalf("wire = %s, want %s", got, want)
	}
}

func TestMessageWithImagesWireShape(t *testing.T) {
	text := "这是什么"
	message := Message{
		Role:    "user",
		Content: &text,
		Images:  []ImagePart{{MimeType: "image/png", Data: []byte{1, 2, 3}}},
	}
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var wire struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL *struct {
				URL string `json:"url"`
			} `json:"image_url"`
		} `json:"content"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatalf("Unmarshal wire: %v", err)
	}
	if len(wire.Content) != 2 {
		t.Fatalf("content parts = %d, want 2", len(wire.Content))
	}
	if wire.Content[0].Type != "text" || wire.Content[0].Text != "这是什么" {
		t.Fatalf("part[0] = %+v, want text", wire.Content[0])
	}
	if wire.Content[1].Type != "image_url" || wire.Content[1].ImageURL == nil {
		t.Fatalf("part[1] = %+v, want image_url", wire.Content[1])
	}
	if got, want := wire.Content[1].ImageURL.URL, "data:image/png;base64,AQID"; got != want {
		t.Fatalf("image url = %q, want %q", got, want)
	}
}

func TestImagePartEncoding(t *testing.T) {
	part := ImagePart{MimeType: "image/png", Data: []byte{1, 2, 3}}
	if got, want := part.Base64(), "AQID"; got != want {
		t.Fatalf("Base64 = %q, want %q", got, want)
	}
	if got, want := part.DataURL(), "data:image/png;base64,AQID"; got != want {
		t.Fatalf("DataURL = %q, want %q", got, want)
	}
	remote := ImagePart{URL: "https://example.com/a.png"}
	if got, want := remote.DataURL(), "https://example.com/a.png"; got != want {
		t.Fatalf("remote DataURL = %q, want %q", got, want)
	}
	untyped := ImagePart{Data: []byte{1}}
	if got, want := untyped.DataURL(), "data:application/octet-stream;base64,AQ=="; got != want {
		t.Fatalf("untyped DataURL = %q, want %q", got, want)
	}
}

func TestMessageUnmarshalAcceptsBothContentShapes(t *testing.T) {
	var message Message
	if err := json.Unmarshal([]byte(`{"role":"assistant","content":"ok"}`), &message); err != nil {
		t.Fatalf("Unmarshal string content: %v", err)
	}
	if message.Text() != "ok" || message.ImageCount() != 0 {
		t.Fatalf("string content = %q images=%d, want ok/0", message.Text(), message.ImageCount())
	}

	if err := json.Unmarshal([]byte(`{"role":"user","content":[{"type":"text","text":"看图"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AQID","detail":"high"}}]}`), &message); err != nil {
		t.Fatalf("Unmarshal openai parts: %v", err)
	}
	if message.Text() != "看图" || message.ImageCount() != 1 {
		t.Fatalf("openai parts = %q images=%d, want 看图/1", message.Text(), message.ImageCount())
	}
	image := message.Images[0]
	if image.MimeType != "image/png" || string(image.Data) != "\x01\x02\x03" || image.Detail != "high" {
		t.Fatalf("openai image = %+v", image)
	}

	if err := json.Unmarshal([]byte(`{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"AQID"}}]}`), &message); err != nil {
		t.Fatalf("Unmarshal anthropic parts: %v", err)
	}
	if message.Content != nil || message.ImageCount() != 1 {
		t.Fatalf("anthropic parts content=%v images=%d, want nil/1", message.Content, message.ImageCount())
	}
	if message.Images[0].MimeType != "image/jpeg" || string(message.Images[0].Data) != "\x01\x02\x03" {
		t.Fatalf("anthropic image = %+v", message.Images[0])
	}

	if err := json.Unmarshal([]byte(`{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}]}`), &message); err != nil {
		t.Fatalf("Unmarshal url source: %v", err)
	}
	if message.ImageCount() != 1 || message.Images[0].URL != "https://example.com/a.png" {
		t.Fatalf("url source image = %+v", message.Images)
	}
}

func TestMessageUnmarshalSkipsUnmodelledParts(t *testing.T) {
	var message Message
	if err := json.Unmarshal([]byte(`{"role":"user","content":[{"type":"audio","text":"ignored"},{"type":"text","text":"keep"}]}`), &message); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if message.Text() != "keep" || message.ImageCount() != 0 {
		t.Fatalf("skipped parts = %q images=%d, want keep/0", message.Text(), message.ImageCount())
	}
}

func TestMessageUnmarshalRejectsMalformedImagePart(t *testing.T) {
	var message Message
	if err := json.Unmarshal([]byte(`{"role":"user","content":[{"type":"image_url"}]}`), &message); err == nil {
		t.Fatal("expected error for image_url part without url")
	}
}

func TestMessageRoundTripKeepsTextAndImages(t *testing.T) {
	text := "看图"
	original := Message{Role: "user", Content: &text, Images: []ImagePart{{MimeType: "image/png", Data: []byte{1, 2, 3}}}}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var restored Message
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if restored.Text() != original.Text() || restored.ImageCount() != 1 {
		t.Fatalf("round trip = %q images=%d", restored.Text(), restored.ImageCount())
	}
	if restored.Images[0].MimeType != "image/png" || string(restored.Images[0].Data) != "\x01\x02\x03" {
		t.Fatalf("round trip image = %+v", restored.Images[0])
	}
}

func TestMessageConvenienceHelpers(t *testing.T) {
	message := Message{Role: "user"}.WithText("看图").WithImages(ImagePart{MimeType: "image/png", Data: []byte{1}})
	if message.Text() != "看图" || message.ImageCount() != 1 {
		t.Fatalf("helpers = %q images=%d", message.Text(), message.ImageCount())
	}
	message = message.WithImages(ImagePart{MimeType: "image/png", Data: []byte{2}})
	if message.ImageCount() != 2 {
		t.Fatalf("WithImages should append, got %d", message.ImageCount())
	}
	if (Message{Role: "user"}).Text() != "" {
		t.Fatal("nil Content should read as empty text")
	}
}
