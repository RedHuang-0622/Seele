# telemetry

`telemetry` 提供无产品语义、可选装配的 Hook、Trace、Metric 与 Audit 原语；它不负责 Agent 执行、上下文策略、工作计划编排或具体可视化前端。

## 公开接口

| 符号 | 用途 |
| --- | --- |
| `Event` / `Operation` | 标准化生命周期载荷，以及按 `CorrelationID` 关联的意图/效果视图 |
| `Hook` / `LifecycleHook` | Agent、LLM、Tool、Handoff、Context Assembly/Compression 和 Error 埋点 |
| `Decorate` | 用泛型装饰器把 Before/After Hook 非侵入地包在任意 handler 外层 |
| `Tracer` / `Span` | 创建 Trace/Span 树、传播标识和记录事件 |
| `TraceSink` / `MetricSink` / `AuditSink` | 三类存储后端的最小插拔接口 |
| `MemoryTracer` | 并发安全的 Trace、Metric、Audit、Query 和实时 Stream 内存实现 |
| `OTelTracer` | 使用调用方注入的 OpenTelemetry `TracerProvider` 发射原生 Span 与 Event |
| `Queryer` / `Streamer` / `ViewModel` | 为瀑布图、实时节点图和条件筛选提供 UI 无关的读取接口 |
| `NoopTracer` | 关闭可观测性时仍保持调用代码无空值分支 |

## 实现细节

- `LifecycleHook` 为每次 Before 生成 `CorrelationID`，After 复用同一标识；`MemoryTracer` 将两条事件聚合为 `Operation`，因此工具写入意图和实际写入效果可以直接比较。Hook 默认采用 best-effort 隔离，Tracer/Sink 故障不会中断业务；需要 fail-closed 行为时显式使用 `WithStrictHookErrors`。
- Trace/Span 标识通过安全随机数生成，并只借助 `context.Context` 传播。`MemoryTracer` 允许多个 Trace 并行存在，同一父 Context 可并发创建多个子 Agent Span，内部索引和订阅者由互斥锁保护。
- 每个结构化事件同时进入 Span 事件序列和 Audit 序列；Span 结束产生 duration Metric，包含 `gen_ai.usage.input_tokens` 或 `gen_ai.usage.output_tokens` 的事件还会生成 Token Metric。三类外部 Sink 可独立注入。
- 实时订阅使用有界 channel，慢消费者不会阻塞 Agent 关键路径；丢弃数量通过 `Subscription.Dropped` 暴露。Audit 存储不使用该有损通道。
- OTel 适配器不修改全局 Provider。语义属性使用 `gen_ai.*`、`error.type` 与 `exception.message`，Seele 自有的关联信息使用 `seele.*` 命名空间；LLM/Tool Span 映射为 Client，Agent/Internal 映射为 Internal。

典型装配路径：

```text
Agent / Context / WorkPlan handler
        │ Decorate
        ▼
LifecycleHook ── Event ──> Tracer
                           ├─ TraceSink
                           ├─ MetricSink
                           ├─ AuditSink
                           └─ Queryer / Streamer ──> 外部 UI
```

## 依赖与验证

- 本包只依赖标准库和 OpenTelemetry；禁止导入 `agent`、`engine`、`seelectx` 或 `workplan`，上层模块通过构造参数注入 `Hook`/`Tracer`。
- 内存实现适合测试、开发调试和轻量嵌入；生产持久化、指标后端和审计保留策略由外部 Sink 实现。
- 单元测试覆盖 JSON 载荷、Before/After 关联、装饰器隔离、并行子 Agent Span、查询/流过滤、多模态 Sink 和 OTel 父子关系。
- 验证：`go test ./telemetry/... -count=1`。
