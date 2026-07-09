// test/tool_switch_test.go
//
// 工具调用 + 号池切换 联合测试。
// 依次加载 account-openai.yaml 和 account-anthropic.yaml，
// 每个配置下注册工具、发起含工具调用的对话、验证调度链路。
package test

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"

	"github.com/RedHuang-0622/Seele/agent"
	"github.com/RedHuang-0622/Seele/agent/core/api"
	"github.com/RedHuang-0622/Seele/engine"
	"github.com/RedHuang-0622/Seele/types"
)

var globalCounter int64

// =============================================================================
// OpenAI 格式 + 工具调用
// =============================================================================
func TestToolSwitch_OpenAI(t *testing.T) {
	result, err := api.LoadFullAccountsConfig("../config/account-openai.yaml")
	if err != nil {
		t.Skipf("加载 account-openai.yaml 失败: %v（跳过）", err)
	}
	testToolCall(t, "openai", result.Pool, result.LLMDefaults)
}

// =============================================================================
// Anthropic 格式 + 工具调用
// =============================================================================
func TestToolSwitch_Anthropic(t *testing.T) {
	result, err := api.LoadFullAccountsConfig("../config/account-anthropic.yaml")
	if err != nil {
		t.Skipf("加载 account-anthropic.yaml 失败: %v（跳过）", err)
	}
	testToolCall(t, "anthropic", result.Pool, result.LLMDefaults)
}

// =============================================================================
// 号池切换
// =============================================================================
func TestToolSwitch_PoolRouting(t *testing.T) {
	result, err := api.LoadFullAccountsConfig("../config/account-openai.yaml")
	if err != nil {
		t.Skipf("加载失败: %v（跳过）", err)
	}
	pool := result.Pool
	if len(pool.All()) < 1 {
		t.Skip("号池为空")
	}
	a1 := pool.Get()
	a2 := pool.GetByProvider(api.ProviderOpenAI)
	if a2 == nil {
		t.Fatal("GetByProvider(openai) 返回 nil")
	}
	t.Logf("round-robin: %q, filter: %q", a1.Name, a2.Name)
}

// =============================================================================
// 核心：工具调用测试
// =============================================================================
func testToolCall(t *testing.T, label string, pool *api.AccountPool, ls api.LLMConfigEntry) {
	if os.Getenv("TEST_INTEGRATION") == "" {
		t.Skip("set TEST_INTEGRATION=1 to run (needs real API key)")
	}
	t.Helper()
	acct := pool.Get()
	if acct == nil {
		t.Skip("无可用账号")
	}

	atomic.StoreInt64(&globalCounter, 0)
	ctx := context.Background()

	llmCfg := types.LLMConfig{
		BaseURL:     acct.BaseURL,
		APIKey:      acct.APIKey,
		Model:       acct.Model,
		MaxTokens:   ls.MaxTokens,
		Timeout:     ls.Timeout,
		Temperature: ls.Temperature,
	}

	agt, err := agent.New(agent.Options{
		LLMConfig:       llmCfg,
		HubStartupDelay: 10,
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	defer agt.Shutdown()

	chatClient := agt.LLM().(*api.ChatClient)
	chatClient.WithAccountPool(pool)
	if ls.Provider != "" {
		chatClient.SetProvider(ls.Provider)
	}

	var callCount int
	agt.RegisterTool(
		"counter",
		"计数器：inc 加一，返回当前值",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"action": map[string]interface{}{"type": "string", "description": "inc/reset"},
			},
		},
		func(_ context.Context, argsJSON string) (string, error) {
			callCount++
			n := atomic.AddInt64(&globalCounter, 1)
			return fmt.Sprintf(`{"value":%d}`, n), nil
		},
	)

	eng := engine.New(agt, engine.WithSystemPrompt(
		"你是一个会使用计数工具的助手。当前使用 "+label+" 格式。",
	))

	// 第一轮：基础对话
	t.Logf("[%s] 第一轮", label)
	r1, err := eng.Chat(ctx, "你好，简单介绍一下自己。")
	if err != nil {
		t.Fatalf("[%s] 第一轮失败: %v", label, err)
	}
	if r1 == "" {
		t.Fatalf("[%s] 第一轮回复为空", label)
	}
	t.Logf("[%s] ✅ 第一轮回复: %.60s...", label, r1)

	// 第二轮：工具调用
	t.Logf("[%s] 第二轮（工具调用）", label)
	r2, err := eng.Chat(ctx, "请调用 counter 工具计数一次，然后告诉我结果。")
	if err != nil {
		t.Logf("[%s] 第二轮 error: %v", label, err)
		hasTool := false
		for _, m := range eng.History() {
			if m.Role == "tool" {
				hasTool = true
				break
			}
		}
		if hasTool {
			t.Logf("[%s] ⚡ tools 已注入 history（ReAct 循环工作）", label)
		}
	} else {
		t.Logf("[%s] ✅ 第二轮回复: %.60s...", label, r2)
		if callCount > 0 {
			t.Logf("[%s] ⚡ counter 被调用 %d 次", label, callCount)
		}
	}

	// 第三轮：历史记忆
	t.Logf("[%s] 第三轮（历史记忆）", label)
	r3, err := eng.Chat(ctx, "刚才你做了什么操作？")
	if err != nil {
		t.Logf("[%s] 第三轮 error: %v", label, err)
	} else {
		t.Logf("[%s] ✅ 第三轮回复: %.60s...", label, r3)
	}

	// 历史摘要
	t.Logf("[%s] 共 %d 条消息", label, len(eng.History()))
	for i, m := range eng.History() {
		c := ""
		if m.Content != nil {
			c = *m.Content
		}
		if len(c) > 50 {
			c = c[:50] + "..."
		}
		if len(m.ToolCalls) > 0 {
			c = fmt.Sprintf("[tool_calls: %d]", len(m.ToolCalls))
		}
		t.Logf("  [%d] %-9s %s", i, m.Role, c)
	}
}

// =============================================================================
// 配置+策略注册 离线验证
// =============================================================================
func TestToolSwitch_ConfigAndRegistry(t *testing.T) {
	ps := api.ProviderStrategyNames()
	t.Logf("ProviderStrategy: %v", ps)
	if !hasName(ps, "openai") {
		t.Error("openai strategy 未注册")
	}
	if !hasName(ps, "anthropic") {
		t.Error("anthropic strategy 未注册")
	}

	if r1, err := api.LoadFullAccountsConfig("../config/account-openai.yaml"); err != nil {
		t.Logf("account-openai.yaml 不存在，跳过")
	} else {
		if r1.LLMDefaults.Provider != api.ProviderOpenAI {
			t.Errorf("openai provider=%q", r1.LLMDefaults.Provider)
		}
		t.Logf("openai: %d accounts", len(r1.Pool.All()))
	}

	if r2, err := api.LoadFullAccountsConfig("../config/account-anthropic.yaml"); err != nil {
		t.Logf("account-anthropic.yaml 不存在，跳过")
	} else {
		if r2.LLMDefaults.Provider != api.ProviderAnthropic {
			t.Errorf("anthropic provider=%q", r2.LLMDefaults.Provider)
		}
		t.Logf("anthropic: %d accounts", len(r2.Pool.All()))
	}
}

func hasName(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
