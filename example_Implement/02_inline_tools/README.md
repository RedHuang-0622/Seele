# 02_inline_tools

该示例演示复杂输入输出结构、嵌套 Schema 和多种内联工具的注册方式。

## 运行

```powershell
go run ./example_Implement/02_inline_tools
```

## 实现细节

- Weather、文本、团队和计数器等结构体展示 `SchemaOf` 如何从 Go 类型生成工具参数定义。
- 示例把 Handler 与工具描述注册到 Agent，供 ReAct 循环选择并调用。

## 验证

- `go build ./example_Implement/02_inline_tools`
