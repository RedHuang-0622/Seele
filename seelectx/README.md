# seelectx

`seelectx` 提供无产品语义的上下文装配、历史所有权、显式压缩与上下文策略接口；它不决定产品何时压缩或保留哪些业务信息。

## 装配契约

`assembly.go` 声明跨会话装配的最小接口：`DurableHistory` 由调用方显式持有，`RequestAssembler` 负责 system/plan/task/skill 与 working history 的拼装，`ToolResultProcessor` 负责原始工具结果的筛选或引用，`QuickChat` 与 `Compressor` 仅在显式调用时工作。

`policy.go` 和 `compressor.go` 提供可替换的上下文策略：结构切片（turn、字符和连续 tool exchange）、工具记录规整、query relevance 排序/丢弃、动态 placeholder、动态压缩 prompt、短对话免 LLM 以及递归摘要。`ContextController` 是唯一可让 loop 响应结构化事件的入口；未注入 controller 时不会进行任何策略判断。

`seelectx` 管理 ReAct 会话的横切状态：响应缓存、历史预算、持久化和追踪，并为 `engine` 提供统一的接口与兼容别名。

## 子模块

| 模块 | 职责 |
| --- | --- |
| [cache/](cache/README.md) | 缓存接口、文件缓存与响应缓存装饰器 |
| [ctx_manager/](ctx_manager/README.md) | token 估算、截断与 LLM 压缩 |
| [storage/](storage/README.md) | 分片文件会话存储 |
| [tracer/](tracer/README.md) | 可导出的追踪树 |
| [react/](react/README.md) | 同步和流式完成结果适配 |

## 实现细节

- 根包以 type alias 和函数转发维持早期 `seelectx` API，具体实现下沉到子包。
- `cache_tool.go` 将缓存能力适配为 Agent 工具注册函数，避免 `seelectx` 直接依赖工具实现。
- `FileStorage` 与 `storage.FileStore` 服务不同层级的本地持久化需求，调用方按接口注入。

## 验证

- `go test ./seelectx/...`
