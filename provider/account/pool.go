package account

import (
	"sort"
	"sync"
)

// ProviderType 模型供应商类型
type ProviderType string

const (
	ProviderOpenAI    ProviderType = "openai"
	ProviderAnthropic ProviderType = "anthropic"
)

// Account 单个 API 账号
type Account struct {
	Name     string       // 账号名称，唯一标识
	Provider ProviderType // 供应商
	BaseURL  string       // API 地址
	APIKey   string       // API 密钥
	Model    string       // 默认模型
	Priority int          // 优先级（数字越小优先级越高）
	MaxRPM   int          // 每分钟最大请求数
	Disabled bool         // 是否禁用
}

// AccountPool 号池
type AccountPool struct {
	accounts []*Account
	current  int // round-robin 索引
	mu       sync.Mutex
}

// NewAccountPool 创建号池，按 Priority 升序排序
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

// Get 获取下一个可用账号（round-robin 轮询，按优先级排序）
func (ap *AccountPool) Get() *Account {
	ap.mu.Lock()
	defer ap.mu.Unlock()
	idx := ap.nextIndex()
	if idx < 0 {
		return nil
	}
	return ap.accounts[idx]
}

// GetByProvider 获取指定供应商的可用账号
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
		if !a.Disabled && a.Provider == provider {
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
