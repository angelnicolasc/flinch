# Changelog

All notable changes to Flinch are recorded here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- A failure inside a hook no longer reaches the host as an error. A panic used to exit with status 2, which Claude Code and Codex treat as a request to block the prompt or keep the agent working. The hook now answers with the host's allow response and exit code 0, sends its answer at most once, and records the last contained failure for `flinch doctor`.
- Flinch no longer reads files outside the checkout when it adds a source line to a diagnostic. A path in a tool's report that leaves the checkout, directly or through a link, is never opened.
- An unreadable linter report counts as a failed run instead of a clean one. If Ruff, ESLint or golangci-lint exits as if it finished but its JSON report has an unexpected shape, a proof is inconclusive and enforcement fails open, so a changed report format can't turn existing debt into new findings.
- golangci-lint v2 works. Flinch detects the installed major version and uses `--output.json.path=stdout` for v2, keeping `--out-format json` for v1.
- Flinch looks for ast-grep only under the name `ast-grep`. On most Linux systems `sg` is a different program, the shadow-utils command that runs a shell under another group.

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
