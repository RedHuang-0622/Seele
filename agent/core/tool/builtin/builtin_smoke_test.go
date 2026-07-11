package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGrepTool_Providers(t *testing.T) {
	g := NewGrepTool()
	if g.ProviderName() != "builtin" {
		t.Errorf("expected 'builtin', got %q", g.ProviderName())
	}
	tools := g.Tools()
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	names := []string{tools[0].Definition.Function.Name, tools[1].Definition.Function.Name}
	t.Logf("GrepTool provides: %v", names)
}

func TestEditorTool_Providers(t *testing.T) {
	e := NewEditorTool()
	tools := e.Tools()
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools))
	}
	for _, tool := range tools {
		t.Logf("EditorTool: %s", tool.Definition.Function.Name)
	}
}

func TestShellTool_Providers(t *testing.T) {
	s := NewShellTool()
	tools := s.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	t.Logf("ShellTool: %s", tools[0].Definition.Function.Name)
}

func TestGitTool_Providers(t *testing.T) {
	g := NewGitTool()
	tools := g.Tools()
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools))
	}
	for _, tool := range tools {
		t.Logf("GitTool: %s", tool.Definition.Function.Name)
	}
}

func TestWriteFileHandler(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	h := &writeFileHandler{}

	args := `{"path":"` + strings.ReplaceAll(path, `\`, `/`) + `","content":"hello world"}`
	result, err := h.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}

	var res map[string]interface{}
	json.Unmarshal([]byte(result), &res)
	if res["status"] != "ok" {
		t.Errorf("expected status=ok, got %v", res["status"])
	}

	data, _ := os.ReadFile(path)
	if string(data) != "hello world" {
		t.Errorf("expected 'hello world', got %q", string(data))
	}
	t.Logf("write_file result: %s", result)
}

func TestEditFileHandler(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edit.txt")
	os.WriteFile(path, []byte("hello world\nfoo bar\nhello world"), 0644)

	h := &editFileHandler{}
	args := `{"path":"` + strings.ReplaceAll(path, `\`, `/`) + `","old_string":"world","new_string":"there"}`
	result, err := h.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("edit_file: %v", err)
	}

	var res map[string]interface{}
	json.Unmarshal([]byte(result), &res)
	t.Logf("edit_file result: %s", result)

	data, _ := os.ReadFile(path)
	if strings.Count(string(data), "there") != 2 {
		t.Errorf("expected 2 replacements, got content: %q", string(data))
	}
}

func TestGlobHandler(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a"), 0644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package b"), 0644)
	os.WriteFile(filepath.Join(dir, "c.txt"), []byte("text"), 0644)

	h := &globHandler{}
	args := `{"pattern":"*.go","path":"` + strings.ReplaceAll(dir, `\`, `/`) + `"}`
	result, err := h.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	t.Logf("glob result: %s", result)

	var files []string
	json.Unmarshal([]byte(result), &files)
	if len(files) != 2 {
		t.Errorf("expected 2 .go files, got %d: %v", len(files), files)
	}
}

func TestBashHandler(t *testing.T) {
	h := &bashHandler{defaultTimeout: 10 * time.Second}

	result, err := h.Execute(context.Background(), `{"command":"echo hello"}`)
	if err != nil {
		t.Fatalf("bash: %v", err)
	}

	var res map[string]interface{}
	json.Unmarshal([]byte(result), &res)
	if res["stdout"] != "hello" {
		t.Errorf("expected stdout='hello', got %v", res["stdout"])
	}
	if res["exit_code"] != float64(0) {
		t.Errorf("expected exit_code=0, got %v", res["exit_code"])
	}
	t.Logf("bash result: %s", result)
}

func TestGitStatusHandler(t *testing.T) {
	h := &gitHandler{action: "status"}
	result, err := h.Execute(context.Background(), "{}")
	if err != nil {
		t.Fatalf("git_status: %v", err)
	}
	t.Logf("git_status result: %s", result)

	var res map[string]interface{}
	json.Unmarshal([]byte(result), &res)
	if _, ok := res["clean"]; !ok {
		t.Error("expected 'clean' field in result")
	}
}

func TestAllProviders(t *testing.T) {
	providers := AllProviders()
	toolCount := 0
	for _, p := range providers {
		t.Logf("provider %q: %d tools", p.ProviderName(), len(p.Tools()))
		toolCount += len(p.Tools())
	}
	t.Logf("total: %d providers, %d tools", len(providers), toolCount)
	if toolCount < 5 {
		t.Errorf("expected at least 5 tools, got %d", toolCount)
	}
}
