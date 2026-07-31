# workplan

`workplan` 是声明式工作流门面：它装配 Plan 执行内核、Graph 编辑外观、运行时和链式 sugar API。

## 公开接口

| API | 用途 |
| --- | --- |
| `New` | 创建新的 Plan 内核及 WorkPlan 门面 |
| `NewFromPlan` | 将已有 Plan 内核装配为可执行 WorkPlan |
| `Plan` | 获取 WorkPlan 的执行内核 |
| `Graph` | 获取用于编辑和查询的 Graph 外观 |
| `Auto` / `Method` / `LLM` | 添加顺序节点 |
| `If` / `Switch` / `Loop` / `Fork` | 添加控制流 |
| `Run` / `Resume` | 执行或从检查点恢复 |
| `ExportJSON` | 导出正式 Seele DSL v1 |

## 实现细节

- `core/plan` 是节点、边和入口的唯一所有者；运行器和调度器直接依赖它。
- `runtime/graph` 是 Plan 的编辑/查询外观，供 sugar、可视化和后续表露层功能使用，不参与执行依赖链。
- DSL v1 的执行节点会直接编译为核心 `AutoNode` 和 `edge.Edge`，不经过 sugar。

## 模块导航

- [core/](core/README.md)：Plan 内核、节点、边和上下文
- [dsl/](dsl/README.md)：正式 JSON 语法与语义校验
- [runtime/](runtime/README.md)：执行、校验、序列化和 Graph 外观
- [sugar/](sugar/README.md)：链式构建 API

## 验证

- `go test ./workplan/...`
