# Phase 2: WorkPlan Declarative Plan — Output

## Summary

Implemented JSON-serializable `Plan` type in `workplan` package, enabling export/import of WorkPlan workflows as declarative data structures. Sugar API remains completely unchanged.

## Files Changed

### 1. `G:\Program\go\Seele\workplan\plan.go` (modified — appended)

Added after the existing `ToTool` method:

**Types:**
- `PlanNodeSpec` — Serializable node spec with JSON tags for all node kinds (auto, method, llm, strategy, approve, if, switch, loop, fork, checkpoint, emit, join)
- `ForkBranchSpec` — Serializable ForkBranch
- `SwitchCaseSpec` — Serializable SwitchCase
- `PlanEdgeSpec` — Serializable edge spec with optional Condition label
- `Plan` — Top-level serializable workflow definition (Name, Description, Version, EntryNodeID, Nodes, Edges)
- `ConditionRegistry` — Maps string condition labels to EdgeCondition functions for deserialization
- `PlanNodeOpt` — Functional option type for `Plan.Add()`

**Builder methods:**
- `NewPlan(name string) *Plan` — Create a Plan
- `(*Plan).Add(id, kind string, opts ...PlanNodeOpt) *Plan` — Add a node
- `(*Plan).Edge(from, to string) *Plan` — Add an edge
- `(*Plan).EdgeWith(from, to, label string) *Plan` — Add a labeled edge

**PlanNodeOpt functions:**
- `WithInput`, `WithSystemPrompt`, `WithToolFilter`, `WithPlanNext`
- `WithApproveOptions`, `WithForkBranches`
- `WithLoopConfig`, `WithIfBranches`, `WithSwitchCases`, `WithEmitKey`

**Helper functions:**
- `nodeToSpec(n *node) PlanNodeSpec` — Convert internal node to serializable spec
- `specToNode(spec PlanNodeSpec) *node` — Convert spec back to internal node
- `specToRunner(spec PlanNodeSpec, factory AgentFactory, defaultPrompt string) (NodeRunner, error)` — Reconstruct runners from spec, returns error for method/strategy (cannot deserialize Go functions)

**WorkPlan methods:**
- `(*WorkPlan).ToPlan() *Plan` — Export WorkPlan to serializable Plan
- `(*WorkPlan).LoadPlan(plan *Plan, registry *ConditionRegistry) error` — Rebuild WorkPlan from Plan, with optional condition resolution

### 2. `G:\Program\go\Seele\workplan\graph.go` (modified)

- Added JSON tags to `Edge` struct: `From` (`json:"from"`), `To` (`json:"to"`), `Condition` (`json:"-"`, excluded from serialization), `Priority` (`json:"priority,omitempty"`), `Label` (`json:"label,omitempty"`)

### 3. `G:\Program\go\Seele\workplan\sugar.go` (untouched)

All sugar methods remain exactly as they were. No modifications.

## Kind Mapping

| String | NodeKind |
|--------|----------|
| "auto" | kindAuto |
| "method" | kindMethod |
| "llm" | kindLLM |
| "strategy" | kindStrategy |
| "approve" | kindApprove |
| "if" | kindIf |
| "switch" | kindSwitch |
| "loop" | kindLoop |
| "fork" | kindFork |
| "checkpoint" | kindCheckpoint |
| "emit" | kindEmit |
| "join" | kindJoin |

## Test Results

```
go vet ./workplan/...       — PASS
go test -race -count=3 ./workplan/...  — PASS (2.259s)
go test -cover ./workplan/...  — PASS (coverage: 6.1%)
```

The full project also builds cleanly (`go build ./...` — no errors).

## Design Decisions

1. **`WithPlanNext` naming**: sugar.go already exports `WithNext` as a `NodeOpt`, which conflicts with the `PlanNodeOpt` version. Renamed to `WithPlanNext` to avoid redeclaration while keeping semantic clarity.

2. **method/strategy rejection in LoadPlan**: These kinds carry Go function references that cannot be serialized. LoadPlan returns a clear error message indicating live references are required.

3. **ConditionRegistry pattern**: Edge conditions are Go functions that cannot be serialized. LoadPlan uses a registry-based approach: the caller pre-registers named condition functions, and PlanEdgeSpec stores only the condition label string.
