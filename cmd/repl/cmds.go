package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/RedHuang-0622/Seele/agent"
	"github.com/RedHuang-0622/Seele/agent/core/api"
	"github.com/RedHuang-0622/Seele/engine"
)

// ── 命令定义 ────────────────────────────────────────────────────────

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

// ── 命令处理 ────────────────────────────────────────────────────────

func handleCommand(raw string, cc *api.ChatClient, eng *engine.Engine, agt *agent.Agent, model string, prompts map[string]string) {
	parts := strings.Fields(raw)
	if len(parts) == 0 {
		return
	}
	cmd := strings.ToLower(parts[0])
	args := parts[1:]

	if cmd == "/" {
		printCmdMenu()
		return
	}

	switch cmd {
	case "/exit", "/quit":
		fmt.Println("  👋 再见！")
		agt.Shutdown()
		os.Exit(0)
	case "/help":
		fmt.Println()
		printCmdMenu()
		fmt.Println()

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
			fmt.Printf("  ✖ %v\n", err)
			return
		}
		if text, ok := prompts[name]; ok {
			eng.SetSystemPrompt(text)
		}
		visible := len(agt.VisibleTools(context.Background()))
		fmt.Printf("  -> %s  (%d tools)\n", name, visible)

	case "/plugins":
		pm := agt.Tools().Plugin()
		if pm == nil {
			fmt.Println("  (无插件系统)")
			return
		}
		all := pm.AllPlugins()
		sort.Strings(all)
		active := agt.Tools().ActivePlugin()
		for _, name := range all {
			p, _ := pm.Plugin(name)
			mark := "  "
			if name == active {
				mark = " ›"
			}
			fmt.Printf(" %s %-12s %s\n", mark, name, p.Description)
		}

	case "/prompts":
		if len(prompts) == 0 {
			fmt.Println("  (无 prompt 文件)")
			return
		}
		names := make([]string, 0, len(prompts))
		for n := range prompts {
			names = append(names, n)
		}
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
						fmt.Printf("  可用: %s ", a.Provider)
						seen[a.Provider] = true
					}
				}
				fmt.Println()
			}
			return
		}
		target := api.ProviderType(strings.ToLower(args[0]))
		if pool := cc.AccountPool(); pool != nil {
			has := false
			for _, a := range pool.All() {
				if a.Provider == target {
					has = true
					break
				}
			}
			if !has {
				fmt.Printf("  ✖ 没有 %q 类型的账号\n", target)
				return
			}
		}
		cc.SetProviderFilter(target)
		fmt.Printf("  -> %s\n", target)

	case "/pool":
		pool := cc.AccountPool()
		if pool == nil {
			fmt.Println("  (单账号模式)")
			return
		}
		for _, a := range pool.All() {
			s := "●"
			if a.Disabled {
				s = "○"
			}
			fmt.Printf("  %s [%s] %s -> %s\n", s, a.Provider, a.Name, a.BaseURL)
		}
		fmt.Printf("  当前: %s\n", providerLabel(cc.ProviderFilter()))

	case "/trace":
		tree := eng.ExportTrace()
		if tree == nil || tree.Root == nil {
			fmt.Println("  (暂无追踪数据)")
			return
		}
		for _, line := range strings.Split(tree.String(), "\n") {
			fmt.Println("  " + line)
		}

	case "/history":
		hist := eng.History()
		if len(hist) == 0 {
			fmt.Println("  (空)")
			return
		}
		fmt.Printf("  %d 条\n", len(hist))
		for i, m := range hist {
			c := ""
			if m.Content != nil {
				c = *m.Content
			}
			if len(c) > 120 {
				c = c[:120] + "..."
			}
			if len(m.ToolCalls) > 0 {
				c = "[tool: " + m.ToolCalls[0].Function.Name + "]"
			}
			fmt.Printf("  [%d] %-9s %s\n", i, m.Role, c)
		}

	case "/clear":
		eng.ClearHistory()
		fmt.Println("  已清空")

	default:
		fmt.Printf("  ✖ 未知命令: %s\n", cmd)
		fmt.Print("  输入 / 查看可用命令\n")
	}
}

// ── 命令菜单 ────────────────────────────────────────────────────────

func printCmdMenu() {
	catOrder := []struct{ Key, Title string }{
		{"system", "系统"}, {"provider", "Provider"},
		{"mode", "模式和工具"}, {"session", "会话"},
	}
	for _, cat := range catOrder {
		items := filterCmds(cat.Key)
		if len(items) == 0 {
			continue
		}
		fmt.Printf("  %s\n", cat.Title)
		for _, c := range items {
			display := c.Name
			if c.Args != "" {
				display = c.Name + " " + c.Args
			}
			fmt.Printf("    %-24s %s\n", display, c.Desc)
		}
	}
}

func filterCmds(cat string) []cmdDef {
	var out []cmdDef
	for _, c := range cmdMenu {
		if c.Category == cat {
			out = append(out, c)
		}
	}
	return out
}

func pluginLabel(agt *agent.Agent) string {
	p := agt.Tools().ActivePlugin()
	if p == "" || p == "default" {
		return ""
	}
	return " [" + p + "]"
}

func providerLabel(p api.ProviderType) string {
	if p == "" {
		return "round-robin"
	}
	return string(p)
}
