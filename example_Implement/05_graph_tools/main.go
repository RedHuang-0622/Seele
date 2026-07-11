// 05_graph_tools/main.go
//
// Graph-as-Tools: 将图编排基础语法（fork / loop / pipeline）封装为 LLM 可调用的工具。

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/RedHuang-0622/Seele/agent"
	"github.com/RedHuang-0622/Seele/agent/core/api"
	"github.com/RedHuang-0622/Seele/agent/core/tool"
	eng "github.com/RedHuang-0622/Seele/engine"

	"github.com/RedHuang-0622/Seele/types"
	"github.com/RedHuang-0622/Seele/workplan"
)

// =============================================================================
// EngineFactory：适配 workplan.AgentFactory
// =============================================================================

type EngineFactory struct {
	engine *agent.Agent
}

func (f *EngineFactory) NewAgent(systemPrompt string) workplan.Agent {
	return eng.New(f.engine, eng.WithSystemPrompt(systemPrompt))
}

// =============================================================================
// 工具参数声明
// =============================================================================

type ForkInput struct {
	Tasks []ForkTask `json:"tasks" desc:"要并发执行的多个任务"`
}

type ForkTask struct {
	Label string `json:"label" desc:"分支标签，如「前端工程师」「后端工程师」"`
	Input string `json:"input" desc:"该分支的任务描述"`
}

type PipelineInput struct {
	Steps []string `json:"steps" desc:"按顺序执行的步骤描述列表"`
}

type LoopInput struct {
	Task        string `json:"task" desc:"要反复执行的任务描述"`
	MaxIter     int    `json:"max_iter,omitempty" desc:"最大迭代次数，默认 3" default:"3"`
	SuccessMark string `json:"success_mark,omitempty" desc:"成功标记，输出包含此文本时停止循环" default:"已完成"`
}

// =============================================================================
// main
// =============================================================================

var configPath = flag.String("c", "../../config/account-anthropic.yaml", "config path")

func main() {
	flag.Parse()
	ctx := context.Background()

	// ── 1. 初始化 Engine ──────────────────────────────────────────────
	result, err := api.LoadFullAccountsConfig(*configPath)
	if err != nil {
		log.Fatalf("load %s: %v", *configPath, err)
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
	engine, err := agent.New(agent.Options{
		LLMConfig:       llmCfg,
		ToolCallTimeOut: 120 * time.Second,
		HubStartupDelay: 10,
	})
	if err != nil {
		log.Fatalf("engine init failed: %v", err)
	}
	defer engine.Shutdown()

	chatClient := engine.LLM().(*api.ChatClient)
	chatClient.WithAccountPool(pool)
	if ls.Provider != "" {
		chatClient.SetProvider(ls.Provider)
	}

	factory := &EngineFactory{engine: engine}

	// ── 2. 注册工具：fork_agents ─────────────────────────────────────
	engine.RegisterTool(
		"fork_agents",
		"并发启动多个 Agent 执行不同任务，适合多角色并行分析、多角度调研等场景。每个分支独立执行，结果合并为 JSON。",
		tool.SchemaOf(ForkInput{}),
		func(ctx context.Context, argsJSON string) (string, error) {
			var input ForkInput
			if err := json.Unmarshal([]byte(argsJSON), &input); err != nil {
				return "", fmt.Errorf("fork_agents: parse input: %w", err)
			}
			if len(input.Tasks) == 0 {
				return `{"error":"no tasks provided"}`, nil
			}

			branches := make([]workplan.ForkBranch, len(input.Tasks))
			for i, t := range input.Tasks {
				branches[i] = workplan.ForkBranch{
					Label:        t.Label,
					SystemPrompt: fmt.Sprintf("你是专家 %s，请严格按角色执行任务。", t.Label),
					Input:        t.Input,
				}
			}

			wp := workplan.New(factory, workplan.WithDefaultPrompt("你是任务协调助手。"))
			wp.Fork("并行执行", branches, 3)

			result, err := wp.Run(ctx)
			if err != nil {
				return "", fmt.Errorf("fork_agents: execute: %w", err)
			}

			return result.FinalOutput(), nil
		},
	)

	// ── 3. 注册工具：run_pipeline ────────────────────────────────────
	engine.RegisterTool(
		"run_pipeline",
		"按顺序执行多个步骤，后一步接收前一步的输出作为输入。适合需要逐步推进的复杂任务。",
		tool.SchemaOf(PipelineInput{}),
		func(ctx context.Context, argsJSON string) (string, error) {
			var input PipelineInput
			if err := json.Unmarshal([]byte(argsJSON), &input); err != nil {
				return "", fmt.Errorf("run_pipeline: parse input: %w", err)
			}
			if len(input.Steps) == 0 {
				return `{"error":"no steps provided"}`, nil
			}

			wp := workplan.New(factory, workplan.WithDefaultPrompt("你是流水线执行助手，严格按步骤执行。"))
			steps := make([]workplan.PipelineStep, len(input.Steps))
			for i, step := range input.Steps {
				stepID := fmt.Sprintf("step_%d", i+1)
				inputText := step
				if i > 0 {
					inputText = step + "\n\n上一步结果：{{.PrevResult}}"
				}
				steps[i] = workplan.Step(stepID, inputText)
			}
			wp.Pipeline(steps...)

			result, err := wp.Run(ctx)
			if err != nil {
				return "", fmt.Errorf("run_pipeline: execute: %w", err)
			}

			return result.FinalOutput(), nil
		},
	)

	// ── 4. 注册工具：loop_task ───────────────────────────────────────
	engine.RegisterTool(
		"loop_task",
		"反复执行一个任务直到满足条件。每次迭代的结果会作为下次的输入。适合迭代优化、渐进式改进等场景。",
		tool.SchemaOf(LoopInput{}),
		func(ctx context.Context, argsJSON string) (string, error) {
			var input LoopInput
			if err := json.Unmarshal([]byte(argsJSON), &input); err != nil {
				return "", fmt.Errorf("loop_task: parse input: %w", err)
			}
			if input.MaxIter <= 0 {
				input.MaxIter = 3
			}
			if input.SuccessMark == "" {
				input.SuccessMark = "已完成"
			}

			wp := workplan.New(factory, workplan.WithDefaultPrompt(fmt.Sprintf(
				"你是迭代执行助手，每次迭代都要比上次更完善。\n如果任务已完成，在回复中包含「%s」。", input.SuccessMark,
			)))

			wp.Auto("body", input.Task+"\n\n{{.PrevResult}}")
			wp.Retry("loop", "body", input.MaxIter, workplan.Contains(input.SuccessMark), "")

			result, err := wp.Run(ctx)
			if err != nil {
				return "", fmt.Errorf("loop_task: execute: %w", err)
			}
			return result.FinalOutput(), nil
		},
	)

	// ── 5. 查看已注册工具 ────────────────────────────────────────────
	fmt.Println("=== Graph-as-Tools: 已注册工具 ===")
	for _, t := range engine.Tools().Tools() {
		fmt.Printf("  • %s — %s\n", t.Function.Name, truncate(t.Function.Description, 70))
	}

	// ── 6. 对话演示 ──────────────────────────────────────────────────
	fmt.Println("\n" + strings.Repeat("═", 60))
	fmt.Println("  🤖 Graph-as-Tools 对话演示")
	fmt.Println(strings.Repeat("═", 60))

	sess := eng.New(engine, eng.WithSystemPrompt(`你是工作流编排专家。
你可以使用以下工具来执行复杂任务：
1. fork_agents — 并发执行多个任务（多角色并行）
2. run_pipeline — 按顺序执行多步骤流水线
3. loop_task — 循环执行直到条件满足

对于简单问题直接回答，对于复杂任务选择合适的工具。`,
	))

	fmt.Println("\n📝 用户: 帮我同时调研 Go 语言的并发模型特点和 Rust 的所有权系统")
	reply, err := sess.Chat(ctx, "帮我同时调研 Go 语言的并发模型特点和 Rust 的所有权系统")
	if err != nil {
		log.Printf("chat error: %v", err)
	}
	fmt.Printf("🤖 Agent: %s\n", truncate(reply, 300))

	fmt.Println("\n📝 用户: 帮我写一个 Go 程序的步骤：先写接口定义，再写实现，最后写测试")
	reply, err = sess.Chat(ctx, "帮我写一个 Go 程序的步骤：先写接口定义，再写实现，最后写测试")
	if err != nil {
		log.Printf("chat error: %v", err)
	}
	fmt.Printf("🤖 Agent: %s\n", truncate(reply, 300))

	fmt.Println("\n📝 用户: 帮我反复优化一段代码，直到我满意")
	reply, err = sess.Chat(ctx, "帮我反复优化一段代码，直到我满意")
	if err != nil {
		log.Printf("chat error: %v", err)
	}
	fmt.Printf("🤖 Agent: %s\n", truncate(reply, 300))

	fmt.Println("\n✅ 示例运行完毕")
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
