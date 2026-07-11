package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/RedHuang-0622/Seele/agent/core/tool/interfaces"
	types "github.com/RedHuang-0622/Seele/types"
)

// ShellTool provides shell command execution tools.
type ShellTool struct {
	DefaultTimeout time.Duration
}

func NewShellTool() *ShellTool {
	return &ShellTool{DefaultTimeout: 30 * time.Second}
}

func (s *ShellTool) ProviderName() string { return "builtin_shell" }

func (s *ShellTool) Tools() []interfaces.ToolEntry {
	return []interfaces.ToolEntry{
		{
			Definition: types.Tool{
				Type: "function",
				Function: types.ToolFunction{
					Name:        "bash",
					Description: "在系统的默认 shell 中执行命令。支持管道、重定向、变量。返回 stdout + stderr + exit_code。超时后进程会被 kill。",
					Parameters: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"command": map[string]interface{}{"type": "string", "description": "要执行的 shell 命令"},
							"timeout": map[string]interface{}{"type": "integer", "description": "超时秒数，默认 30。0 表示使用默认超时。"},
							"workdir": map[string]interface{}{"type": "string", "description": "工作目录，默认为当前目录"},
						},
						"required": []string{"command"},
					},
				},
			},
			Handler: &bashHandler{defaultTimeout: s.DefaultTimeout},
		},
	}
}

// ── bashHandler ─────────────────────────────────────────────────

type bashHandler struct {
	defaultTimeout time.Duration
}

type bashInput struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
	Workdir string `json:"workdir,omitempty"`
}

type bashResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

func (h *bashHandler) Execute(ctx context.Context, argsJSON string) (string, error) {
	var input bashInput
	if err := json.Unmarshal([]byte(argsJSON), &input); err != nil {
		return "", fmt.Errorf("bash: invalid args: %w", err)
	}
	if input.Command == "" {
		return `{"stdout":"","stderr":"","exit_code":0}`, nil
	}

	// Determine shell
	shell := "sh"
	shellFlag := "-c"
	if _, err := os.Stat("/bin/bash"); err == nil {
		shell = "bash"
	} else if _, err := os.Stat("C:\\Windows\\System32\\cmd.exe"); err == nil {
		// Windows
		shell = "cmd.exe"
		shellFlag = "/c"
		// Check if PowerShell is available
		if _, err := os.Stat("C:\\Windows\\System32\\WindowsPowerShell\\v1.0\\powershell.exe"); err == nil {
			shell = "powershell"
			shellFlag = "-Command"
		}
	}

	cmd := exec.CommandContext(ctx, shell, shellFlag, input.Command)
	if input.Workdir != "" {
		cmd.Dir = input.Workdir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Determine timeout
	timeout := h.defaultTimeout
	if input.Timeout > 0 {
		timeout = time.Duration(input.Timeout) * time.Second
	}

	// Create timeout context
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("bash: start: %w", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case <-runCtx.Done():
		cmd.Process.Kill()
		return "", fmt.Errorf("bash: timeout after %v", timeout)
	case err := <-done:
		exitCode := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				return "", fmt.Errorf("bash: %w", err)
			}
		}

		result := bashResult{
			Stdout:   strings.TrimSpace(stdout.String()),
			Stderr:   strings.TrimSpace(stderr.String()),
			ExitCode: exitCode,
		}
		out, _ := json.Marshal(result)
		return string(out), nil
	}
}
