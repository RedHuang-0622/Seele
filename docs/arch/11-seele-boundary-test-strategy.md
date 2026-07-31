# Seele 边界测试与验收方案

## 分层测试矩阵

| 层级 | 命令/范围 | 关键验收 |
| --- | --- | --- |
| WorkPlan 单元 | `go test ./workplan/...` | Node 最小接口、Plan 生命周期、DAG 校验、并发/取消、edge-list/adjacency/matrix round-trip、精确错误路径 |
| AccountPool 单元/并发 | `go test ./accountpool ./agent/core/api` | P2C 低负载选择、容量、取消、禁用、幂等 Release、HTTP/stream lease 生命周期 |
| Tools 契约 | `go test ./tools/...` | FC provider、provider/tool 重名、权限、超时、重试、MCP/microHub/Skills adapter、快照隔离 |
| Context | `go test ./seelectx/...` | turn/字符/tool exchange 切片、规整、相关性、Placeholder、短历史不调用 LLM、递归压缩与结构化结果 |
| Hook/Tracer | `go test ./telemetry/...` | 生命周期 Before/After、CorrelationID、并行 Span、panic 捕获、trace/metric/audit、Query/Stream、OTel 映射 |
| Session | `go test ./session/...` | 无工具临时 Chat、显式 history owner、assembler、raw/filtered/reference tool result、显式 ContextController、Agent/LLM/Tool telemetry |
| 全仓静态 | `go test ./... -run '^$' && go vet ./... && go build ./...` | 依赖方向和全仓编译 |

## 依赖边界检查

```text
rg "agent/core/tool|agent/gateway/tool" -g "*.go" .       # 必须无结果
go list -deps ./tools/... | rg "agent|workplan|seelectx|accountpool"  # 必须无结果
rg "runtime/graph|runtime/serialize|workplan/dsl" -g "*.go" . # 必须无结果
```

## WorkPlan 图形状验收

使用一个四节点 DAG：`inspect -> backend/tests -> integrate`，分别验证：

1. `ExportEdgeList` 输出 `version/entry/nodes/edges`，重新 `ImportEdgeList` 后 Plan 已 Seal 且边数一致。
2. 邻接表导出导入保持入口和邻接关系。
3. 邻接矩阵导入时矩阵行数、列数和 0/1 值错误返回 `$.matrix[row][column]`。
4. 节点 decoder 返回错误时返回 `$.nodes[index]` 和节点 ID 语义。
5. 自定义 FunctionNode 与 Auto/Agent adapter 可在同一 Plan 中混合执行。

## Context/Hook/Tracer 验收

- 没有注入 `ContextController` 时，ReActLoop 不调用 compressor、不替换 history。
- 注入 `PolicyController` 后，`after_tool` 或 `before_model` 事件可以显式触发压缩；短历史不调用 QuickChat。
- 压缩会话不继承主 history，不注册工具；主任务和压缩任务只通过结构化 request/result 关联。
- LLM/tool 的 Before 与 After 共享 CorrelationID，Tracer 能还原 intent/effect Operation。
- 并行子代理使用同一个 TraceID、不同 SpanID 和正确 parent；慢订阅者有 Dropped 计数。

## 真实 API 与 subagent 冒烟

真实测试必须显式打开，缺少配置时只能 `Skip`，不能使用仓库内硬编码凭证：

```powershell
$env:TEST_INTEGRATION = "true"
$env:RUN_WORKPLAN_API_SMOKE = "true"
$env:SMOKE_CONFIG = "config/account-openai.yaml"
go test ./test -run "Smoke|WorkPlanAPI" -v -count=1 -timeout 180s
```

冒烟场景：

1. 从 `SMOKE_CONFIG` 创建真实 ChatClient。
2. 用 `NodeDecoder` 从 formal edge-list materialize AutoNode，构建混合 Function/Auto Plan。
3. 执行包含分支和汇聚的 Plan，验证每个节点返回结果、取消和错误传播。
4. 用一个 Seelex 侧 TaskExecutor adapter 构造 SubagentNode，验证 Plan 只看到 `Node.Run`，Task 生命周期不进入 Scheduler。
5. 验证 ChatStream 在真实 provider 下返回文本和 tool-call 两种结果。

真实测试不得默认运行，避免 CI 消耗费用；token usage、模型和账号标识只进入测试输出的结构化 telemetry，不打印 API key。

## 并发、泄漏与环境限制

```text
go test ./accountpool ./tools/... ./telemetry/... -race
go test ./... -count=1 -timeout 300s
```

Windows 环境如果没有 gcc/CGO，`-race` 失败属于环境限制，必须在报告中明确，不得把普通测试结果冒充 race 结论。Agent Shutdown、account lease、telemetry subscriptions 和 MCP detach 需要用 goleak/超时测试检查 goroutine 泄漏。

## 完成标准

- 核心包测试、vet、build 全部通过。
- 新旧依赖边界 grep 检查符合约束。
- 真实 API 测试在有配置时通过；无配置时明确 skip。
- 架构文档、模块 README、测试方案与代码事实一致。
