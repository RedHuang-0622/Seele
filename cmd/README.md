# cmd

`cmd` 存放可执行程序。每个子目录都是独立命令入口，运行和配置说明在其自身 README 中。

| 命令 | 用途 |
| --- | --- |
| [repl/](repl/README.md) | Seele 交互式编码助手 |
| [smoke/](smoke/README.md) | 使用真实模型 API 验证 builtin Function Calling 链路 |

## 验证

- `go build ./cmd/...`
