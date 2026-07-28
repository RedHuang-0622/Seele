# Gap-D: WorkPlan Tracer + Streaming

## 变更概要

为 WorkPlan 添加了两项新能力：可观测性追踪（Tracer）和节点级流式输出（Streaming）。

---

## 任务 1：WorkPlan Tracer

### 设计决策

WorkPlan 包保持"零外部依赖"原则，在包内定义自己的 Tracer/Span 接口，不直接导入 `contexts/tracer` 包。上层（Engine）通过适配器将 `tracer.Tracer` 转换为 `workplan.Tracer` 接口。

### 新增文件

- **`workplan/tracer_internal.go`** — 内部追踪接口定义
  - `SpanKind` 类型（`SpanWorkPlan` / `SpanNode`）
  - `Span` 接口（`End` / `SetAttr`）
  - `SpanOption` 类型 + `WithSpanError` 辅助函数
  - `Tracer` 接口（`NewTrace` / `StartSpan`）

### 修改文件

- **`workplan/plan.go`**
  - `WorkPlan` 结构体新增 `tracer Tracer` 字段
  - `WithTracer` 函数选项
  - `Run()` 方法：开头创建 root span（`NewTrace`），每个节点执行前创建子 span（`StartSpan`），记录 `node_id`、`node_kind`、`output_length`、`error` 等属性，所有 return 路径均确保 root span 正确 End
- **`workplan/primitive.go`**
  - `Resume()` 方法：与 `Run()` 相同的追踪模式，root span 额外记录 `resume_from` 属性，approve 节点执行和后续节点均有独立 span

### 追踪数据模型

```
Trace: exec_{timestamp}
  Root Span (kind=workplan)
    ├─ Node Span (kind=node) — "node:{id_1}"
    │   attrs: node_id, node_kind, output_length, error
    ├─ Node Span (kind=node) — "node:{id_2}"
    └─ Node Span (kind=node) — "node:{id_3}"
```

---

## 任务 2：WorkPlan Streaming

### 设计决策

流式支持是可选能力，通过接口检查实现：如果 Agent 实现了 `StreamAgent` 接口且节点配置了 `onChunk` 回调，则调用 `ChatStream` 替代 `Chat`。

### 修改文件

- **`workplan/plan.go`**
  - 新增 `StreamAgent` 接口：`ChatStream(ctx, input, onChunk func(string)) (string, error)`
- **`workplan/node.go`**
  - `node` 结构体新增 `onChunk func(string)` 字段
- **`workplan/sugar.go`**
  - 新增 `WithStreamCallback(fn func(string)) NodeOpt`
  - `Auto()` 和 `LLM()` 方法创建策略时传播 `onChunk` 回调
- **`workplan/strategy.go`**
  - `AgentStrategy` 和 `LLMStrategy` 新增 `onChunk` 字段
  - `Execute()` 方法：检测 Agent 是否实现 `StreamAgent`，是则调用 `ChatStream`
- **`workplan/runner.go`**
  - `autoRunner` 新增 `onChunk` 字段
  - `Run()` 方法：检测 Agent 是否实现 `StreamAgent`，是则调用 `ChatStream`

### 使用示例

```go
wp := workplan.New(factory, nil, "prompt")
wp.Auto("分析", "请分析以下数据",
    workplan.WithStreamCallback(func(chunk string) {
        fmt.Print(chunk) // 实时输出流式结果
    }),
)
```

---

## 文件清单

| 文件 | 操作 | 说明 |
|------|------|------|
| `G:/Program/go/Seele/workplan/tracer_internal.go` | 新增 | 内部 Tracer/Span 接口定义 |
| `G:/Program/go/Seele/workplan/plan.go` | 修改 | Tracer 字段、WithTracer、Run/Resume 埋点 |
| `G:/Program/go/Seele/workplan/primitive.go` | 修改 | Resume 埋点 |
| `G:/Program/go/Seele/workplan/node.go` | 修改 | onChunk 字段 |
| `G:/Program/go/Seele/workplan/strategy.go` | 修改 | AgentStrategy/LLMStrategy 流式支持 |
| `G:/Program/go/Seele/workplan/runner.go` | 修改 | autoRunner 流式支持 |
| `G:/Program/go/Seele/workplan/sugar.go` | 修改 | WithStreamCallback + onChunk 传播 |

---

## 测试结果

```bash
go vet ./workplan/...       # PASS
go build ./workplan/...     # PASS
go test -race ./workplan/...    # PASS (1.798s)
go test -race ./contexts/tracer/...  # PASS (2.323s)
go test -race ./engine/...       # PASS (41.160s)
```
