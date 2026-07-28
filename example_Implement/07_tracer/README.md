# 07_tracer

该示例演示为 Engine 注入 `SimpleTracer`，并在一次对话后读取树形追踪结果。

## 运行

```powershell
go run ./example_Implement/07_tracer
```

## 实现细节

- `tracer.NewSimpleTracer` 记录 LLM、工具和会话 span；Engine 通过函数选项接收该实例。
- 示例输出树形事件，说明可观测性是可选依赖，未注入时可用 NoopTracer 替代。

## 验证

- `go build ./example_Implement/07_tracer`
