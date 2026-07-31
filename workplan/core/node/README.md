# workplan/core/node

该包定义 Plan 节点的最小执行合约和可选的通用节点实现。

## 核心接口

```go
type Node interface {
    ID() string
    Run(context.Context, *types.WorkflowContext) (string, error)
}
```

`Kinded` 只是内置节点的可选描述接口，调度器不能要求它存在。`AutoNode`、`FunctionNode` 和 `LLMNode` 是 Seele 提供的抽象实现；自定义节点无需继承它们。

## 边界

节点自行持有执行所需的 Agent、Task adapter 或工具函数。Scheduler 不注入或识别 AgentFactory，Plan 核心也不依赖 Agent、Seelex 或 Task。

## 编解码

通用拓扑编解码见 [../../codec/README.md](../../codec/README.md)。产品 DSL 的节点字段由调用方提供的 NodeDecoder 解释。

## 验证

```text
go test ./workplan/core/node/...
```
