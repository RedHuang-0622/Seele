# cmd/repl

`seele-repl` 是交互式 Agent 示例入口。它装配账户、Agent、通用 builtin Provider、tracer 与 Session；文件、Git、Shell 和 WorkPlan 产品工具不再由 Seele 默认注册。

## 运行

```powershell
go run ./cmd/repl -c <accounts.yaml>
```

配置由 `agent/core/api.LoadFullAccountsConfig` 读取。不要把 API Key、账户文件或真实服务地址提交到仓库。

## 实现细节

- `main.go` 显式注册 [`tools/builtin`](../../tools/builtin/README.md)，Agent 本身不默认启用这些工具。
- 工具插件机制仍可筛选调用方注册的工具，但 REPL 不再自动注入产品 provider。
- `prompts/`、`ui.go` 和 `cmds.go` 只处理示例交互与渲染。

## 依赖与验证

- 工具运行时：[`../../tools/README.md`](../../tools/README.md)
- Agent：[`../../agent/README.md`](../../agent/README.md)
- 验证：`go build ./cmd/repl`
