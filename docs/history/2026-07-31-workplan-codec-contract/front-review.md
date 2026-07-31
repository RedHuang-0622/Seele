# 前置审查报告

## 需求摘要

将 WorkPlan codec 收敛为产品可读的泛型 `nodes + edges` 文档，增加结构化数据传输与统一错误格式，同时保持现有 Node/Runner 兼容。

## 影响文件清单

| 文件路径 | 修改类型 | 具体位置 | 修改原因 |
| --- | --- | --- | --- |
| `errors/` | 新增 | 根级错误包 | 统一 struct/function/step/raw/path/message 错误载荷 |
| `workplan/codec/` | 修改 | codec API 与 JSON 文档 | 增加泛型产品节点/边文档和 DSL 渲染入口 |
| `workplan/core/types/context.go` | 修改 | WorkflowContext、Value | 用 JSON Value 作为内部传输，同时保留旧 string 镜像 |
| `workplan/runtime/{scheduler,forkexec,runner}/` | 修改 | 结果写入和错误包装 | 统一结构化数据传播及执行错误上下文 |
| `workplan/core/node/`、`workplan/sugar/` | 修改 | 节点输入读取 | 优先读取结构化 Value，保持旧 Node 接口兼容 |
| `workplan/codec/*_test.go`、`workplan/core/types/*_test.go` | 新增/修改 | 契约、错误和泛型测试 | 验证产品 JSON、错误路径和 Value round-trip |
| `workplan/README.md`、`workplan/codec/README.md`、根 README | 修改 | API/示例/边界 | 说明新的 canonical DSL 与兼容入口 |

## 依赖分析

- 上游依赖：Seelex 的 Plan DSL、NodeDecoder、产品节点适配器。
- 下游影响：codec、scheduler、fork runtime、checkpoint 和现有 Sugar 节点。
- 根 `errors` 只依赖标准库，不反向依赖 WorkPlan，避免循环依赖。

## 循环依赖检查

- [x] 根错误包仅依赖标准库。
- [x] codec 依赖根错误包和 WorkPlan core，不被根错误包反向依赖。
- [x] Node/Runner 不依赖产品 DSL。

## 风险预估

- 兼容风险：旧 `Node.Run() (string, error)` 和旧 `NodeDefinition{type,data}` 仍被示例/测试使用，采用新增 API 与 Value 镜像避免一次性破坏。
- 序列化风险：泛型 `input` 由调用方选择类型，JSON 解码失败必须携带 `$.nodes[i].input` 路径。
- 行为风险：结构化 Value 与旧 string 镜像必须同步更新，所有调度/分支/快照写入点需要统一走 Context 方法。

## 建议方案

新增 canonical `codec.Document[T]`/`NodeSpec[T]`/`EdgeSpec`，由调用方注入 `NodeFactory[T]` 渲染为 core Plan；保留原 codec 作为兼容层。新增根 `seeleerrors.Error`，让 codec 和 Runner 返回同一 JSON 可序列化错误信封。WorkflowContext 引入 JSON Value 及同步旧字段的方法，不直接破坏既有 Node 接口。
