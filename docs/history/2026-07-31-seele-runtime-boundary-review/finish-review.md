# Seele 运行时边界首轮实现最终审查报告

## 变更概览

本次审查覆盖当前工作区未提交的首轮实现，稳定设计基线为[运行时边界架构](../../arch/09-seele-runtime-boundary.md)和[扩展契约](../../arch/10-seele-extension-contracts.md)。

| 变更组 | 主要文件 | 规模 | 设计意图/模式 |
| --- | --- | --- | --- |
| ReAct 压缩策略 | [`engine/loop.go`](../../../engine/loop.go)、[`engine/engine_test.go`](../../../engine/engine_test.go)、`engine_smoke_test.go` | 约 +291/-12 行，另有 smoke test | Strategy、Functional Options、显式 helper |
| Plan 内核幂等写入 | [`workplan/core/plan/plan.go`](../../../workplan/core/plan/plan.go)、测试与 smoke test | 约 +120/-5 行，另有 smoke test | Copy-on-write、CAS、显式 Replace |
| Scheduler 防重复执行 | [`workplan/runtime/scheduler/scheduler.go`](../../../workplan/runtime/scheduler/scheduler.go)、测试与 smoke test | 约 +41 行，另有 smoke test | 防御式去重、并发验收 |
| 架构文档 | [`docs/arch/`](../../arch/README.md) | 4 个新增文档及总索引更新 | Ports and Adapters、Factory、消费方接口 |

## 审查结论

| 维度 | 状态 | 评分 | 备注 |
| --- | :---: | :---: | --- |
| 正确性 | 🚨 | C | Graph 公开行为被静默改写；条件闭包比较不可靠；显式压缩存在状态覆盖与错误吞掉 |
| 可读性 | ⚠️ | B | 核心意图有注释，但存在重复测试标题、错误注释和 API 文档未同步 |
| 架构 | 🚨 | C | 默认关闭自动压缩是正确方向，但 threshold/custom 仍由主 ReAct 主动触发，尚未建立 ephemeral 边界 |
| 安全性 | ✅ | B | 未发现新增密钥、命令注入或环境权限扩大；但 callback 可原地修改 live history，数据完整性保护不足 |
| 性能 | ⚠️ | B | 调度器每轮新增 map/slice 分配；新增 engine 测试每例等待 5 秒 Hub timeout |
| Go 专项 | ⚠️ | B | 默认 test、vet、普通 build 通过；race 因环境缺少 C compiler 未完成，且观察到一次 tracer 时间分辨率不稳定 |

## 发现的问题

### 🚨 严重（4 个）

#### 1. `Plan.AddNode` 静默破坏 Graph 的既有覆盖语义，修改测试不能替代兼容迁移

位置：[`workplan/core/plan/plan.go`](../../../workplan/core/plan/plan.go#L38)、[`workplan/runtime/graph/graph.go`](../../../workplan/runtime/graph/graph.go#L29)、[`workplan/runtime/graph/graph_test.go`](../../../workplan/runtime/graph/graph_test.go#L70)

`AddNode` 从“同 ID 覆盖”改成“保留第一个节点”，但 `Graph.AddNode` 仍直接转发。审查开始时，HEAD 中的既有公开测试 `TestAddNodeDuplicateOverwrites` 明确要求第二个节点覆盖第一个节点；审查期间该测试被改名并改为接受 first-write-wins，但没有迁移 Graph API、builder、外部调用方或模块 README。修改断言只能让测试变绿，不能证明 breaking change 已完成迁移。

当前差异可用以下命令确认：

```powershell
git diff HEAD -- workplan/core/plan/plan.go workplan/runtime/graph/graph_test.go
```

差异同时改变实现语义和原有契约测试，却没有提供 Graph 侧 `ReplaceNode` 或版本迁移说明。

建议二选一并保持全链路一致：

1. 保持 `AddNode` 原有 replace/upsert 语义，另加 `AddNodeIfAbsent` 用于并发幂等构造；这是兼容性更好的方案。
2. 若确定修改公开语义，则 Graph 必须新增 `ReplaceNode`，迁移所有 builder/serializer，更新模块 README，并以 major/breaking change 管理。

在兼容策略明确前，当前状态不能作为无破坏升级合并。

#### 2. 使用 `reflect.Value.Pointer` 判断条件闭包身份会错误合并不同语义的边

位置：[`workplan/core/plan/plan.go`](../../../workplan/core/plan/plan.go#L14)

`conditionsEqual` 假定两个非 nil function 的 code pointer 相同就代表同一个闭包。Go 的 `reflect.Value.Pointer` 文档明确说明：对 Func 返回的是 underlying code pointer，**不保证足以唯一标识一个 function value**。由同一个函数文字创建、但捕获值不同的闭包可以共享 code pointer。

因此下面两条语义不同的边可能被错误去重：

```go
for _, expected := range []string{"backend", "tests"} {
    expected := expected
    p.AddEdge(edge.Edge{
        From: "inspect",
        To:   "integrate",
        Condition: func(wc *types.WorkflowContext) bool {
            return wc.Vars["branch"] == expected
        },
    })
}
```

结果会是条件边丢失，属于执行语义错误。现有测试把两个函数字面量写在不同源码行，无法覆盖同一代码入口、不同捕获值的情况。

建议不要对 function value 做身份去重。可选方案：

- 只对 `Condition == nil` 的无条件边按 `(From, To)` 去重。
- 为 edge 增加稳定 `ID`/`ConditionKey`，由 assembler 显式提供幂等键。
- 让 `AddEdge` 保持追加语义，另提供 `AddEdgeIfAbsent(key, edge)`。

#### 3. `CompressNow` 在配置 cache/storage 的 session 中会被下一次 `Run` 覆盖

位置：[`engine/loop.go`](../../../engine/loop.go#L118)、[`engine/loop.go`](../../../engine/loop.go#L128)、[`engine/loop.go`](../../../engine/loop.go#L143)、[`engine/loop.go`](../../../engine/loop.go#L317)

状态序列如下：

```text
Run #1 -> defer saveToCache() 写入未压缩 history
CompressNow() -> 只替换内存 rl.history
Run #2 -> restoreFromCache() 用旧缓存覆盖已压缩 history
```

因此显式压缩在最需要持久化的产品会话中不生效，现有测试全部使用无 cache/storage 的 loop，未覆盖该路径。

修复方向不是简单在 `CompressNow` 内盲目写缓存，而是先明确 history owner：

- 外部 history owner：Seelex 传入/持久化，Seele 不自动 restore/save。
- runtime history owner：显式 state store 接口负责原子 replace。

至少需要增加“有 cache 的 CompressNow 后下一次 Run 仍使用压缩结果”的回归测试。

#### 4. 自定义压缩错误被完全吞掉，并且 callback 可在报错前原地破坏 history

位置：[`engine/loop.go`](../../../engine/loop.go#L152)、[`engine/loop.go`](../../../engine/loop.go#L303)

`maybeCompress` 返回 error，但 `Run` 使用 `_ = rl.maybeCompress(ctx)` 丢弃。`WithCustomCompression(nil)`、callback 超时或结构校验失败都会静默继续。注释称“Errors are returned for visibility”，实际没有任何 hook、trace 或返回值可观察该错误。

同时 callback 接收 live `rl.history` slice。callback 可以先修改消息，再返回 error；即使 `Run` 选择 fallback，history 也已经被部分污染。

建议：

- 不在主 ReAct loop 内执行 Seelex 压缩 callback；由 Seelex 先完成 ephemeral compression，再显式替换下一次输入。
- 若短期保留迁移接口，传入深/防御性副本，只在成功且结果合法时原子替换。
- policy 配置错误、context cancel/timeout 和 callback error 必须返回或进入明确的 error hook；不得静默降级。
- 校验返回 history 非 nil、保留必要消息并满足调用方约束。

### ⚠️ 警告（5 个）

#### 1. 主 ReAct 仍保留 threshold/custom 自动触发路径，与已确认边界不一致

位置：[`engine/loop.go`](../../../engine/loop.go#L23)、[`engine/loop.go`](../../../engine/loop.go#L289)

默认 `CompressionPolicyDisabled` 已消除原来的无条件自动压缩，这是本轮最重要的正向改动。但 `CompressionPolicyThreshold` 和 `CompressionPolicyCustom` 仍由 `Run` 内的 `maybeCompress` 主动执行，且注释把 Seelex-managed custom thunk 作为推荐替代。

确认过的目标边界是：Seelex 直接编排无工具的 EphemeralSession/QuickChat；Seele 主 ReAct 不触发压缩。若 threshold 仅为迁移兼容，应默认关闭、标明删除版本并避免把 custom main-loop callback 设计成长期 API。

当前实现尚未新增 EphemeralSession/QuickChat，也没有完成 usage 保真和工具外移，因此只能视为阶段一，不是边界改造完成态。

#### 2. 两个 A/B 测试是假阳性，未验证注释声称的行为

位置：[`workplan/runtime/scheduler/scheduler_smoke_test.go`](../../../workplan/runtime/scheduler/scheduler_smoke_test.go#L196)、[`engine/engine_smoke_test.go`](../../../engine/engine_smoke_test.go#L138)

- Scheduler 取消测试创建的是 `slow0..slow5` 节点，却用 `leafNames(0, 6)` 添加指向 `leaf0..leaf5` 的边。实际日志为 `err=node "leaf5" not found`，51ms 即返回；测试只检查 `err != nil` 和耗时，因此错误地报告 deadline 取消通过。
- Engine 的 `TestAB_ConcurrentLoopsCancelIndependently` 创建了 `WithCancel` context，但直到所有 goroutine 完成后才 defer cancel；没有取消任何 session，也没有比较各 loop 的 history，无法证明“独立取消”或“history 不串线”。

应分别断言 `errors.Is(err, context.DeadlineExceeded/Canceled)`，并在并发测试中主动取消一个指定 session、验证其他 session 正常完成及各自历史只包含自己的标记。

#### 3. “默认不压缩”测试只搜索 summary sentinel，无法证明 history 未被改写

位置：[`engine/engine_test.go`](../../../engine/engine_test.go#L589)、[`engine/engine_smoke_test.go`](../../../engine/engine_smoke_test.go#L27)

测试没有强制断言 Provider 只收到一次请求、消息数量/顺序/内容与输入一致。若未来改用不同 summary 文案、硬裁剪或无 sentinel 的压缩方式，测试仍可能通过。

应使用 spy completer 断言：调用次数为 1；工具为空/符合会话配置；完整 history（加当前 user message）逐条一致；运行后主 history hash 未变化。

#### 4. 公开 API 与模块 README 未同步

位置：[`workplan/core/plan/README.md`](../../../workplan/core/plan/README.md)、[`workplan/runtime/graph/README.md`](../../../workplan/runtime/graph/README.md)、[`engine/README.md`](../../../engine/README.md)

新增了 `ReplaceNode`、压缩 policy、`WithCustomCompression`、`CompressNow` 和 `ReActLoop.AppendHistory`，同时改变 `AddNode` 语义，但相应模块 README 未描述这些公开入口、默认行为、错误和兼容策略，违反仓库文档同步要求。

另外 `CompressNow` 注释写“returns the new history slice”，实际签名只返回 `error`；`engine_test.go` 的 Compression policy 标题和未完成注释重复出现。

#### 5. 新增调度去重每轮分配，但没有可触发的生产路径测试

位置：[`workplan/runtime/scheduler/scheduler.go`](../../../workplan/runtime/scheduler/scheduler.go#L112)

`queued` 在加入 `nextReady` 时立即置位，当前单线程结果归并路径已经阻止同一节点在一轮内重复入队。`dedupeReadyNodeIDs` 每轮额外分配 map 和 slice，但新增测试只直接测试 helper，没有构造能在 Scheduler 中产生 duplicate ready 的场景。

建议先证明重复来源并修正产生重复的内核不变量；如果保留防御层，增加 end-to-end 回归并复用原 slice 做原地去重，避免无必要分配。

### 💡 建议（4 个）

1. 将首轮拆成明确提交：先只删除默认自动压缩并加严格回归测试；再单独实现 ephemeral completion；Plan 幂等语义不要混入同一提交。
2. engine 单测应使用不会启动 Hub/MCP 的轻量 Agent fixture。当前新增的每个用例都等待约 5 秒 Hub startup timeout，A/B engine 三例耗时约 15 秒，全量 engine 测试约 60 秒。
3. `WithCompressionPolicy` 应拒绝未知枚举值；当前非法值落入 switch default 并静默等价于 disabled。
4. smoke test 中不需要通过 `var _ = types.NewWorkflowContext` 保留未使用 import；删除无意义 import/占位，保持测试意图聚焦。

## ✅ 亮点

- `CompressionPolicyDisabled` 成为默认值，原固定 `6144 -> 8192` 路径不再默认触发，方向正确。
- Plan 已作为 scheduler 的直接依赖，Graph 仍是 facade，没有重新引入 `scheduler -> graph` 反向依赖。
- Plan 的 copy-on-write + CAS 写入路径没有新增锁顺序或明显数据竞争。
- 新增测试覆盖了并发构造、fork 并发上限和显式压缩的基本路径。
- 未发现新增密钥、固定真实服务地址、命令注入或文件/工作区权限扩张。
- `go vet ./...` 与 `go build ./...` 通过。

## Go 专项检查

| 检查 | 结果 |
| --- | --- |
| 新增 `return nil, nil` | 未发现；仓库存在既有命中，不属于本轮新增 |
| 新增包级可变状态 | 未发现 |
| `for range` 取 `&item` | 未发现 |
| interface nil | 本轮未发现新增问题 |
| `go vet ./...` | 通过 |
| `go build ./...` | 通过 |
| `go test ./...` | 当前快照通过；此前一次运行出现 tracer duration=0，重跑通过，属于待稳定化的时间分辨率问题 |
| `go test -race` / `go build -race` | 未完成：当前 Windows 环境 `CGO_ENABLED=0`，启用后缺少 `gcc` |

## 测试记录

| 命令 | 结果 |
| --- | --- |
| `go test ./workplan/runtime/graph ./workplan/core/plan ./workplan/runtime/scheduler -count=1` | 通过；Graph 测试已在审查期间改为 first-write-wins |
| `RUN_AB=true go test -tags seele_ab ./workplan/core/plan ./workplan/runtime/scheduler -run TestAB -v -count=1` | 表面通过；scheduler cancel 用例实际因 node not found 提前结束 |
| `RUN_AB=true go test -tags seele_ab ./engine -run TestAB -v -count=1` | 通过，但 concurrent cancel 用例未执行 cancel |
| `go test ./...` | 当前快照通过，engine 约 60 秒；此前一次运行出现 tracer duration=0，重跑通过 |
| `go vet ./...` | 通过 |
| `go build ./...` | 通过 |
| `CGO_ENABLED=1 go test -race ...` | 环境失败：`gcc` 不在 PATH |

## 最终判断

- [ ] ✅ 通过，可合并
- [ ] ⚠️ 有条件通过
- [x] 🚨 不通过

合并前至少必须完成：

1. 恢复或完整迁移 `AddNode` 的公开语义；不能只修改既有测试断言。
2. 删除基于 function code pointer 的条件边去重，改用可靠的显式幂等键或保守策略。
3. 修复 `CompressNow` 与 cache/storage 的状态所有权，并让 custom compression 错误可观察且不能原地污染 history。
4. 修正两个假阳性 A/B 测试，并重新运行默认、A/B 与 race 测试。

边界方向值得保留，但当前实现仍是未完成且存在回归的阶段性版本。
