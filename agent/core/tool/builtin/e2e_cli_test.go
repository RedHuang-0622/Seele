// Package builtin 的端到端集成测试。
// 需要真实 LLM API 配置，通过 -config 标志指定。
package builtin

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/RedHuang-0622/Seele/agent"
	"github.com/RedHuang-0622/Seele/agent/core/api"
	"github.com/RedHuang-0622/Seele/engine"
	"github.com/RedHuang-0622/Seele/types"
)

var configPath = os.Getenv("SEELE_CONFIG")

func init() {
	if configPath == "" {
		configPath = "../../../config/account-openai.yaml"
	}
}

// TestCLI_EndToEnd 用真实 LLM 验证工具调用是否完整链路正常。
// 需 LLM API Key，默认跳过。设置 RUN_E2E=true 启用。
func TestCLI_EndToEnd(t *testing.T) {
	if os.Getenv("RUN_E2E") != "true" {
		t.Skip("跳过端到端测试：设置 RUN_E2E=true 启用")
	}

	result, err := api.LoadFullAccountsConfig(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
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
		ToolCallTimeOut: 30 * time.Second,
		HubStartupDelay: 1,
	})
	if err != nil {
		t.Fatalf("agent init: %v", err)
	}
	defer agt.Shutdown()

	chatClient := agt.LLM().(*api.ChatClient)
	chatClient.WithAccountPool(pool)

	// 注册所有内置工具
	RegisterAll(agt.Tools())

	agt.RegisterTool("get_time", "获取当前时间",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		func(_ context.Context, _ string) (string, error) {
			return fmt.Sprintf(`"%s"`, time.Now().Format("2006-01-02 15:04:05")), nil
		},
	)

	eng := engine.New(agt, engine.WithSystemPrompt(
		"你是编码助手，可以使用内置工具。回答请简洁。用中文回复。"))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	t.Run("get_time", func(t *testing.T) {
		reply, err := eng.Chat(ctx, "现在几点？用 get_time 工具查一下")
		if err != nil {
			t.Fatalf("get_time: %v", err)
		}
		t.Logf("回复: %s", reply)
	})

	t.Run("grep_search", func(t *testing.T) {
		reply, err := eng.Chat(ctx, "搜索所有包含RegisterTool的Go文件")
		if err != nil {
			t.Fatalf("grep_search: %v", err)
		}
		t.Logf("回复: %s", reply)
	})

	t.Run("bash", func(t *testing.T) {
		reply, err := eng.Chat(ctx, "用bash执行go version看看当前版本")
		if err != nil {
			t.Fatalf("bash: %v", err)
		}
		t.Logf("回复: %s", reply)
	})
}
