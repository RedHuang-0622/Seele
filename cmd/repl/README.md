# cmd/repl

`seele-repl` 是交互式编码助手命令。它创建 Agent、注册本地工具和 WorkPlan 工具，并以插件模式同步切换工具可见性与系统提示词。

## 运行

```powershell
go run ./cmd/repl -c <accounts.yaml>
```

配置文件由 `agent/core/api.LoadFullAccountsConfig` 读取；不要将 API Key 提交到仓库。

## 实现细节

- `main.go` 装配账户池、Agent、内置工具、WorkPlan 工具、Tracer 和 Engine，并在退出时调用 `Shutdown`。
- `pluginDef` 定义 read/write/git/shell/plan 等工具白名单，`switch_mode` 让用户或 LLM 在模式间切换。
- `prompts/` 中的同名 Markdown 与插件关联，`ui.go` 和 `cmds.go` 处理交互命令、审批和渲染。

## 依赖与验证

- 工具实现：[agent/core/tool/builtin](../../agent/core/tool/builtin/README.md)
- 验证：`go build ./cmd/repl`
