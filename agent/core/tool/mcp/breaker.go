package mcp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// BreakerEventType 熔断器事件类型。
type BreakerEventType string

const (
	BreakerOpened     BreakerEventType = "opened"      // 熔断器打开，开始退避
	BreakerHalfOpen   BreakerEventType = "half_open"   // 退避到期，允许探测调用
	BreakerClosed     BreakerEventType = "closed"      // 熔断器关闭（恢复正常）
	BreakerRecovering BreakerEventType = "recovering"  // 后台恢复 ping 已启动
	BreakerRecovered  BreakerEventType = "recovered"   // 后台恢复成功
)

// BreakerEvent 熔断器状态变更事件。
type BreakerEvent struct {
	ServerName string
	Type       BreakerEventType
	Failures   int
}

// breakerState 单个 MCP server 的熔断状态。
type breakerState struct {
	failures   int
	lastFail   time.Time
	open       bool        // 熔断器打开：拒绝所有调用
	until      time.Time   // 熔断持续到此时（含退避）
	recovering bool        // 后台恢复 goroutine 是否已启动
}

// mcpBreaker 按 server name 管理熔断与自动恢复。
// 零值不可用，必须通过 newBreaker() 创建。
type mcpBreaker struct {
	mu           sync.Mutex
	servers      map[string]*breakerState
	maxFails     int           // 连续失败 N 次后打开熔断
	backoffBase  time.Duration // 首次熔断时长
	backoffMax   time.Duration // 最大熔断时长
	pingInterval time.Duration // 恢复检查间隔
	events       chan<- BreakerEvent // 事件通知 channel，nil = 不启用
}

func newBreaker() *mcpBreaker {
	return &mcpBreaker{
		servers:      make(map[string]*breakerState),
		maxFails:     3,
		backoffBase:  5 * time.Second,
		backoffMax:   60 * time.Second,
		pingInterval: 3 * time.Second,
	}
}

// SetEventsChannel 设置熔断器事件通知 channel。nil = 不启用。
// 必须在第一次调用 beforeCall/afterCall 之前设置。
func (b *mcpBreaker) SetEventsChannel(ch chan<- BreakerEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = ch
}

func (b *mcpBreaker) emit(serverName string, typ BreakerEventType, failures int) {
	if b.events == nil {
		return
	}
	select {
	case b.events <- BreakerEvent{ServerName: serverName, Type: typ, Failures: failures}:
	default:
		// channel 满则丢弃，不阻塞
	}
}

// beforeCall 在发起 MCP 调用前检查熔断器。返回 nil 表示允许调用。
func (b *mcpBreaker) beforeCall(serverName string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	st := b.servers[serverName]
	if st == nil {
		return nil
	}
	if !st.open {
		return nil
	}
	if time.Now().Before(st.until) {
		b.emit(serverName, BreakerOpened, st.failures)
		return fmt.Errorf("mcp breaker: server %q is degraded (backoff until %s)", serverName, st.until.Format("15:04:05"))
	}
	// 熔断超时到期，进入半开状态：允许一次探测调用
	st.open = false
	b.emit(serverName, BreakerHalfOpen, st.failures)
	return nil
}

// afterCall 在 MCP 调用返回后更新熔断状态。
// isConnErr 为 true 表示连接/传输层错误（需计入熔断），false 表示业务逻辑错误（不计入）。
func (b *mcpBreaker) afterCall(serverName string, isConnErr bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	st := b.servers[serverName]
	if st == nil {
		st = &breakerState{}
		b.servers[serverName] = st
	}

	if isConnErr {
		st.failures++
		st.lastFail = time.Now()
		if st.failures >= b.maxFails {
			st.open = true
			shift := st.failures - b.maxFails
			if shift > 4 {
				shift = 4
			}
			backoff := b.backoffBase * time.Duration(1<<shift)
			if backoff > b.backoffMax {
				backoff = b.backoffMax
			}
			st.until = time.Now().Add(backoff)
			b.emit(serverName, BreakerOpened, st.failures)
		}
	} else {
		st.failures = 0
		st.open = false
		b.emit(serverName, BreakerClosed, 0)
	}
}

// isOpen 返回熔断器是否处于打开状态（仅检查，不修改）。
func (b *mcpBreaker) isOpen(serverName string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.servers[serverName]
	if st == nil {
		return false
	}
	return st.open && time.Now().Before(st.until)
}

// startRecovery 启动后台恢复 goroutine。只启动一次。
// pingFn 返回 nil 表示服务器已恢复。
func (b *mcpBreaker) startRecovery(serverName string, pingFn func(context.Context) error) {
	b.mu.Lock()
	st := b.servers[serverName]
	if st == nil || st.recovering || !st.open {
		b.mu.Unlock()
		return
	}
	st.recovering = true
	b.emit(serverName, BreakerRecovering, st.failures)
	b.mu.Unlock()

	go func() {
		for {
			time.Sleep(b.pingInterval)

			b.mu.Lock()
			st := b.servers[serverName]
			if st == nil || !st.open {
				b.mu.Unlock()
				return
			}
			b.mu.Unlock()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			err := pingFn(ctx)
			cancel()

			if err == nil {
				b.mu.Lock()
				st := b.servers[serverName]
				if st != nil {
					st.failures = 0
					st.open = false
					st.recovering = false
				}
				b.mu.Unlock()
				b.emit(serverName, BreakerRecovered, 0)
				return
			}
		}
	}()
}

// isConnectivityError 判断是否为连接/传输层错误（而非 MCP 业务逻辑错误）。
func isConnectivityError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	indicators := []string{
		"connection refused", "connection reset", "broken pipe",
		"eof", "unexpected eof", "no such host", "no connection",
		"transport", "timeout", "deadline exceeded", "canceled",
		"connection closed", "tcp", "dial", "connectex",
		"wsarecv", "wsasend", "signal:", "exit status",
		"process already finished", "stdin",
	}
	for _, ind := range indicators {
		if strings.Contains(msg, ind) {
			return true
		}
	}
	return false
}
