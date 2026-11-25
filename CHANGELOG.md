# Changelog

All notable changes to Flinch are recorded here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- `flinch check --ci --base` reports an error before running checks if the requested revision is missing.

- Stop and CI read direct check records from Git, including IDs with non-ASCII characters. Records stored in archive subdirectories stay outside the catalog.

- `flinch check --ci --base` reports a deleted check record from the PR base and exits with status 1. Previously, CI looked for records in the proposed checkout, so deleting a record also removed it from the integrity check.

## [0.1.2] - 2026-11-25

### Fixed

- `flinch setup` works with Homebrew on Apple Silicon. There Homebrew's prefix, `/opt/homebrew`, is also its own Git checkout, so setup took the installed executable for one inside a checkout and refused it. A formula in Homebrew's Cellar now counts as outside any checkout. Any other path in a checkout, or a Cellar in a repository that isn't Homebrew's, still doesn't.
- Vitest 5 proofs recognize a failed `.rejects` or `.resolves`. Vitest 5 bundles its `expect` code into `vitest` itself, so the failure no longer came from `@vitest/expect`, where Flinch looked for it, and the proof ended inconclusive. Flinch now also recognizes Vitest's own `.rejects` and `.resolves` frames there.
- Proofs work when the temp directory has more than one spelling. Flinch named each proof tree after the temp directory as given, but the tools that ran in it reported files by their real path. On Windows, Jest spells out the 8.3 short names that `TEMP` keeps (such as `RUNNER~1`). On macOS, whose `/var` is a symlink, every tool sees the tree without it. Flinch then couldn't match the case and its failure across the broken and the fixed tree, so Jest proofs on Windows, and Go, Jest, Vitest, Ruff and lint proofs on macOS, ended inconclusive. Proof trees are now created at the temp directory's real path. Flinch also passes test files to Jest as exact paths with `--runTestsByPath`, so a file named like `[id].test.ts` matches itself and nothing else.

## [0.1.1] - 2026-11-25

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

[Unreleased]: https://github.com/angelnicolasc/flinch/compare/v0.1.2...HEAD
[0.1.2]: https://github.com/angelnicolasc/flinch/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/angelnicolasc/flinch/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/angelnicolasc/flinch/releases/tag/v0.1.0
