# 实现方案

## 设计目标

1. `nodes` 和 `edges` 成为产品可读的最小 DSL 单元。
2. Node 输入使用泛型 `T`，默认 JSON 传输；边只表达拓扑关系。
3. DSL 渲染只负责把文档交给调用方 NodeFactory，不解释产品语义。
4. WorkPlan 内部优先使用 JSON Value/struct，保留旧 string 字段作为兼容镜像。
5. 所有新 codec/runner 错误统一包含 `struct`、`function`、`step`、`raw`、`path`、`message`。

## 设计模式选择

| 模式 | Go 实现 | 应用位置 | 理由 |
| --- | --- | --- | --- |
| Adapter | `NodeFactoryFunc[T]`、旧 codec 兼容入口 | DSL → core Plan | 产品节点语义留在调用方 |
| Builder/Renderer | `Render[T]` | Document → Plan | 集中校验版本、ID、边和可达性 |
| Value Object | `core/types.Value` | WorkflowContext | 统一 JSON 数据传输和文本兼容 |
| Chain of Responsibility | `errors.Wrap` | codec/runner | 保留 cause 并累积执行上下文 |

## 方案对比

| 维度 | 方案 A：直接替换旧 codec | 方案 B：新增泛型 canonical API + 兼容层 |
| --- | --- | --- |
| 产品可读性 | 高 | 高 |
| 兼容性 | 低，旧 NodeCodec 全部迁移 | 高，旧入口继续可用 |
| 类型安全 | 中 | 高，`Document[T]` 约束 input |
| 改动面 | 大且集中 | 分层增量，便于回滚 |
| 迁移成本 | 一次性高 | 调用方可逐步迁移 |
| 推荐度 | 不推荐 | 推荐 |

## 推荐：方案 B

以泛型 `codec.Document[T]` 作为新主线，同时保留旧 `EdgeList`/`NodeDefinition`。WorkflowContext 通过 `Value` 增强内部传输，不删除旧 string 字段，避免 Sugar 和第三方 Node 一次性失效。

## 核心接口

```go
type NodeSpec[T any] struct {
    ID    string `json:"id"`
    Input T      `json:"input"`
    Kind  string `json:"kind,omitempty"`
}

type EdgeSpec struct {
    From string `json:"from"`
    To   string `json:"to"`
}

type NodeFactory[T any] interface {
    BuildNode(NodeSpec[T]) (node.Node, error)
}

func Import[T any](data []byte, factory NodeFactory[T]) (*coreplan.Plan, error)
```

## 实现步骤

| # | 步骤 | 文件 | 设计模式 |
| --- | --- | --- | --- |
| 1 | 增加根结构化错误包和测试 | `errors/` | Value Object |
| 2 | 增加泛型 Document/Render/Import/Export | `workplan/codec/` | Adapter/Builder |
| 3 | 增加 JSON Value 和 Context 写入方法 | `workplan/core/types/` | Value Object |
| 4 | 让 scheduler/fork/checkpoint/runner 同步 Value 与旧字段 | `workplan/runtime/` | Adapter |
| 5 | 更新 README、示例和契约测试 | `workplan/README.md`、`example_Implement/10_workplan_codec` | Documentation |

## 测试策略

- 泛型产品 JSON 的导入、导出和 round-trip。
- 非法节点、非法边、重复边、循环、缺失入口的结构化错误。
- `Value[T]` JSON 编解码、文本兼容和 Context Clone。
- DAG 分支汇合后 Value 与旧 string 镜像一致。
- `go test ./workplan/...`、`go vet ./...`、`go build ./...`、`git diff --check`。

## 回滚方案

删除新 `errors` 包和 canonical codec API，保留旧 `NodeDefinition`/`EdgeList`；Value 字段为新增字段，不影响旧 JSON 快照读取。
