# Flinch

Flinch is a Go engine and CLI for checking whether a repository test or lint rule catches a regression. It can compare the broken and fixed trees and record the result for a Git revision. The current CLI exposes the check commands directly. Automatic capture and delivery through agent sessions are planned.

## Status

This repository currently implements the deterministic Go core engine and CLI:

- **Repository contracts**: `.flinch/flinch.toml` policy configuration, `.flinch/reflexes/<id>.yml` protection records, and `.flinch/rejected.yml` scoped rejections.
- **Isolated verification (`flinch prove`)**: evaluates candidate tests (`go test -json`, `pytest` via JUnit XML), linters/analyzers (`go vet -json`, `go build`, `ruff`, `eslint`, `golangci-lint`, `ast-grep`, and repository-native commands), and guards over isolated Git worktrees without modifying the working tree or Git index.
  - Test proofs run on the fixed tree once and on the broken tree (with product files reverted and test/support files preserved) twice, classifying `verified`, `refuted`, `compile_only`, `skipped_on_broken`, `flaky`, and `inconclusive`.
  - Lint and structure proofs match findings by rule/selector, enclosing symbol, and normalized expression context, track file renames and pure-insertion fixes, and validate in-scope allowed near-miss cases.
- **Enforcement and activation accreditation (`flinch check`)**:
  - `--turn`: diffs against turn checkpoints by occurrence identity so pre-existing debt does not block, checks reflex integrity and newly added suppression pragmas against a trusted revision, and enforces unified deadlines and continuation budgets.
  - `--ci`: evaluates active deterministic protections across the repository or relative to `--base <ref>`, outputting `text`, `json`, `github`, or `sarif`.
  - `--accredit`: exercises supported lint and command controls in a disposable checkout of the selected revision. It records clean execution, negative-control attribution, and any configured allowed cases. The caller supplies the CI event, job, run ID, and required-check status. In this version, an `active` CI record relies on those inputs. Independent confirmation from the CI provider and accreditation for Go and pytest test selectors are planned.
- **Durable local queue and diagnostics**: `flinch signal`, `flinch queue`, `flinch checkpoint`, `flinch init`, `flinch doctor`, and `flinch stats` (telemetry-free local logs with explicit denominators and observed scope).

### Planned next

- Host adapters for Claude Code, Codex, and OpenCode (`SessionStart`, `UserPromptSubmit`, `Stop`, and OpenCode session plugins).
- Autonomous local/team worker loop for candidate synthesis and idempotent Git/PR delivery.
- End-to-end two-clone transfer trials and retrospective benchmark evaluation.

## Build and test

Requires Go 1.26+ and Git on `PATH`.

```sh
go build ./cmd/flinch
go test -count=1 ./...
# With CGO enabled (for example on Linux/macOS):
go test -race -count=1 ./...
```

## Quick CLI overview

```sh
flinch init
flinch doctor
flinch prove --spec candidate.yml --json
flinch checkpoint --session demo --prompt "Limit response body reads"
flinch check --turn --session demo
flinch check --ci --format github
flinch check --accredit --ref HEAD --surface ci
flinch stats --json
```
