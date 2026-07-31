# workplan/runtime/validate

该包校验 Plan 的入口、节点引用、边引用和可执行拓扑。它不解释产品 DSL，也不识别节点的具体实现类型。

通用 JSON 拓扑格式和字段级错误见 [../../codec/README.md](../../codec/README.md)。

## 验证

```text
go test ./workplan/runtime/validate/...
```
