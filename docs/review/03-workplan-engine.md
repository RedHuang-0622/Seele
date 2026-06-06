# WorkPlan 工作流引擎

> 声明式 DAG 引擎 · 9 种执行原语 · 两段式审批 · 零框架依赖

---

## 1. 设计理念

WorkPlan 是 Seele 的声明式工作流引擎。核心思路：**糖（sugar）+ 原语（primitive）分离**。

```
用户代码 ─→ sugar.go (Auto/If/Loop/Fork...) ─→ node 结构体 ─→ plan.go Run() ─→ primitive.go 执行
   │                │                              │                    │
   │  声明式 DSL     │  构建 DAG                    │  拓扑校验          │  9 种原语
   │  (零执行逻辑)   │  (填充 nodeIndex)             │  (DFS 环检测)      │  (实际执行)
```

- **sugar.go**：所有公有方法（Auto、Approve、If、Loop、Fork...）只做两件事：构造 `node` 结构体 + 调用 `primitiveAddNode` 注册。零执行逻辑。
- **primitive.go**：所有执行逻辑在此。`primitiveAuto`、`primitiveLoop`、`primitiveFork` 等私有方法。
- **plan.go**：执行引擎入口 `Run()` / `Resume()`，节点遍历，状态管理。
- **validate.go**：拓扑校验，防止无效 DAG 和死循环。

## 2. 九种执行原语

| 原语 | 糖方法 | 行为 |
|------|--------|------|
| `kindAuto` | `Auto(id, input)` | Agent 跑完整 ReAct 循环，自主决策 |
| `kindApprove` | `Approve(id, input, options)` | 生成计划后暂停等人确认 |
| `kindIf` | `If(id, cond, trueID, falseID)` | 二选一条件分支 |
| `kindSwitch` | `Switch(id, cases...)` | 多路条件分支 |
| `kindLoop` | `Loop(id, bodyID)` | 带 Signal 的循环，支持 Until/MaxIter |
| `kindFork` | `Fork(id, branches)` | 多 Agent 并发执行 |
| `kindJoin` | (无糖，Fork 自动关联) | 汇合 Fork 的分支结果 |
| `kindCheckpoint` | `Checkpoint(id)` | 快照当前输出 |
| `kindEmit` | `Emit(id, key)` | 把当前输出写入命名变量 |

## 3. DAG 构建与执行

### 3.1 链式构建

```go
wp := workplan.New(factory, gate, "系统提示词")

wp.Auto("分析", "分析需求：{{.PrevResult}}")      // ① 需求分析
  .Emit("", "requirement")                        // ② 保存结果 → vars["requirement"]
  .Fork("开发", []ForkBranch{                     // ③ 并行开发
      {Label: "前端", SystemPrompt: "...", Input: "实现：{{.Vars.requirement}}"},
      {Label: "后端", SystemPrompt: "...", Input: "实现：{{.Vars.requirement}}"},
  })
  .Approve("审查", "审查结果：{{.PrevResult}}",    // ④ 人工审查
      Choices("execute", "skip", "abort"))
  .Switch("决策",                                  // ⑤ 条件路由
      Case(Contains("通过"), "部署"),
      Default("结束"),
  )
```

### 3.2 模板变量

```
{{.PrevResult}}  — 上一节点的 JSON 输出
{{.Vars.key}}    — Emit 写入的命名变量
```

### 3.3 自动 next 推导

`primitiveAddNode` 自动维护链表：新节点的 ID 成为上一个节点的 `next`。条件节点（If/Switch）不自动推导，next 由分支逻辑决定。

## 4. 两段式审批 (Q-K-V 模型)

### 4.1 三个角色

```
Q (Question)  — 展示给用户的问题/计划内容
K (Key)      — 用户可选的选项（如 "execute", "skip", "abort"）
V (Value)    — K 对应的动作值（服务端持有，不发给客户端）
```

### 4.2 流程

```
Run() 遇 Approve 节点
  ├─ Plan Agent 生成结构化计划 (Q)
  ├─ 构建 Question {ID, Content, Options, KVS}
  ├─ pauseSnapshot ← 保存断点上下文
  ├─ return PausedWorkPlan (StateAwaitingApproval)
  │
  │  ┌─ CLI/网络 传输 Question 给用户 ─┐
  │  │  用户看到: "请审查以下计划..."    │
  │  │  [1] 执行  [2] 跳过  [3] 终止   │
  │  │  用户选择: 1 (key="execute")    │
  │  └────────────────────────────────┘
  │
  ├─ SetDecision("execute")  ← 调用方设置 V
  ├─ Resume()
  │   ├─ executeApprove() → 匹配 V → ChoiceExecute
  │   │   └─ Agent.Chat() 执行计划
  │   └─ 继续执行后续节点
  └─ return 最终结果
```

### 4.3 嵌套审批

Resume 后的后续节点可能还有 Approve → 再次暂停。`pauseSnapshot` + `PausedWorkPlan` 引用支持无限嵌套（实际受 `maxApprovalLoops=10` 限制）。

### 4.4 三种 Gate 实现

| Gate | 适用场景 | 阻塞方式 |
|------|---------|---------|
| `CLIApprovalGate` | 本地开发调试 | `fmt.Scanln` |
| `NetworkApprovalGate` | 容器/网络部署 | channel + OnQuestion 推送 |
| `AutoApproveGate` | 自动化测试 | 直接返回第一个选项 |

## 5. Loop 与 Signal（Reactive 模式）

```go
sig := wp.Loop("重试", "body_node",
    Until(Contains("成功")),
    MaxIter(5),
    OnExhausted("fail_handler"),
)

// 每次迭代结果实时推送（不等待 Loop 结束）
sig.OnUpdate(func(jsonVal string) {
    log.Println("本次迭代结果:", jsonVal)
})

// 阻塞等待 Loop 结束
finalResult := sig.Wait()
```

Signal 内部：
- `set()` 在每次迭代后调用，触发所有 `OnUpdate` 回调
- `close()` 在 Loop 结束时调用，解除 `Wait()` 阻塞
- `Get()` 和 `GetString()` 无阻塞读取当前值

## 6. Fork 并发模型

```
Fork("并行开发", []ForkBranch{
    {Label: "前端", Input: "..."},
    {Label: "后端", Input: "..."},
    {Label: "测试", Input: "..."},
})
  │
  ├─ goroutine 1: Agent("前端").Chat() ─┐
  ├─ goroutine 2: Agent("后端").Chat() ─┤ 信号量 max 3
  ├─ goroutine 3: Agent("测试").Chat() ─┘
  │
  └─ wg.Wait() → 汇合结果 → {"前端": "...", "后端": "...", "测试": "..."}
```

并发安全：
- 信号量限制并发数（`maxConcurrentFork = 3`）
- context 取消检查（获取信号量前 + 后双重检查）
- panic recovery（B12 修复后）
- nil agent 检查（B12 修复后）

## 7. 拓扑校验

`Validate()` 在 `Run()` 自动调用：

1. **节点级检查**：Loop 必须有 body + 退出条件，Fork 必须有分支
2. **引用完整性**：所有 `next/trueID/falseID/bodyID/loopExhaustedID` 指向的节点必须存在
3. **环检测**：DFS 三色标记（白/灰/黑），排除 Loop body（Loop 的迭代不构成环）

```go
if err := wp.Validate(); err != nil {
    return nil, err  // "cycle detected: A → B → C → A"
}
```

## 8. 执行状态机

```
NotStarted ─→ Executing ─→ Completed
                 │
                 ├─→ AwaitingApproval ─→ Executing ─→ ...
                 │        (暂停等人)      (Resume)
                 │
                 ├─→ Aborted (ctx cancel / user abort)
                 └─→ Failed  (node error)
```
