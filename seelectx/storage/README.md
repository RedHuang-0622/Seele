# seelectx/storage

该包实现持久化会话存储：保存会话元数据和消息分片，并按会话恢复或删除数据。

## 公开入口

| 符号 | 用途 |
| --- | --- |
| `Storage` | 会话创建、读写、列举和删除接口 |
| `NewFileStore` / `NewStore` | 创建文件后端 |
| `SessionMeta` | 会话元数据 |

## 实现细节

- `FileStore` 将元数据与内容分离，并按固定大小分片消息，避免单个会话文件无限增长。
- 文件操作通过内部锁串行化，保证同一存储实例的读写一致性。
- `Store`/`NewStore` 是兼容别名；新代码优先使用语义明确的 `FileStore`。

## 依赖与验证

- Session 注入点：[session](../../session/README.md)
- 验证：`go test ./seelectx/storage/...`
