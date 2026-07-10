package api

import (
	"sort"
	"sync"
	"time"
)

// ProviderType 模型供应商类型
type ProviderType string

const (
	ProviderOpenAI    ProviderType = "openai"
	ProviderAnthropic ProviderType = "anthropic"
)

// Account 单个 API 账号
type Account struct {
	Name        string       // 账号名称，唯一标识
	Provider    ProviderType // 供应商
	BaseURL     string       // API 地址
	APIKey      string       // API 密钥
	Model       string       // 默认模型
	Priority    int          // 优先级（数字越小优先级越高）
	MaxRPM      int          // 每分钟最大请求数
	Disabled    bool         // 是否禁用
	MaxTokens   int          // 覆盖全局 llm_config.max_tokens（0=使用全局）
	Timeout     int          // 覆盖全局 llm_config.timeout（0=使用全局）
	Temperature float64      // 覆盖全局 llm_config.temperature（0=使用全局）

	// 运行时限流状态
	mu     sync.Mutex
	window []time.Time // 滑动窗口：当前分钟内各请求的时间戳
}

// allow 检查是否允许发送请求（基于 RPM 限流）。
// 返回 true 表示允许，false 表示超过限制。
func (a *Account) allow() bool {
	if a.MaxRPM <= 0 {
		return true // 不限流
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	// 移除窗口外（超过 1 分钟）的旧记录
	cutoff := now.Add(-time.Minute)
	j := 0
	for _, t := range a.window {
		if t.After(cutoff) {
			a.window[j] = t
			j++
		}
	}
	a.window = a.window[:j]
	if len(a.window) >= a.MaxRPM {
		return false // 超限
	}
	a.window = append(a.window, now)
	return true
}

// AccountPool 账号池
type AccountPool struct {
	accounts []*Account
	current  int // round-robin 索引
	mu       sync.Mutex
}

// NewAccountPool 创建账号池，按 Priority 升序排序
func NewAccountPool(accounts ...*Account) *AccountPool {
	sort.Slice(accounts, func(i, j int) bool {
		return accounts[i].Priority < accounts[j].Priority
	})
	return &AccountPool{
		accounts: accounts,
	}
}

// Add 添加账号，添加后重新按优先级排序
func (ap *AccountPool) Add(a *Account) {
	ap.mu.Lock()
	defer ap.mu.Unlock()
	ap.accounts = append(ap.accounts, a)
	sort.Slice(ap.accounts, func(i, j int) bool {
		return ap.accounts[i].Priority < ap.accounts[j].Priority
	})
}

// nextIndex 从 current 位置开始查找下一个可用账号的索引。
// 返回 -1 表示无可用账号。
func (ap *AccountPool) nextIndex() int {
	n := len(ap.accounts)
	if n == 0 {
		return -1
	}
	for i := 0; i < n; i++ {
		idx := (ap.current + i) % n
		if !ap.accounts[idx].Disabled {
			ap.current = (idx + 1) % n
			return idx
		}
	}
	return -1
}

// Get 获取下一个可用且未被限流的账号（round-robin 轮询，按优先级排序）。
// 所有账号都被限流或禁用时返回 nil。
func (ap *AccountPool) Get() *Account {
	ap.mu.Lock()
	defer ap.mu.Unlock()
	n := len(ap.accounts)
	if n == 0 {
		return nil
	}
	for i := 0; i < n; i++ {
		idx := (ap.current + i) % n
		a := ap.accounts[idx]
		if !a.Disabled && a.allow() {
			ap.current = (idx + 1) % n
			return a
		}
	}
	return nil
}

// GetByProvider 获取指定供应商的可用且未被限流的账号。
// 没有匹配的可用账号时返回 nil。
func (ap *AccountPool) GetByProvider(provider ProviderType) *Account {
	ap.mu.Lock()
	defer ap.mu.Unlock()
	n := len(ap.accounts)
	if n == 0 {
		return nil
	}
	for i := 0; i < n; i++ {
		idx := (ap.current + i) % n
		a := ap.accounts[idx]
		if !a.Disabled && a.Provider == provider && a.allow() {
			ap.current = (idx + 1) % n
			return a
		}
	}
	return nil
}

// All 返回所有账号的副本
func (ap *AccountPool) All() []*Account {
	ap.mu.Lock()
	defer ap.mu.Unlock()
	result := make([]*Account, len(ap.accounts))
	copy(result, ap.accounts)
	return result
}

// Select 按名称切换当前账号。
// 找到时定位到该账号（下一次 Get 返回它），返回账号本身。
// 找不到或已禁用时返回 nil，current 不动。
func (ap *AccountPool) Select(name string) *Account {
	ap.mu.Lock()
	defer ap.mu.Unlock()
	for i, a := range ap.accounts {
		if a.Name == name && !a.Disabled {
			ap.current = i // 定位
			return a
		}
	}
	return nil
}

// Current 返回当前指向的账号（下一次 Get 会返回的账号）。
// 没有可用账号时返回 nil。
func (ap *AccountPool) Current() *Account {
	ap.mu.Lock()
	defer ap.mu.Unlock()
	if len(ap.accounts) == 0 {
		return nil
	}
	// 从 current 位置开始找第一个非 disabled 的账号
	n := len(ap.accounts)
	for i := 0; i < n; i++ {
		idx := (ap.current + i) % n
		if !ap.accounts[idx].Disabled {
			return ap.accounts[idx]
		}
	}
	return nil
}
