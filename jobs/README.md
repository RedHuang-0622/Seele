# jobs - the asynchronous job root capability

`jobs` turns "one long-running task" into a first-class framework object: a job
that is **dispatched**, then **observed / fetched / killed / retired** through a
handle. It is the framework-side generalisation of the async job face that was
first proven inside Seelex (`seelebridge/tools/job_contract.go`).

## Boundary

```text
┌────────────────── Seele (no product semantics) ──────────────────┐
│  jobs: contract + Manager + jobs_manage management tool           │
│  tools · tools/permission · session · workplan · event (existing) │
└──────────────────────────────────────────────────────────────────┘
        ▲ register Executors + call Manager      jobs never imports a product
┌────────────────── product (e.g. seelex) ─────────────────────────┐
│  dispatch-side tools: bash_bg / read_batch / fork_subagents       │
│  Executors: process / inline / worker / seat                      │
└──────────────────────────────────────────────────────────────────┘
```

The split is *management is generic, dispatch is product-specific*: a
management action has the same meaning for every job kind, while a dispatch
tool is bound to a concrete command and prompt vocabulary. `jobs` therefore
ships the manager and `jobs_manage`, and **no dispatcher and no executor**.

## Usage

```go
manager, err := jobs.New(
    jobs.WithExecutor(myProcessExecutor),   // product-supplied
    jobs.WithExecutor(myWorkerExecutor),
    jobs.WithSessionResolver(sessionKeyFromContext),
    jobs.WithSubjectResolver(subjectFromContext),
    jobs.WithLimits(jobs.Limits{InFlight: 32, HardCap: 30 * time.Minute}),
)

handle, err := manager.Dispatch(ctx, jobs.Spec{
    Kind:        "process",
    Scope:       jobs.Scope{Session: sessionID, Subject: "emp_exec"},
    Node:        "impl",       // attribution only
    Batch:       batchID,      // attribution only
    Description: "run the integration suite",
    Payload:     json.RawMessage(`{"command":"go test ./..."}`),
})
```

## Dispatch, or declare-then-start

`Dispatch` registers the job *and* starts its executor. A product that owns the
execution body itself (it launches the process, or runs its own goroutine) uses
the register-only pair instead, so it can report "the body failed to start" in
the dispatch receipt rather than as a ghost running row:

```go
handle, err := manager.Declare(ctx, jobs.Spec{Kind: "process", Scope: scope}) // register only
// ... the product starts its own body, keyed by handle ...
if err := startMyBody(handle); err != nil {
    _ = manager.Complete(ctx, handle, jobs.StateFailed, 1, err.Error()) // external terminal entry
}
```

`Declare` does not require `Description` (a register-only caller may fill the row
title later); `Dispatch` still does. `Start(ctx, handle)` launches the executor
of a declared job when the body *is* a registered executor. An executor reads its
own identity from `Spec.Handle`, which the manager fills in before `Start` runs.
`Complete` is the external twin of `Sink.Complete` (idempotent; a non-terminal
state is coerced to `failed`).

## Read-only peek

`Fetch` consumes: it reads the increment, advances the cursor, and retires a
terminal job once its last byte is delivered. `Peek` is its read-only twin: same
budget, but it neither advances the cursor nor retires, for a two-phase read or a
probe that must not consume.

## Output ownership

By default the manager creates and owns `<outputDir>/<handle>.log`, and removes
it when the job is retired. `Spec.OutputPath` hands that file to the product
instead: the manager then never creates or holds a write handle for it (it only
reads it by offset), so a directory holding it stays removable while the job is
registered, and the product owns its lifecycle. `Record.OutputRef` reports the
path either way.

## Scope is two parallel fields

`Scope{Session, Subject}` is never a concatenated string. `Session` is the
isolation and reclamation key (destroying a session reclaims its jobs, exactly
like a product's `CloseSessionAsync`); `Subject` is the grouping dimension
(`emp_<role>`). Both participate in authorization; attribution tags (`Node`,
`Batch`) do not.

Two-tier queries:

| call | meaning |
|---|---|
| `Snapshot(Scope{Session})` | every registered job of the session |
| `Snapshot(Scope{Session, Subject})` | only that subject's jobs |
| `Reclaim(ctx, Scope{Session})` | session-level reclamation |
| `Reclaim(ctx, Scope{Session, Subject})` | reclamation of one subject only |

## Invariants

| # | invariant |
|---|---|
| I-1 | finalization happens exactly once, on all four paths (normal exit, hard cap, kill, executor panic) |
| I-2 | a hard cap synthesises `exit=124` and a kill `exit=137`, and the cause is noted in the output file |
| I-3 | handles are `a<seq>` with a monotonic counter (never `len(table)`); dedup applies only while the existing job runs |
| I-4 | records and handles live in memory only; persisting them would leave forever-running ghost rows |
| I-5 | reclamation is scope destruction (`Reclaim`), and `Close` cancels every live job |
| I-6 | output is bounded; over-cap bytes are dropped while still reported as written |
| I-7 | fetch / peek / kill / done / complete refuse a caller outside the job's scope |

`Observe` is deliberately context-free (it is a read-only reading); a product
that needs isolation on the read path gates it itself, exactly as it does today.

## What is not here

No push. `Events()` is a change-signal port only: buffer of one, latest-wins,
never advancing a cursor and never entering a prompt. The framework never
delivers a result into a busy session - that invariant belongs to the product
runtime.