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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/RedHuang-0622/Seele/agent"
	"github.com/RedHuang-0622/Seele/agent/core/api"
	"github.com/RedHuang-0622/Seele/agent/core/tool/builtin"
	"github.com/RedHuang-0622/Seele/agent/core/tool/holder"
	"github.com/RedHuang-0622/Seele/seelectx/storage"
	"github.com/RedHuang-0622/Seele/seelectx/tracer"
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
	{"plan", "WorkPlan 工作流模式", []string{"switch_mode", "plan_*", "get_time"}, nil},
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

	// ── WorkPlan 工具（LLM 可动态构建 DAG 工作流）──────────────────
	wpt := builtin.NewWorkPlanTool(builtin.NewChatAgentFactory(agt.LLM()))
	agt.Tools().Register(wpt)

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
	store, err := storage.NewStore("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "✖ 初始化存储失败: %v\n", err)
		os.Exit(1)
	}
	hooks := buildHooks()

	eng := engine.New(agt,
		engine.WithTracer(tr),
		engine.WithStore(store),
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
					"type":        "string",
					"enum":        []interface{}{"default", "read", "write", "git", "shell"},
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

			if mode == "" || mode == "default" {
				agt.Tools().DeactivatePlugin()
			} else {
				if err := agt.Tools().ActivatePlugin(mode); err != nil {
					return fmt.Sprintf(`{"error":"unknown mode: %s"}`, mode), nil
				}
			}

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
