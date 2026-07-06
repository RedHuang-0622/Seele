package apigw

import (
	"context"

	"github.com/RedHuang-0622/Seele/agent/api"
)

// DefaultGateway 是基于 api.AccountPool 的默认 API 网关实现。
type DefaultGateway struct {
	pool *api.AccountPool
}

// NewDefaultGateway 创建基于指定账号池的默认网关。
func NewDefaultGateway(pool *api.AccountPool) *DefaultGateway {
	return &DefaultGateway{pool: pool}
}

// Select 从账号池中 round-robin 选取下一个可用账号。
func (g *DefaultGateway) Select(ctx context.Context) (*api.Account, error) {
	return g.pool.Get(), nil
}

// Health 遍历账号池中所有账号，返回其状态映射。
// 当前实现假定所有已注册账号均为健康状态（error = nil）。
func (g *DefaultGateway) Health(ctx context.Context) map[string]error {
	result := make(map[string]error)
	for _, acct := range g.pool.All() {
		result[acct.Name] = nil
	}
	return result
}

// Register 将账号添加到池中，自动按优先级重新排序。
func (g *DefaultGateway) Register(account *api.Account) {
	g.pool.Add(account)
}
