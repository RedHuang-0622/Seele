# Phase 3: WorkPlan 糖→Agent 工具

## 目标

增强 WorkPlan.ToTool 方法，让 WorkPlan 可注册为 Agent 的内置工具。

## 修改文件

1. `G:\Program\go\Seele\workplan\plan.go`
2. `G:\Program\go\Seele\agent\agent.go`
3. `G:\Program\go\Seele\workplan\strategy_test.go`（新增测试）

## 变更详情

### workplan/plan.go

1. **WorkPlanTool.PlanRef 字段新增**
   - `PlanRef *Plan` — 绑定 ToTool 调用时由 ToPlan() 生成的 Plan 快照，供外部自省。

2. **ToTool 方法增强**
   - 调用 `wp.ToPlan()` 生成快照并赋值给 `PlanRef`。
   - Run 闭包增加 argsJSON 解析：将 json.Unmarshal 提取的 string 类型参数注入 `wp.vars`，供 WorkPlan 执行时通过 `ec.Vars` 读取。
   - 非 string 参数静默忽略，非法 JSON 不 panic。

3. **Run 方法的 vars 初始化兼容**
   - 将 `wp.vars = make(map[string]string)` 改为 nil-guard `if wp.vars == nil { wp.vars = make(map[string]string) }`，保留 ToTool 预注入的 vars。

### agent/agent.go

1. **新增 import**: `wp "github.com/RedHuang-0622/Seele/workplan"`
2. **新增 RegisterWorkPlanTool 方法**: 接收 `wp.WorkPlanTool`，委托给 `RegisterTool`。

### workplan/strategy_test.go

新增 4 个测试函数：
- `TestToTool_PlanRef` — 验证 PlanRef 非空且 EntryNodeID 正确。
- `TestToTool_ArgsInjection` — 验证 argsJSON 正确注入到 `wp.vars`。
- `TestToTool_ArgsInjection_NonString` — 验证非 string 类型值被忽略。
- `TestToTool_ArgsInjection_InvalidJSON` — 验证非法 JSON 不 panic 且不注入 key。
- 同时增强 `TestToTool` 添加 PlanRef 断言。

## 测试结果

```
> go vet ./workplan/... ./agent/...
（无输出，通过）

> go test -race -count=3 ./workplan/... ./agent/...
ok  github.com/RedHuang-0622/Seele/workplan  1.754s
ok  github.com/RedHuang-0622/Seele/agent/core/tool  1.800s
（其余子包无 test files）

> go test -cover ./workplan/... ./agent/...
ok  github.com/RedHuang-0622/Seele/workplan  1.091s  coverage: 16.2%
ok  github.com/RedHuang-0622/Seele/agent/core/tool  1.091s  coverage: 85.9%
```

全部通过。
