package main

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestRealAPIBuiltinSmoke(t *testing.T) {
	if os.Getenv("RUN_REAL_API_SMOKE") != "true" {
		t.Skip("set RUN_REAL_API_SMOKE=true and provide SMOKE_CONFIG or SEELE_SMOKE_* credentials")
	}
	options := optionsFromEnvironment()
	client, model, err := newChatClient(options)
	if err != nil {
		t.Fatalf("create real client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), options.Timeout)
	defer cancel()
	started := time.Now()
	results, err := runSmokeSuite(ctx, client)
	if err != nil {
		t.Fatalf("real API smoke model=%s: %v", model, err)
	}
	for _, result := range results {
		t.Logf("case=%s tool=%s result=%s reply=%s", result.Name, result.Tool, result.ToolResult, compact(result.Reply, 120))
	}
	t.Logf("real API smoke passed model=%s cases=%d duration=%s", model, len(results), time.Since(started))
}
