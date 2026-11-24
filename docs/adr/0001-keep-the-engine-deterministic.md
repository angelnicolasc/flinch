# 0001. Keep the engine deterministic

- **Status:** Accepted
- **Implementation:** Implemented
- **Date:** 2026-11-24

## Context

Coding agents are probabilistic. If you ask the same model twice, it can read the same correction two different ways. That is fine for writing code. It is a weak basis for telling an agent its turn is not done. A repair request has to point at something another engineer can rerun: a test case, a lint finding or a command's output.

## Decision

The session agent handles interpretation. It reads the correction, writes the fix and proposes a test, rule or instruction. Flinch's Go engine handles everything that has to be repeatable:
- it picks candidate turns from Git changes;
- it runs the repository's own tools;
- it reads their structured output and applies fixed thresholds.

No model call sits between a tool result and a verdict, and the engine makes no network calls of its own.

## Alternatives considered

- **A second model as judge.** It could interpret more corrections and settle borderline failures. It would also add a provider and a per-turn cost. It would put a second opinion where the developer needs a reproducible answer.
- **A shared knowledge curator.** The [knowledge factory](https://www.langchain.com/blog/knowledge-factory) pattern fits procedures that change often. Executable obligations already have a review path in the repository, so Flinch keeps them there.

## Consequences

Every verdict traces back to a selector, a tool result and a code revision. Rerunning it needs the same code, dependencies and tool versions. When a runner is missing or the deadline hits, the attempt ends without credit, and the agent's turn ends normally.

## In the code

- [`internal/author/nominate.go`](../../internal/author/nominate.go): `NominateCorrection` selects rework.
- [`internal/prove/prove.go`](../../internal/prove/prove.go): `Verify` interprets the runs.
- [`internal/check/check.go`](../../internal/check/check.go): `RunTurn` decides whether the host returns a finding to the session.
