# Contributing to Flinch

Thanks for helping. Bug reports, reproductions and focused pull requests are all welcome. For anything larger than a fix, open an issue first so the approach can be agreed before you write the code.

## Build and test

You need Go 1.26 and Git on your `PATH`.

```sh
go build ./...
go vet ./...
go test -count=1 ./...
```

Some integration tests drive real tools: pytest, Ruff, Node.js, Vitest, Jest, ESLint, ast-grep and golangci-lint. Without a tool, its tests skip. To run them, install the tools and point Flinch at the JavaScript ones and golangci-lint:

```sh
export FLINCH_REAL_VITEST=/path/to/node_modules/.bin/vitest
export FLINCH_REAL_JEST=/path/to/node_modules/.bin/jest
export FLINCH_REAL_ESLINT=/path/to/node_modules/.bin/eslint
export FLINCH_REAL_AST_GREP=/path/to/node_modules/.bin/ast-grep
export FLINCH_REAL_GOLANGCI_LINT=/path/to/golangci-lint
```

Set `FLINCH_REQUIRE_TOOLS=1` to turn a missing tool into a failure instead of a skip.

CI installs the pinned versions listed in [`.github/workflows/ci.yml`](.github/workflows/ci.yml) and runs the suite on Linux and Windows with `FLINCH_REQUIRE_TOOLS=1`. The job also fails if any integration test listed in [`.github/required-tests.txt`](.github/required-tests.txt) is missing, skipped or failing. When you add an integration test, add it there.

The fuzz tests' seeds run as ordinary tests. To explore further, run one target, for example `go test ./internal/gitutil -run '^$' -fuzz '^FuzzLocalPath$' -fuzztime 60s`. Every Monday, [`.github/workflows/weekly.yml`](.github/workflows/weekly.yml) fuzzes each target, runs the suite with the latest release of each tool, and installs the latest Flinch release through every documented channel.

## Where things live

| Package | Responsibility |
|---|---|
| `cmd/flinch` | CLI entry point |
| `internal/hook` | Host adapters for Claude Code, Codex and Antigravity |
| `internal/usersetup` | User hook registration, removal and installation consent |
| `internal/workspacetrust` | Read-only workspace and trust checks shared by hooks and diagnostics |
| `internal/author` | Nominating corrections and reusing the agent's test as a check |
| `internal/prove` | Proving a candidate check against the broken code, the fix and an allowed case |
| `internal/check` | End-of-turn and CI enforcement |
| `internal/runner` | Running native tools with an isolated environment |
| `internal/gitutil` | Snapshots, ref-free proof trees and repository paths |
| `internal/reflex` | Check records under `.flinch/checks/` |

[How Flinch works](docs/design.md) explains how the pieces fit together, and the [decision records](docs/adr/) explain why each piece works the way it does.

## Making a change

- **One logical change per commit.** Each commit builds and passes the tests on its own, so `git bisect` keeps working.
- **Commit subjects** use American English and sentence case, start with an imperative verb and name the observable change. For example, write *Prevent duplicate proposals after a worker retry*, not *Update worker logic*.
- **Branch names** use lowercase and hyphens, such as `fix/deduplicate-deliveries` or `feat/replay-regression-tests`.
- **Bug fixes** come with a regression test that fails before the fix and passes after it.
- **Design changes** update the relevant record in `docs/adr/`, or add a new one with the same sections.
- **Signed commits.** Sign your commits with GPG or SSH so GitHub shows them as verified. `main` accepts only signed commits.

## Pull requests

Describe what changed, why it matters and how you verified it. List only the checks that actually ran. If a trade-off or a known limitation affects the review, say so. CI must pass on both Linux and Windows.

`main` keeps a linear history. Once a pull request is approved and green, the maintainer fast-forwards `main` to its commits, so rebase onto `main` instead of merging it into your branch.

## License

By contributing, you agree that your contributions are licensed under the [Apache License 2.0](LICENSE), the same license as the project.
