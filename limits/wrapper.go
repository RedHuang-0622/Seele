package limits

import (
	"context"
	"errors"
	"time"

	"github.com/RedHuang-0622/Seele/types"
)

// CompleteOnly is the minimum synchronous capability limits can decorate.
//
// It exists because products (seelex among them) hand limits a deliberately
// narrow completer interface such as agent.Completer, which only guarantees
// Complete. Wrapping such a value must not require the caller to widen its own
// type: WrapComplete accepts CompleteOnly and recovers full streaming whenever
// the concrete value happens to implement types.ChatCompleter.
type CompleteOnly interface {
	Complete(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error)
}

// completeOnlyAdapter promotes a CompleteOnly completer to types.ChatCompleter.
//
// The streaming behaviour mirrors agent.NewWithComponents' composedClient: the
// full reply arrives in one piece and is delivered as a single text delta. Use
// it only when the inner value genuinely lacks streaming; WrapComplete prefers
// the real streaming path when it is available.
type completeOnlyAdapter struct {
	inner CompleteOnly
}

var _ types.ChatCompleter = (*completeOnlyAdapter)(nil)

func (a *completeOnlyAdapter) Complete(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	return a.inner.Complete(ctx, messages, tools)
}

func (a *completeOnlyAdapter) CompleteStream(
	ctx context.Context,
	messages []types.Message,
	tools []types.Tool,
	onChunk func(delta string),
) (string, string, []types.ToolCall, error) {
	message, err := a.inner.Complete(ctx, messages, tools)
	if err != nil {
		return "", "", nil, err
	}
	content := ""
	if message.Content != nil {
		content = *message.Content
		if onChunk != nil && content != "" {
			onChunk(content)
		}
	}
	return content, message.ReasoningContent, message.ToolCalls, nil
}

func (a *completeOnlyAdapter) CompleteStreamEvents(
	ctx context.Context,
	messages []types.Message,
	tools []types.Tool,
	onEvent func(types.StreamEvent),
) (string, string, []types.ToolCall, error) {
	return a.CompleteStream(ctx, messages, tools, func(delta string) {
		if onEvent != nil {
			onEvent(types.StreamEvent{Type: types.StreamEventText, Content: delta})
		}
	})
}

// wrapper is the assembled request path: admission, retry classification and
// token settlement around one inner completer.
//
// Retry rules that matter for correctness:
//   - The non-streaming path retries on retryable failures and settles the
//     provider-reported usage against the estimate.
//   - The streaming paths retry only when nothing has been emitted yet, so a
//     client never sees duplicated output.
//   - Every attempt re-acquires admission, because each attempt consumes real
//     provider quota.
type wrapper struct {
	assembly *Assembly
	gate     *Gate
	next     types.ChatCompleter
	key      string
}

var _ types.ChatCompleter = (*wrapper)(nil)

func (w *wrapper) Complete(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	cost := w.assembly.Cost(messages, tools)
	for attempt := 1; ; attempt++ {
		permit, err := w.admit(ctx, cost)
		if err != nil {
			return types.Message{}, err
		}
		message, callErr := w.next.Complete(ctx, messages, tools)
		if callErr == nil {
			if message.Usage != nil {
				permit.Settle(*message.Usage)
			}
			permit.Release()
			return message, nil
		}
		permit.Release()

		retry, waitErr := w.retryOrStop(ctx, attempt, callErr)
		if waitErr != nil {
			return types.Message{}, waitErr
		}
		if !retry {
			return types.Message{}, callErr
		}
	}
}

func (w *wrapper) CompleteStream(
	ctx context.Context,
	messages []types.Message,
	tools []types.Tool,
	onChunk func(delta string),
) (string, string, []types.ToolCall, error) {
	cost := w.assembly.Cost(messages, tools)
	for attempt := 1; ; attempt++ {
		permit, err := w.admit(ctx, cost)
		if err != nil {
			return "", "", nil, err
		}
		emitted := false
		guarded := func(delta string) {
			emitted = true
			if onChunk != nil {
				onChunk(delta)
			}
		}
		content, reasoning, toolCalls, callErr := w.next.CompleteStream(ctx, messages, tools, guarded)
		permit.Release()
		if callErr == nil {
			return content, reasoning, toolCalls, nil
		}
		if emitted {
			return content, reasoning, toolCalls, callErr
		}
		retry, waitErr := w.retryOrStop(ctx, attempt, callErr)
		if waitErr != nil {
			return content, reasoning, toolCalls, waitErr
		}
		if !retry {
			return content, reasoning, toolCalls, callErr
		}
	}
}

func (w *wrapper) CompleteStreamEvents(
	ctx context.Context,
	messages []types.Message,
	tools []types.Tool,
	onEvent func(types.StreamEvent),
) (string, string, []types.ToolCall, error) {
	cost := w.assembly.Cost(messages, tools)
	for attempt := 1; ; attempt++ {
		permit, err := w.admit(ctx, cost)
		if err != nil {
			return "", "", nil, err
		}
		emitted := false
		guarded := func(event types.StreamEvent) {
			if event.Type != types.StreamEventError {
				emitted = true
			}
			if onEvent != nil {
				onEvent(event)
			}
		}
		content, reasoning, toolCalls, callErr := w.next.CompleteStreamEvents(ctx, messages, tools, guarded)
		permit.Release()
		if callErr == nil {
			return content, reasoning, toolCalls, nil
		}
		if emitted {
			return content, reasoning, toolCalls, callErr
		}
		retry, waitErr := w.retryOrStop(ctx, attempt, callErr)
		if waitErr != nil {
			return content, reasoning, toolCalls, waitErr
		}
		if !retry {
			return content, reasoning, toolCalls, callErr
		}
	}
}

// admit performs one admission and reports the outcome. A context that is
// already done never reaches the provider.
func (w *wrapper) admit(ctx context.Context, cost Cost) (*Permit, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	permit, err := w.gate.Acquire(ctx, cost)
	if err != nil {
		kind := ObservationRejected
		if errors.Is(err, ErrQueueTimeout) {
			kind = ObservationQueueTimeout
		}
		w.assembly.emit(Observation{Kind: kind, Key: w.key, Err: err})
		return nil, err
	}
	w.assembly.emit(Observation{Kind: ObservationAdmitted, Key: w.key, Wait: permit.Wait()})
	return permit, nil
}

// retryOrStop waits out the backoff for the next attempt. It returns
// (false, nil) when no further attempt should happen, and (false, err) when the
// caller's context ended while waiting.
func (w *wrapper) retryOrStop(ctx context.Context, attempt int, callErr error) (bool, error) {
	if ctx != nil && ctx.Err() != nil {
		return false, ctx.Err()
	}
	delay, retry := RetryDelay(attempt, w.assembly.RetryPolicy(), callErr, w.assembly.randSource())
	if !retry {
		return false, nil
	}
	w.gate.recordRetry()
	w.assembly.emit(Observation{
		Kind:    ObservationRetry,
		Key:     w.key,
		Attempt: attempt,
		Delay:   delay,
		Err:     callErr,
	})
	if delay <= 0 {
		return true, nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-timer.C:
		return true, nil
	}
}
