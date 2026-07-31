package seelectx

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/RedHuang-0622/Seele/types"
)

// DurableHistory is an optional owner of session history. ReAct loops only
// use it when explicitly injected; they never discover or create one on their
// own. This keeps working messages separate from durable session state.
type DurableHistory interface {
	Load(ctx context.Context) ([]types.Message, error)
	Save(ctx context.Context, messages []types.Message) error
	Clear(ctx context.Context) error
}

// MemoryHistory is a small in-memory DurableHistory implementation useful for
// tests, ephemeral sessions, and callers that do not need persistence.
type MemoryHistory struct {
	mu       sync.RWMutex
	messages []types.Message
}

func NewMemoryHistory(initial ...types.Message) *MemoryHistory {
	h := &MemoryHistory{}
	_ = h.Save(context.Background(), initial)
	return h
}

func (h *MemoryHistory) Load(context.Context) ([]types.Message, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return cloneMessages(h.messages), nil
}

func (h *MemoryHistory) Save(_ context.Context, messages []types.Message) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.messages = cloneMessages(messages)
	return nil
}

func (h *MemoryHistory) Clear(_ context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.messages = nil
	return nil
}

// PromptBlock is an opaque prompt contribution. The block name is metadata;
// ordering and interpretation are deliberately left to RequestAssembler.
type PromptBlock struct {
	Name     string
	Messages []types.Message
}

// AssemblyRequest contains the caller-selected working history, prompt blocks,
// and visible tools for one model request.
type AssemblyRequest struct {
	WorkingHistory []types.Message
	Blocks         []PromptBlock
	Tools          []types.Tool
}

// AssembledRequest is the provider-neutral request consumed by an LLM client.
type AssembledRequest struct {
	Messages []types.Message
	Tools    []types.Tool
}

// RequestAssembler chooses how system, plan/task, skill, and working history
// blocks become one model request. Seelex may replace this with a product
// specific implementation.
type RequestAssembler interface {
	Assemble(ctx context.Context, request AssemblyRequest) (AssembledRequest, error)
}

// DefaultRequestAssembler preserves caller order: blocks first, then working
// history. It performs copying only and intentionally does no compression,
// trimming, or product-specific prompt rewriting.
type DefaultRequestAssembler struct{}

func (DefaultRequestAssembler) Assemble(_ context.Context, request AssemblyRequest) (AssembledRequest, error) {
	var messages []types.Message
	for _, block := range request.Blocks {
		messages = append(messages, cloneMessages(block.Messages)...)
	}
	messages = append(messages, cloneMessages(request.WorkingHistory)...)
	tools := append([]types.Tool(nil), request.Tools...)
	return AssembledRequest{Messages: messages, Tools: tools}, nil
}

// ToolResult is the unmodified result returned by a tool handler.
type ToolResult struct {
	CallID    string
	Name      string
	Arguments string
	Raw       string
	Err       error
}

// ToolResultView is the representation appended to the next model request.
// A processor may return a filtered projection, a reference, or the raw text.
type ToolResultView struct {
	Content  string
	Metadata map[string]any
}

// ToolResultProcessor lets Seelex inspect and select tool output before it is
// added to a ReAct working history. A nil processor means legacy raw handling.
type ToolResultProcessor interface {
	Process(ctx context.Context, result ToolResult) (ToolResultView, error)
}

// ToolResultProcessorFunc adapts a function to ToolResultProcessor.
type ToolResultProcessorFunc func(context.Context, ToolResult) (ToolResultView, error)

func (f ToolResultProcessorFunc) Process(ctx context.Context, result ToolResult) (ToolResultView, error) {
	return f(ctx, result)
}

// RawToolResultProcessor explicitly preserves the handler's raw output. It is
// useful when Seelex wants to perform filtering outside Seele.
type RawToolResultProcessor struct{}

func (RawToolResultProcessor) Process(_ context.Context, result ToolResult) (ToolResultView, error) {
	if result.Err != nil {
		return ToolResultView{Content: fmt.Sprintf(`{"error":%q}`, result.Err.Error())}, nil
	}
	return ToolResultView{Content: result.Raw}, nil
}

func cloneMessages(messages []types.Message) []types.Message {
	if messages == nil {
		return nil
	}
	out := make([]types.Message, len(messages))
	copy(out, messages)
	for i := range out {
		if messages[i].Content != nil {
			value := *messages[i].Content
			out[i].Content = &value
		}
		if messages[i].ToolCalls != nil {
			out[i].ToolCalls = append([]types.ToolCall(nil), messages[i].ToolCalls...)
		}
	}
	return out
}

// MarshalMessages is a small provider-neutral helper for history adapters.
func MarshalMessages(messages []types.Message) (string, error) {
	b, err := json.Marshal(messages)
	if err != nil {
		return "", fmt.Errorf("seelectx: marshal messages: %w", err)
	}
	return string(b), nil
}
