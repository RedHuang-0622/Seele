package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/RedHuang-0622/Seele/agent/core/tool/interfaces"
	types "github.com/RedHuang-0622/Seele/types"
)

// GrepTool 提供文件系统搜索工具。
type GrepTool struct{}

// NewGrepTool 创建文件系统搜索工具。
func NewGrepTool() *GrepTool {
	return &GrepTool{}
}

func (g *GrepTool) ProviderName() string { return "builtin" }

func (g *GrepTool) Tools() []interfaces.ToolEntry {
	return []interfaces.ToolEntry{
		{
			Definition: types.Tool{
				Type: "function",
				Function: types.ToolFunction{
					Name:        "grep_search",
					Description: "搜索文件内容，支持 glob 模式匹配文件路径",
					Parameters: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"pattern":    map[string]interface{}{"type": "string", "description": "搜索的内容关键词"},
							"path":       map[string]interface{}{"type": "string", "description": "搜索根目录，默认 \".\""},
							"glob":       map[string]interface{}{"type": "string", "description": "文件 glob 模式过滤，如 \"*.go\""},
							"max_results": map[string]interface{}{"type": "integer", "description": "最大返回结果数，默认 20"},
						},
						"required": []string{"pattern"},
					},
				},
			},
			Handler: &grepHandler{},
		},
		{
			Definition: types.Tool{
				Type: "function",
				Function: types.ToolFunction{
					Name:        "read_file",
					Description: "读取指定文件的内容，支持行范围",
					Parameters: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"path":       map[string]interface{}{"type": "string", "description": "文件路径"},
							"start_line": map[string]interface{}{"type": "integer", "description": "起始行号，从 1 开始，默认 1"},
							"end_line":   map[string]interface{}{"type": "integer", "description": "结束行号，-1 表示文件末尾，默认 -1"},
						},
						"required": []string{"path"},
					},
				},
			},
			Handler: &readFileHandler{},
		},
	}
}

// ── grepHandler ─────────────────────────────────────────────────

type grepHandler struct{}

type grepInput struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path,omitempty"`
	Glob       string `json:"glob,omitempty"`
	MaxResults int    `json:"max_results,omitempty"`
}

type grepResult struct {
	Path    string `json:"path"`
	LineNum int    `json:"line_num"`
	Content string `json:"content"`
}

func (h *grepHandler) Execute(ctx context.Context, argsJSON string) (string, error) {
	var input grepInput
	if err := json.Unmarshal([]byte(argsJSON), &input); err != nil {
		return "", fmt.Errorf("grepHandler: invalid args: %w", err)
	}

	if input.Pattern == "" {
		return "[]", nil
	}
	if input.Path == "" {
		input.Path = "."
	}
	if input.MaxResults <= 0 {
		input.MaxResults = 20
	}

	var results []grepResult
	root := input.Path

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip inaccessible entries
		}

		// Skip directories
		if info.IsDir() {
			// Skip hidden directories
			if strings.HasPrefix(info.Name(), ".") && info.Name() != "." {
				return filepath.SkipDir
			}
			return nil
		}

		// Apply glob filter if specified
		if input.Glob != "" {
			matched, err := filepath.Match(input.Glob, info.Name())
			if err != nil || !matched {
				return nil
			}
		}

		// Check context cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Read file content
		data, err := os.ReadFile(path)
		if err != nil {
			return nil // skip unreadable files
		}

		lines := strings.Split(string(data), "\n")
		for lineNum, line := range lines {
			if strings.Contains(line, input.Pattern) {
				results = append(results, grepResult{
					Path:    path,
					LineNum: lineNum + 1,
					Content: strings.TrimSpace(line),
				})
				if len(results) >= input.MaxResults {
					return filepath.SkipAll
				}
			}
		}

		return nil
	})
	if err != nil && len(results) == 0 {
		return "", fmt.Errorf("grepHandler: walk failed: %w", err)
	}

	out, err := json.Marshal(results)
	if err != nil {
		return "", fmt.Errorf("grepHandler: marshal failed: %w", err)
	}
	return string(out), nil
}

// ── readFileHandler ─────────────────────────────────────────────

type readFileHandler struct{}

type readFileInput struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
}

func (h *readFileHandler) Execute(ctx context.Context, argsJSON string) (string, error) {
	var input readFileInput
	if err := json.Unmarshal([]byte(argsJSON), &input); err != nil {
		return "", fmt.Errorf("readFileHandler: invalid args: %w", err)
	}

	if input.Path == "" {
		return "", fmt.Errorf("readFileHandler: path is required")
	}

	if input.StartLine <= 0 {
		input.StartLine = 1
	}

	data, err := os.ReadFile(input.Path)
	if err != nil {
		return "", fmt.Errorf("readFileHandler: read file %q failed: %w", input.Path, err)
	}

	lines := strings.Split(string(data), "\n")

	startIdx := input.StartLine - 1
	if startIdx >= len(lines) {
		return "", fmt.Errorf("readFileHandler: start_line %d exceeds file length %d", input.StartLine, len(lines))
	}

	endIdx := len(lines)
	if input.EndLine > 0 && input.EndLine >= input.StartLine {
		endIdx = input.EndLine
	}
	if endIdx > len(lines) {
		endIdx = len(lines)
	}

	outLines := lines[startIdx:endIdx]
	result := strings.Join(outLines, "\n")

	// If the file ends without a trailing newline, re-add the separator
	// to keep output consistent with line-numbered display.
	return result, nil
}
