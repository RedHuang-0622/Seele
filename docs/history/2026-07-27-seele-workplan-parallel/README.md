# WorkPlan Parallel Execution

## Scope

This change makes every fork branch own an isolated deep copy of the parent
`WorkflowContext`. A branch can mutate its own `PrevOutput`, `PrevResults`,
`Vars`, `Metadata`, and result state without changing its parent or siblings.

## Design

- `ParentSnapshot` freezes a deep-copied parent `WorkflowContext` before any
  branch starts. `ContextManager` is the only component that creates isolated
  `BranchContext` values and merges `BranchResult` values at a join boundary.
- `ForkCoordinator` is the single execution type for Scheduler fan-out and
  explicit `ForkNode`; `Coordinator` remains only as a compatibility alias.
- `ForkPolicy` defaults to fail-fast. `JoinPolicy` is derived as require-all
  for fail-fast and successful-only for explicit best-effort unless callers
  explicitly override it.
- `runtime/forkexec.ForkCoordinator` is the only parallel branch executor. Both
  Scheduler automatic forks and explicit `ForkNode` delegate to it.
- The default policy is `fail_fast`. `best_effort` is available only by an
  explicit policy setting.
- The coordinator owns the cancellation context, configured semaphore,
  `WaitGroup`, panic recovery, stable branch-ID ordering, join aggregation,
  and lifecycle event delivery.
- A panic becomes a `panicked` branch result. In fail-fast mode it cancels
  sibling work; in best-effort mode successful branch results can still join.
- Panic recovery is installed before `Prepare`, so a panic from context setup,
  limiter acquisition, or branch execution always becomes a `panicked` result.
- Scheduler passes a branch-bound `AgentFactory` to Auto strategy nodes at
  execution time; it never mutates or uses the shared factory captured while
  the graph was built.
- Approval nodes use the same branch-bound `AgentFactory` after their gate
  permits execution, so `kind: "manual"` cannot fall back to the shared
  construction-time account.
- Scheduler execution is dependency-count based. A join node is eligible only
  after all activated incoming branches finish, so multi-level DAG joins do
  not end when paths diverge.

## Runtime Injection Boundary

`forkexec.BranchRuntime` is supplied by Seelex through a resolver. It carries
read-only branch metadata together with an optional branch-bound
`AgentFactory` and limiter. Seele consumes those injected values only; it does
not load or inspect Seelex configuration.

## Observable Events

Every branch lifecycle event includes its branch ID and is one of:

`queued`, `started`, `completed`, `failed`, `canceled`, or `panicked`.

Register the event hook with `workplan.WithBranchEventHook`; configure runtime
injection with `workplan.WithBranchRuntimeResolver`; set automatic fork policy
and concurrency with `workplan.WithForkPolicy` and
`workplan.WithMaxForkConcurrency`.

## Failure Responses

When `plan_run` fails, its JSON response remains `status: failed` and also
contains every NodeResult known at the failure point. This preserves branch
state for callers that need diagnostics or recovery decisions.

`WorkPlanTool` accepts injected branch event hooks, runtime resolvers, fork and
join policies, and a concurrency limit. Both `plan_load` and `plan_clear`
create WorkPlans through the same configured construction path.

## Verification

The accompanying tests cover context isolation, panic conversion, fail-fast
cancelation, explicit best-effort aggregation, lifecycle events, injected
runtime factories for both automatic and approval nodes, stable joins, nested
DAG joins, failure responses, race detection, and bounded branch concurrency.
See `test-report.md` at repository root for the executed command log and
results.
