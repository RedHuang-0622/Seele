// cmd/repl/main.go — Seele CLI 编码助手终端。
//
// LLM 可以通过 switch_mode 工具自主切换插件（read/write/git/shell），
// 实现"专人专事"：读代码时只暴露搜索工具，改代码时只暴露编辑工具。
//
// 运行：
//   go run ./cmd/repl/ -c config/account-openai.yaml
//
// 命令：
//   /              命令菜单
//   /plugins       列出插件
//   /plugin <name> 手动切换插件（工具集+prompt同步切换）
//   /prompts       列出 prompt 文件
//   /prompt <name> 仅切换 prompt
//   LLM 也可通过 switch_mode 工具自主切换

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/RedHuang-0622/Seele/agent"
	"github.com/RedHuang-0622/Seele/agent/core/api"
	"github.com/RedHuang-0622/Seele/agent/core/tool/builtin"
	"github.com/RedHuang-0622/Seele/agent/core/tool/holder"
	"github.com/RedHuang-0622/Seele/contexts/tracer"
	"github.com/RedHuang-0622/Seele/engine"
	"github.com/RedHuang-0622/Seele/types"
)

var configPath = flag.String("c", "config/account-openai.yaml", "LLM 配置路径")

// ── 插件定义 ──────────────────────────────────────────────────────────
// 每个插件 = 工具过滤规则。include 空 = 全部工具可见。
// switch_mode 内置于所有插件中，LLM 可自主调用切换。

type pluginDef struct {
	Name        string
	Description string
	Include     []string
	Exclude     []string
}

var plugins = []pluginDef{
	{"default", "所有工具可用", nil, nil},
	{"read", "阅读/搜索模式", []string{"switch_mode", "grep*", "read_file", "glob", "git_status", "git_log", "git_diff", "get_time"}, nil},
	{"write", "编辑模式", []string{"switch_mode", "write*", "edit*", "read_file", "bash", "git_diff", "git_status", "get_time"}, nil},
	{"git", "Git 模式", []string{"switch_mode", "git_*", "bash", "get_time"}, nil},
	{"shell", "Shell/DevOps 模式", []string{"switch_mode", "bash", "get_time"}, nil},
}

func initPlugins(agt *agent.Agent) {
	pm := holder.NewPluginManager()
	for _, p := range plugins {
		pm.Define(holder.NewPlugin(p.Name, p.Description, p.Include, p.Exclude))
	}
	agt.Tools().WithPluginManager(pm)
}

func main() {
	flag.Parse()
	ctx := context.Background()

	// ── Agent ──────────────────────────────────────────────────────────
	result, err := api.LoadFullAccountsConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✖ 加载配置失败: %v\n", err)
		os.Exit(1)
	}
	ls := result.LLMDefaults
	pool := result.Pool
	first := pool.All()[0]

	llmCfg := types.LLMConfig{
		BaseURL: first.BaseURL, APIKey: first.APIKey, Model: first.Model,
		MaxTokens: ls.MaxTokens, Timeout: ls.Timeout, Temperature: ls.Temperature,
	}

	agt, err := agent.New(agent.Options{
		LLMConfig: llmCfg, ToolCallTimeOut: 120 * time.Second, HubStartupDelay: 10,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "✖ Agent 初始化失败: %v\n", err)
		os.Exit(1)
	}
	defer agt.Shutdown()

	chatClient := agt.LLM().(*api.ChatClient)
	chatClient.WithAccountPool(pool)
	if ls.Provider != "" {
		chatClient.SetProvider(ls.Provider)
	}

	// ── 工具注册 ──────────────────────────────────────────────────────
	builtin.RegisterAll(agt.Tools())
	agt.RegisterTool("get_time", "获取当前日期和时间",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		func(_ context.Context, _ string) (string, error) {
			return fmt.Sprintf(`"%s"`, time.Now().Format("2006-01-02 15:04:05")), nil
		},
	)

	// ── 插件系统 ──────────────────────────────────────────────────────
	initPlugins(agt)

	// ── Prompt 文件加载 ───────────────────────────────────────────────
	prompts := loadPrompts()
	defaultPrompt := prompts["default"]
	if defaultPrompt == "" {
		defaultPrompt = `You are Seele CLI, an intelligent coding assistant.

You can switch between specialized modes using the switch_mode tool:
- read: code search and reading (grep, read_file, glob)
- write: file editing (write_file, edit_file)
- git: git operations (git_status, git_diff, git_log)
- shell: command execution (bash)
- default: all tools available

When you need tools outside your current mode, call switch_mode to change.
Always respond in the user's language.`
	}

	// ── Engine ────────────────────────────────────────────────────────
	tr := tracer.NewSimpleTracer()
	hooks := buildHooks()

	eng := engine.New(agt,
		engine.WithTracer(tr),
		engine.WithHooks(hooks),
		engine.WithSystemPrompt(defaultPrompt),
	)

	// ── 注册 switch_mode 工具（闭包捕获 eng/agt/prompts）────────────
	agt.RegisterTool(
		"switch_mode",
		"切换工作模式以改变可用工具集。模式包括：default(全部), read(搜索/读取), write(编辑), git(版本控制), shell(命令执行)。切换后后续回合自动生效。",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"mode": map[string]interface{}{
					"type": "string",
					"enum": []interface{}{"default", "read", "write", "git", "shell"},
					"description": "目标模式",
				},
			},
			"required": []string{"mode"},
		},
		func(_ context.Context, argsJSON string) (string, error) {
			var input struct {
				Mode string `json:"mode"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &input); err != nil {
				return "", fmt.Errorf("switch_mode: %w", err)
			}
			mode := strings.ToLower(input.Mode)

			// 切换插件
			if mode == "" || mode == "default" {
				agt.Tools().DeactivatePlugin()
			} else {
				if err := agt.Tools().ActivatePlugin(mode); err != nil {
					return fmt.Sprintf(`{"error":"unknown mode: %s"}`, mode), nil
				}
			}

			// 切换 prompt
			if text, ok := prompts[mode]; ok {
				eng.SetSystemPrompt(text)
			}

			visible := agt.VisibleTools(context.Background())
			all := agt.Tools().Tools()
			return fmt.Sprintf(`{"mode":"%s","visible_tools":%d,"total_tools":%d}`,
				mode, len(visible), len(all)), nil
		},
	)

	// ── 欢迎 ──────────────────────────────────────────────────────────
	printWelcome(first.Model, chatClient.ProviderFilter(), pool, agt)

	// ── 信号处理（Ctrl+C 优雅退出）────────────────────────────────────
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			fmt.Println("\n  👋 正在关闭...")
			stop()
			agt.Shutdown()
		})
	}
	go func() {
		<-ctx.Done()
		cleanup()
		os.Exit(0)
	}()

	// ── 主循环（流式输出）─────────────────────────────────────────────
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("  > ")
		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}
		if strings.HasPrefix(input, "/") {
			handleCommand(input, chatClient, eng, agt, llmCfg.Model, prompts)
			continue
		}

		// 流式 Chat
		start := time.Now()
		_, err := eng.ChatStream(ctx, input, func(chunk string) {
			fmt.Print(chunk)
		})
		elapsed := time.Since(start).Seconds()
		if err != nil {
			fmt.Printf("\n  ✖ %v\n", err)
			continue
		}

		// 元信息条
		totalTokens := extractTokens(eng)
		pf := chatClient.ProviderFilter()
		plugin := agt.Tools().ActivePlugin()
		tag := ""
		if plugin != "" && plugin != "default" {
			tag = " [" + plugin + "]"
		}
		fmt.Printf("\n  [%s%s %stok %.1fs]\n\n", providerLabel(pf), tag, totalTokens, elapsed)
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "  ✖ 读取输入错误: %v\n", err)
	}
}

// ── Prompt 文件加载 ──────────────────────────────────────────────────

func loadPrompts() map[string]string {
	prompts := make(map[string]string)
	for _, dir := range []string{"prompts", "cmd/repl/prompts"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				name := strings.TrimSuffix(e.Name(), ".md")
				data, err := os.ReadFile(filepath.Join(dir, e.Name()))
				if err == nil {
					prompts[name] = string(data)
				}
			}
		}
		if len(prompts) > 0 {
			break
		}
	}
	return prompts
}

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
			if a.Disabled { s = "○" }
			fmt.Printf("  %s [%s] %s -> %s\n", s, a.Provider, a.Name, a.BaseURL)
		}
	}
	fmt.Printf("  %s  %s\n", model, providerLabel(pf))
	fmt.Printf("  %d tools loaded\n", len(agt.Tools().Tools()))
	fmt.Print("\n  输入 / 查看命令，LLM 可自动调用 switch_mode 切换模式\n\n")
}

// ── 命令处理 ────────────────────────────────────────────────────────

type cmdDef struct{ Name, Args, Desc, Category string }

var cmdMenu = []cmdDef{
	{"/exit", "", "退出", "system"},
	{"/help", "", "帮助", "system"},
	{"/model", "", "模型/插件信息", "system"},
	{"/maxloops", "<n>", "设置最大 tool 循环（默认 25）", "system"},
	{"/switch", "<provider>", "切换 provider", "provider"},
	{"/pool", "", "号池状态", "provider"},
	{"/plugin", "", "查看当前插件", "mode"},
	{"/plugin", "<name>", "切换插件", "mode"},
	{"/plugins", "", "列出插件", "mode"},
	{"/prompts", "", "列出 prompt 文件", "mode"},
	{"/prompt", "<name>", "切换 prompt", "mode"},
	{"/tools", "", "当前可见工具", "mode"},
	{"/history", "", "历史", "session"},
	{"/clear", "", "清空历史", "session"},
	{"/trace", "", "追踪树", "session"},
}

func printCmdMenu() {
	catOrder := []struct{ Key, Title string }{
		{"system", "系统"}, {"provider", "Provider"},
		{"mode", "模式和工具"}, {"session", "会话"},
	}
	for _, cat := range catOrder {
		items := filterCmds(cat.Key)
		if len(items) == 0 { continue }
		fmt.Printf("  %s\n", cat.Title)
		for _, c := range items {
			display := c.Name
			if c.Args != "" { display = c.Name + " " + c.Args }
			fmt.Printf("    %-24s %s\n", display, c.Desc)
		}
	}
}

func filterCmds(cat string) []cmdDef {
	var out []cmdDef
	for _, c := range cmdMenu {
		if c.Category == cat { out = append(out, c) }
	}
	return out
}

func handleCommand(raw string, cc *api.ChatClient, eng *engine.Engine, agt *agent.Agent, model string, prompts map[string]string) {
	parts := strings.Fields(raw)
	if len(parts) == 0 { return }
	cmd := strings.ToLower(parts[0])
	args := parts[1:]

	if cmd == "/" { printCmdMenu(); return }

	switch cmd {
	case "/exit", "/quit":
		fmt.Println("  👋 再见！")
		agt.Shutdown()
		os.Exit(0)
	case "/help":
		fmt.Println(); printCmdMenu(); fmt.Println()

	case "/model":
		p := pluginLabel(agt)
		fmt.Printf("  Model: %s%s\n", model, p)
		fmt.Printf("  Provider: %s\n", providerLabel(cc.ProviderFilter()))

	case "/maxloops":
		if len(args) < 1 {
			fmt.Println("  当前 MaxLoops: 25")
			fmt.Println("  用法: /maxloops <循环次数>")
			return
		}
		n := 25
		if _, err := fmt.Sscanf(args[0], "%d", &n); err == nil && n > 0 {
			eng.SetMaxLoops(n)
			fmt.Printf("  MaxLoops -> %d\n", n)
		}

	// ── Plugin ──────────────────────────────────────────────────
	case "/plugin":
		if len(args) < 1 {
			fmt.Printf("  当前: %s\n", pluginLabel(agt))
			fmt.Println("  用法: /plugin <name>  (/plugins 查看列表)")
			return
		}
		name := strings.ToLower(args[0])
		if name == "default" || name == "" {
			agt.Tools().DeactivatePlugin()
		} else if err := agt.Tools().ActivatePlugin(name); err != nil {
			fmt.Printf("  ✖ %v\n", err); return
		}
		if text, ok := prompts[name]; ok {
			eng.SetSystemPrompt(text)
		}
		visible := len(agt.VisibleTools(context.Background()))
		fmt.Printf("  -> %s  (%d tools)\n", name, visible)

	case "/plugins":
		pm := agt.Tools().Plugin()
		if pm == nil { fmt.Println("  (无插件系统)"); return }
		all := pm.AllPlugins(); sort.Strings(all)
		active := agt.Tools().ActivePlugin()
		for _, name := range all {
			p, _ := pm.Plugin(name)
			mark := "  "
			if name == active { mark = " ›" }
			fmt.Printf(" %s %-12s %s\n", mark, name, p.Description)
		}

	case "/prompts":
		if len(prompts) == 0 { fmt.Println("  (无 prompt 文件)"); return }
		names := make([]string, 0, len(prompts))
		for n := range prompts { names = append(names, n) }
		sort.Strings(names)
		for _, n := range names {
			fmt.Printf("    %s\n", n)
		}

	case "/tools":
		tools := agt.VisibleTools(context.Background())
		all := agt.Tools().Tools()
		p := pluginLabel(agt)
		fmt.Printf("  %d/%d tools%s\n", len(tools), len(all), p)
		for _, t := range tools {
			fmt.Printf("    * %s\n", t.Function.Name)
		}

	// ── Provider ────────────────────────────────────────────────
	case "/switch":
		if len(args) < 1 {
			fmt.Printf("  当前: %s\n", providerLabel(cc.ProviderFilter()))
			if pool := cc.AccountPool(); pool != nil {
				seen := make(map[api.ProviderType]bool)
				for _, a := range pool.All() {
					if !seen[a.Provider] {
						fmt.Printf("  可用: %s ", a.Provider); seen[a.Provider] = true
					}
				}
				fmt.Println()
			}
			return
		}
		target := api.ProviderType(strings.ToLower(args[0]))
		if pool := cc.AccountPool(); pool != nil {
			has := false
			for _, a := range pool.All() { if a.Provider == target { has = true; break } }
			if !has { fmt.Printf("  ✖ 没有 %q 类型的账号\n", target); return }
		}
		cc.SetProviderFilter(target)
		fmt.Printf("  -> %s\n", target)

	case "/pool":
		pool := cc.AccountPool()
		if pool == nil { fmt.Println("  (单账号模式)"); return }
		for _, a := range pool.All() {
			s := "●"; if a.Disabled { s = "○" }
			fmt.Printf("  %s [%s] %s -> %s\n", s, a.Provider, a.Name, a.BaseURL)
		}
		fmt.Printf("  当前: %s\n", providerLabel(cc.ProviderFilter()))

	case "/trace":
		tree := eng.ExportTrace()
		if tree == nil || tree.Root == nil { fmt.Println("  (暂无追踪数据)"); return }
		for _, line := range strings.Split(tree.String(), "\n") { fmt.Println("  " + line) }

	case "/history":
		hist := eng.History()
		if len(hist) == 0 { fmt.Println("  (空)"); return }
		fmt.Printf("  %d 条\n", len(hist))
		for i, m := range hist {
			c := ""
			if m.Content != nil { c = *m.Content }
			if len(c) > 120 { c = c[:120] + "..." }
			if len(m.ToolCalls) > 0 { c = "[tool: " + m.ToolCalls[0].Function.Name + "]" }
			fmt.Printf("  [%d] %-9s %s\n", i, m.Role, c)
		}

	case "/clear":
		eng.ClearHistory(); fmt.Println("  已清空")

	default:
		fmt.Printf("  ✖ 未知命令: %s\n", cmd)
		fmt.Print("  输入 / 查看可用命令\n")
	}
}

func pluginLabel(agt *agent.Agent) string {
	p := agt.Tools().ActivePlugin()
	if p == "" || p == "default" { return "" }
	return " [" + p + "]"
}

func providerLabel(p api.ProviderType) string {
	if p == "" { return "round-robin" }
	return string(p)
}
