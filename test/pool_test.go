// test/pool_test.go
//
// 连接池并发压测 & 池子繁忙问题复现
//
// 测试目标：
//  1. 模拟 ExampleFullOpsWorkflow 多 Agent + Fork 并发 dispatch 场景
//  2. 确认池子繁忙是"连接没释放"还是"池子太小"
//  3. 测量不同并发度下的 dispatch 延迟分布
//
// Agent 使用真实 Runtime.NewAgent() + autoMockLLM，
// 走完整 ReAct 循环（LLM → tool_calls → dispatch → text）。
//
// 运行：
//
//	单元测试：go test -v -run TestPool -timeout 60s ./test/
//	集成测试：TEST_INTEGRATION=1 go test -v -run TestPool_Integration -timeout 120s ./test/
package test

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	seelectx "github.com/RedHuang-0622/Seele/context"
)

// =============================================================================
// 测试 1：Agent 管道并发 Dispatch（真实 Agent + autoMockLLM）
// =============================================================================

// TestPool_AgentPipeline 使用真实 Agent.Chat() 管道，
// autoMockLLM 自动在 tool_calls / text 之间切换，支持并发 Agent 安全交叠。
func TestPool_AgentPipeline(t *testing.T) {
	// ── 单 provider 10 并发 agent.Chat ──────────────────────────
	t.Run("single_provider_10_concurrent", func(t *testing.T) {
		llmSrv := newAutoMockLLM("tool_0", `{}`, `"all done"`)
		defer llmSrv.Close()

		llmClient, tools := newTestTools(llmSrv.URL())
		prov := newMockProvider("pressure")
		for i := 0; i < 10; i++ {
			prov.AddTool(fmt.Sprintf("tool_%d", i), fmt.Sprintf("tool %d for pressure test", i))
		}
		tools.Register(prov)

		ctx := context.Background()
		var wg sync.WaitGroup
		var okCount, failCount int32
		latencies := make([]time.Duration, 10)

		start := time.Now()
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				agent := seelectx.New(llmClient, tools, "", seelectx.SessionConfig{MaxLoops: 2})
				callStart := time.Now()
				_, err := agent.Chat(ctx, fmt.Sprintf("call tool_%d", idx))
				latencies[idx] = time.Since(callStart)
				if err != nil {
					atomic.AddInt32(&failCount, 1)
					t.Logf("agent[%d] error: %v", idx, err)
				} else {
					atomic.AddInt32(&okCount, 1)
				}
			}(i)
		}
		wg.Wait()
		totalTime := time.Since(start)

		t.Logf("single_provider_10_concurrent: ok=%d fail=%d total=%v",
			okCount, failCount, totalTime)
		if failCount > 0 {
			t.Errorf("%d agents failed", failCount)
		}
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		t.Logf("latency p50=%v p90=%v p99=%v",
			latencies[len(latencies)*5/10],
			latencies[len(latencies)*9/10],
			latencies[len(latencies)-1])
	})

	// ── 多 provider 并发 ───────────────────────────────────────
	t.Run("multi_provider_10_concurrent", func(t *testing.T) {
		llmSrv := newAutoMockLLM("tool", `{}`, `"all done"`)
		defer llmSrv.Close()

		llmClient, tools := newTestTools(llmSrv.URL())
		for i := 0; i < 10; i++ {
			tools.Register(newMockProvider(fmt.Sprintf("provider_%d", i)))
		}

		ctx := context.Background()
		var wg sync.WaitGroup
		var okCount, failCount int32

		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				agent := seelectx.New(llmClient, tools, "", seelectx.SessionConfig{MaxLoops: 2})
				_, err := agent.Chat(ctx, fmt.Sprintf("call tool"))
				if err != nil {
					atomic.AddInt32(&failCount, 1)
				} else {
					atomic.AddInt32(&okCount, 1)
				}
			}(i)
		}
		wg.Wait()
		if failCount > 0 {
			t.Errorf("%d agents failed", failCount)
		}
	})

	// ── 单 provider 多工具并发 ────────────────────────────────
	t.Run("multi_tool_10_concurrent", func(t *testing.T) {
		llmSrv := newAutoMockLLM("tool_0", `{}`, `"all done"`)
		defer llmSrv.Close()

		llmClient, tools := newTestTools(llmSrv.URL())
		prov := newMockProvider("multi_tool")
		for i := 0; i < 20; i++ {
			prov.AddTool(fmt.Sprintf("tool_%d", i), fmt.Sprintf("tool %d", i))
		}
		tools.Register(prov)

		ctx := context.Background()
		var wg sync.WaitGroup
		var okCount, failCount int32

		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				agent := seelectx.New(llmClient, tools, "", seelectx.SessionConfig{MaxLoops: 2})
				_, err := agent.Chat(ctx, fmt.Sprintf("call tool_%d", idx%20))
				if err != nil {
					atomic.AddInt32(&failCount, 1)
				} else {
					atomic.AddInt32(&okCount, 1)
				}
			}(i)
		}
		wg.Wait()
		if failCount > 0 {
			t.Errorf("%d agents failed", failCount)
		}
	})
}
