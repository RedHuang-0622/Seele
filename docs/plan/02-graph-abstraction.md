# WorkPlan 底层图抽象重构

> 目标：在 sugar 层完全不变的前提下，将底层重构为 Node + Edge 图引擎。同时暴露低级 API 给高级用户。

---

## 1. 问题诊断

当前 WorkPlan 的边逻辑分散在 **4 处**，加新节点类型必须改路由逻辑：

```
1. primitiveAddNode    → 线性链自动推导 next（硬编码排除 If/Switch）
2. primitiveNext       → If/Switch/Loop 的条件路由（swtich-case 膨胀点）
3. primitiveLoop       → 耗尽后的 exhausted 出口（特殊逻辑嵌入执行原语）
4. Run()               → currentID = wp.primitiveNext(...)（硬编码遍历）
```

**核心问题**：Edge 不是一等公民。它是 node 的一个字符串字段 (`next`)，不是独立的数据结构。条件边（If 的 true/false 分支、Switch 的多路匹配、Loop 的 exhausted 出口）用不同的字段名存储，在 `primitiveNext` 里用 switch-case 统一处理。

加 `kindTool`、`kindRetry`、`kindSubgraph` 都会继续膨胀这个 switch-case。

---

## 2. 目标架构

```
┌─────────────────────────────────────────────────────┐
│  sugar.go  （公有 DSL，不变）                        │
│  Auto() / If() / Fork() / Loop() / ...               │
│  ↓ 构造 node，调用 graph.AddNode() / graph.AddEdge() │
├─────────────────────────────────────────────────────┤
│  graph.go  （新增：图引擎层）                         │
│  Graph { nodes, edges, entry }                       │
│  Execute(ctx, ec) → 统一遍历 + 统一路由              │
│  Validate() → DFS 三色环检测（从 validate.go 移入）   │
├─────────────────────────────────────────────────────┤
│  runner.go （新增：NodeRunner 实现）                  │
│  autoRunner / ifRunner / switchRunner /               │
│  loopRunner / forkRunner / approveRunner / ...       │
│  每个 Runner 封装一种节点的执行 + 出边逻辑           │
├─────────────────────────────────────────────────────┤
│  node.go   （保留：node 结构体、Signal、Question 等） │
│  plan.go   （修改：Run 委托给 Graph.Execute）         │
│  primitive.go（逐步消解：逻辑迁入各 Runner）           │
└─────────────────────────────────────────────────────┘
```

**核心约束**：sugar.go 的方法签名一个不改。`wp.Auto("分析","...")` 对外行为完全一致。

---

## 3. Edge：从字符串字段到一等公民

### 3.1 Edge 类型定义（graph.go）

```go
type Edge struct {
    From      string         // 源节点 ID
    To        string         // 目标节点 ID
    Condition EdgeCondition  // nil = 无条件边
    Priority  int            // 条件边之间的优先级，0 = 最高
    Label     string         // 标签（调试用），如 "true"、"false"、"exhausted"
}

type EdgeCondition func(ec *ExecutionContext) bool
```

### 3.2 现有隐式边 → 显式 Edge 映射

```
现状                              → Edge 表示
───────────────────────────────────────────────────────────
node.next = "B"                   → Edge{From: "A", To: "B"}                          // 无条件边
ifCond → trueID / falseID         → Edge{From: "If", To: "B", Condition: condTrue}     // 条件边
                                   + Edge{From: "If", To: "C", Condition: condFalse}
switchCases[0].Match → nextID     → Edge{From: "Sw", To: "B", Condition: match1, P:0}
switchCases[1].Match → nextID     → Edge{From: "Sw", To: "C", Condition: match2, P:1}
loopExhaustedID                   → Edge{From: "Loop", To: "Exh", Condition: exhausted, P:1}
```

**关键**：If 节点的 `next` 不再有意义——两个出边都是条件边。Switch 同理。Loop 默认出边指向循环体入口（loop 内部控制），耗尽出边指向 exhausted handler。

---

## 4. Graph 执行引擎（graph.go）

### 4.1 数据结构

```go
type Graph struct {
    mu    sync.RWMutex
    nodes map[string]NodeRunner
    edges []Edge
    entry string
}

// NodeRunner 是可执行节点的最小抽象。
type NodeRunner interface {
    ID() string
    Run(ctx context.Context, ec *ExecutionContext) (output string, err error)
}
```

### 4.2 ExecutionContext —— 图执行期间传递的共享状态

```go
type ExecutionContext struct {
    Vars       map[string]string // eimit 写入的变量
    PrevOutput string            // 上一节点的 JSON 输出
    Result     *WorkPlanResult   // 累积执行结果
    Metadata   map[string]any    // 扩展字段（tracing span、user info 等）
}
```

### 4.3 执行循环

```go
func (g *Graph) Execute(ctx context.Context, ec *ExecutionContext) error {
    if g.entry == "" {
        return fmt.Errorf("graph: no entry node")
    }
    
    current := g.entry
    for current != "" {
        select {
        case <-ctx.Done():
            return ctx.Err()
        default:
        }
        
        node, ok := g.nodes[current]
        if !ok {
            return fmt.Errorf("graph: node %q not found", current)
        }
        
        output, err := node.Run(ctx, ec)
        if err != nil {
            return fmt.Errorf("graph: node %q: %w", current, err)
        }
        ec.PrevOutput = output
        
        next := g.resolve(current, ec)
        current = next
    }
    
    return nil
}

// ExecuteFrom 从指定节点恢复执行（用于 Resume 场景）。
func (g *Graph) ExecuteFrom(ctx context.Context, startNodeID string, ec *ExecutionContext) error {
    // 同 Execute，但 current 初始值为 startNodeID
    current := startNodeID
    // ... 同上循环
}
```

### 4.4 统一路由（不再有分散的 switch-case）

```go
// resolve 从当前节点出发，按优先级找到第一条匹配的边。
// 无条件边直接返回；条件边按 Priority 升序依次评估。
func (g *Graph) resolve(currentID string, ec *ExecutionContext) string {
    g.mu.RLock()
    defer g.mu.RUnlock()
    
    var candidates []Edge
    for _, e := range g.edges {
        if e.From == currentID {
            candidates = append(candidates, e)
        }
    }
    
    // 无条件边：直接返回
    for _, e := range candidates {
        if e.Condition == nil {
            return e.To
        }
    }
    
    // 条件边：按 Priority 排序后依次匹配
    sort.Slice(candidates, func(i, j int) bool {
        return candidates[i].Priority < candidates[j].Priority
    })
    for _, e := range candidates {
        if e.Condition != nil && e.Condition(ec) {
            return e.To
        }
    }
    
    return "" // 没有出边 = 图结束
}
```

### 4.5 构造方法（AddNode / AddEdge 是公开的低级 API）

```go
func NewGraph() *Graph {
    return &Graph{nodes: make(map[string]NodeRunner)}
}

func (g *Graph) AddNode(node NodeRunner) {
    g.mu.Lock()
    g.nodes[node.ID()] = node
    g.mu.Unlock()
}

func (g *Graph) AddEdge(e Edge) {
    g.mu.Lock()
    g.edges = append(g.edges, e)
    g.mu.Unlock()
}

func (g *Graph) SetEntry(nodeID string) {
    g.entry = nodeID
}
```

### 4.6 Validate（从 validate.go 搬入，图层面统一校验）

```go
func (g *Graph) Validate() error {
    // 1. 入口节点存在性
    if _, ok := g.nodes[g.entry]; !ok {
        return fmt.Errorf("entry node %q not found", g.entry)
    }
    
    // 2. 边引用完整性
    nodeIDs := make(map[string]bool)
    for id := range g.nodes {
        nodeIDs[id] = true
    }
    for _, e := range g.edges {
        if !nodeIDs[e.To] {
            return fmt.Errorf("edge %q → %q: target node %q not found", e.From, e.To, e.To)
        }
    }
    
    // 3. DFS 三色环检测
    if err := g.detectCycle(); err != nil {
        return err
    }
    
    return nil
}
```

**注意**：Loop 的 body 节点引用自身通过 Edge 表达（`Edge{From: bodyID, To: bodyID, Condition: loopNotDone}`），这在 DFS 检测时是显式的环。Validate 需要在检测时**跳过 Loop body 的自引用边**——这些边在运行时由 Loop Runner 内部控制迭代，不会导致图引擎死循环。

---

## 5. NodeRunner 实现（runner.go）

每种原语拆成一个 `NodeRunner` 实现。以 `autoRunner` 为例：

```go
type autoRunner struct {
    id          string
    systemPrompt string
    input        string
    toolFilter   []string
    factory      AgentFactory
    loopTimes    int
}

func (r *autoRunner) ID() string { return r.id }

func (r *autoRunner) Run(ctx context.Context, ec *ExecutionContext) (string, error) {
    input := renderTemplate(r.input, ec)
    agent := r.factory.NewAgent(r.systemPrompt, r.loopTimes)
    if f, ok := agent.(interface{ SetToolFilter([]string) }); ok && len(r.toolFilter) > 0 {
        f.SetToolFilter(r.toolFilter)
    }
    out, err := agent.Chat(ctx, input)
    if err != nil {
        return "", err
    }
    return toJSON(out), nil
}
```

**与现有 `primitiveAuto` 的唯一区别**：`input` 的渲染从 Runner 内部完成（通过 `renderTemplate(r.input, ec)`），`ec.PrevOutput` 由 Graph.Execute 在调用前写入。

### 各 Runner 的"静态输入 from node" vs "动态状态 from ec" 约定

| Runner | 静态（构建时从 sugar 传入） | 动态（运行时从 ec 读取） |
|--------|---------------------------|------------------------|
| autoRunner | systemPrompt, toolFilter, loopTimes | PrevOutput (作为 input 模板变量) |
| approveRunner | approveOptions, approveTimeout | PrevOutput, Vars |
| ifRunner | 无（它不执行，只路由） | PrevOutput（传给 Condition） |
| loopRunner | bodyID, untilFn, maxIter, exhaustedID | PrevOutput（初始 input） |
| forkRunner | branches (每个含 prompt + input) | PrevOutput, Vars |
| toolRunner | toolName, argsJSON | PrevOutput, Vars（模板渲染用） |

**关键原则**：Runner 内部不访问 WorkPlan 的其他节点。它通过 ec 拿到运行时上下文，通过构造参数拿到静态配置。

---

## 6. 糖层改造（sugar.go）

**方法签名不变，内部从"构造 node → append 到数组"变成"构造 Runner → graph.AddNode/AddEdge"**。

以 `Auto` 为例：

```go
// 旧实现
func (wp *WorkPlan) Auto(id, input string, opts ...NodeOption) *WorkPlan {
    n := &node{id: wp.genID(id), kind: kindAuto, input: input, ...}
    return wp.primitiveAddNode(n)
}

// 新实现
func (wp *WorkPlan) Auto(id, input string, opts ...NodeOption) *WorkPlan {
    nodeID := wp.genID(id)
    runner := &autoRunner{
        id:          nodeID,
        input:       input,
        factory:     wp.factory,
        systemPrompt: wp.defaultPrompt,
        loopTimes:   wp.defaultLoopTimes,
        // ... 从 opts 提取配置
    }
    wp.graph.AddNode(runner)
    
    // 自动推导线性边（和旧版 primitiveAddNode 行为一致）
    if wp.lastNodeID != "" {
        wp.graph.AddEdge(Edge{From: wp.lastNodeID, To: nodeID})
    }
    wp.lastNodeID = nodeID
    return wp
}
```

`If` 的改造：

```go
func (wp *WorkPlan) If(id string, cond func(string) bool, trueID, falseID string) *WorkPlan {
    nodeID := wp.genID(id)
    runner := &controlRunner{id: nodeID, kind: "if"} // 控制节点执行 = 透传 prev
    wp.graph.AddNode(runner)
    
    // 两个条件边
    wp.graph.AddEdge(Edge{
        From: nodeID, To: trueID, Priority: 0,
        Condition: func(ec *ExecutionContext) bool { return cond(fromJSON(ec.PrevOutput)) },
        Label: "true",
    })
    wp.graph.AddEdge(Edge{
        From: nodeID, To: falseID, Priority: 1,
        Condition: func(ec *ExecutionContext) bool { return !cond(fromJSON(ec.PrevOutput)) },
        Label: "false",
    })
    
    wp.lastNodeID = nodeID
    return wp
}
```

**注意**：`trueID` 和 `falseID` 在 `If` 被调用时可能还不存在（后续节点还没注册）。Edge 的 To 字段存储字符串 ID，校验在 `Validate()` 里统一做——运行时图已经完整。这和当前 `node.ifTrueID` / `node.ifFalseID` 的校验时机一致。

---

## 7. Resume / Approve 兼容性

暂停恢复是这次重构最需要小心的点。

### 旧机制

```
Run() 遇 Approve → pauseSnapshot{currentID, prevJSON, result, ...} 
  → 返回 PausedWorkPlan
  → 外部 SetDecision(v)
  → Resume() → executeApprove() → 从 currentID 继续
```

### 新机制

```go
// Graph 支持从指定节点恢复
func (g *Graph) ExecuteFrom(ctx context.Context, startNodeID string, ec *ExecutionContext) error {
    // ... 同 Execute，但 current 初始值为 startNodeID
}

// Resume 委托给 Graph.ExecuteFrom
func (wp *WorkPlan) Resume(ctx context.Context) (*WorkPlanResult, error) {
    snap := wp.pauseSnapshot
    // ... 校验 snap ...
    
    // 执行暂停的 approve 节点
    nID := snap.currentID
    approveRunner := wp.graph.nodes[nID].(*approveRunner)
    result := approveRunner.executeApprove(ctx, snap) // approve 的执行逻辑留在 runner 内
    
    // 继续执行后续节点
    nextID := wp.graph.resolve(nID, ec)
    if nextID != "" {
        wp.graph.ExecuteFrom(ctx, nextID, ec)
    }
    
    return ec.Result, nil
}
```

**pauseSnapshot 结构不变**。依然是 `{currentID, prevJSON, result, ...}`。关键在于 `ExecuteFrom` 能从一个节点开始跑。

---

## 8. 公开的低级 API

```go
// WorkPlan 新增公开方法
func (wp *WorkPlan) AddNode(runner NodeRunner) *WorkPlan
func (wp *WorkPlan) AddEdge(e Edge) *WorkPlan
func (wp *WorkPlan) SetEntry(nodeID string) *WorkPlan
```

高级用户可以直接构图：

```go
wp := workplan.New(factory, gate, "系统提示词")

// 自己实现 NodeRunner
wp.AddNode(&MyCodeReviewNode{threshold: 5}).
   AddNode(&MyDeployNode{env: "staging"}).
   AddEdge(Edge{From: "design", To: "code_review"}).
   AddEdge(Edge{From: "code_review", To: "deploy",
       Condition: func(ec *ExecutionContext) bool {
           return strings.Contains(ec.PrevOutput, `"approved"`)
       },
   }).
   AddEdge(Edge{From: "code_review", To: "fix_code",
       Condition: func(ec *ExecutionContext) bool {
           return strings.Contains(ec.PrevOutput, `"rejected"`)
       },
   })
```

**低级 API 和糖可以混用**——`AddNode` 不会破坏 `lastNodeID` 的线性链推导。但要小心：手动加边后糖的自动推导可能产生意外的第二条边。建议约定：混用时，手动加边放在最后。

---

## 9. 文件变更清单

| 文件 | 操作 | 说明 |
|------|------|------|
| `workplan/graph.go` | **新建** | Graph / Edge / ExecutionContext / Execute / resolve / Validate |
| `workplan/runner.go` | **新建** | 所有 NodeRunner 实现（autoRunner / ifRunner / loopRunner / forkRunner / approveRunner / controlRunner / checkpointRunner / emitRunner） |
| `workplan/plan.go` | 修改 | Run() 委托给 graph.Execute()；保留 pauseSnapshot + Resume |
| `workplan/sugar.go` | 修改 | 每个糖方法内部从"构造 node → primitiveAddNode"改为"构造 Runner → graph.AddNode/AddEdge" |
| `workplan/node.go` | 修改 | node 结构体仍保留（部分 Runner 构造依赖），但 `next` / `ifTrueID` / `ifFalseID` / `loopExhaustedID` 字段逐步废弃 |
| `workplan/primitive.go` | 修改 | 逻辑迁入各 Runner → 逐渐消解；模板渲染 `primitiveRenderInput` 提升为 `renderTemplate` |
| `workplan/validate.go` | 修改 | 节点级校验移入 graph.Validate()；保留 runner 构造期校验 |

---

## 10. 不做的

- **不改 sugar 方法签名**：`Auto`、`If`、`Fork`、`Loop`、`Approve`、`Gate`、`Retry`、`Checkpoint`、`Emit`、`Pipeline`、`Switch` 对外接口完全不变
- **不引入泛型 Graph[State]**：状态依然是 `ExecutionContext`（`map[string]string`），不给图引擎加泛型参数。泛型 State 是未来版本考虑的事
- **不做子图嵌套**：图引擎保留 `AddSubgraph()` 的扩展点但不实现。先做平面图
- **不做图可视化生成**：不输出 DOT / Mermaid。由应用层通过 `graph.Nodes()` / `graph.Edges()` 自取
- **不做边权重 / 超时 / 回退**：保持 Edge 为轻量结构，不引入网络图的概念

---

## 11. 风险点与回归测试

| 风险 | 缓解 |
|------|------|
| Loop body 自引用边被 Validate 误判为环 | Validate 的 DFS 检测显式跳过 Loop body → Loop body 的边 |
| If/Switch 在 ID 还未注册时就引用了 falseID/trueID | 校验推迟到 Validate()（和现在行为一致），构造期只存字符串 |
| Resume 后的节点遍历 | 用 `ExecuteFrom` 重走 graph.resolve()，不用复制 Run() 的遍历逻辑 |
| sugar 自动推导 next 和手动 AddEdge 冲突 | 约定：手动 AddEdge 后 lastNodeID 不更新，糖不再自动追加边 |

**回归测试清单**：

1. 6 个 example_implement 全部跑通（sugar API 兼容性）
2. 用 `AddNode` + `AddEdge` 构建一个和 example 等价的 WorkPlan（低级 API 正确性）
3. 审批暂停 + Resume 正常（`ExecuteFrom` 正确性）
4. Loop 的 Signal + OnUpdate 正常（Runner 内部状态管理不受影响）
5. Fork 并发正常
6. 空的图 → Execute 返回 error
7. 单节点图 → Execute 正常完成
