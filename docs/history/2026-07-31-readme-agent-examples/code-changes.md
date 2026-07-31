# 代码变更摘要

本次变更围绕根 README 的能力说明、CI 可见性和 Agent 开发示例展开，不改变 Seele 的公开运行时契约。

## 新增/修改文件

| 文件 | 类型 | 说明 | 设计模式 |
| --- | --- | --- | --- |
| [`README.md`](../../../README.md) | 修改 | 增加 CI badge、当前实现优势、离线学习路径；修正 WorkPlan codec 和 Node 接口示例 | 文档化边界 |
| [`example_Implement/README.md`](../../../example_Implement/README.md) | 修改 | 增加 08–10 示例索引，并标注离线/真实 API 使用方式 | 导航索引 |
| [`example_Implement/08_composable_agent/`](../../../example_Implement/08_composable_agent/README.md) | 新增 | 显式 Agent、工具 Registry、内置工具、ReAct 和 Telemetry 的离线示例 | 组合、适配器 |
| [`example_Implement/09_context_pipeline/`](../../../example_Implement/09_context_pipeline/README.md) | 新增 | History、PromptBlock、Placeholder、ToolResultProcessor 和显式压缩示例 | 策略、装饰器 |
| [`example_Implement/10_workplan_codec/`](../../../example_Implement/10_workplan_codec/README.md) | 新增 | 自定义 Node/NodeCodec、edge list、邻接表/矩阵导出和 DAG 执行示例 | 接口、编解码器 |

## API 变更

| API | 变更 | 兼容性 |
| --- | --- | --- |
| Seele runtime API | 无变更；示例仅使用现有公开接口 | 完全兼容 |

## 设计模式使用

| 模式 | 文件 | 效果 |
| --- | --- | --- |
| 组合/依赖注入 | `08_composable_agent/main.go` | LLM、ToolRuntime、Telemetry 可独立替换，示例不启动隐式基础设施 |
| 策略/装饰器 | `09_context_pipeline/main.go` | 请求拼装、工具结果投影和压缩策略彼此解耦 |
| Codec/接口 | `10_workplan_codec/main.go` | 产品节点字段留在调用方 `NodeCodec`，Plan 内核只处理拓扑和调度 |

## 接口抽象

| 接口 | 实现方 | 使用方 |
| --- | --- | --- |
| `agent.Completer` | `scriptedCompleter` | `agent.NewWithComponents` |
| `agent.ToolRuntime` | `registryRuntime` | Agent 与 Engine |
| `seelectx.QuickChat` | `countingQuickChat` | `RecursiveCompressor` |
| `workplan.Node` / `codec.NodeCodec` | `exampleNode` / `exampleNodeCodec` | codec 与 runner |

## 循环依赖检查

- [x] `tools` 未新增到 `agent`、`workplan`、`seelectx` 或 `accountpool` 的反向依赖。
- [x] `workplan` 未新增到 `agent` 或 `seelectx` 的产品依赖。
- [x] 新示例只依赖公开模块接口。

## 验证

```powershell
go test ./... -count=1 -timeout 300s
go vet ./...
go build ./...
go test ./example_Implement/08_composable_agent ./example_Implement/09_context_pipeline ./example_Implement/10_workplan_codec -count=1
git diff --check
```

以上命令均已通过；三个新增示例也已分别执行 `go run`。

## Commit 记录

| Commit | Type | 子目标 | Message |
| --- | --- | --- | --- |
| （待用户确认） | docs/examples | README、CI badge 与离线示例 | `docs(examples): document framework strengths and add offline paths` |
