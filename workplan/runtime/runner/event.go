package runner

import (
	"context"
	"fmt"
	"strings"

	frameworkevent "github.com/RedHuang-0622/Seele/event"
)

// EventConfig binds one WorkPlan execution to a product-owned plan identity
// and an optional root event Sink. PlanID is supplied by the caller because
// WorkPlan itself deliberately has no product identity.
type EventConfig struct {
	Sink            frameworkevent.Sink
	PlanID          string
	RunID           string
	HeartbeatPolicy frameworkevent.HeartbeatPolicy
	ErrorHandler    frameworkevent.ErrorHandler
	Locators        []frameworkevent.Locator
}

// WithEventSink enables root event delivery for a runner. A non-empty planID
// is required when a Sink is configured so every WorkPlan event is addressable.
func WithEventSink(sink frameworkevent.Sink, planID string) Option {
	return func(r *Runner) {
		r.eventConfig.Sink = sink
		r.eventConfig.PlanID = planID
	}
}

// WithEventRunID fixes the run identity for caller-side correlation. Omit it
// to let the event Recorder create an opaque ID for each Run or Resume call.
func WithEventRunID(runID string) Option {
	return func(r *Runner) { r.eventConfig.RunID = runID }
}

// WithEventHeartbeatPolicy enables periodic node liveness events.
func WithEventHeartbeatPolicy(policy frameworkevent.HeartbeatPolicy) Option {
	return func(r *Runner) { r.eventConfig.HeartbeatPolicy = policy }
}

// WithEventErrorHandler observes Sink delivery failures without changing plan
// execution control flow.
func WithEventErrorHandler(handler frameworkevent.ErrorHandler) Option {
	return func(r *Runner) { r.eventConfig.ErrorHandler = handler }
}

// WithEventLocators adds module-owned locations to every event from this
// runner. Callers can compose Agent, WorkPlan, or product locators freely.
func WithEventLocators(locators ...frameworkevent.Locator) Option {
	return func(r *Runner) { r.eventConfig.Locators = append(r.eventConfig.Locators, locators...) }
}

// SetEventConfig replaces runner event configuration before execution.
func (r *Runner) SetEventConfig(config EventConfig) {
	if r == nil {
		return
	}
	config.Locators = append([]frameworkevent.Locator(nil), config.Locators...)
	r.eventConfig = config
}

func (r *Runner) startEvents(ctx context.Context) (context.Context, *frameworkevent.Recorder, error) {
	if r.eventConfig.Sink == nil {
		return ctx, nil, nil
	}
	if strings.TrimSpace(r.eventConfig.PlanID) == "" {
		return ctx, nil, fmt.Errorf("event plan ID is required when an event sink is configured")
	}
	locators := append([]frameworkevent.Locator(nil), r.eventConfig.Locators...)
	locators = append(locators, frameworkevent.LocatorFunc(func() frameworkevent.Location {
		ids := map[string]string{"plan_id": r.eventConfig.PlanID}
		if r.eventConfig.RunID != "" {
			ids["run_id"] = r.eventConfig.RunID
		}
		return frameworkevent.Location{Kind: "workplan.run", IDs: ids}
	}))
	recorder, err := frameworkevent.NewRecorder(r.eventConfig.Sink,
		frameworkevent.WithSource("workplan"),
		frameworkevent.WithScope(frameworkevent.Scope{PlanID: r.eventConfig.PlanID, RunID: r.eventConfig.RunID}),
		frameworkevent.WithLocators(locators...),
		frameworkevent.WithHeartbeatPolicy(r.eventConfig.HeartbeatPolicy),
		frameworkevent.WithErrorHandler(r.eventConfig.ErrorHandler),
	)
	if err != nil {
		return ctx, nil, fmt.Errorf("create workplan event recorder: %w", err)
	}
	return frameworkevent.WithRecorder(ctx, recorder), recorder, nil
}

func publishPlanStart(ctx context.Context, recorder *frameworkevent.Recorder) {
	if recorder != nil {
		recorder.Publish(ctx, frameworkevent.Event{
			Source: "workplan.runner", Type: frameworkevent.TypeLifecycle, Status: frameworkevent.StatusRunning,
		})
	}
}

func publishPlanEnd(ctx context.Context, recorder *frameworkevent.Recorder, resultAborted bool, err error) {
	if recorder == nil {
		return
	}
	status := frameworkevent.StatusCompleted
	if err != nil {
		status = frameworkevent.StatusFailed
	} else if resultAborted {
		status = frameworkevent.StatusCanceled
	}
	recorder.Publish(ctx, frameworkevent.Event{
		Source: "workplan.runner", Type: frameworkevent.TypeLifecycle, Status: status,
		Failure: frameworkevent.FailureFrom(err),
	})
}
