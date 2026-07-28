# agent/core/tool/holder

`Holder` 是工具注册和分发中心，向上提供可见工具列表和按名称执行，向下聚合多个 `ToolProvider`。

## 实现要点

- Provider 变化时重建名称到 `ToolEntry` 的索引，使 `Dispatch` 为 O(1) 查找并支持可配置重试/超时。
- `RWMutex` 保护 Provider 列表和索引；读路径不暴露内部可变 map。
- `PluginManager` 用 include/exclude 通配规则计算插件可见工具；因此可见性在网关前即可收敛。

公共协议见 [interfaces/](../interfaces/README.md)，最终可见性检查由 [gateway/tool](../../../gateway/tool/README.md) 负责。

## 公开入口与验证

- `New`/`NewWithConfig` 创建 Holder；`Register`、`Unregister`、`Tools`、`Dispatch` 是主要操作。
- 验证：`go test ./agent/core/tool/holder/...`
