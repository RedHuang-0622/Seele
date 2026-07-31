package telemetry

import (
	"context"
	"errors"
)

// RecorderSink adapts a Tracer to the callback-oriented EventSink contract.
type RecorderSink struct {
	Tracer Tracer
}

func (s RecorderSink) Emit(ctx context.Context, event Event) error {
	if s.Tracer == nil {
		return errors.New("recorder sink tracer is required")
	}
	return s.Tracer.Record(ctx, event)
}

// FanoutSink delivers immutable event copies to multiple callback sinks.
type FanoutSink []EventSink

func (sinks FanoutSink) Emit(ctx context.Context, event Event) error {
	var errs []error
	for _, sink := range sinks {
		if sink == nil {
			continue
		}
		if err := sink.Emit(ctx, cloneEvent(event)); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

var (
	_ EventSink = RecorderSink{}
	_ EventSink = FanoutSink{}
)
