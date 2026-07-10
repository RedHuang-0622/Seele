# Phase 1: NodeStrategy 接口清理 — 完成报告

## 变更摘要

将 `NodeStrategy.Execute` 的签名从 `Execute(ctx, input, ec)` 简化为 `Execute(ctx, ec)`，由 `strategyRunner` 统一接管模板渲染职责。

## 修改的文件

### 1. workplan/strategy.go
- **NodeStrategy 接口**: 移除 `input string` 参数，接口变为 `Execute(ctx context.Context, ec *ExecutionContext) (string, error)`
- **MethodStrategy.Execute**: 使用 `ec.PrevOutput` 替代 `renderTemplate(input, ec)`
- **LLMStrategy.Execute**: 同上
- **AgentStrategy.Execute**: 同上
- **新增 DeprecatedNodeStrategy**: 保存旧签名的桥接接口 `Execute(ctx, input, ec)`
- 更新注释中的示例代码

### 2. workplan/runner.go
- **strategyRunner**: 新增 `input string` 字段，存储原始输入模板
- **strategyRunner.Run**: 接管模板渲染逻辑，渲染 `r.input` 后写入 `ec.PrevOutput`，再调用 `r.strategy.Execute(ctx, ec)`

### 3. workplan/cache_strategy.go
- **CachedStrategy.Execute**: 移除 `input string` 参数，使用新签名
- **CachedStrategy.buildKey**: 移除 `input string` 参数，缓存键仅由 keyPrefix + ec.PrevOutput 构成

### 4. workplan/sugar.go
- **Auto/LlM/Strategy 糖方法**: 在 `strategyRunner` 构造器中传入 `input: n.input`
- **Strategy 糖方法**: 同上

### 5. workplan/strategy_test.go
- 更新所有策略 Execute 调用，移除多余的 `input` 参数
- 更新 `TestMethodStrategy` 断言，验证 `ec.PrevOutput` 被正确传入

## 测试结果

```
$ go vet ./workplan/...        # 通过，无输出
$ go test -race -count=3 ./workplan/...  # PASS (2.004s)
$ go test -cover ./workplan/...          # PASS (1.481s, 6.1%)
```

## 架构变化

**重构前**:
```
strategyRunner.Run → strategy.Execute(ctx, r.id, ec)
  └→ MethodStrategy 内部: renderTemplate(input, ec) → fn(ctx, rendered)
  └→ LLMStrategy 内部:  renderTemplate(input, ec) → agent.Chat(ctx, rendered)
  └→ AgentStrategy 内部: renderTemplate(input, ec) → agent.Chat(ctx, rendered)
```

**重构后**:
```
strategyRunner.Run → ec.PrevOutput = renderTemplate(r.input, ec) → strategy.Execute(ctx, ec)
  └→ MethodStrategy: fn(ctx, ec.PrevOutput)   // PrevOutput 已渲染
  └→ LLMStrategy:    agent.Chat(ctx, ec.PrevOutput)
  └→ AgentStrategy:  agent.Chat(ctx, ec.PrevOutput)
```
