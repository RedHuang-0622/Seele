# cmd/smoke

`cmd/smoke` 验证 Function Calling、Agent 装配、Session/ReAct 循环、WorkPlan 与根 `tools/builtin` Provider 的完整链路。离线测试使用本地协议模拟器；真实链路测试支持任意 Anthropic 兼容 API。

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

`real_chain_smoke_test.go` 同时覆盖本地协议模拟与真实链路。它优先读取 `ANTHROPIC_BASE_URL`、`ANTHROPIC_AUTH_TOKEN`（或 `ANTHROPIC_API_KEY`）和 `ANTHROPIC_MODEL`；环境变量缺失时才读取本机 `~/.claude/settings.json`。不具备完整配置时，真实 API 用例会跳过，而本地用例仍可运行。

```powershell
# 全部装配链路；真实 API 配置缺失时，仅跳过真实 API 用例。
go test ./cmd/smoke -run RealChain -v -count=1 -timeout 300s

# 仅验证不需要凭据的 OpenAI 兼容协议模拟链路。
go test ./cmd/smoke -run 'TestRealChain(ReActToolCalling|WorkPlanImportExport)$' -v -count=1
```

## 验证场景

| 场景 | 强制调用 | 验收条件 |
| --- | --- | --- |
| 基础计算 | `calculate` | 工具实际执行，结果字段等于 `42` |
| 当前时间 | `get_time` | 工具实际执行，返回时区为 `UTC` |
| 文本统计 | `text_stats` | 工具实际执行，单词数等于 `3` |
| 本地装配链路 | `calculate` | OpenAI 兼容协议、Agent、Session/ReAct 与 tool result 回填完整贯通 |
| 真实 ReAct 与 WorkPlan | `calculate`、`auto → emit → auto` | 真实 Anthropic 兼容 API 可完成工具调用与节点间变量传递 |
| 真实并发 Fork | 三个独立 Session | 分支有时间重叠，聚合节点可读取全部分支输出 |
| 核心 DAG 原语 | FunctionNode、LLMNode、Edge | 函数/流式 LLM/汇聚节点可运行，且 edge list、邻接表、邻接矩阵导出一致 |

命令通过 Session Hook 捕获真实工具调用和执行效果。模型只返回非空文本但没有调用指定工具时，冒烟测试会失败。

## 实现细节

- 命令使用 `agent.NewWithComponents` 显式装配 `ChatClient`、`holder`、`gateway` 和 `builtin.Provider`，不会启动 microHub 或隐式加载产品工具。
- 每个场景使用独立 Session history，避免前一个工具调用影响后续判断；三个场景共享无会话状态的 Agent 装配。
- `suite_test.go` 使用脚本化模型做离线链路回归；`real_api_test.go` 仅在显式环境开关存在时访问真实 API；`real_chain_smoke_test.go` 的真实用例使用 Anthropic 兼容协议，并在缺少配置或设置 `SEELE_SKIP_REAL_API` 时跳过。

## 依赖与验证

- Builtin 工具：[`../../tools/builtin/README.md`](../../tools/builtin/README.md)
- Agent 装配：[`../../agent/README.md`](../../agent/README.md)
- Session：[`../../session/README.md`](../../session/README.md)
- 离线验证：`go test ./cmd/smoke -count=1`
