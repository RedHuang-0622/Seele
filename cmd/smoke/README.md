# cmd/smoke

`cmd/smoke` 使用真实模型 API 验证 Function Calling、Agent 装配、ReAct 循环和根 `tools/builtin` Provider 的完整链路。

## 运行

通过未提交到仓库的账户配置运行：

```powershell
$env:SMOKE_CONFIG = "<absolute-path-to-accounts.yaml>"
go run ./cmd/smoke
```

也可以通过 `SEELE_SMOKE_BASE_URL`、`SEELE_SMOKE_API_KEY`、`SEELE_SMOKE_MODEL` 和可选的 `SEELE_SMOKE_PROVIDER` 提供单个 client 配置。不要把真实密钥、账户文件或服务地址写入仓库。

真实 API Go 测试默认跳过，显式启用方式如下：

```powershell
$env:RUN_REAL_API_SMOKE = "true"
$env:SMOKE_CONFIG = "<absolute-path-to-accounts.yaml>"
go test ./cmd/smoke -run TestRealAPIBuiltinSmoke -v -count=1 -timeout 180s
```

## 验证场景

| 场景 | 强制调用 | 验收条件 |
| --- | --- | --- |
| 基础计算 | `calculate` | 工具实际执行，结果字段等于 `42` |
| 当前时间 | `get_time` | 工具实际执行，返回时区为 `UTC` |
| 文本统计 | `text_stats` | 工具实际执行，单词数等于 `3` |

命令通过 Engine Hook 捕获真实工具调用和执行效果。模型只返回非空文本但没有调用指定工具时，冒烟测试会失败。

## 实现细节

- 命令使用 `agent.NewWithComponents` 显式装配 `ChatClient`、`holder`、`gateway` 和 `builtin.Provider`，不会启动 microHub 或隐式加载产品工具。
- 每个场景使用独立 Engine history，避免前一个工具调用影响后续判断；三个场景共享无会话状态的 Agent 装配。
- `suite_test.go` 使用脚本化模型做离线链路回归；`real_api_test.go` 仅在显式环境开关存在时访问真实 API。

## 依赖与验证

- Builtin 工具：[`../../tools/builtin/README.md`](../../tools/builtin/README.md)
- Agent 装配：[`../../agent/README.md`](../../agent/README.md)
- Engine：[`../../engine/README.md`](../../engine/README.md)
- 离线验证：`go test ./cmd/smoke -count=1`
