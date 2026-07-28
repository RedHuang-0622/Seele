# seelectx

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
