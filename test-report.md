# 测试报告

日期：2026-07-27

## 概览

| 项目 | 结果 | 关键指标 |
|---|:---:|---|
| 定向单元测试 | 通过 | `core/types`、`runtime/scheduler`、`sugar/fork` |
| Fork/join 功能测试 | 通过 | 父上下文继承、分支结果汇总和 join 聚合连续 20 轮通过 |
| 自动 fork 资源与失败测试 | 通过 | 可配置 semaphore 上限和 fail-fast 取消兄弟分支连续 20 轮通过 |
| 竞态测试 | 通过 | `go test -race ... -count=3`，未发现 data race |
| 资源占用测试 | 通过 | semaphore 最大并发为 2，连续 100 轮通过 |
| goroutine 泄漏 | 通过 | `goleak.VerifyNone` 验证全部分支退出 |
| 差异检查 | 通过 | `git diff --check` 无空白错误 |

## 覆盖重点

- `WorkflowContext.Clone` 深拷贝 `PrevResults`、`Vars`、`WorkPlanResult` 与嵌套 `Metadata`。
- Scheduler 自动 fork 为每个分支创建独立上下文，父节点与兄弟分支互不污染。
- `TestForkJoinContextInheritance` 验证分支继承 fork 时父快照，join 汇总 `PrevResults` 与聚合 `PrevOutput`。
- `TestSchedulerForkRespectsMaxForkConcurrency` 与 `TestSchedulerForkFailFastCancelsSiblings` 验证自动 fork 的资源上限、取消传播和失败后禁止 join。
- typed slice/map 与 pointer Metadata 均由反射递归复制，避免非 JSON 容器保留引用。
- 显式 `ForkNode` 在启动 goroutine 前为每个分支创建上下文副本。
- `ForkNode` 的 semaphore 始终限制活跃分支数，且分支退出后无 goroutine 残留。

## 综合判断

- [x] 通过

## Parallel Coordinator Verification (2026-07-27)

| Dimension | Result | Evidence |
|---|:---:|---|
| Functional regression | Passed | Coordinator panic handling, explicit best-effort, injected runtime, branch events, divergent paths, nested joins, and failed `plan_run` results passed with `-count=10`. |
| Resource occupancy | Passed | Automatic and explicit fork concurrency limit tests passed with `-count=100`; active branch counters return to zero. |
| Race detector | Environment blocked | Windows ThreadSanitizer failed to reserve 70-88 MiB virtual memory with error 87 in both sandboxed and elevated runs. The test binaries start successfully; no race report was produced. WSL is installed but its Linux kernel is unavailable on this machine. |
| Diff validation | Passed | `git diff --check` completed without whitespace errors. |

Commands executed:

```powershell
go test ./workplan/runtime/forkexec ./workplan/runtime/scheduler ./workplan/sugar/fork ./agent/core/tool/builtin -run 'Test(RunRecoversPanicAndCancelsSibling|BestEffortJoinUsesStableBranchIDs|Run_UsesInjectedBranchRuntimeAndEmitsBranchEvents|PlanRunFailureIncludesKnownNodeResults|NestedForkDependencyJoin|RunWithForkDivergent|SchedulerForkRespectsMaxForkConcurrency|SchedulerForkFailFastCancelsSiblings|ForkJoinContextInheritance)' -count=10 -timeout=2m

go test ./workplan/runtime/scheduler ./workplan/sugar/fork -run 'Test(SchedulerForkRespectsMaxForkConcurrency|Run_RespectsMaxConcurrentAndReleasesGoroutines)' -count=100 -timeout=3m

go test -race ./workplan/runtime/forkexec ./workplan/runtime/scheduler ./workplan/sugar/fork ./agent/core/tool/builtin -run 'Test(RunRecoversPanicAndCancelsSibling|BestEffortJoinUsesStableBranchIDs|Run_UsesInjectedBranchRuntimeAndEmitsBranchEvents|PlanRunFailureIncludesKnownNodeResults|NestedForkDependencyJoin|RunWithForkDivergent|SchedulerForkRespectsMaxForkConcurrency|SchedulerForkFailFastCancelsSiblings|ForkJoinContextInheritance)' -count=3 -timeout=3m
```
