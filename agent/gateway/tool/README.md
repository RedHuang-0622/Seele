# agent/gateway/tool

该包定义工具访问网关。它将 `Holder` 的全量工具集合收敛为当前插件和权限上下文可见、可执行的集合。

## 实现要点

- `Gateway` 抽象可见工具查询与分发，调用方不需要知道 Provider 来源。
- `DefaultGateway` 查询 Holder，再按插件过滤；执行路径保留 context 以支持取消和超时。
- 权限审批模型位于 `agent/core/tool/permission`，该包负责将可见性边界应用到 Agent 调用路径。

## 公开入口与验证

- `Gateway` 定义工具查询与调用接口，`NewDefaultGateway` 创建 Holder 适配器。
- 验证：`go test ./agent/gateway/tool/...`
