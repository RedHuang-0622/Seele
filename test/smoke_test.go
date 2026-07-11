// smoke_test.go — 真实 API 冒烟测试。
//
// 运行：
//   cd G:\Program\go\Seele
//   go test ./test/ -run Smoke -v -count=1 -timeout 120s
//
// 默认使用 config/account-openai.yaml，可通过环境变量覆盖：
//   $env:SMOKE_CONFIG="config/account-anthropic.yaml"
//   go test ./test/ -run Smoke -v -count=1 -timeout 120s
//
// 测试项：
//   Smoke_GetTime         → 简单无参数 tool_call
//   Smoke_GrepSearch      → 文件搜索 tool
//   Smoke_Bash            → shell 命令执行 tool
//   Smoke_MultiTool       → 多 tool 并发调用（验证 Anthropic 合并修复）
//   Smoke_GitStatus       → git 操作
//   Smoke_WriteFile       → 文件写入
package test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/RedHuang-0622/Seele/agent"
	"github.com/RedHuang-0622/Seele/agent/core/api"
	"github.com/RedHuang-0622/Seele/agent/core/tool/builtin"
	"github.com/RedHuang-0622/Seele/engine"
	"github.com/RedHuang-0622/Seele/types"
)

var smokeConfig = getEnv("SMOKE_CONFIG", "config/account-openai.yaml")

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func initAgent(t *testing.T) (*agent.Agent, *engine.Engine) {
	t.Helper()

	result, err := api.LoadFullAccountsConfig(smokeConfig)
	if err != nil {
		t.Fatalf("load config %q: %v", smokeConfig, err)
	}
	ls := result.LLMDefaults
	pool := result.Pool
	first := pool.All()[0]

	llmCfg := types.LLMConfig{
		BaseURL:     first.BaseURL,
		APIKey:      first.APIKey,
		Model:       first.Model,
		MaxTokens:   ls.MaxTokens,
		Timeout:     ls.Timeout,
		Temperature: ls.Temperature,
	}

	agt, err := agent.New(agent.Options{
		LLMConfig:       llmCfg,
		ToolCallTimeOut: 60 * time.Second,
		HubStartupDelay: 1,
	})
	if err != nil {
		t.Fatalf("agent init: %v", err)
	}
	t.Cleanup(func() { agt.Shutdown() })

	chatClient := agt.LLM().(*api.ChatClient)
	chatClient.WithAccountPool(pool)

	// Register built-in tools
	builtin.RegisterAll(agt.Tools())
	agt.RegisterTool(
		"get_time",
		"获取当前日期和时间",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		func(_ context.Context, _ string) (string, error) {
			return fmt.Sprintf(`"%s"`, time.Now().Format("2006-01-02 15:04:05")), nil
		},
	)

	t.Logf("Model: %s | Accounts: %d | Tools: %d",
		first.Model, len(pool.All()), len(agt.Tools().Tools()))

	return agt, engine.New(agt,
		engine.WithSystemPrompt("You are a test assistant. Use tools when asked. Keep responses short."),
	)
}

func runChat(t *testing.T, eng *engine.Engine, prompt string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	reply, err := eng.Chat(ctx, prompt)
	if err != nil {
		t.Fatalf("Chat(%q): %v", prompt, err)
	}
	return reply
}

// ── 测试用例 ──────────────────────────────────────────────────────────

// Smoke_GetTime 验证最基础的 tool_call 链路：prompt → LLM 识别需要工具 → 调用 → 返回结果。
func TestSmoke_GetTime(t *testing.T) {
	_, eng := initAgent(t)
	reply := runChat(t, eng, "现在几点？用 get_time 工具查一下")
	t.Logf("get_time reply: %s", reply)
	if reply == "" {
		t.Error("empty reply")
	}
}

// Smoke_GrepSearch 验证带参数的工具调用：LLM 正确解析参数并搜索文件。
func TestSmoke_GrepSearch(t *testing.T) {
	_, eng := initAgent(t)
	reply := runChat(t, eng, "搜索当前目录所有包含 'func main' 的 Go 文件")
	t.Logf("grep_search reply: %s", reply)
	if reply == "" {
		t.Error("empty reply")
	}
}

// Smoke_Bash 验证 shell 命令执行工具。
func TestSmoke_Bash(t *testing.T) {
	_, eng := initAgent(t)
	reply := runChat(t, eng, "用 bash 执行 go version 看看 Go 版本")
	t.Logf("bash reply: %s", reply)
	if reply == "" {
		t.Error("empty reply")
	}
}

// Smoke_MultiTool 验证单轮多个 tool_call 的兼容性。
// OpenAI 允许多条独立 tool 消息；Anthropic 需要合并为单条。
// 此测试覆盖两种 provider 的路径。
func TestSmoke_MultiTool(t *testing.T) {
	_, eng := initAgent(t)
	reply := runChat(t, eng, "同时做两件事：1) 用 get_time 查时间 2) 用 bash 执行 echo hello")
	t.Logf("multi_tool reply: %s", reply)
	if reply == "" {
		t.Error("empty reply")
	}
}

// Smoke_GitStatus 验证 git 工具。
func TestSmoke_GitStatus(t *testing.T) {
	_, eng := initAgent(t)
	reply := runChat(t, eng, "用 git_status 查看当前仓库状态")
	t.Logf("git_status reply: %s", reply)
	if reply == "" {
		t.Error("empty reply")
	}
}

// Smoke_WriteFile 验证文件写入工具。
func TestSmoke_WriteFile(t *testing.T) {
	dir := t.TempDir()
	_, eng := initAgent(t)
	reply := runChat(t, eng,
		fmt.Sprintf("用 write_file 在 %s/smoke.txt 写入内容 'smoke test ok'", dir))
	t.Logf("write_file reply: %s", reply)
	if reply == "" {
		t.Error("empty reply")
	}
}
