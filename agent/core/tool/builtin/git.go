package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/RedHuang-0622/Seele/agent/core/tool/interfaces"
	types "github.com/RedHuang-0622/Seele/types"
)

// GitTool provides git operation tools.
type GitTool struct{}

func NewGitTool() *GitTool { return &GitTool{} }
func (g *GitTool) ProviderName() string { return "builtin_git" }

func (g *GitTool) Tools() []interfaces.ToolEntry {
	return []interfaces.ToolEntry{
		{
			Definition: types.Tool{
				Type: "function",
				Function: types.ToolFunction{
					Name:        "git_status",
					Description: "显示当前 git 仓库的工作区状态（modified/added/deleted/untracked 文件列表）。",
					Parameters: map[string]interface{}{
						"type":       "object",
						"properties": map[string]interface{}{},
					},
				},
			},
			Handler: &gitHandler{action: "status"},
		},
		{
			Definition: types.Tool{
				Type: "function",
				Function: types.ToolFunction{
					Name:        "git_diff",
					Description: "显示当前未暂存的更改差异（working tree vs index）。",
					Parameters: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"path": map[string]interface{}{"type": "string", "description": "只显示指定文件或目录的 diff，可选"},
							"staged": map[string]interface{}{"type": "boolean", "description": "显示已暂存（staged）的 diff，默认 false"},
						},
					},
				},
			},
			Handler: &gitHandler{action: "diff"},
		},
		{
			Definition: types.Tool{
				Type: "function",
				Function: types.ToolFunction{
					Name:        "git_log",
					Description: "显示最近的 git commit 历史。",
					Parameters: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"count": map[string]interface{}{"type": "integer", "description": "显示的 commit 数量，默认 10"},
							"branch": map[string]interface{}{"type": "string", "description": "分支名，默认为当前分支"},
						},
					},
				},
			},
			Handler: &gitHandler{action: "log"},
		},
	}
}

// ── gitHandler ──────────────────────────────────────────────────

type gitHandler struct {
	action string
}

type gitLogInput struct {
	Count  int    `json:"count,omitempty"`
	Branch string `json:"branch,omitempty"`
}

type gitDiffInput struct {
	Path   string `json:"path,omitempty"`
	Staged bool   `json:"staged,omitempty"`
}

func (h *gitHandler) Execute(ctx context.Context, argsJSON string) (string, error) {
	switch h.action {
	case "status":
		return h.gitStatus(ctx)
	case "diff":
		return h.gitDiff(ctx, argsJSON)
	case "log":
		return h.gitLog(ctx, argsJSON)
	default:
		return "", fmt.Errorf("git: unknown action %q", h.action)
	}
}

func (h *gitHandler) gitStatus(ctx context.Context) (string, error) {
	out, err := runGit(ctx, "status", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("git status: %w", err)
	}
	if strings.TrimSpace(out) == "" {
		return `{"clean":true,"changes":[]}`, nil
	}

	type change struct {
		IndexState string `json:"index_state"`
		WorkState  string `json:"work_state"`
		Path       string `json:"path"`
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	changes := make([]change, 0, len(lines))
	for _, line := range lines {
		if len(line) < 4 {
			continue
		}
		changes = append(changes, change{
			IndexState: string(line[0]),
			WorkState:  string(line[1]),
			Path:       strings.TrimSpace(line[3:]),
		})
	}

	result := map[string]interface{}{
		"clean":   len(changes) == 0,
		"changes": changes,
	}
	b, _ := json.Marshal(result)
	return string(b), nil
}

func (h *gitHandler) gitDiff(ctx context.Context, argsJSON string) (string, error) {
	var input gitDiffInput
	json.Unmarshal([]byte(argsJSON), &input) // ignore parse errors, use defaults

	args := []string{"diff"}
	if input.Staged {
		args = []string{"diff", "--staged"}
	}
	if input.Path != "" {
		args = append(args, "--", input.Path)
	}

	out, err := runGit(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("git diff: %w", err)
	}
	if strings.TrimSpace(out) == "" {
		return `{"diff":"(no changes)"}`, nil
	}
	result := map[string]string{"diff": out}
	b, _ := json.Marshal(result)
	return string(b), nil
}

func (h *gitHandler) gitLog(ctx context.Context, argsJSON string) (string, error) {
	var input gitLogInput
	json.Unmarshal([]byte(argsJSON), &input)
	if input.Count <= 0 {
		input.Count = 10
	}

	args := []string{"log", fmt.Sprintf("--max-count=%d", input.Count), "--format= %h %s [%an, %ar]"}
	if input.Branch != "" {
		args = append(args, input.Branch)
	}

	out, err := runGit(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("git log: %w", err)
	}
	result := map[string]interface{}{
		"count": input.Count,
		"log":   strings.TrimSpace(out),
	}
	b, _ := json.Marshal(result)
	return string(b), nil
}

func runGit(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s", strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
