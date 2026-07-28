# workplan/sugar/switch

该包构建 If 与 Switch 控制节点，以及常用字符串条件。

## 公开入口

| 符号 | 用途 |
| --- | --- |
| `If` / `Switch` | 在图中添加条件控制节点 |
| `Contains` / `NotContains` | 基于当前结果的条件函数 |
| `ControlNode` | 保存节点类别和分支配置 |

## 实现细节

- If 和 Switch 通过节点配置与条件边表达分支，而不是在构建阶段执行条件。
- 条件函数针对当前节点输出计算，最终下一跳仍由 Scheduler 的边解析统一决定。
- `ControlNode` 复用基础节点模型，便于校验器检查所有边引用。

## 依赖与验证

- 路由：[core/edge](../../core/edge/README.md)
- 验证：`go test ./workplan/sugar/switch/...`
