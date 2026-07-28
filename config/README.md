# config

`config` 读取应用启动所需的 YAML 配置，并转换为稳定的 `types` 配置模型；它不创建网络客户端或执行账户选择。

## 公开入口

| 符号 | 用途 |
| --- | --- |
| `LoadConfig` | 读取单个 `LLMConfig` |
| `LoadAppConfig` | 读取包含 LLM、Hub 与 Registry 的应用配置 |

## 实现细节

- 使用 `yaml.v3` 反序列化到 `types`，在返回前补齐模型层的默认值。
- 读取错误带有文件路径上下文，调用方可直接向用户报告错误配置的位置。

## 依赖与验证

- 配置模型：[types/](../types/README.md)
- 验证：`go test ./config/...`
