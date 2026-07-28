# agent/core

`agent/core` 保存可复用的 LLM 协议、工具模型与工具基础设施，不负责应用级装配。

| 目录 | 职责 |
| --- | --- |
| [api/](api/README.md) | HTTP LLM 客户端、Provider 策略和账户池 |
| [function/](function/README.md) | Provider 的工具调用编解码策略 |
| [tool/](tool/README.md) | Schema 生成、工具注册、执行与权限体系 |

依赖方向由 `agent` 指向这些实现；核心包通过接口暴露扩展点，避免由工具或 Provider 反向依赖装配层。
