# Changelog

All notable changes to Flinch are recorded here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Hook adapters for Claude Code, Codex and Antigravity, installed with `flinch init --hooks <host>`.
- Same-turn authoring: after a correction, Flinch proves a test the agent already wrote or asks the agent once to add a protection.
- Proofs against the original mistake. A test must fail at the same assertion in two fresh broken trees and pass on the fix together with a legitimate neighbor.
- Test runners for Go, pytest, Vitest and Jest. Static checks for go vet, go build, Ruff, ESLint, golangci-lint, ast-grep and repository commands.
- End-of-turn enforcement that blocks only on findings the turn introduced, with one deadline per hook and a fixed continuation budget.
- Check records under `.flinch/checks/`, checked against the trusted revision so that a deleted or weakened check is reported.
- `flinch doctor`, `pause`, `resume`, `skip`, `report`, `check --ci` and `prove`.
