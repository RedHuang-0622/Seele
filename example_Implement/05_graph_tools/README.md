# 05_graph_tools

该示例用可声明的图工具描述 Pipeline、Loop 与 Fork 输入，并展示 WorkPlan 图可作为工具能力暴露。

## 运行

```powershell
go run ./example_Implement/05_graph_tools
```

## 实现细节

- 示例定义图构建所需的输入结构，并以 `workplan/agent.NewFactory` 提供节点执行依赖。
- Pipeline、循环和 Fork 的参数模型展示工作流如何从工具调用中被动态构造。

## 验证

- `go build ./example_Implement/05_graph_tools`
