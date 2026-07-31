package seelectx

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/RedHuang-0622/Seele/seelectx/ctx_manager"
	"github.com/RedHuang-0622/Seele/types"
)

var ErrNilCompleter = errors.New("seelectx: nil completer")

// CompressionRequest describes an explicit compression operation. Triggering,
// candidate selection, checkpointing, and billing remain outside this package.
type CompressionRequest struct {
	SessionID      string
	ContextVersion string
	PromptVersion  string
	History        []types.Message
	MaxTokens      int
	Query          string
}

type CompressionResult struct {
	Messages []types.Message
	Usage    *types.Usage
	Depth    int
}

type Compressor interface {
	Compress(ctx context.Context, request CompressionRequest) (CompressionResult, error)
}

// LLMCompressor is an optional adapter around the generic compression helper.
// It is never invoked by ReActLoop unless a caller explicitly asks it to run.
type LLMCompressor struct{ client types.ChatCompleter }

func NewLLMCompressor(client types.ChatCompleter) (*LLMCompressor, error) {
	if client == nil {
		return nil, ErrNilCompleter
	}
	return &LLMCompressor{client: client}, nil
}

func (c *LLMCompressor) Compress(ctx context.Context, request CompressionRequest) (CompressionResult, error) {
	if c == nil || c.client == nil {
		return CompressionResult{}, ErrNilCompleter
	}
	messages, err := ctx_manager.CompressHistory(ctx, c.client, cloneMessages(request.History), request.MaxTokens)
	if err != nil {
		return CompressionResult{}, err
	}
	return CompressionResult{Messages: messages}, nil
}

type CompressionPromptInput struct {
	History       []types.Message
	Query         string
	CurrentTokens int
	TargetTokens  int
	Depth         int
}

// CompressionPromptStrategy builds the ephemeral QuickChat prompt for each
// recursion level. It may dynamically use query, token budget, and depth.
type CompressionPromptStrategy interface {
	Build(ctx context.Context, input CompressionPromptInput) ([]types.Message, error)
}

type CompressionPromptFunc func(context.Context, CompressionPromptInput) ([]types.Message, error)

func (f CompressionPromptFunc) Build(ctx context.Context, input CompressionPromptInput) ([]types.Message, error) {
	return f(ctx, input)
}

type DefaultCompressionPrompt struct{}

func (DefaultCompressionPrompt) Build(_ context.Context, input CompressionPromptInput) ([]types.Message, error) {
	system := fmt.Sprintf(
		"Compress the supplied conversation into a faithful structured checkpoint under approximately %d tokens. Preserve decisions, unresolved work, errors, result references, and tool outcomes. Do not call tools. Recursion depth: %d.",
		input.TargetTokens, input.Depth,
	)
	if input.Query != "" {
		system += " Prefer information relevant to this query: " + input.Query
	}
	user := renderCompressionHistory(input.History)
	return []types.Message{
		{Role: "system", Content: &system},
		{Role: "user", Content: &user},
	}, nil
}

// RecursiveCompressor repeatedly invokes an isolated QuickChat until the
// summary fits the requested budget. Short conversations are never sent to an
// LLM: they are returned unchanged or deterministically trimmed.
type RecursiveCompressor struct {
	Chat        QuickChat
	Prompt      CompressionPromptStrategy
	Normalizer  HistoryNormalizer
	Segmenter   Segmenter
	Selector    *RelevanceSelector
	MinMessages int
	MinTokens   int
	MaxDepth    int
}

func (c RecursiveCompressor) Compress(ctx context.Context, request CompressionRequest) (CompressionResult, error) {
	if c.Chat == nil {
		return CompressionResult{}, fmt.Errorf("seelectx: recursive compressor requires QuickChat")
	}
	maxTokens := request.MaxTokens
	if maxTokens <= 0 {
		maxTokens = ctx_manager.DefaultConfig().MaxTokens
	}
	history := cloneMessages(request.History)
	if ctx_manager.EstimateHistoryTokens(history) <= maxTokens {
		return CompressionResult{Messages: history}, nil
	}
	minMessages := c.MinMessages
	if minMessages <= 0 {
		minMessages = 6
	}
	minTokens := c.MinTokens
	if minTokens <= 0 {
		minTokens = 512
	}
	if len(history) < minMessages || ctx_manager.EstimateHistoryTokens(history) < minTokens {
		return CompressionResult{Messages: ctx_manager.TrimHistory(history, maxTokens)}, nil
	}
	if c.Normalizer != nil {
		history = c.Normalizer.Normalize(history)
	}
	if c.Selector != nil {
		segmenter := c.Segmenter
		if segmenter == nil {
			segmenter = StructuralSegmenter{}
		}
		history = joinSegments(c.Selector.Select(request.Query, segmenter.Segment(history)))
	}
	prompt := c.Prompt
	if prompt == nil {
		prompt = DefaultCompressionPrompt{}
	}
	maxDepth := c.MaxDepth
	if maxDepth <= 0 {
		maxDepth = 4
	}
	var totalUsage types.Usage
	for depth := 1; depth <= maxDepth; depth++ {
		currentTokens := ctx_manager.EstimateHistoryTokens(history)
		if currentTokens <= maxTokens {
			return CompressionResult{Messages: history, Usage: nonZeroUsage(totalUsage), Depth: depth - 1}, nil
		}
		messages, err := prompt.Build(ctx, CompressionPromptInput{
			History: history, Query: request.Query, CurrentTokens: currentTokens,
			TargetTokens: maxTokens, Depth: depth,
		})
		if err != nil {
			return CompressionResult{}, fmt.Errorf("seelectx: build compression prompt at depth %d: %w", depth, err)
		}
		response, err := c.Chat.Complete(ctx, QuickChatRequest{Messages: messages, Tools: nil})
		if err != nil {
			return CompressionResult{}, fmt.Errorf("seelectx: compression completion at depth %d: %w", depth, err)
		}
		if response.Content == nil || strings.TrimSpace(*response.Content) == "" {
			return CompressionResult{}, fmt.Errorf("seelectx: compression completion at depth %d returned empty content", depth)
		}
		if response.Usage != nil {
			totalUsage.PromptTokens += response.Usage.PromptTokens
			totalUsage.CompletionTokens += response.Usage.CompletionTokens
			totalUsage.TotalTokens += response.Usage.TotalTokens
		}
		summary := strings.TrimSpace(*response.Content)
		history = []types.Message{{Role: "system", Content: &summary}}
	}
	currentTokens := ctx_manager.EstimateHistoryTokens(history)
	if currentTokens > maxTokens {
		return CompressionResult{}, fmt.Errorf("seelectx: recursive compression exceeded budget after %d levels: %d > %d tokens", maxDepth, currentTokens, maxTokens)
	}
	return CompressionResult{Messages: history, Usage: nonZeroUsage(totalUsage), Depth: maxDepth}, nil
}

func joinSegments(segments []ContextSegment) []types.Message {
	var history []types.Message
	for _, segment := range segments {
		history = append(history, cloneMessages(segment.Messages)...)
	}
	return history
}

func nonZeroUsage(usage types.Usage) *types.Usage {
	if usage == (types.Usage{}) {
		return nil
	}
	return &usage
}

func renderCompressionHistory(history []types.Message) string {
	var builder strings.Builder
	for _, message := range history {
		builder.WriteString(message.Role)
		builder.WriteString(": ")
		if message.Content != nil {
			builder.WriteString(*message.Content)
		}
		for _, call := range message.ToolCalls {
			builder.WriteString(fmt.Sprintf(" tool_use %s(%s)", call.Function.Name, call.Function.Arguments))
		}
		builder.WriteByte('\n')
	}
	return builder.String()
}
