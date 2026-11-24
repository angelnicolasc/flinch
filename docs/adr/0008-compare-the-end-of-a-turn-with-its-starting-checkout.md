# 0008. Compare the end of a turn with its starting checkout

- **Status:** Accepted
- **Implementation:** Implemented
- **Date:** 2026-11-24

## Context

CI catches a regression after the agent has handed the task back. A scan at the end of the turn catches it sooner, but it also sees whatever was already broken in the developer's checkout. Blocking on that debt would punish the agent for problems it did not cause. `HEAD` is the wrong baseline too, because the checkout may hold uncommitted work before the first tool call.

## Decision

When the developer submits a prompt, Flinch captures the checkout as a Git tree. At Stop, the checker selects the reviewed checks whose scope intersects the changed paths and compares them against that capture:
- **Static findings** are compared by occurrence: the rule or selector, the path, the enclosing symbol and the normalized expression, plus the count of matching occurrences.
- **A test** triggers a repair only if the selected case passed at the start of the turn and fails now.

If the capture failed, the turn finishes without enforcement. An earlier version fell back to `HEAD` in that case, and uncommitted debt that already existed could block the agent.

## Alternatives considered

- **Line numbers.** They shift when someone adds an import.
- **A count per file.** It stays the same when the agent fixes one occurrence and introduces another.
- **The whole suite at every Stop.** It charges the agent for unrelated work.

## Consequences

Moving an expression a few lines down keeps its identity. Changing its path, symbol or expression can give it a new one. Rebuilding the starting tree spends part of the turn budget when a comparison needs it. The native test still runs in CI after the session closes.

## In the code

- [`internal/check/checkpoint.go`](../../internal/check/checkpoint.go): `CaptureCheckpoint` records the start of the turn.
- [`internal/check/check.go`](../../internal/check/check.go): `RunTurn` selects the checks and compares the results.
- [`internal/finding/diff.go`](../../internal/finding/diff.go): `OccurrenceKey` and `NewFindings` identify new static occurrences.
