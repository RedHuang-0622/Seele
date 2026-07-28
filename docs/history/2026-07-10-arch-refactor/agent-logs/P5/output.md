# Phase 5: Tracer OTel 化 — 输出文档

## 实施概要

在 `SimpleTracer` 中增加可选的 OTel span 发射能力。`NoopTracer` 零开销不受影响。

## 修改文件

### 1. `G:\Program\go\Seele\go.mod`

新增依赖：
- `go.opentelemetry.io/otel v1.44.0`
- `go.opentelemetry.io/otel/trace v1.44.0`
- `go.opentelemetry.io/otel/sdk v1.44.0`

### 2. `G:\Program\go\Seele\contexts\tracer\tracer.go`

变更内容：

- **import 新增**：`go.opentelemetry.io/otel/attribute`、`go.opentelemetry.io/otel/trace`（别名 `oteltrace`）
- **SimpleTracer 结构体新增字段**：
  - `otelTracer oteltrace.Tracer` — OTel tracer 实例（nil = 不发射 OTel）
  - `otelTp oteltrace.TracerProvider` — OTel TracerProvider 引用
- **新增方法**：`WithOTelTracerProvider(tp oteltrace.TracerProvider)` — 设置可选的 OTel TracerProvider，内部创建 Tracer 实例
- **simpleSpan 结构体新增字段**：
  - `ctx context.Context` — 存储创建时的 context，用于 OTel 父 span 传播
- **simpleSpan.End() 变更**：在原有结束逻辑后，检查 `otelTracer != nil`，非 nil 时调用 `emitOTelSpan(node)` 发射 OTel span
- **新增方法**：`simpleSpan.emitOTelSpan(node *Node)` — 将本地 span 映射为 OTel span 并发射，包括：
  - SpanKind 映射（ReActLoop→Internal, LLMCall→Client, ToolDispatch→Client, CacheOp→Internal）
  - 本地 Attrs 转为 OTel attributes
  - 补充标准属性：`local.span.id`、`local.span.kind`、`local.node.name`、`local.node.status`、`local.duration_ns`、`local.duration_ms`
  - 创建并立即结束 OTel span（低精度模式）
- **Export() 完全不变**

### 3. `G:\Program\go\Seele\contexts\tracer\tracer_test.go`

新增 8 个测试函数：

| 测试函数 | 验证点 |
|---|---|
| `TestSimpleTracer_WithOTel_SpanEmitted` | 设置 OTel provider 后 root span 被发射，包含 model 属性 |
| `TestSimpleTracer_WithOTel_ChildrenSpans` | 多种 SpanKind 的正确映射（Internal/Client） |
| `TestSimpleTracer_WithOTel_DurationRecorded` | local.duration_ms 属性包含正确持续时间 |
| `TestSimpleTracer_WithoutOTel_NoChange` | 不设 provider 时 otelTracer/otelTp 均为 nil |
| `TestSimpleTracer_WithOTel_ErrorStatus` | error attr 和 status 正确传播到 OTel |
| `TestSimpleTracer_WithOTel_Concurrent` | 并发场景下 OTel span 均正常发射 |
| `TestSimpleTracer_WithOTel_MultipleExports` | 多次 Export 后 OTel 仍正常工作 |
| `TestNoopTracer_WithOTelNotImported` | NoopTracer 完全不受 OTel 影响 |

## 架构决策

### 低精度模式
OTel span 在 `End()` 时创建并立即结束，而非在 `StartSpan` 时创建。优点是：
- 最小侵入：无需修改 StartSpan 逻辑
- **不影响** context 传播：不引入 OTel span context 到现有链路

缺点是 OTel 原生 StartTime/EndTime 无法反映真实持续时间（真实持续时间通过 `local.duration_ms` 属性传递）。

## 测试结果

```
$ go vet ./contexts/tracer/...
  -> PASS

$ go test -race -count=3 ./contexts/tracer/...
  -> ok github.com/RedHuang-0622/Seele/contexts/tracer 2.328s

$ go test -cover ./contexts/tracer/...
  -> ok coverage: 82.7% of statements
```
