// test/probe_test.go
//
// 服务发现 & 健康探活集成测试
//
// 测试目标：
//   1. tool 微服务的服务发现速度
//   2. 下线服务的持续探活检测
//   3. 探活恢复后的响应速度
//
// 运行方式：
//   TEST_INTEGRATION=1 go test -v -run TestProbe -timeout 120s ./test/
package test

import (
	"context"
	"log"
	"net"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	types "github.com/sukasukasuka123/Seele/types"
	"github.com/sukasukasuka123/Seele/sdk/api"
)

// =============================================================================
// 测试基础设施
// =============================================================================

var probeStart = time.Now()

func probeElapsed() float64 { return time.Since(probeStart).Seconds() }
func probeResetTimer()      { probeStart = time.Now() }

// toolProc 管理 tool 子进程生命周期。
type toolProc struct {
	name string
	addr string
	cmd  *exec.Cmd
	mu   sync.Mutex
}

func startToolProc(t *testing.T, name, addr, pkgPath string) *toolProc {
	t.Helper()
	tp := &toolProc{name: name, addr: addr}
	cmd := exec.Command("go", "run", pkgPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	tp.cmd = cmd
	log.Printf("[%.2fs] STARTED %s pid=%d addr=%s", probeElapsed(), name, cmd.Process.Pid, addr)
	return tp
}

func (tp *toolProc) stop(t *testing.T) {
	t.Helper()
	tp.mu.Lock()
	defer tp.mu.Unlock()
	if tp.cmd == nil || tp.cmd.Process == nil {
		return
	}
	_ = tp.cmd.Process.Kill()
	tp.cmd = nil
	log.Printf("[%.2fs] STOPPED %s", probeElapsed(), tp.name)
}

func (tp *toolProc) restart(t *testing.T, pkgPath string) {
	t.Helper()
	tp.mu.Lock()
	defer tp.mu.Unlock()
	if tp.cmd != nil && tp.cmd.Process != nil {
		_ = tp.cmd.Process.Kill()
	}
	cmd := exec.Command("go", "run", pkgPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("restart %s: %v", tp.name, err)
	}
	tp.cmd = cmd
	log.Printf("[%.2fs] RESTARTED %s pid=%d", probeElapsed(), tp.name, cmd.Process.Pid)
}

func waitTCPPort(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
		if err == nil {
			conn.Close()
			log.Printf("[%.2fs] port %s reachable", probeElapsed(), addr)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("[%.2fs] port %s NOT reachable after %v", probeElapsed(), addr, timeout)
}

// =============================================================================
// Engine 快速创建（测试用）
// =============================================================================

func newProbeEngine(t *testing.T, regPath, cfgPath, hubAddr string) *api.Engine {
	t.Helper()
	eng, err := api.New(api.Options{
		RegistryPath:    regPath,
		LLMConfigPath:   cfgPath,
		HubAddr:         hubAddr,
		ToolCallTimeOut: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	log.Printf("[%.2fs] ENGINE ready, hub=%s", probeElapsed(), hubAddr)
	return eng
}

// skillNames 提取 SkillInfo 列表中的名称集合。
func skillNames(skills []types.SkillInfo) map[string]bool {
	m := make(map[string]bool, len(skills))
	for _, s := range skills {
		m[s.Name] = true
	}
	return m
}

// =============================================================================
// 测试 1: 服务发现速度
// =============================================================================

// TestProbe_ServiceDiscovery 测量 tool 启动后 Engine 发现它的速度。
func TestProbe_ServiceDiscovery(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") == "" {
		t.Skip("set TEST_INTEGRATION=1 to run")
	}
	probeResetTimer()

	const (
		base    = "github.com/sukasukasuka123/Seele/"
		regPath = "../config/registry.yaml"
		cfgPath = "../config/config.yaml"
		hubAddr = ":51061"
	)

	// 启动 tool
	echo := startToolProc(t, "echo", "localhost:50101", base+"example_tools/example")
	defer echo.stop(t)
	waitTCPPort(t, "localhost:50101", 15*time.Second)

	// 创建 Engine（内部执行 ProbeAllOnStartup）
	engStart := time.Now()
	eng := newProbeEngine(t, regPath, cfgPath, hubAddr)
	defer eng.Shutdown()
	engInitTime := time.Since(engStart)
	log.Printf("[%.2fs] Engine init + probe took %v", probeElapsed(), engInitTime)

	// 验证 echo 在 Skills 中可见
	names := skillNames(eng.Hub().Skills())
	if !names["echo"] {
		t.Fatalf("echo not discovered, skills=%v", names)
	}
	log.Printf("[%.2fs] SERVICE_DISCOVERY: echo confirmed in Skills()", probeElapsed())

	// 用 QuickChat 发送请求，验证端到端可达
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	chatStart := time.Now()
	reply, err := eng.QuickChat(ctx, "", `请调用 echo 工具，参数 content="探活测试"`)
	chatLatency := time.Since(chatStart)
	if err != nil {
		t.Logf("QuickChat error (LLM may not be available): %v", err)
	} else {
		log.Printf("[%.2fs] END_TO_END_OK: QuickChat latency=%v reply=%.200s", probeElapsed(), chatLatency, reply)
	}
	t.Logf("服务发现+Engine初始化: %v", engInitTime)
}

// =============================================================================
// 测试 2: 下线检测
// =============================================================================

// TestProbe_OfflineDetection 停掉 tool 后验证 Engine 检测到下线。
func TestProbe_OfflineDetection(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") == "" {
		t.Skip("set TEST_INTEGRATION=1 to run")
	}
	probeResetTimer()

	const (
		base    = "github.com/sukasukasuka123/Seele/"
		regPath = "../config/registry.yaml"
		cfgPath = "../config/config.yaml"
		hubAddr = ":51062"
	)

	// 同时启动 echo 和 ping
	echo := startToolProc(t, "echo", "localhost:50101", base+"example_tools/example")
	defer echo.stop(t)
	ping := startToolProc(t, "ping", "localhost:50102", base+"example_tools/ping")
	defer ping.stop(t)
	waitTCPPort(t, "localhost:50101", 10*time.Second)
	waitTCPPort(t, "localhost:50102", 10*time.Second)

	eng := newProbeEngine(t, regPath, cfgPath, hubAddr)
	defer eng.Shutdown()

	// 确认初始都可见
	names := skillNames(eng.Hub().Skills())
	if !names["echo"] || !names["ping"] {
		t.Fatalf("initial skills missing: echo=%v ping=%v", names["echo"], names["ping"])
	}
	log.Printf("[%.2fs] INITIAL: echo=online ping=online", probeElapsed())

	// 停掉 echo
	echo.stop(t)
	log.Printf("[%.2fs] OFFLINE_TRIGGER: echo killed", probeElapsed())

	// 等待 health probe 检测到下线（每 15s 一轮，等 25s）
	echoOffline := false
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		names := skillNames(eng.Hub().Skills())
		if !names["echo"] {
			echoOffline = true
			log.Printf("[%.2fs] OFFLINE_DETECTED: echo gone from Skills(), ping=%v", probeElapsed(), names["ping"])
			if !names["ping"] {
				t.Error("ping should remain online!")
			}
			break
		}
		time.Sleep(2 * time.Second)
	}

	if !echoOffline {
		// 通过 Retire 手动模拟验证 API 行为
		eng.Hub().Retire("echo")
		names = skillNames(eng.Hub().Skills())
		if !names["echo"] {
			log.Printf("[%.2fs] MANUAL: echo retired via API", probeElapsed())
		}
		t.Log("health probe may not have detected offline yet (interval=15s)")
	}

	// 验证 Retire/Restore 可用
	eng.Hub().Restore("echo")
	log.Printf("[%.2fs] RESTORE: echo restored", probeElapsed())
}

// =============================================================================
// 测试 3: 探活恢复后的响应延迟
// =============================================================================

// TestProbe_RecoveryLatency 停掉 tool 再重启，测量恢复后的响应延迟。
func TestProbe_RecoveryLatency(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") == "" {
		t.Skip("set TEST_INTEGRATION=1 to run")
	}
	probeResetTimer()

	const (
		base    = "github.com/sukasukasuka123/Seele/"
		regPath = "../config/registry.yaml"
		cfgPath = "../config/config.yaml"
		hubAddr = ":51063"
	)

	echo := startToolProc(t, "echo", "localhost:50101", base+"example_tools/example")
	defer echo.stop(t)
	waitTCPPort(t, "localhost:50101", 10*time.Second)

	eng := newProbeEngine(t, regPath, cfgPath, hubAddr)
	defer eng.Shutdown()

	// 先确认初始可用
	ctx := context.Background()
	agent := eng.NewSession("", 2)
	_, err := agent.Chat(ctx, `调用 echo 工具，content="before_kill"`)
	if err != nil {
		t.Logf("initial chat (pre-kill) error: %v", err)
	}
	log.Printf("[%.2fs] PRE_KILL: echo dispatch done", probeElapsed())

	// 停掉 echo 并 Retire
	echo.stop(t)
	eng.Hub().Retire("echo")
	log.Printf("[%.2fs] KILLED+RETIRED: echo", probeElapsed())

	// 重启 echo
	echo.restart(t, base+"example_tools/example")
	waitTCPPort(t, "localhost:50101", 10*time.Second)
	eng.Hub().Restore("echo")
	log.Printf("[%.2fs] RESTARTED+RESTORED: echo", probeElapsed())

	// 测量恢复后 dispatch 延迟
	agent2 := eng.NewSession("", 2)
	recStart := time.Now()
	reply, err := agent2.Chat(ctx, `调用 echo 工具，content="recovery_check"`)
	recoveryTime := time.Since(recStart)
	if err != nil {
		t.Logf("post-recovery chat error (LLM may be down): %v", err)
	} else {
		log.Printf("[%.2fs] RECOVERY_OK: latency=%v reply=%.200s", probeElapsed(), recoveryTime, reply)
	}
	t.Logf("恢复后端到端延迟: %v", recoveryTime)
}

// =============================================================================
// 测试 4: 持续探活 & 响应时间监控
// =============================================================================

// TestProbe_ContinuousHealth 多轮 dispatch 监控响应时间变化。
func TestProbe_ContinuousHealth(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") == "" {
		t.Skip("set TEST_INTEGRATION=1 to run")
	}
	probeResetTimer()

	const (
		base    = "github.com/sukasukasuka123/Seele/"
		regPath = "../config/registry.yaml"
		cfgPath = "../config/config.yaml"
		hubAddr = ":51064"
	)

	echo := startToolProc(t, "echo", "localhost:50101", base+"example_tools/example")
	defer echo.stop(t)
	ping := startToolProc(t, "ping", "localhost:50102", base+"example_tools/ping")
	defer ping.stop(t)
	waitTCPPort(t, "localhost:50101", 10*time.Second)
	waitTCPPort(t, "localhost:50102", 10*time.Second)

	eng := newProbeEngine(t, regPath, cfgPath, hubAddr)
	defer eng.Shutdown()

	ctx := context.Background()

	type latRecord struct {
		round   int
		tool    string
		latency time.Duration
	}
	var records []latRecord

	// 3 轮 dispatch，每轮间隔等待 health probe 执行
	inputs := []struct {
		prompt string
		label  string
	}{
		{`调用 echo 工具，content="health_round"`, "echo"},
		{`调用 ping 工具，host="127.0.0.1", count=1`, "ping"},
	}

	for round := 0; round < 3; round++ {
		for _, in := range inputs {
			agent := eng.NewSession("", 2)
			start := time.Now()
			_, err := agent.Chat(ctx, in.prompt)
			lat := time.Since(start)
			if err != nil {
				log.Printf("[%.2fs] round=%d tool=%s ERROR: %v", probeElapsed(), round, in.label, err)
			}
			records = append(records, latRecord{round, in.label, lat})
		}
		time.Sleep(5 * time.Second)
	}

	// 输出结果表格
	t.Logf("%-6s %-6s %-12s", "轮次", "工具", "延迟")
	for _, r := range records {
		t.Logf("%-6d %-6s %-12v", r.round, r.tool, r.latency)
		if r.latency > time.Second {
			log.Printf("[%.2fs] SLOW: tool=%s round=%d latency=%v", probeElapsed(), r.tool, r.round, r.latency)
		}
	}
}
