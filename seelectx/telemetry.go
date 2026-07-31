package seelectx

import (
	"context"
	"errors"
	"fmt"

	"github.com/RedHuang-0622/Seele/telemetry"
)

// TelemetryContextController decorates context policy evaluation with a
// correlated intent/effect pair. It is optional and does not change the
// wrapped controller's decisions.
type TelemetryContextController struct {
	Next ContextController
	Hook telemetry.Hook
}

func (c TelemetryContextController) Handle(ctx context.Context, event ContextEvent) (ContextDecision, error) {
	if c.Next == nil {
		return ContextDecision{}, fmt.Errorf("seelectx: telemetry context controller requires next controller")
	}
	if c.Hook == nil {
		return c.Next.Handle(ctx, event)
	}
	hookCtx, invocation, err := c.Hook.Before(ctx, telemetry.Action{
		Type: telemetry.EventContextAssembleBefore, Name: string(event.Kind),
		SpanName: "context." + string(event.Kind), SpanKind: telemetry.SpanInternal,
		Attributes: telemetry.Attributes{
			"seele.context.event":         string(event.Kind),
			"seele.context.turn":          event.Turn,
			"seele.context.message_count": len(event.History),
		},
	})
	if err != nil {
		return ContextDecision{}, fmt.Errorf("seelectx: telemetry context before: %w", err)
	}
	decision, handleErr := c.Next.Handle(hookCtx, event)
	afterErr := c.Hook.After(hookCtx, invocation, telemetry.Effect{
		Error: handleErr,
		Attributes: telemetry.Attributes{
			"seele.context.replaced":      decision.ReplaceHistory,
			"seele.context.message_count": len(decision.History),
		},
	})
	return decision, errors.Join(handleErr, afterErr)
}

// TelemetryCompressor decorates an explicit Compressor and records the
// compression budget, recursion depth, and resulting message count.
type TelemetryCompressor struct {
	Next Compressor
	Hook telemetry.Hook
}

func (c TelemetryCompressor) Compress(ctx context.Context, request CompressionRequest) (CompressionResult, error) {
	if c.Next == nil {
		return CompressionResult{}, fmt.Errorf("seelectx: telemetry compressor requires next compressor")
	}
	if c.Hook == nil {
		return c.Next.Compress(ctx, request)
	}
	hookCtx, invocation, err := c.Hook.Before(ctx, telemetry.Action{
		Type: telemetry.EventContextCompressBefore, Name: "compress",
		SpanName: "context.compress", SpanKind: telemetry.SpanInternal,
		Attributes: telemetry.Attributes{
			"seele.context.input_messages": len(request.History),
			"seele.context.max_tokens":     request.MaxTokens,
			"seele.context.version":        request.ContextVersion,
			"seele.context.prompt_version": request.PromptVersion,
		},
	})
	if err != nil {
		return CompressionResult{}, fmt.Errorf("seelectx: telemetry compression before: %w", err)
	}
	result, compressErr := c.Next.Compress(hookCtx, request)
	afterErr := c.Hook.After(hookCtx, invocation, telemetry.Effect{
		Error: compressErr,
		Attributes: telemetry.Attributes{
			"seele.context.output_messages":   len(result.Messages),
			"seele.context.compression_depth": result.Depth,
		},
	})
	return result, errors.Join(compressErr, afterErr)
}

var (
	_ ContextController = TelemetryContextController{}
	_ Compressor        = TelemetryCompressor{}
)
