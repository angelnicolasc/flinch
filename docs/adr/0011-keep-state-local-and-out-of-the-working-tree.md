# 0011. Keep state local and out of the working tree

- **Status:** Accepted
- **Implementation:** Implemented
- **Date:** 2026-11-24

## Context

A teammate reviewing a check needs the assertion, the selector and the legitimate neighbor. The runtime on the author's laptop varies with load and tells them nothing. The first records stored that runtime, the proof date and the runner version, so every new proof produced a diff nobody needed. Turn checkpoints also hold session identifiers that have no business in a team diff.

## Decision

Flinch keeps two kinds of state, in two places:
- **Reviewed state** lives in `.flinch/checks/<id>.yml`, which holds the stable contract: tool, file, selector, neighbor, scope and surfaces. Proving a check again keeps the file's bytes, comments included. `flinch init` writes only the settings that differ from the defaults.
- **Local state** lives under `flinch/` in the Git common directory, which linked worktrees share. It covers checkpoints, event logs, proof leases and measurements. Each local proof summary is bound to a fingerprint of the check and its artifact. Snapshots are Git objects, and replays run in the system temp folder.

By default, a checkpoint stores a hash of the prompt, not its text. Keeping redacted text requires opting in.

Tool caches stay out of the checkout too:
- pytest runs with its cache provider off;
- Ruff, Vitest and Jest run without a cache;
- golangci-lint reuses `flinch/golangci-lint` under the user cache directory.

By default, retention cleanup removes checkpoints and log entries older than 30 days, along with idle proof trees.

## Alternatives considered

- **The latest report beside each test.** It changes tracked files every time a different machine runs the test.
- **A hosted event store.** It needs an account and a data-transfer policy for information the local hooks already have.

## Consequences

After a turn, `git status` shows the developer's work, the check and its record, and nothing else. Teammates who clone the repository get the contract, not the author's timings. Clearing Flinch's local state loses history, but nothing the team reviewed.

## In the code

- [`internal/gitutil/git.go`](../../internal/gitutil/git.go): `FlinchStateDir` resolves the Git common directory.
- [`internal/check/checkpoint.go`](../../internal/check/checkpoint.go): `HashPrompt` applies the prompt policy.
- [`internal/reflex/reflex.go`](../../internal/reflex/reflex.go) and [`localproof.go`](../../internal/reflex/localproof.go): split the reviewed contract from local measurements.
- [`internal/runner/env.go`](../../internal/runner/env.go): gives each tool run its own scratch directory and cache settings.
