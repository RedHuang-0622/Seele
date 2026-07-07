// Package apigw 提供 API 账号网关抽象。
//
// Gateway 负责从账号池中选择可用账号、健康检查和注册管理，
// 为上层（agent loop / session）提供统一的账号访问入口。
package apigw

import (
	"context"

	"github.com/RedHuang-0622/Seele/agent/core/api"
)

// Gateway 是 API 账号网关的抽象接口。
type Gateway interface {
	// Select 从账号池中选择一个可用账号。
	Select(ctx context.Context) (*api.Account, error)

	// Health 返回所有账号的健康状态。
	// key 为账号名，value 为错误；nil 表示正常。
	Health(ctx context.Context) map[string]error

	// Register 向账号池注册一个新账号。
	Register(account *api.Account)
}
