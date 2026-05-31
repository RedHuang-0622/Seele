package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sukasukasuka123/Seele/core/agent"
)

// REPLOptions 控制 REPL 行为。
type REPLOptions struct {
	Prompt           string      // 提示符，默认 "> "
	SystemPrompt     string      // Agent 系统提示词（字符串，优先级低于 SystemPromptPath）
	SystemPromptPath string      // Agent 系统提示词文件路径（推荐，支持热更新）
	Engine           *agent.Agent // 必填
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

	agent := opts.Engine.NewSession(opts.SystemPrompt, 16)

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

	// 审批回调：LLM 不参与审批流程，REPL 直接处理
	agent.OnApproval = func(ctx context.Context, approvalJSON string) (string, error) {
		return handleApproval(out, in, approvalJSON)
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
			for _, s := range opts.Engine.Hub().Skills() {
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

// ── 审批处理 ──────────────────────────────────────────────────────

// handleApproval 解析 awaiting_approval 响应，展示选项给用户，
// 读取用户输入的 key 并返回。LLM 完全不参与此流程。
func handleApproval(out io.Writer, in io.Reader, approvalJSON string) (string, error) {
	var approval struct {
		QuestionID string `json:"question_id"`
		Content    string `json:"content"`
		Options    []struct {
			Key         string `json:"key"`
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"options"`
	}
	if err := json.Unmarshal([]byte(approvalJSON), &approval); err != nil {
		return "", fmt.Errorf("parse approval: %w", err)
	}

	// 构建 key 集合用于校验输入
	validKeys := make(map[string]bool, len(approval.Options))
	var keyList []string
	for _, opt := range approval.Options {
		validKeys[opt.Key] = true
		keyList = append(keyList, opt.Key)
	}

	fmt.Fprintln(out, "\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Fprintln(out, "[审批] 工作流需要你的决定：")
	fmt.Fprintln(out, approval.Content)
	fmt.Fprintln(out, "\n选项：")
	for i, opt := range approval.Options {
		desc := ""
		if opt.Description != "" {
			desc = " — " + opt.Description
		}
		fmt.Fprintf(out, "  [%d] %s (%s)%s\n", i+1, opt.Label, opt.Key, desc)
	}
	fmt.Fprintf(out, "\n请输入选项 key（%s）> ", strings.Join(keyList, " / "))

	scanner := bufio.NewScanner(in)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("read input: %w", err)
		}
		return "", fmt.Errorf("input closed")
	}

	input := strings.TrimSpace(scanner.Text())
	if input == "" && len(approval.Options) > 0 {
		// 空输入 → 默认第一个选项
		return approval.Options[0].Key, nil
	}

	// 支持数字索引（1-based → key）
	if idx := parseChoiceIndex(input); idx > 0 && idx <= len(approval.Options) {
		return approval.Options[idx-1].Key, nil
	}

	// 直接匹配 key
	if validKeys[input] {
		return input, nil
	}

	// 尝试匹配 label（大小写不敏感）
	lower := strings.ToLower(input)
	for _, opt := range approval.Options {
		if strings.ToLower(opt.Label) == lower {
			return opt.Key, nil
		}
	}

	fmt.Fprintf(out, "[警告] 无效选项 %q，使用默认值: %s\n", input, approval.Options[0].Key)
	return approval.Options[0].Key, nil
}

// parseChoiceIndex 尝试将输入解析为 1-based 数字索引。
// 支持 "1", "2" 以及 "1." "2." 等格式。
func parseChoiceIndex(s string) int {
	s = strings.TrimRight(s, ".)）")
	s = strings.TrimSpace(s)
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err == nil && n > 0 {
		return n
	}
	return 0
}

// OneShot 创建临时 Agent，执行单次对话并返回结果。
// 适合脚本或管道场景。
func OneShot(ctx context.Context, engine *agent.Agent, systemPrompt, userInput string) (string, error) {
	return engine.QuickChat(ctx, systemPrompt, userInput)
}

// OneShotStream 创建临时 Agent，执行单次流式对话。
// onChunk 为 nil 时默认直接打印到 stdout。
func OneShotStream(ctx context.Context, engine *agent.Agent, systemPrompt, userInput string, onChunk func(string)) (string, error) {
	if onChunk == nil {
		onChunk = func(delta string) { fmt.Print(delta) }
	}
	return engine.QuickChatStream(ctx, systemPrompt, userInput, onChunk)
}
