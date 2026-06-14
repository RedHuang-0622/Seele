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
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	prov "github.com/RedHuang-0622/Seele/provider"
	"github.com/RedHuang-0622/Seele/core/session"
	types "github.com/RedHuang-0622/Seele/types"
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
				agent := session.New(llmClient, tools, "", 2)
				callStart := time.Now()
				_, err := agent.Chat(ctx, fmt.Sprintf("call tool_%d", idx))
				latencies[idx] = time.Since(callStart)
				if err != nil {
					atomic.AddInt32(&failCount, 1)
				} else {
					atomic.AddInt32(&okCount, 1)
				}
			}(i)
		}
		wg.Wait()
		elapsed := time.Since(start)

		t.Logf("10 并发 agent.Chat 完成: ok=%d fail=%d total=%v", okCount, failCount, elapsed)
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		t.Logf("延迟分布 %s", formatLatencyStats(latencies))
		if failCount > 0 {
			t.Errorf("%d agent.Chat calls failed", failCount)
		}
	})

	// ── 模拟 Fork：3 agent × 4 calls = 12 并发 agent.Chat ─────
	t.Run("fork_sim_3x4", func(t *testing.T) {
		llmSrv := newAutoMockLLM("tool_0", `{}`, `"all done"`)
		defer llmSrv.Close()

		llmClient, tools := newTestTools(llmSrv.URL())
		prov := newMockProvider("fork_sim")
		for i := 0; i < 10; i++ {
			prov.AddTool(fmt.Sprintf("tool_%d", i), "")
		}
		tools.Register(prov)

		ctx := context.Background()
		agents := 3
		callsPerAgent := 4
		totalCalls := agents * callsPerAgent

		var wg sync.WaitGroup
		var okCount, failCount int32
		latencies := make([]time.Duration, totalCalls)
		callIdx := int32(-1)

		start := time.Now()
		for a := 0; a < agents; a++ {
			wg.Add(1)
			go func(agentID int) {
				defer wg.Done()
				var innerWg sync.WaitGroup
				for c := 0; c < callsPerAgent; c++ {
					innerWg.Add(1)
					go func(call int) {
						defer innerWg.Done()
						agent := session.New(llmClient, tools, "", 2)
						idx := atomic.AddInt32(&callIdx, 1)
						callStart := time.Now()
						_, err := agent.Chat(ctx, fmt.Sprintf("call tool_%d", call))
						latencies[idx] = time.Since(callStart)
						if err != nil {
							atomic.AddInt32(&failCount, 1)
						} else {
							atomic.AddInt32(&okCount, 1)
						}
					}(c)
				}
				innerWg.Wait()
			}(a)
		}
		wg.Wait()
		elapsed := time.Since(start)

		t.Logf("%d agent × %d calls: ok=%d fail=%d total=%v",
			agents, callsPerAgent, okCount, failCount, elapsed)
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		t.Logf("延迟分布 %s", formatLatencyStats(latencies))
		if failCount > 0 {
			t.Errorf("%d/%d agent.Chat calls failed", failCount, totalCalls)
		}
	})
}

// =============================================================================
// 测试 2：Agent 管道连接释放验证
// =============================================================================

// countingProvider 记录 Dispatch 被调用的并发数，用于检测"连接"是否泄漏。
// 重构后通过 countingHandler（策略模式）实现计数逻辑。
type countingProvider struct {
	active    int64
	maxActive int64
	total     int64
	mu        sync.Mutex
	records   []dispatchRecord
	tools     []types.Tool
}

type dispatchRecord struct {
	name    string
	start   time.Time
	end     time.Time
	latency time.Duration
}

func newCountingProvider(name string) *countingProvider {
	return &countingProvider{}
}

func (p *countingProvider) ProviderName() string { return "counting" }

func (p *countingProvider) Tools() []prov.ToolEntry {
	entries := make([]prov.ToolEntry, len(p.tools))
	for i, t := range p.tools {
		name := t.Function.Name
		entries[i] = prov.ToolEntry{
			Definition: t,
			Handler: &countingHandler{
				toolName: name,
				counter:  p,
			},
		}
	}
	return entries
}

func (p *countingProvider) AddTool(name, desc string) {
	p.tools = append(p.tools, types.Tool{
		Type: "function",
		Function: types.ToolFunction{
			Name:        name,
			Description: desc,
			Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
	})
}

// countingHandler 实现 ToolHandler，统计并发调用数据。
type countingHandler struct {
	toolName string
	counter  *countingProvider
}

func (h *countingHandler) Execute(ctx context.Context, argsJSON string) (string, error) {
	active := atomic.AddInt64(&h.counter.active, 1)
	atomic.AddInt64(&h.counter.total, 1)

	for {
		cur := atomic.LoadInt64(&h.counter.maxActive)
		if active <= cur || atomic.CompareAndSwapInt64(&h.counter.maxActive, cur, active) {
			break
		}
	}

	h.counter.mu.Lock()
	rec := dispatchRecord{name: h.toolName, start: time.Now()}
	h.counter.mu.Unlock()

	// 模拟 50ms 处理时间
	time.Sleep(50 * time.Millisecond)

	atomic.AddInt64(&h.counter.active, -1)

	h.counter.mu.Lock()
	rec.end = time.Now()
	rec.latency = rec.end.Sub(rec.start)
	h.counter.records = append(h.counter.records, rec)
	h.counter.mu.Unlock()

	return `{"status":"ok","tool":"` + h.toolName + `","args":` + argsJSON + `}`, nil
}

// TestPool_AgentConnectionRelease 通过 Agent.Chat() 管道验证 dispatch 后连接正常释放。
func TestPool_AgentConnectionRelease(t *testing.T) {
	llmSrv := newAutoMockLLM("tool_0", `{}`, `"done"`)
	defer llmSrv.Close()

	llmClient, tools := newTestTools(llmSrv.URL())
	prov := newCountingProvider("leak_check")
	for i := 0; i < 6; i++ {
		prov.AddTool(fmt.Sprintf("tool_%d", i), "")
	}
	tools.Register(prov)

	ctx := context.Background()
	concurrency := 6

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			agent := session.New(llmClient, tools, "", 2)
			_, _ = agent.Chat(ctx, fmt.Sprintf("call tool_%d", idx))
		}(i)
	}
	wg.Wait()

	t.Logf("并发数=%d, 最大活跃=%d, 总调用=%d, 最终活跃=%d",
		concurrency, prov.maxActive, prov.total, prov.active)

	if prov.maxActive < int64(concurrency) {
		t.Logf("maxActive(%d) < concurrency(%d): 存在串行化瓶颈", prov.maxActive, concurrency)
	}

	if atomic.LoadInt64(&prov.active) != 0 {
		t.Errorf("连接泄漏: active=%d, expected 0", prov.active)
	} else {
		t.Log("OK: 所有 dispatch 完成后 active=0，连接已释放")
	}

	if prov.total != int64(concurrency) {
		t.Errorf("dispatch 次数不匹配: total=%d expected=%d", prov.total, concurrency)
	}

	var lats []time.Duration
	for _, r := range prov.records {
		lats = append(lats, r.latency)
	}
	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
	t.Logf("延迟: min=%v max=%v", lats[0], lats[len(lats)-1])
}

// =============================================================================
// 测试 3：Agent 管道队列堆积场景
// =============================================================================

// TestPool_AgentQueueBuildup 通过 Agent.Chat() 管道模拟 pool 容量不足时的行为。
func TestPool_AgentQueueBuildup(t *testing.T) {
	llmSrv := newAutoMockLLM("tool_0", `{}`, `"done"`)
	defer llmSrv.Close()

	llmClient, tools := newTestTools(llmSrv.URL())
	prov := newCountingProvider("queue_test")
	for i := 0; i < 4; i++ {
		prov.AddTool(fmt.Sprintf("tool_%d", i), "")
	}
	tools.Register(prov)

	waves := []int{8, 8, 8}

	for waveIdx, count := range waves {
		ctx := context.Background()
		var wg sync.WaitGroup
		var okCount, failCount int32
		var maxLat int64

		waveStart := time.Now()
		for i := 0; i < count; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				agent := session.New(llmClient, tools, "", 2)
				start := time.Now()
				_, err := agent.Chat(ctx, fmt.Sprintf("call tool_%d", idx%4))
				lat := time.Since(start)
				if err != nil {
					atomic.AddInt32(&failCount, 1)
				} else {
					atomic.AddInt32(&okCount, 1)
				}
				for {
					cur := atomic.LoadInt64(&maxLat)
					if lat.Nanoseconds() <= cur ||
						atomic.CompareAndSwapInt64(&maxLat, cur, lat.Nanoseconds()) {
						break
					}
				}
			}(i)
		}
		wg.Wait()
		waveLat := time.Since(waveStart)

		log.Printf("[POOL] wave=%d ok=%d fail=%d total=%v maxLat=%v",
			waveIdx, okCount, failCount, waveLat, time.Duration(maxLat))

		if failCount > 0 {
			t.Logf("第 %d 波: %d/%d 失败", waveIdx, failCount, count)
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Logf("三波完成: maxActive=%d total=%d finalActive=%d",
		prov.maxActive, prov.total, prov.active)
}

// =============================================================================
// 测试 4：集成——复现 ExampleFullOpsWorkflow 池子繁忙
// =============================================================================

func TestPool_Integration_WorkflowStress(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") == "" {
		t.Skip("set TEST_INTEGRATION=1 to run")
	}

	const (
		base    = "github.com/RedHuang-0622/Seele/"
		regPath = "../config/registry.yaml"
		cfgPath = "../config/config.yaml"
		hubAddr = ":51065"
	)

	tools := []struct {
		name string
		addr string
		pkg  string
	}{
		{"echo", "localhost:50101", base + "example_tools/example"},
		{"ping", "localhost:50102", base + "example_tools/ping"},
		{"suka_secret", "localhost:50100", base + "example_tools/suka_secret"},
	}
	for _, tl := range tools {
		tr := startToolProc(t, tl.name, tl.addr, tl.pkg)
		defer tr.stop(t)
		waitTCPPort(t, tl.addr, 15*time.Second)
	}
	probeResetTimer()

	eng := newProbeEngine(t, regPath, cfgPath, hubAddr)
	defer eng.Shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	t.Run("3_agents_concurrent_dispatch", func(t *testing.T) {
		agents := 3
		var wg sync.WaitGroup
		var okCount, failCount int32
		var totalLatency int64

		start := time.Now()
		for i := 0; i < agents; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				agent := eng.NewSession("", 2)

				var innerWg sync.WaitGroup
				tools := []string{"echo", "ping"}
				for _, tn := range tools {
					innerWg.Add(1)
					go func(toolName string) {
						defer innerWg.Done()
						callStart := time.Now()
						_, err := agent.Chat(ctx, fmt.Sprintf("调用 %s 工具", toolName))
						lat := time.Since(callStart)
						atomic.AddInt64(&totalLatency, lat.Nanoseconds())
						if err != nil {
							atomic.AddInt32(&failCount, 1)
							log.Printf("[POOL_STRESS] agent=%d tool=%s FAIL: %v", idx, toolName, err)
						} else {
							atomic.AddInt32(&okCount, 1)
							log.Printf("[POOL_STRESS] agent=%d tool=%s OK latency=%v", idx, toolName, lat)
						}
					}(tn)
				}
				innerWg.Wait()
			}(i)
		}
		wg.Wait()
		elapsed := time.Since(start)
		avgLat := time.Duration(totalLatency / int64(okCount+failCount+1))

		t.Logf("3 agents × 2 tools 结果: ok=%d fail=%d total=%v avg_lat=%v",
			okCount, failCount, elapsed, avgLat)

		if failCount > 0 {
			t.Logf("出现 %d 次失败——可能是 hub maxStreams 瓶颈", failCount)
		}
	})

	t.Run("gradual_pressure", func(t *testing.T) {
		for _, concurrency := range []int{1, 2, 4, 6, 8} {
			var wg sync.WaitGroup
			var ok, fail int32
			var latSum int64

			start := time.Now()
			for i := 0; i < concurrency; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					agent := eng.NewSession("", 1)
					cs := time.Now()
					_, err := agent.Chat(ctx, "调用 echo 工具，content=pressure_test")
					lat := time.Since(cs)
					atomic.AddInt64(&latSum, lat.Nanoseconds())
					if err != nil {
						atomic.AddInt32(&fail, 1)
						errStr := err.Error()
						if len(errStr) > 200 {
							errStr = errStr[:200]
						}
						log.Printf("[GRADUAL] concurrency=%d FAIL: %s", concurrency, errStr)
					} else {
						atomic.AddInt32(&ok, 1)
					}
				}()
			}
			wg.Wait()
			elapsed := time.Since(start)
			avg := time.Duration(latSum / int64(concurrency))

			status := "OK"
			if fail > 0 {
				status = fmt.Sprintf("WARN(%d failed)", fail)
			}
			if elapsed > time.Duration(concurrency)*500*time.Millisecond {
				status += " SLOW"
			}
			log.Printf("[GRADUAL] concurrency=%d ok=%d fail=%d avg=%v total=%v %s",
				concurrency, ok, fail, avg, elapsed, status)

			if fail > 0 {
				t.Logf("并发=%d 时出现 %d 次失败——可能是瓶颈点", concurrency, fail)
			}
		}
	})
}

// =============================================================================
// 测试 5：Provider 注册/注销并发安全
// =============================================================================

func TestPool_RegisterUnregisterRace(t *testing.T) {
	llmSrv := newAutoMockLLM("stable_tool", `{}`, `"done"`)
	defer llmSrv.Close()

	llmClient, tools := newTestTools(llmSrv.URL())
	prov1 := newMockProvider("stable")
	prov1.AddTool("stable_tool", "")
	tools.Register(prov1)

	ctx := context.Background()
	var wg sync.WaitGroup

	// 并发 agent.Chat（走完整管道）
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			agent := session.New(llmClient, tools, "", 2)
			_, _ = agent.Chat(ctx, "call stable_tool")
		}()
	}

	// 同时注册/注销 provider
	for i := 0; i < 5; i++ {
		prov := newMockProvider(fmt.Sprintf("dynamic_%d", i))
		prov.AddTool(fmt.Sprintf("dyn_tool_%d", i), "")
		tools.Register(prov)
		time.Sleep(1 * time.Millisecond)
		tools.Unregister(prov.ProviderName())
	}

	wg.Wait()
	t.Log("并发 agent.Chat + register/unregister 未 panic")
}

// =============================================================================
// 工具函数
// =============================================================================

func formatLatencyStats(lats []time.Duration) string {
	if len(lats) == 0 {
		return "empty"
	}
	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("n=%d min=%v ", len(lats), lats[0]))
	if len(lats) >= 2 {
		sb.WriteString(fmt.Sprintf("p50=%v ", lats[len(lats)/2]))
	}
	if len(lats) >= 10 {
		sb.WriteString(fmt.Sprintf("p90=%v ", lats[len(lats)*9/10]))
	}
	sb.WriteString(fmt.Sprintf("max=%v", lats[len(lats)-1]))
	return sb.String()
}
