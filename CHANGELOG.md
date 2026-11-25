# Changelog

All notable changes to Flinch are recorded here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-11-25

First public release. Claude Code, Codex and Antigravity support is in Preview.

### Added

- Hook adapters for Claude Code, Codex and Antigravity, installed once per user with `flinch setup`. Setup shows the files and commands it will change before it writes them, and `flinch setup --remove` detaches them.
- A workspace trust gate: hooks act only where the host trusts the workspace. `flinch enable` and `flinch disable` grant or revoke a clone for Claude Code and Antigravity when the host keeps no record, and `flinch doctor` explains each decision.
- `flinch allow-commands` approves command and script checks defined by a repository, bound to their contents.
- `flinch init --shared --hooks <host>` for teams that commit hook settings to the repository.
- Homebrew and Scoop manifests published with each stable release.
- Same-turn authoring: after a correction, Flinch proves a test the agent already wrote or asks the agent once to add a protection.
- Proofs against the original mistake. A test must fail at the same assertion in two fresh broken trees and pass on the fix together with a legitimate neighbor.
- Test runners for Go, pytest, Vitest and Jest. Static checks for go vet, go build, Ruff, ESLint, golangci-lint, ast-grep and repository commands.
- End-of-turn enforcement that blocks only on findings the turn introduced, with one deadline per hook and a fixed continuation budget.
- Check records under `.flinch/checks/`, checked against the trusted revision so that a deleted or weakened check is reported.
- `flinch doctor`, `pause`, `resume`, `skip`, `report`, `check --ci` and `prove`.

[Unreleased]: https://github.com/angelnicolasc/flinch/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/angelnicolasc/flinch/releases/tag/v0.1.0
