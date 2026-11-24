# 0003. Prove every check against the real mistake

- **Status:** Accepted
- **Implementation:** Implemented
- **Date:** 2026-11-24

## Context

Agents are good at writing tests that go green, but a green test is not necessarily a test that catches the bug. A test can pass both before and after the fix and protect nothing. A lint rule can catch the bad call by banning the call outright, including the legitimate one next to it.

SWE-bench grades a fix with two sets of tests: `FAIL_TO_PASS` tests must flip, and `PASS_TO_PASS` tests must keep passing ([grading harness](https://github.com/SWE-bench/SWE-bench/blob/main/swebench/harness/grading.py)). Flinch grades the check itself the same way.

## Decision

A test counts as a check when it meets three conditions:
1. It passes on the fix, and so does a distinct neighboring case.
2. It fails on the original mistake in two separate runs. Each run uses a fresh tree that restores the broken product code and keeps the new tests and fixtures.
3. Both failures land on the same recognized assertion in the selected case, with the same normalized diagnostic.

The verifier finds the assertion lines before running anything. A case can have several assertions, a table row or subtest, and assertions that span several lines. Recognized forms include:
- in Go, `t.Errorf` after a comparison, testify, `reflect.DeepEqual` and `errors.Is`;
- in pytest, `assert` and `pytest.raises`;
- in Vitest and Jest, `expect` chains, including `.rejects` and `.toThrow`.

A failure from fixture setup, or from a helper in another file, does not count.

An earlier version accepted exactly one assertion per case. Ordinary tests check more than one thing, such as the default and then the boundary, so they closed without a check.

A manual candidate can declare `expected_signal` for a custom harness. Both broken runs must match it. If the verifier also recognizes assertion lines, the location rule still applies.

Static analyzers are deterministic, so the verifier runs them once per revision and matches the finding to the incident site. A command guard must show its declared signal on two independent broken runs and pass on the fix. Static checks and guards also run against an allowed example at an in-scope product path.

## Alternatives considered

- **A green run on the fix.** It is cheap, and it credits tests that miss the regression.
- **An exit code alone.** It credits a broken configuration or an unrelated failing case.
- **One assertion per case.** It was simpler, and it excluded ordinary tests.

## Consequences

Every recorded check comes with the case that failed, the line it failed on and the neighbor that passed. A test that passes on the broken code earns no credit. A skip, an ambiguous selector or a failure that cannot be tied to an assertion leaves the proof inconclusive. `flinch doctor` counts the cases that closed as `no_recognized_assertion`. Either way, the test stays in the diff for ordinary review.

## In the code

- [`internal/prove/assertions.go`](../../internal/prove/assertions.go): `findAssertionSites` and `matchFailure` locate the assertions and attribute each failure.
- [`internal/prove/prove.go`](../../internal/prove/prove.go): `verifyTest`, `verifyTestNeighbors`, `verifyLintOrStructure` and `verifyGuard` run each protocol.
