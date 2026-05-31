package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sukasukasuka123/Seele/sdk/api"
)

// REPLOptions 控制 REPL 行为。
type REPLOptions struct {
	Prompt           string      // 提示符，默认 "> "
	SystemPrompt     string      // Agent 系统提示词（字符串，优先级低于 SystemPromptPath）
	SystemPromptPath string      // Agent 系统提示词文件路径（推荐，支持热更新）
	Engine           *api.Engine // 必填
	Output           io.Writer   // 输出目标，默认 os.Stdout
	Input            io.Reader   // 输入源，默认 os.Stdin
	Stream           bool        // true 时使用流式输出，默认 false
}

// RunREPL 启动交互式 REPL，直到 ctx 取消、输入结束或用户输入 exit/quit。
//
// 内置指令：
//
//	/skills  — 列出当前可用 skills
//	/clear   — 清空对话历史（保留 system 消息，热加载模式下重读 prompt 文件）
//	/reload  — 重新加载 system prompt 文件（仅热加载模式）
//	/help    — 显示帮助
//	exit|quit — 退出
func RunREPL(ctx context.Context, opts REPLOptions) {
	if opts.Engine == nil {
		panic("cli.RunREPL: Engine must not be nil")
	}
	if opts.Prompt == "" {
		opts.Prompt = "> "
	}
	out := opts.Output
	if out == nil {
		out = os.Stdout
	}
	in := opts.Input
	if in == nil {
		in = os.Stdin
	}

	agent := opts.Engine.NewAgent(opts.SystemPrompt, 16)

	// 热加载：若指定了 prompt 文件路径，启动文件监听，修改文件无需重启
	var loader *PromptLoader
	if opts.SystemPromptPath != "" {
		var err error
		loader, err = NewPromptLoader(opts.SystemPromptPath)
		if err != nil {
			fmt.Fprintf(out, "[警告] prompt 文件加载失败 (%v)，使用内置默认值\n", err)
		} else {
			defer loader.Stop()
			// 用文件内容替换内置 prompt
			agent.UpdateSystemPrompt(loader.Get())
			opts.SystemPrompt = loader.Get()
		}
	}

	scanner := bufio.NewScanner(in)

	fmt.Fprint(out, opts.Prompt)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			fmt.Fprintln(out, "\n[已停止]")
			return
		default:
		}

		line := strings.TrimSpace(scanner.Text())
		var err error
		switch line {
		case "exit", "quit":
			fmt.Fprintln(out, "Bye.")
			return
		case "/help":
			fmt.Fprintln(out, "指令: /skills  /clear  /reload  /help  exit")
		case "/skills":
			for _, s := range opts.Engine.Skills() {
				fmt.Fprintf(out, "  %-20s %s  [%s]\n", s.Name, s.Description, s.Addr)
			}
		case "/clear":
			agent.ClearHistory()
			// 热加载模式：重读文件，确保清空后使用最新 prompt
			if loader != nil {
				if content, err := loader.Reload(); err == nil {
					agent.UpdateSystemPrompt(content)
				}
			}
			fmt.Fprintln(out, "[历史已清空]")
		case "/reload":
			if loader != nil {
				if content, err := loader.Reload(); err == nil {
					agent.UpdateSystemPrompt(content)
					fmt.Fprintf(out, "[prompt 已重载] (%d bytes)\n", len(content))
				} else {
					fmt.Fprintf(out, "[重载失败] %v\n", err)
				}
			} else {
				fmt.Fprintln(out, "[提示] 未启用热加载模式（未设置 SystemPromptPath）")
			}
		default:
			if opts.Stream {
				_, err = agent.ChatStream(ctx, line, func(delta string) {
					fmt.Fprint(out, delta)
				})
				fmt.Fprintln(out) // 流结束后补换行
			} else {
				var reply string
				reply, err = agent.Chat(ctx, line)
				if err == nil {
					fmt.Fprintln(out, reply)
				}
			}
			if err != nil {
				fmt.Fprintf(out, "[错误] %v\n", err)
			}
		}
		fmt.Fprint(out, opts.Prompt)
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(out, "\n[输入错误] %v\n", err)
	}
}

// OneShot 创建临时 Agent，执行单次对话并返回结果。
// 适合脚本或管道场景。
func OneShot(ctx context.Context, engine *api.Engine, systemPrompt, userInput string) (string, error) {
	return engine.QuickChat(ctx, systemPrompt, userInput)
}

// OneShotStream 创建临时 Agent，执行单次流式对话。
// onChunk 为 nil 时默认直接打印到 stdout。
func OneShotStream(ctx context.Context, engine *api.Engine, systemPrompt, userInput string, onChunk func(string)) (string, error) {
	if onChunk == nil {
		onChunk = func(delta string) { fmt.Print(delta) }
	}
	return engine.QuickChatStream(ctx, systemPrompt, userInput, onChunk)
}
