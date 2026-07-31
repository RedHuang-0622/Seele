# seelectx/tracer

该包实现可选的内存追踪树，用于记录一次会话中 LLM、工具和工作流节点的层级事件。

## 公开入口

| 符号 | 用途 |
| --- | --- |
| `Tracer` / `Span` | 追踪抽象 |
| `NewSimpleTracer` | 创建树形追踪实现 |
| `NoopTracer` | 零开销空实现 |
| `Tree` / `Node` | 导出和展示的追踪数据 |

## 实现细节

- `SimpleTracer` 以 span 栈构建父子树，记录状态、属性、事件和耗时；完成时关闭节点。
- 读写锁保护活跃树和节点，`NoopTracer` 消除未启用可观测性时的空值判断。
- `Truncate` 与 `JoinAttrs` 提供展示层所需的安全摘要，避免把长输入直接塞入 UI。

## 依赖与验证

- 调用方：[session](../../session/README.md)、[workplan](../../workplan/README.md)
- 验证：`go test ./seelectx/tracer/...`
