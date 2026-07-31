package telemetry

import (
	"context"
	"errors"
	"fmt"
)

// Action describes an operation's intent before execution.
type Action struct {
	Type          EventType
	Name          string
	SpanName      string
	SpanKind      SpanKind
	CorrelationID string
	Attributes    Attributes
}

// Effect describes what actually happened after an action.
type Effect struct {
	Status     Status
	Error      error
	Attributes Attributes
}

// Invocation is the opaque token that joins Before and After calls.
type Invocation struct {
	action        Action
	correlationID string
	span          Span
	disabled      bool
}

// Hook instruments lifecycle boundaries without importing the instrumented module.
type Hook interface {
	Before(ctx context.Context, action Action) (context.Context, Invocation, error)
	After(ctx context.Context, invocation Invocation, effect Effect) error
}

// ErrorHook records an exception that is not already represented by an After effect.
type ErrorHook interface {
	OnError(ctx context.Context, name string, err error, attributes Attributes) error
}

// LifecycleHook converts before/after actions into correlated tracer events.
type LifecycleHook struct {
	tracer       Tracer
	strict       bool
	errorHandler func(error)
}

// HookOption configures observability failure isolation.
type HookOption func(*LifecycleHook)

// WithStrictHookErrors makes telemetry failures visible to the instrumented call.
// The default is best-effort isolation so observability outages do not stop Agent work.
func WithStrictHookErrors() HookOption {
	return func(hook *LifecycleHook) { hook.strict = true }
}

// WithHookErrorHandler observes best-effort telemetry failures.
func WithHookErrorHandler(handler func(error)) HookOption {
	return func(hook *LifecycleHook) { hook.errorHandler = handler }
}

// NewLifecycleHook creates a non-invasive lifecycle hook.
func NewLifecycleHook(tracer Tracer, options ...HookOption) (*LifecycleHook, error) {
	if tracer == nil {
		return nil, errors.New("telemetry tracer is required")
	}
	hook := &LifecycleHook{tracer: tracer}
	for _, option := range options {
		if option != nil {
			option(hook)
		}
	}
	return hook, nil
}

// Before starts a span and records the intent event.
func (h *LifecycleHook) Before(ctx context.Context, action Action) (context.Context, Invocation, error) {
	if phaseForEvent(action.Type) != PhaseBefore {
		return ctx, Invocation{}, fmt.Errorf("before hook requires a before event type: %q", action.Type)
	}
	correlationID := action.CorrelationID
	if correlationID == "" {
		var err error
		correlationID, err = newIdentifier(16)
		if err != nil {
			return ctx, Invocation{}, err
		}
	}
	spanName := action.SpanName
	if spanName == "" {
		spanName = action.Name
	}
	if spanName == "" {
		spanName = string(action.Type)
	}
	spanKind := action.SpanKind
	if spanKind == "" {
		spanKind = spanKindForEvent(action.Type)
	}

	var span Span
	var err error
	if _, ok := TraceFromContext(ctx); ok {
		ctx, span, err = h.tracer.StartSpan(ctx, spanName, spanKind, action.Attributes)
	} else {
		ctx, span, err = h.tracer.StartTrace(ctx, spanName, spanKind, action.Attributes)
	}
	if err != nil {
		hookErr := fmt.Errorf("start lifecycle span: %w", err)
		if h.handleError(hookErr) != nil {
			return ctx, Invocation{}, hookErr
		}
		return ctx, Invocation{action: action, correlationID: correlationID, disabled: true}, nil
	}

	trace := TraceContext{TraceID: span.TraceID(), SpanID: span.ID(), ParentSpanID: span.ParentID()}
	event := Event{
		Type:          action.Type,
		Phase:         PhaseBefore,
		Name:          action.Name,
		TraceID:       trace.TraceID,
		SpanID:        trace.SpanID,
		ParentSpanID:  trace.ParentSpanID,
		CorrelationID: correlationID,
		Attributes:    cloneAttributes(action.Attributes),
		Status:        StatusUnset,
	}
	if err := h.tracer.Record(ctx, event); err != nil {
		hookErr := fmt.Errorf("record lifecycle intent: %w", err)
		if h.handleError(hookErr) != nil {
			span.End(ctx, StatusError, err)
			return ctx, Invocation{}, hookErr
		}
	}
	return ctx, Invocation{action: action, correlationID: correlationID, span: span}, nil
}

// After records the effect, correlates it with the intent, and closes the span.
func (h *LifecycleHook) After(ctx context.Context, invocation Invocation, effect Effect) error {
	if invocation.disabled {
		return nil
	}
	if invocation.span == nil {
		return errors.New("invalid lifecycle invocation: span is missing")
	}
	afterType, err := matchingAfter(invocation.action.Type)
	if err != nil {
		return err
	}
	status := effect.Status
	if effect.Error != nil {
		status = StatusError
	} else if status == "" || status == StatusUnset {
		status = StatusOK
	}
	event := Event{
		Type:          afterType,
		Phase:         PhaseAfter,
		Name:          invocation.action.Name,
		TraceID:       invocation.span.TraceID(),
		SpanID:        invocation.span.ID(),
		ParentSpanID:  invocation.span.ParentID(),
		CorrelationID: invocation.correlationID,
		Attributes:    cloneAttributes(effect.Attributes),
		Status:        status,
	}
	if effect.Error != nil {
		event.Error = errorInfo(effect.Error)
	}
	recordErr := h.tracer.Record(ctx, event)
	invocation.span.End(ctx, status, effect.Error)
	if recordErr != nil {
		return h.handleError(fmt.Errorf("record lifecycle effect: %w", recordErr))
	}
	return nil
}

// OnError emits an instant error event on the current span.
func (h *LifecycleHook) OnError(ctx context.Context, name string, err error, attributes Attributes) error {
	if err == nil {
		return nil
	}
	trace, ok := TraceFromContext(ctx)
	if !ok {
		hookErr := errors.New("record lifecycle error: trace context is missing")
		return h.handleError(hookErr)
	}
	event := Event{
		Type:         EventError,
		Phase:        PhaseInstant,
		Name:         name,
		TraceID:      trace.TraceID,
		SpanID:       trace.SpanID,
		ParentSpanID: trace.ParentSpanID,
		Attributes:   cloneAttributes(attributes),
		Status:       StatusError,
		Error:        errorInfo(err),
	}
	if recordErr := h.tracer.Record(ctx, event); recordErr != nil {
		return h.handleError(fmt.Errorf("record lifecycle error: %w", recordErr))
	}
	return nil
}

func (h *LifecycleHook) handleError(err error) error {
	if err == nil {
		return nil
	}
	if h.errorHandler != nil {
		h.errorHandler(err)
	}
	if h.strict {
		return err
	}
	return nil
}

// Handler is the generic operation shape accepted by Decorate.
type Handler[I, O any] func(context.Context, I) (O, error)

// Decorate wraps a handler with Before/After telemetry and leaves business logic unaware of tracing.
func Decorate[I, O any](
	next Handler[I, O],
	hook Hook,
	action func(I) Action,
	effect func(O, error) Effect,
) Handler[I, O] {
	return func(ctx context.Context, input I) (output O, err error) {
		if next == nil {
			return output, errors.New("decorated handler is nil")
		}
		if hook == nil {
			return next(ctx, input)
		}
		intent := Action{}
		if action != nil {
			intent = action(input)
		}
		hookCtx, invocation, hookErr := hook.Before(ctx, intent)
		if hookErr != nil {
			return output, hookErr
		}
		defer func() {
			if recovered := recover(); recovered != nil {
				panicErr, ok := recovered.(error)
				if !ok {
					panicErr = fmt.Errorf("panic: %v", recovered)
				}
				_ = hook.After(hookCtx, invocation, Effect{
					Status: StatusError,
					Error:  panicErr,
					Attributes: Attributes{
						AttributeExceptionEscaped: true,
					},
				})
				panic(recovered)
			}
		}()
		output, err = next(hookCtx, input)
		outcome := Effect{Error: err}
		if effect != nil {
			outcome = effect(output, err)
		}
		return output, errors.Join(err, hook.After(hookCtx, invocation, outcome))
	}
}

func spanKindForEvent(eventType EventType) SpanKind {
	switch eventType {
	case EventAgentStart:
		return SpanAgent
	case EventLLMBefore:
		return SpanLLM
	case EventToolBefore:
		return SpanTool
	default:
		return SpanInternal
	}
}

func errorInfo(err error) *ErrorInfo {
	if err == nil {
		return nil
	}
	return &ErrorInfo{Type: fmt.Sprintf("%T", err), Message: err.Error()}
}

var (
	_ Hook      = (*LifecycleHook)(nil)
	_ ErrorHook = (*LifecycleHook)(nil)
)
