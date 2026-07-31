# seelectx/cache

该包提供可替换的会话/响应缓存接口，以及文件后端和响应缓存装饰器。

## 公开入口

| 符号 | 用途 |
| --- | --- |
| `Provider` | 缓存读写、删除和统计的接口 |
| `NewFileCache` | 基于文件系统创建 TTL 缓存 |
| `NewResponseCache` | 为 Provider 添加基于请求内容的响应缓存 |
| `Config` | 控制目录、TTL 和容量 |

## 实现细节

- `FileCache` 把条目编码到文件并检查过期时间；配置决定存储目录与生命周期。
- `ResponseCache` 用请求内容的稳定摘要作为键，包装任意 `Provider`，因此缓存策略不侵入 LLM 客户端。
- Provider 接口使 Session 可在测试中替换内存实现，也避免缓存层依赖 Agent。

## 依赖与验证

- 会话入口：[session](../../session/README.md)
- 验证：`go test ./seelectx/cache/...`
