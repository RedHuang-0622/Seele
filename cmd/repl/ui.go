package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/RedHuang-0622/Seele/agent"
	"github.com/RedHuang-0622/Seele/agent/core/api"
	"github.com/RedHuang-0622/Seele/engine"
	"github.com/RedHuang-0622/Seele/seelectx/tracer"
)

// ── 可视化回调 ──────────────────────────────────────────────────────

func buildHooks() *engine.LoopHooks {
	return &engine.LoopHooks{
		OnLLMStart: func(ctx context.Context, info engine.LLMInfo) {
			if info.Turn == 0 {
				fmt.Printf("\n  -- Round %d --\n", info.Turn+1)
			} else {
				fmt.Printf("\n  -- Round %d (tool loop) --\n", info.Turn+1)
			}
		},
		OnLLMComplete: func(ctx context.Context, info engine.LLMInfo) {
			if len(info.ToolCalls) > 0 {
				names := make([]string, len(info.ToolCalls))
				for i, tc := range info.ToolCalls {
					names[i] = tc.Function.Name
				}
				fmt.Printf("  calling: %s\n", strings.Join(names, ", "))
				if info.Usage != nil {
					fmt.Printf("    tokens: ↑%d ↓%d\n",
						info.Usage.PromptTokens, info.Usage.CompletionTokens)
				}
			}
		},
		OnToolStart: func(ctx context.Context, info engine.ToolCallInfo) {
			args := tryFormatArgs(info.Arguments)
			fmt.Printf("  \033[33m✎ %s\033[0m(%s)\n", info.Name, args)
		},
		OnToolComplete: func(ctx context.Context, info engine.ToolCallInfo) {
			if info.Error != nil {
				fmt.Printf("  \033[31m  ✖ %s\033[0m -> %v\n", info.Name, info.Error)
			} else {
				preview := truncateDisplay(info.Result, 180)
				fmt.Printf("  \033[32m  ✓ %s\033[0m (%v)\n", info.Name, info.Duration.Round(time.Millisecond))
				if preview != "" {
					fmt.Printf("    %s\n", preview)
				}
			}
		},
	}
}

func tryFormatArgs(raw string) string {
	if raw == "{}" || raw == "" {
		return ""
	}
	return truncateDisplay(raw, 100)
}

func truncateDisplay(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func extractTokens(eng *engine.Engine) string {
	if tree := eng.ExportTrace(); tree != nil && tree.Root != nil {
		for _, c := range tree.Root.Children {
			if c.Kind == tracer.SpanLLMCall {
				if t, ok := c.Attrs["total_tokens"]; ok {
					return t
				}
			}
		}
	}
	return "?"
}

// ── 欢迎 ────────────────────────────────────────────────────────────

func printWelcome(model string, pf api.ProviderType, pool *api.AccountPool, agt *agent.Agent) {
	fmt.Printf("\n  Seele CLI\n")
	fmt.Println(strings.Repeat("-", 50))
	if pool != nil {
		for _, a := range pool.All() {
			s := "●"
			if a.Disabled {
				s = "○"
			}
			fmt.Printf("  %s [%s] %s -> %s\n", s, a.Provider, a.Name, a.BaseURL)
		}
	}
	fmt.Printf("  %s  %s\n", model, providerLabel(pf))
	fmt.Printf("  %d tools loaded\n", len(agt.Tools().Tools()))
	fmt.Print("\n  输入 / 查看命令，LLM 可自动调用 switch_mode 切换模式\n\n")
}
