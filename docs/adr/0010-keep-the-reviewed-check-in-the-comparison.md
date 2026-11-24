# 0010. Keep the reviewed check in the comparison

- **Status:** Accepted
- **Implementation:** Implemented
- **Date:** 2026-11-24

## Context

An agent facing a red test has two ways to turn it green: fix the code or weaken the test. Weakening the test is a form of reward hacking. The agent could also drop a check altogether: if Flinch loaded records from the working tree, the patch would only have to delete one.

## Decision

At turn end, Flinch loads check records from the trusted Git revision (`HEAD` by default), not from the working tree, so a deleted or edited record is still seen. For each record, it compares two things with the reviewed version:
- the record itself;
- the protected region of its artifact: the named subtest or table row in Go, or the region the selector identifies in other formats.

It also scans added lines for suppression directives such as `//nolint`, `eslint-disable` and `# noqa`. A change to a protected case goes back to the agent like any other finding, within the continuation budget.

When a finding is wrong, the developer records why with `flinch report <id> --at <path:line> --reason "<explanation>"`.

## Alternatives considered

- **Comparing whole test files.** It flags unrelated edits and makes adding coverage harder.
- **Trusting the latest exit code.** It loses the difference between fixing the code and loosening the assertion.

## Consequences

Within a turn, an agent cannot quietly delete or weaken a protected case. Deliberate changes go through ordinary review, where the team decides. The comparison covers the record and the selected region. Changes to shared helpers and test-discovery settings still need the team's usual review.

## In the code

- [`internal/check/check.go`](../../internal/check/check.go): `loadTrustedReflexes` reads records from the trusted revision.
- [`internal/check/integrity.go`](../../internal/check/integrity.go) and [`integrity_case.go`](../../internal/check/integrity_case.go): `CheckIntegrity` and `artifactCaseIntact` compare the working changes.
- [`internal/check/checkpoint.go`](../../internal/check/checkpoint.go): `HasSuppressionDirective` detects suppressions.
- [`internal/cli/hook_cmd.go`](../../internal/cli/hook_cmd.go): `runReport` records the developer's explanation.
