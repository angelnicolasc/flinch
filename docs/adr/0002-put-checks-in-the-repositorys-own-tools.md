# 0002. Put checks in the repository's own tools

- **Status:** Accepted
- **Implementation:** Implemented
- **Date:** 2026-11-24

## Context

You correct an agent today. Tomorrow a teammate's agent, maybe on another host, makes the same mistake, and the teammate only runs `npm test`. A protection stored in a Flinch-only format never runs there. A protection stored in the project's own test suite runs wherever the suite runs, including CI and clones that never installed Flinch.

## Decision

The assertion stays in an ordinary repository artifact: a test case, a lint rule or a build constraint. Flinch adds a short record in `.flinch/checks/` that names:
- the tool, the file and the selector;
- the legitimate neighbor;
- the turn scope.

The record's ID combines a readable slug with a digest of the test path and the full selector. `TestRetry/zero` in two different packages therefore yields two different checks.

The turn scope covers the test file and the product files the fix changed. For Go, it also covers the other Go files in the test's package. The first version scoped a check to its test directory, so a later change that only touched `src/` never triggered it. Scoping by the fix's product paths closed that gap.

Older records under `.flinch/reflexes/` still load. When both folders hold the same ID, the record in `.flinch/checks/` wins.

## Alternatives considered

**A Flinch rules engine.** It would give uniform diagnostics. It would also make Flinch a requirement for every developer and every CI job that runs the check.

## Consequences

Reviewers see one more test case and a short record next to the fix. A teammate runs it with the usual command. If the protected code later moves to a new file, in-turn enforcement needs a reviewed scope update. The native test keeps running in the suite either way. The team decides which commands run in CI.

## In the code

- [`internal/author/reuse.go`](../../internal/author/reuse.go): `productScope` and `makeReflexID` derive the scope and the ID.
- [`internal/reflex/reflex.go`](../../internal/reflex/reflex.go): reads records and preserves reviewed files.
- `TestProductOnlyRepeatInTurnAndNativeClone`: repeats the mistake through a source-only edit, then runs the project's own pytest or Vitest command in a second clone.
