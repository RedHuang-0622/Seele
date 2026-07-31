# 08_composable_agent

该示例展示无网络、无配置文件的 Agent 显式装配路径：调用方选择 LLM、工具 Provider 和 Telemetry，`agent` 只负责把它们组合成可执行 runtime。

## 运行

```powershell
go run ./example_Implement/08_composable_agent
```

无需 API Key 或外部服务。示例使用确定性的 `scriptedCompleter` 模拟模型先请求 `calculate`，再读取工具结果。

## 展示的 API 与场景

- `tools.NewRegistry` 与 `builtin.New`：按需注册无产品语义的内置工具。
- `agent.NewWithComponents`：显式注入最小 `Completer` 与 `ToolRuntime`，不启动 microHub 或读取账号配置。
- `session.NewSession`：以 `Runtime`、`History`、`Context`、`Telemetry` 四个概念装配完整会话。
- `Session.Chat`：执行一次完整的 LLM → tool call → LLM ReAct 循环。
- `telemetry.NewLifecycleHook`：采集 Agent、LLM 和工具的 intent/effect 事件。

预期输出包含计算结果 `42`，并显示两次模型调用、三个可见内置工具和一对工具生命周期事件。

## 验证

```powershell
go test ./example_Implement/08_composable_agent -count=1
```
