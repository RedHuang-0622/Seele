package gateway

import (
	"context"
	"testing"

	roottools "github.com/RedHuang-0622/Seele/tools"
)

// Benchmarks for the gateway permission gate. Run with:
//
//	go test -bench . -benchmem -run '^$' ./tools/gateway
func BenchmarkGatewayDispatchAllowed(b *testing.B) {
	g := newPermGateway(
		makeMetaEntry("read_doc", &roottools.ToolMeta{Kind: roottools.ToolKindRead}),
	)
	ctx := ctxWithSubject(context.Background(), "guest")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := g.Dispatch(ctx, "read_doc", `{}`); err != nil {
			b.Fatalf("Dispatch: %v", err)
		}
	}
}

func BenchmarkGatewayVisibleTools(b *testing.B) {
	g := newPermGateway(
		makeMetaEntry("read_doc", &roottools.ToolMeta{Kind: roottools.ToolKindRead}),
		makeMetaEntry("write_doc", &roottools.ToolMeta{Kind: roottools.ToolKindWrite}),
		makeMetaEntry("ctl_stop", &roottools.ToolMeta{Kind: roottools.ToolKindControl, Signal: roottools.SignalTerm}),
	)
	ctx := ctxWithSubject(context.Background(), "guest")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = g.VisibleTools(ctx)
	}
}
