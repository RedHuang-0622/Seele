package seelectx

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/RedHuang-0622/Seele/types"
)

type SegmentKind string

const (
	SegmentSystem       SegmentKind = "system"
	SegmentTurn         SegmentKind = "turn"
	SegmentToolExchange SegmentKind = "tool_exchange"
)

// ContextSegment is a semantic slice of history. Start and End use the source
// history's half-open indexes; a split oversized message may reuse its index.
type ContextSegment struct {
	Start     int
	End       int
	Turn      int
	Kind      SegmentKind
	CharCount int
	Messages  []types.Message
	Score     float64
}

type Segmenter interface {
	Segment(history []types.Message) []ContextSegment
}

// StructuralSegmenter cuts history at system, turn, and continuous
// assistant-tool_use/tool_result boundaries. MaxChars adds a character budget
// without breaking a normal multi-message tool exchange.
type StructuralSegmenter struct{ MaxChars int }

func (s StructuralSegmenter) Segment(history []types.Message) []ContextSegment {
	var segments []ContextSegment
	turn := 0
	for i := 0; i < len(history); {
		message := history[i]
		if message.Role == "user" {
			turn++
		}
		kind, end := SegmentTurn, i+1
		if message.Role == "system" {
			kind = SegmentSystem
		}
		if len(message.ToolCalls) > 0 {
			kind = SegmentToolExchange
			for end < len(history) && history[end].Role == "tool" {
				end++
			}
		} else if message.Role == "tool" {
			kind = SegmentToolExchange
			for end < len(history) && history[end].Role == "tool" {
				end++
			}
		}
		messages := cloneMessages(history[i:end])
		segments = append(segments, splitSegment(ContextSegment{
			Start: i, End: end, Turn: turn, Kind: kind, Messages: messages,
		}, s.MaxChars)...)
		i = end
	}
	return segments
}

func splitSegment(segment ContextSegment, maxChars int) []ContextSegment {
	segment.CharCount = messagesChars(segment.Messages)
	if maxChars <= 0 || segment.CharCount <= maxChars || segment.Kind == SegmentToolExchange {
		return []ContextSegment{segment}
	}
	var out []ContextSegment
	for _, message := range segment.Messages {
		if message.Content == nil || len(*message.Content) <= maxChars {
			part := segment
			part.Messages = []types.Message{message}
			part.CharCount = messagesChars(part.Messages)
			out = append(out, part)
			continue
		}
		content := *message.Content
		for len(content) > 0 {
			end := maxChars
			if end > len(content) {
				end = len(content)
			}
			chunk := content[:end]
			content = content[end:]
			copyMessage := message
			copyMessage.Content = &chunk
			part := segment
			part.Messages = []types.Message{copyMessage}
			part.CharCount = len(chunk)
			out = append(out, part)
		}
	}
	return out
}

func messagesChars(messages []types.Message) int {
	total := 0
	for _, message := range messages {
		if message.Content != nil {
			total += len(*message.Content)
		}
		total += len(message.ReasoningContent)
		for _, call := range message.ToolCalls {
			total += len(call.Function.Name) + len(call.Function.Arguments)
		}
	}
	return total
}

type HistoryNormalizer interface {
	Normalize(history []types.Message) []types.Message
}

// FlattenToolExchanges merges one assistant tool_use and its continuous tool
// results into a compact assistant record. Other messages remain unchanged.
type FlattenToolExchanges struct{}

func (FlattenToolExchanges) Normalize(history []types.Message) []types.Message {
	segments := (StructuralSegmenter{}).Segment(history)
	var normalized []types.Message
	for _, segment := range segments {
		if segment.Kind != SegmentToolExchange {
			normalized = append(normalized, cloneMessages(segment.Messages)...)
			continue
		}
		var lines []string
		for _, message := range segment.Messages {
			for _, call := range message.ToolCalls {
				lines = append(lines, fmt.Sprintf("tool_use %s(%s)", call.Function.Name, call.Function.Arguments))
			}
			if message.Role == "tool" && message.Content != nil {
				lines = append(lines, fmt.Sprintf("tool_result %s: %s", message.Name, *message.Content))
			}
		}
		content := strings.Join(lines, "\n")
		normalized = append(normalized, types.Message{Role: "assistant", Content: &content})
	}
	return normalized
}

type RelevanceStrategy interface {
	Score(query string, segment ContextSegment) float64
}

// LexicalRelevance is a deterministic default strategy based on query-token
// overlap. Product embeddings or rerankers can replace it.
type LexicalRelevance struct{}

func (LexicalRelevance) Score(query string, segment ContextSegment) float64 {
	queryTokens := tokenSet(query)
	if len(queryTokens) == 0 || segment.Kind == SegmentSystem {
		if segment.Kind == SegmentSystem {
			return 1
		}
		return 0
	}
	text := ""
	for _, message := range segment.Messages {
		if message.Content != nil {
			text += " " + *message.Content
		}
	}
	candidate := tokenSet(text)
	matches := 0
	for token := range queryTokens {
		if _, ok := candidate[token]; ok {
			matches++
		}
	}
	return float64(matches) / float64(len(queryTokens))
}

type RelevanceSelector struct {
	Strategy    RelevanceStrategy
	MinScore    float64
	MaxSegments int
}

func (s RelevanceSelector) Select(query string, segments []ContextSegment) []ContextSegment {
	strategy := s.Strategy
	if strategy == nil {
		strategy = LexicalRelevance{}
	}
	selected := make([]ContextSegment, 0, len(segments))
	for _, segment := range segments {
		segment.Score = strategy.Score(query, segment)
		if segment.Kind == SegmentSystem || segment.Score >= s.MinScore {
			selected = append(selected, segment)
		}
	}
	sort.SliceStable(selected, func(i, j int) bool { return selected[i].Score > selected[j].Score })
	if s.MaxSegments > 0 && len(selected) > s.MaxSegments {
		selected = selected[:s.MaxSegments]
	}
	return selected
}

func tokenSet(value string) map[string]struct{} {
	returnValues := strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '_'
	})
	set := make(map[string]struct{}, len(returnValues))
	for _, value := range returnValues {
		set[value] = struct{}{}
	}
	return set
}

type PlaceholderResolver interface {
	Resolve(ctx context.Context, name string) (string, error)
}

type PlaceholderResolverFunc func(context.Context, string) (string, error)

func (f PlaceholderResolverFunc) Resolve(ctx context.Context, name string) (string, error) {
	return f(ctx, name)
}

// PlaceholderRequestAssembler resolves {{name}} placeholders immediately
// before delegating to the wrapped assembler.
type PlaceholderRequestAssembler struct {
	Resolver PlaceholderResolver
	Next     RequestAssembler
}

var placeholderPattern = regexp.MustCompile(`\{\{([a-zA-Z0-9_.-]+)\}\}`)

func (a PlaceholderRequestAssembler) Assemble(ctx context.Context, request AssemblyRequest) (AssembledRequest, error) {
	if a.Resolver == nil {
		return AssembledRequest{}, fmt.Errorf("seelectx: placeholder resolver is required")
	}
	for blockIndex := range request.Blocks {
		request.Blocks[blockIndex].Messages = cloneMessages(request.Blocks[blockIndex].Messages)
		for messageIndex := range request.Blocks[blockIndex].Messages {
			message := &request.Blocks[blockIndex].Messages[messageIndex]
			if message.Content == nil {
				continue
			}
			resolved, err := resolvePlaceholders(ctx, *message.Content, a.Resolver)
			if err != nil {
				return AssembledRequest{}, fmt.Errorf("seelectx: block %q message %d: %w", request.Blocks[blockIndex].Name, messageIndex, err)
			}
			message.Content = &resolved
		}
	}
	next := a.Next
	if next == nil {
		next = DefaultRequestAssembler{}
	}
	return next.Assemble(ctx, request)
}

func resolvePlaceholders(ctx context.Context, input string, resolver PlaceholderResolver) (string, error) {
	var resolveErr error
	result := placeholderPattern.ReplaceAllStringFunc(input, func(match string) string {
		if resolveErr != nil {
			return match
		}
		name := placeholderPattern.FindStringSubmatch(match)[1]
		value, err := resolver.Resolve(ctx, name)
		if err != nil {
			resolveErr = fmt.Errorf("resolve placeholder %q: %w", name, err)
			return match
		}
		return value
	})
	return result, resolveErr
}

type ContextEventKind string

const (
	ContextBeforeModel    ContextEventKind = "before_model"
	ContextAfterAssistant ContextEventKind = "after_assistant"
	ContextAfterTool      ContextEventKind = "after_tool"
)

type ContextEvent struct {
	Kind    ContextEventKind
	Turn    int
	Query   string
	History []types.Message
	Tool    *ToolResult
}

type ContextDecision struct {
	ReplaceHistory bool
	History        []types.Message
}

// ContextPolicy decides whether a structured event requires a context change.
// It contains no execution code and can be replaced independently of the
// controller and compressor.
type ContextPolicy interface {
	Evaluate(ctx context.Context, event ContextEvent) (ContextPolicyDecision, error)
}

type ContextPolicyDecision struct {
	ReplaceHistory bool
	History        []types.Message
	Compress       bool
	MaxTokens      int
}

type ContextPolicyFunc func(context.Context, ContextEvent) (ContextPolicyDecision, error)

func (f ContextPolicyFunc) Evaluate(ctx context.Context, event ContextEvent) (ContextPolicyDecision, error) {
	return f(ctx, event)
}

// ContextController is the only autonomous context hook understood by the
// ReAct loop. Without explicit injection, no policy is evaluated.
type ContextController interface {
	Handle(ctx context.Context, event ContextEvent) (ContextDecision, error)
}

type ContextControllerFunc func(context.Context, ContextEvent) (ContextDecision, error)

func (f ContextControllerFunc) Handle(ctx context.Context, event ContextEvent) (ContextDecision, error) {
	return f(ctx, event)
}

// PolicyController is an optional generic assembly of policy and compressor.
// ReActLoop never constructs one and therefore has no threshold or algorithm.
type PolicyController struct {
	Policy     ContextPolicy
	Compressor Compressor
}

func (c PolicyController) Handle(ctx context.Context, event ContextEvent) (ContextDecision, error) {
	if c.Policy == nil {
		return ContextDecision{}, fmt.Errorf("seelectx: policy controller requires policy")
	}
	policyDecision, err := c.Policy.Evaluate(ctx, event)
	if err != nil {
		return ContextDecision{}, fmt.Errorf("seelectx: evaluate context policy: %w", err)
	}
	if policyDecision.ReplaceHistory {
		return ContextDecision{ReplaceHistory: true, History: cloneMessages(policyDecision.History)}, nil
	}
	if !policyDecision.Compress {
		return ContextDecision{}, nil
	}
	if c.Compressor == nil {
		return ContextDecision{}, fmt.Errorf("seelectx: policy requested compression without compressor")
	}
	compressed, err := c.Compressor.Compress(ctx, CompressionRequest{
		History: event.History, Query: event.Query, MaxTokens: policyDecision.MaxTokens,
	})
	if err != nil {
		return ContextDecision{}, fmt.Errorf("seelectx: policy compression: %w", err)
	}
	return ContextDecision{ReplaceHistory: true, History: compressed.Messages}, nil
}

// ContextEventObserver receives policy decisions without coupling seelectx to
// a telemetry implementation. A telemetry.EventSink can be adapted here.
type ContextEventObserver interface {
	Observe(ctx context.Context, event ContextEvent, decision ContextDecision, err error)
}

type ContextEventObserverFunc func(context.Context, ContextEvent, ContextDecision, error)

func (f ContextEventObserverFunc) Observe(ctx context.Context, event ContextEvent, decision ContextDecision, err error) {
	f(ctx, event, decision, err)
}

type ObservedContextController struct {
	Next     ContextController
	Observer ContextEventObserver
}

func (c ObservedContextController) Handle(ctx context.Context, event ContextEvent) (ContextDecision, error) {
	if c.Next == nil {
		err := fmt.Errorf("seelectx: observed context controller requires next controller")
		if c.Observer != nil {
			c.Observer.Observe(ctx, event, ContextDecision{}, err)
		}
		return ContextDecision{}, err
	}
	decision, err := c.Next.Handle(ctx, event)
	if c.Observer != nil {
		c.Observer.Observe(ctx, event, decision, err)
	}
	return decision, err
}
