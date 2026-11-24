# 0007. Choose the strongest rung

- **Status:** Accepted
- **Implementation:** Implemented
- **Date:** 2026-11-24

## Context

Corrections need different kinds of protection. Package visibility can make a bad dependency fail to compile. A regression test can tell an explicit zero from an omitted option. A naming convention may have no failure to observe at all.

Lauren Tan describes how the GrokBot team turns review feedback into safer APIs and lint rules, so the next change cannot repeat the mistake ([*All Code Is Slop (Except Mine)*](https://ai.engineer/talks/O_zcc6Xl12g-all-code-is-slop-except-mine)). Flinch follows the same order: first make the mistake impossible, then detectable, and only then write it down.

## Decision

The authoring request asks for the strongest rung that fits, in this order: a structural change, an existing lint rule, a focused test, then a guard. Automatic reuse recognizes test cases and configured lint findings. The verifier also accepts structure and guard candidates supplied explicitly. Every executable candidate needs incident evidence and an allowed case.

When a convention has no executable fit, the agent can add one concise line to a project guide the host already loads. The line counts as guidance, not as a check, and only under these conditions:
- The guide and its managed section already exist. Flinch never creates an instructions file.
- The host actually loads the guide. Flinch checks each host's instruction entry points, including the Codex root override and Claude Code `@` imports.
- The edit is exactly one insertion inside the section. Removing it restores the previous bytes, line endings included.
- The section stays within 30 lines by default.

An inconclusive executable proof never turns into guidance.

## Alternatives considered

- **A reminder for every correction.** It skips the proof run, and every future session pays for it in context.
- **A script for every convention.** It produces rules that only encode wording preferences.

## Consequences

Guidance is recorded and counted separately from executable checks. It proves the edit and that the host reads the file. Whether the next agent follows it is up to that agent, which is why the line is the floor and not the default.

## In the code

- [`internal/author/guide.go`](../../internal/author/guide.go): `ManagedGuide` resolves the guide each host loads.
- [`internal/author/line.go`](../../internal/author/line.go): `ValidateManagedLine` checks the insertion.
- `TestClaudeCreditsOnlyAnExistingImportedGuide`: shows what a real import changes in the outcome.
