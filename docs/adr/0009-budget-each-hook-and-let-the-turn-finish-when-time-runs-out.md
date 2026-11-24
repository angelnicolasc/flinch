# 0009. Budget each hook and let the turn finish when time runs out

- **Status:** Accepted
- **Implementation:** Implemented
- **Date:** 2026-11-24

## Context

A Stop hook is synchronous, so the developer waits while it runs, and the host may call it again after every repair. If each subprocess or callback gets its own allowance, the wait grows with the size of the diff. An early build registered a 30-second Stop timeout, yet its reuse proof could run for up to 90 seconds. The host would have killed the hook in the middle of a proof.

## Decision

Each hook runs on one clock, started before the CLI reads its input:
- The installer registers 10 seconds for prompt capture and 30 seconds for Stop.
- The CLI keeps 250 milliseconds in reserve to write its response.
- Discovery, snapshots, enforcement and proofs all draw from that single deadline.
- Enforcement gets up to 10 seconds by default, counted from hook entry.

Test time is charged to the human turn across its continuations: 20 seconds per turn by default, and at most 5 seconds per test. A turn gets at most one repair per finding and two continuations in total. Flinch saves the charge before asking the agent to continue. If the save fails, it does not ask.

## Alternatives considered

- **A timeout per candidate.** A large diff could hold the host past its own limit.
- **A counter that resets on each callback.** A repeated Stop could spend the same budget twice.
- **Moving everything to CI.** The feedback would arrive after the agent has left.

## Consequences

The agent's turn simply finishes in three cases: Flinch cannot attribute a finding to this turn, cannot save its state or runs out of time. A lock serializes callbacks that share a checkpoint. Each processed Stop leaves a receipt, so a duplicate delivery never requests the same repair twice or repeats a success message. Anything deferred still runs wherever the team runs its native tests.

## In the code

- [`internal/cli/hook_cmd.go`](../../internal/cli/hook_cmd.go): `runHook` starts the clock and bounds input and output.
- [`internal/hook/hook.go`](../../internal/hook/hook.go): `WithBudget` and `handleStop` carry the deadline through the adapter.
- [`internal/check/check.go`](../../internal/check/check.go): `RunTurn` enforces the budget.
- [`internal/check/checkpoint.go`](../../internal/check/checkpoint.go): `LockTurn` and `SaveCheckpoint` persist the turn state.
