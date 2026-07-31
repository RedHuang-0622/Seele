package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/RedHuang-0622/Seele/seelectx"
	"github.com/RedHuang-0622/Seele/types"
)

type countingQuickChat struct {
	mu    sync.Mutex
	calls int
}

func (c *countingQuickChat) Complete(context.Context, seelectx.QuickChatRequest) (types.Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	content := "checkpoint: decisions kept; pending tests retained"
	return types.Message{Role: "assistant", Content: &content}, nil
}

func (c *countingQuickChat) Calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

type contextDemoResult struct {
	AssembledMessages []types.Message
	ToolResultView    string
	Compressed        []types.Message
	ShortChatCalls    int
	LongChatCalls     int
}

func runContextDemo(ctx context.Context) (contextDemoResult, error) {
	history := seelectx.NewMemoryHistory(
		message("user", "继续实现 context pipeline"),
		message("assistant", "先读取显式装配规则"),
	)
	workingHistory, err := history.Load(ctx)
	if err != nil {
		return contextDemoResult{}, fmt.Errorf("load history: %w", err)
	}

	assembler := seelectx.PlaceholderRequestAssembler{
		Resolver: seelectx.PlaceholderResolverFunc(func(_ context.Context, name string) (string, error) {
			switch name {
			case "plan":
				return "inspect -> implement -> test", nil
			case "skill":
				return "Go interface-first", nil
			default:
				return "", fmt.Errorf("unknown placeholder %q", name)
			}
		}),
	}
	assembled, err := assembler.Assemble(ctx, seelectx.AssemblyRequest{
		Blocks: []seelectx.PromptBlock{
			{Name: "plan", Messages: []types.Message{message("system", "Plan: {{plan}}")}},
			{Name: "skill", Messages: []types.Message{message("system", "Skill: {{skill}}")}},
		},
		WorkingHistory: workingHistory,
	})
	if err != nil {
		return contextDemoResult{}, fmt.Errorf("assemble request: %w", err)
	}

	processor := seelectx.ToolResultProcessorFunc(func(_ context.Context, result seelectx.ToolResult) (seelectx.ToolResultView, error) {
		var payload struct {
			Reference string `json:"result_ref"`
			Status    string `json:"status"`
		}
		if err := json.Unmarshal([]byte(result.Raw), &payload); err != nil {
			return seelectx.ToolResultView{}, fmt.Errorf("decode tool result: %w", err)
		}
		return seelectx.ToolResultView{
			Content: fmt.Sprintf("result_ref=%s status=%s", payload.Reference, payload.Status),
		}, nil
	})
	toolView, err := processor.Process(ctx, seelectx.ToolResult{
		Name: "run_tests",
		Raw:  `{"result_ref":"artifact://test/42","status":"passed","verbose_log":"omitted"}`,
	})
	if err != nil {
		return contextDemoResult{}, fmt.Errorf("process tool result: %w", err)
	}

	shortChat := &countingQuickChat{}
	shortCompressor := seelectx.RecursiveCompressor{Chat: shortChat}
	if _, err := shortCompressor.Compress(ctx, seelectx.CompressionRequest{
		History: workingHistory, MaxTokens: 100,
	}); err != nil {
		return contextDemoResult{}, fmt.Errorf("compress short history: %w", err)
	}

	longChat := &countingQuickChat{}
	longHistory := make([]types.Message, 0, 6)
	for index := 0; index < 6; index++ {
		longHistory = append(longHistory, message("user", strings.Repeat(fmt.Sprintf("turn-%d ", index), 100)))
	}
	compressed, err := (seelectx.RecursiveCompressor{
		Chat: longChat, MinMessages: 6, MinTokens: 10, MaxDepth: 2,
	}).Compress(ctx, seelectx.CompressionRequest{
		History: longHistory, Query: "测试状态", MaxTokens: 40,
	})
	if err != nil {
		return contextDemoResult{}, fmt.Errorf("compress long history: %w", err)
	}

	return contextDemoResult{
		AssembledMessages: assembled.Messages,
		ToolResultView:    toolView.Content,
		Compressed:        compressed.Messages,
		ShortChatCalls:    shortChat.Calls(),
		LongChatCalls:     longChat.Calls(),
	}, nil
}

func message(role, content string) types.Message {
	return types.Message{Role: role, Content: &content}
}

func main() {
	result, err := runContextDemo(context.Background())
	if err != nil {
		panic(err)
	}
	for _, item := range result.AssembledMessages {
		fmt.Printf("%s: %s\n", item.Role, value(item.Content))
	}
	fmt.Println("tool view:", result.ToolResultView)
	fmt.Println("compressed:", value(result.Compressed[0].Content))
	fmt.Printf("quickchat calls: short=%d long=%d\n", result.ShortChatCalls, result.LongChatCalls)
}

func value(content *string) string {
	if content == nil {
		return ""
	}
	return *content
}
