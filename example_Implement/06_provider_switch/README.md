# 06_provider_switch

该示例展示在同一 Agent 中加载多个账户，并在 Provider 或账户之间切换。

## 运行

```powershell
go run ./example_Implement/06_provider_switch
```

运行前准备不纳入版本控制的多账户 YAML 配置。

## 实现细节

- 配置先由 `LoadFullAccountsConfig` 解析为账户池和默认 LLM 配置。
- `ChatClient` 通过账户池与 Provider 策略完成选择，示例不直接构造 Provider 特定 HTTP 请求。

## 验证

- `go build ./example_Implement/06_provider_switch`
