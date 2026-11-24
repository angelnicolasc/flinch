# Contributing to Flinch

Thanks for helping. Bug reports, reproductions and focused pull requests are all welcome. For anything larger than a fix, open an issue first so the approach can be agreed before you write the code.

## Build and test

You need Go 1.26 and Git on your `PATH`.

```sh
go build ./...
go vet ./...
go test -count=1 ./...
```

Some regression tests drive real Python and JavaScript runners. They skip when a tool is missing. To run them, install pytest, Ruff, Node.js, Vitest and Jest, and point Flinch at the JavaScript binaries:

```sh
export FLINCH_REAL_VITEST=/path/to/node_modules/.bin/vitest
export FLINCH_REAL_JEST=/path/to/node_modules/.bin/jest
```

CI installs the versions it tests against, listed in [`.github/workflows/ci.yml`](.github/workflows/ci.yml), and runs the suite on Linux and Windows.

## Where things live

| Package | Responsibility |
|---|---|
| `cmd/flinch` | CLI entry point |
| `internal/hook` | Host adapters for Claude Code, Codex and Antigravity |
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
