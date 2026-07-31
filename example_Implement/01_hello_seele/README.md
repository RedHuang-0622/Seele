# 01_hello_seele

最小入门示例：创建 Agent、注册时间和计算内联工具，再用 Session 发起一次对话。

## 运行

```powershell
go run ./example_Implement/01_hello_seele
```

运行前按 `main.go` 中的配置加载方式提供有效 LLM 配置，切勿把密钥写回示例源码。

## 实现细节

- 输入/输出结构体经 `tool.SchemaOf` 转为 JSON Schema，Handler 使用 JSON 参数实现本地时间与计算能力。
- 示例展示 `agent.New` → 工具注册 → `session.New` → Chat 的最短装配路径。

## 验证

- `go build ./example_Implement/01_hello_seele`
