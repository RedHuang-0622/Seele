# 根能力破坏性重构前置审查

## 最终决策更新

本文件最初审查时保留了“TaskList 是否进入 Seele”的待定项。随后架构决策已经明确：Task/TaskList 是 Seelex 上层产物，不进入 Seele 核心；Plan 子代理如需 Task，由 Seelex 提供 Node adapter。以下结论以最终决策和当前实现为准。

## 根因

当前失败来自边界建模，而不是 Graph API 缺少几个方法：

1. Graph 只是 Plan 的转发 facade，制造了重复概念和状态入口。
2. NodeKind、AgentFactory、BranchRuntime 携带产品语义进入通用调度器。
3. DSL 直接把 `kind:auto` 编译为 Agent 节点，导致 Seele 解释 Seelex 产品语义。
4. ReActLoop 同时持有 working history、durable storage 和隐式压缩路径。
5. 工具实现、MCP、microHub、文件/Git/工作区工具与 agent 绑定，无法独立替换。
6. 账号池使用独立 round-robin/RPM 状态，无法表达真实并发租约和 P2C 负载。

## 目标边界

```text
agent       = LLM client + composition root
workplan    = Plan/Node/Edge + runtime
accountpool = P2C account lease pool
seelectx    = history/prompt/context/compression primitives
tools       = FC contract + provider adapters
telemetry   = optional Hook/Tracer/OTel infrastructure
Task        = Seelex product layer
```

约束：`tools` 不导入 `agent`；`workplan` 不导入 agent/seelectx/task；`seelectx` 不导入 agent；所有可变状态由拥有该语义的模块持有。

## 迁移切片

1. WorkPlan：Plan 单一状态源、最小 Node、Plan 生命周期和 edge-list/邻接表/矩阵 codec。
2. AccountPool：根级泛型 P2C pool，ChatClient 在 HTTP/stream 生命周期内持有 lease。
3. Tools：通用 provider 物理迁移到根 `tools/`，移除产品 builtin 的默认注册。
4. Context/Agent：显式 Components、durable history owner、RequestAssembler、ContextController、QuickChat 和递归压缩。
5. Telemetry：结构化 Hook/Tracer、intent/effect correlation、Metric/Audit/OTel。

## 风险控制

- 允许 breaking refactor，不建立双写或双状态源。
- 每个切片先接口、再实现、再包级测试；全仓编译作为切片交界验收。
- 真实 API 测试只在显式环境变量打开时运行；无凭证时 skip。
- Windows 无 gcc 时明确报告 race 环境限制。
