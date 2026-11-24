# 0005. Let the corrected agent write the check

- **Status:** Accepted
- **Implementation:** Implemented
- **Date:** 2026-11-24

## Context

Right after a correction, the agent that made the fix has everything in context: what the developer said, the code that now works and the project's test conventions. Often it has already added the right test. A separate writer, whether a background job or a second agent, would have to reconstruct all of that. It would also need its own way back into the developer's diff.

## Decision

At the end of a corrective turn, Flinch runs the existing checks first. Then, if the turn was nominated (see [ADR 0006](0006-nominate-deterministically-let-the-agent-decide.md)), it looks in the fix for one of two candidates and tries to prove it ([ADR 0003](0003-prove-every-check-against-the-real-mistake.md)):
- a new test case it can select;
- a configured lint rule that separates the broken code from the fix.

A proven candidate gets a record next to the fix. The Claude Code and Codex adapters return a one-line `systemMessage` that names it.

If the fix contains nothing reusable, the Stop hook asks the same agent, once, to add one protection or reply `flinch skip: <reason>`. The request spells out what makes a test count: a new case that fails at an assertion in its own body, plus a neighbor that still passes. A malformed guidance edit gets at most one formatting repair. Both requests draw on the turn's continuation budget ([ADR 0009](0009-budget-each-hook-and-let-the-turn-finish-when-time-runs-out.md)).

When a proof comes back inconclusive, the test stays in the diff for ordinary review and the attempt closes. Nothing falls back to a line of prose.

## Alternatives considered

- **A background worker.** It could keep going after the session closes. It would need its own editing rights and a channel back to the developer.
- **Asking on every correction.** It would spend another generation even when the fix already contains the test.

## Consequences

The developer reviews the fix, the test and the record as one change. The session agent owns every code edit, and Flinch only verifies. If the developer closes the session in the middle of a request, the outcome can stay unknown.

## In the code

- [`internal/hook/hook.go`](../../internal/hook/hook.go): `handleStop` orders enforcement, nomination, reuse and the authoring request.
- [`internal/author/reuse.go`](../../internal/author/reuse.go): `ReuseFirst` finds candidates, calls the verifier and writes the record after a successful proof.
