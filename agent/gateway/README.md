# agent/gateway

`agent/gateway` 仅保留旧 API 账户选择边界；通用工具可见性、权限与调用网关已迁移到根 `tools/gateway`。

## 子模块

| 目录 | 职责 |
| --- | --- |
| [`api/`](api/README.md) | 从旧账户池选择 LLM 账户 |

## 依赖方向与验证

- 工具网关：[`../../tools/gateway/README.md`](../../tools/gateway/README.md)
- 新账号池：[`../../accountpool/README.md`](../../accountpool/README.md)
- 验证：`go test ./agent/gateway/...`
