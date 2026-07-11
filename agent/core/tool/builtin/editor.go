package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/RedHuang-0622/Seele/agent/core/tool/interfaces"
	types "github.com/RedHuang-0622/Seele/types"
)

// EditorTool provides file editing and glob search tools.
type EditorTool struct{}

func NewEditorTool() *EditorTool { return &EditorTool{} }
func (e *EditorTool) ProviderName() string { return "builtin_editor" }

func (e *EditorTool) Tools() []interfaces.ToolEntry {
	return []interfaces.ToolEntry{
		{
			Definition: types.Tool{
				Type: "function",
				Function: types.ToolFunction{
					Name:        "write_file",
					Description: "写入内容到指定文件。如果文件存在则覆盖，不存在则创建。自动创建父目录。",
					Parameters: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"path":    map[string]interface{}{"type": "string", "description": "文件路径（绝对或相对当前工作目录）"},
							"content": map[string]interface{}{"type": "string", "description": "要写入的文件内容"},
						},
						"required": []string{"path", "content"},
					},
				},
			},
			Handler: &writeFileHandler{},
		},
		{
			Definition: types.Tool{
				Type: "function",
				Function: types.ToolFunction{
					Name:        "edit_file",
					Description: "编辑文件：查找并替换文件中的内容。支持精确字符串匹配与替换。",
					Parameters: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"path":       map[string]interface{}{"type": "string", "description": "文件路径"},
							"old_string": map[string]interface{}{"type": "string", "description": "要查找的旧字符串（区分大小写，精确匹配）"},
							"new_string": map[string]interface{}{"type": "string", "description": "替换后的新字符串"},
						},
						"required": []string{"path", "old_string", "new_string"},
					},
				},
			},
			Handler: &editFileHandler{},
		},
		{
			Definition: types.Tool{
				Type: "function",
				Function: types.ToolFunction{
					Name:        "glob",
					Description: "查找匹配 glob 模式的文件路径列表。",
					Parameters: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"pattern": map[string]interface{}{"type": "string", "description": "glob 模式，如 \"**/*.go\" 或 \"src/**/*.ts\""},
							"path":    map[string]interface{}{"type": "string", "description": "搜索根目录，默认 \".\""},
						},
						"required": []string{"pattern"},
					},
				},
			},
			Handler: &globHandler{},
		},
	}
}

// ── writeFileHandler ─────────────────────────────────────────────

type writeFileHandler struct{}
type writeFileInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (h *writeFileHandler) Execute(ctx context.Context, argsJSON string) (string, error) {
	var input writeFileInput
	if err := json.Unmarshal([]byte(argsJSON), &input); err != nil {
		return "", fmt.Errorf("write_file: invalid args: %w", err)
	}
	if input.Path == "" {
		return "", fmt.Errorf("write_file: path is required")
	}

	// Create parent directories
	dir := filepath.Dir(input.Path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", fmt.Errorf("write_file: create dir %q: %w", dir, err)
		}
	}

	if err := os.WriteFile(input.Path, []byte(input.Content), 0644); err != nil {
		return "", fmt.Errorf("write_file: write %q: %w", input.Path, err)
	}

	return fmt.Sprintf(`{"status":"ok","path":%q,"size":%d}`, input.Path, len(input.Content)), nil
}

// ── editFileHandler ──────────────────────────────────────────────

type editFileHandler struct{}
type editFileInput struct {
	Path      string `json:"path"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

func (h *editFileHandler) Execute(ctx context.Context, argsJSON string) (string, error) {
	var input editFileInput
	if err := json.Unmarshal([]byte(argsJSON), &input); err != nil {
		return "", fmt.Errorf("edit_file: invalid args: %w", err)
	}
	if input.Path == "" || input.OldString == "" {
		return "", fmt.Errorf("edit_file: path and old_string are required")
	}

	data, err := os.ReadFile(input.Path)
	if err != nil {
		return "", fmt.Errorf("edit_file: read %q: %w", input.Path, err)
	}

	content := string(data)
	if !strings.Contains(content, input.OldString) {
		return "", fmt.Errorf("edit_file: old_string not found in %q", input.Path)
	}

	// Count occurrences
	count := strings.Count(content, input.OldString)
	newContent := strings.ReplaceAll(content, input.OldString, input.NewString)

	if err := os.WriteFile(input.Path, []byte(newContent), 0644); err != nil {
		return "", fmt.Errorf("edit_file: write %q: %w", input.Path, err)
	}

	return fmt.Sprintf(`{"status":"ok","path":%q,"replacements":%d}`, input.Path, count), nil
}

// ── globHandler ──────────────────────────────────────────────────

type globHandler struct{}
type globInput struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
}

func (h *globHandler) Execute(ctx context.Context, argsJSON string) (string, error) {
	var input globInput
	if err := json.Unmarshal([]byte(argsJSON), &input); err != nil {
		return "", fmt.Errorf("glob: invalid args: %w", err)
	}
	if input.Pattern == "" {
		return "[]", nil
	}
	root := input.Path
	if root == "" {
		root = "."
	}

	// Use double-star matching via filepath.Glob (limited) or manual Walk
	// filepath.Glob doesn't support **, so we use a Walk-based approach
	var results []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		matched, err := filepath.Match(input.Pattern, path)
		if err != nil {
			return nil
		}
		if matched {
			results = append(results, path)
		}
		// Also try matching just the base name
		if !matched {
			matched, err = filepath.Match(input.Pattern, info.Name())
			if matched && err == nil {
				results = append(results, path)
			}
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("glob: walk error: %w", err)
	}

	out, err := json.Marshal(results)
	if err != nil {
		return "", fmt.Errorf("glob: marshal: %w", err)
	}
	return string(out), nil
}

// ── bytes import usage ───────────────────────────────────────────
var _ = bytes.Contains
