# gap-B-maxrpm: MaxRPM 限流 + Provider 校验

## 改动概览

### 1. MaxRPM 限流 (`agent/core/api/pool.go`)

**Account 结构体新增字段：**
- `mu sync.Mutex` -- 限流状态锁
- `window []time.Time` -- 滑动窗口：当前分钟内各请求的时间戳

**新增方法 `allow() bool`：**
- `MaxRPM <= 0` 表示不限流，直接返回 true
- 滑动窗口算法：移除超过 1 分钟的旧记录，检查当前窗口计数是否达到上限
- 未超限则记录当前时间戳并返回 true；超限返回 false

**`Get()` 改造：**
- 不再使用 `nextIndex()`，改为内联循环遍历
- 每次选取候选账号后调用 `a.allow()` 检查限流
- 被限流的账号跳过，继续尝试下一个
- 所有账号都被限流或禁用时返回 nil

**`GetByProvider()` 改造：**
- 同样加入 `a.allow()` 检查
- 同时匹配 `Provider` 和限流状态

### 2. Provider 名字校验 (`agent/core/api/client.go`)

**`effectiveStrategy()`：**
- 当指定 provider 的策略未注册时，不再静默回退
- 改为通过 `slog.Warn` 记录告警日志，包含请求的策略名称
- 仍保留 `openai` fallback 以保证接口兼容

**`Complete()`：**
- `effectiveAccount()` 返回 nil 且 pool 存在时，返回明确错误 `"ChatClient: all accounts rate-limited or disabled"`

**`doStreamRequest()`：**
- 同上，`effectiveAccount()` 返回 nil 且 pool 存在时返回明确错误

## 验证结果

```bash
> go vet ./agent/core/api/...
# 无输出，通过

> go build ./...
# 无输出，通过

> go test -count=1 -race ./agent/... ./engine/...
ok  github.com/RedHuang-0622/Seele/agent/core/tool  1.937s
ok  github.com/RedHuang-0622/Seele/engine            1.216s
# 全部通过，无竞态
```

## 改动文件

- `G:\Program\go\Seele\agent\core\api\pool.go` -- MaxRPM 限流状态 + allow() + Get/GetByProvider 限流检查
- `G:\Program\go\Seele\agent\core\api\client.go` -- effectiveStrategy 告警 + Complete/doStreamRequest 账号不可用错误
