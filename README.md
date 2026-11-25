# Flinch

Your agent makes a mistake and you correct it. A week later, another session makes the same mistake.

Flinch turns the correction into a test or lint rule in your repository and proves it fails on the original mistake before keeping it. From then on, it runs that check at the end of every turn that touches the same code. The check lives in your own test suite or lint config, so it also runs in CI and in clones that never installed Flinch. Flinch ships hook adapters for Claude Code, Codex and Antigravity.

## Install

Paste this into Claude Code, Codex or Antigravity:

```text
Install Flinch for my user account using docs/install.md from
https://github.com/angelnicolasc/flinch. Select the release archive for my OS
and architecture, verify its SHA-256, and install the binary at a stable path.
Run flinch setup --plan and show me its complete output. Wait for my
confirmation before running flinch setup --yes. Then run flinch doctor.
```

Setup installs user hooks once. From then on, Flinch acts only in repositories your agent already trusts. Where a host keeps no trust record, such as the Antigravity App, run `flinch enable` once in that clone. The first time Flinch acts in a repository, Claude Code and Codex show one line: `flinch: active in this repo · flinch pause to turn it off`. [Hosts and workspace trust](docs/hosts.md)

With Homebrew or Scoop:

```sh
brew install angelnicolasc/tap/flinch
flinch setup
```

```powershell
scoop bucket add angelnicolasc https://github.com/angelnicolasc/scoop-bucket
scoop install angelnicolasc/flinch
flinch setup
```

`flinch setup` shows the files and hook commands it will write and asks before it changes anything. Every release publishes SHA-256 checksums next to its archives, and [docs/install.md](docs/install.md) shows how to verify them. Teams that want the hook settings committed to the repository can use `flinch init --shared --hooks <host>`.

## Why

- Agents repeat mistakes across sessions, branches and teammates.
- A line in `AGENTS.md` is context the model weighs, but nothing checks that the next agent follows it.
- A regression test protects you only if it fails on the bug, and a green test doesn't tell you that.

## What lands in your diff

Suppose your agent treats a retry delay of zero as "not set." You tell it that zero means retry immediately. It fixes `src/retry.ts` and adds a test case. At the end of the turn, Flinch does three things:

1. It restores the original code in two fresh trees and sees the new case fail at its assertion both times.
2. It checks that the case and an existing neighbor pass on the fix.
3. It writes a short record next to the fix:

```yaml
# .flinch/checks/retrydelay-treats-zero-as-immediate-5c732ef5565e0251.yml
id: retrydelay-treats-zero-as-immediate-5c732ef5565e0251
version: 1
scope: '{src/retry.ts,tests/retry.test.ts}'
rung: test
obligation: Preserve behavior checked by retryDelay › treats zero as immediate.
artifact:
  tool: vitest
  path: tests/retry.test.ts
  selector: retryDelay › treats zero as immediate
cases:
  allowed: retryDelay › uses the default when omitted
surfaces:
  pull_request:
    mode: block
  agent_turn:
    mode: repair_if_supported
```

Review the test and the record together with the fix. If any later session changes `src/retry.ts` and brings the bug back, Flinch runs that case at Stop and sends the agent back with the failure before it hands you the turn. `npm test` catches it in CI either way.

Most turns show you nothing. When Flinch proves a test the agent already wrote, Claude Code and Codex get a one-line message that names the check. When the fix contains nothing reusable, Flinch asks the agent once to add a protection, or to reply `flinch skip: <reason>` if the correction was a one-off.

## What counts as a check

- **Proof.** Tests and command guards fail on the original mistake in two independent runs and pass on the fix, along with a legitimate neighbor. Static analyzers are deterministic, so they run once per revision.
- **Runners.** Go tests, pytest, Vitest and Jest report results per case. A test can contain several assertions, but both broken runs must fail at the same assertion with the same diagnostic. Go vet, Go build, Ruff and ast-grep provide the static rungs.
- **End of turn.** Only problems the turn introduced can block it. A test must pass at the start of the turn and fail now. Static findings are compared by rule, path, symbol and expression. If Flinch can't tell, or runs out of time, the turn finishes.
- **Guidance.** A single line in a project guide the host already loads counts as its own outcome, never as a check.

## Design decisions

Each decision has an ADR that covers the alternatives Flinch rejected and what the choice costs.

- **No model decides a verdict.** The agent writes the fix and proposes the check. Flinch's Go engine runs your tools and applies fixed rules, so anyone can rerun a verdict. ([ADR 0001](docs/adr/0001-keep-the-engine-deterministic.md))
- **Checks are graded the way SWE-bench grades fixes.** A check counts only if it fails on the original mistake in two fresh trees, passes on the fix, and leaves a legitimate neighbor passing. ([ADR 0003](docs/adr/0003-prove-every-check-against-the-real-mistake.md))
- **Proofs never touch your staging.** Replays run in temporary trees built from Git objects. They create no commits or refs and leave your index alone. ([ADR 0004](docs/adr/0004-run-proofs-in-ref-free-trees.md))
- **Only new problems block.** Each turn is compared with the checkout it started from, by occurrence rather than by line number, so existing debt never stops the agent. ([ADR 0008](docs/adr/0008-compare-the-end-of-a-turn-with-its-starting-checkout.md))
- **Every hook runs on one deadline.** Stop gets 30 seconds and a turn gets at most two continuations. When time runs out, the turn finishes. ([ADR 0009](docs/adr/0009-budget-each-hook-and-let-the-turn-finish-when-time-runs-out.md))
- **A weakened check still shows up.** Records load from the reviewed revision, so deleting or loosening a protected test during a turn becomes a finding. ([ADR 0010](docs/adr/0010-keep-the-reviewed-check-in-the-comparison.md))
- **One engine, three hosts.** Claude Code, Codex and Antigravity share the same proof and enforcement. Each adapter only translates its host's events. ([ADR 0012](docs/adr/0012-share-the-engine-and-adapt-to-each-hosts-turn-lifecycle.md))
- **Install once, act only where trust exists.** Setup installs user hooks after one review. Before it reads project settings, every hook checks the host's saved trust for the workspace, or a `flinch enable` grant for the clone. A repository can never grant itself access. ([ADR 0013](docs/adr/0013-install-once-per-user-and-act-only-in-trusted-workspaces.md))

## Commands

```sh
flinch setup                  # review and install the user hooks; --remove detaches them
flinch doctor                 # hooks, workspace trust and discovered tools for this checkout
flinch enable                 # allow Claude Code and Antigravity in this clone; flinch disable revokes it
flinch allow-commands         # approve the repository's own command checks after reviewing them
flinch pause                  # turn Flinch off in this clone; flinch resume turns it back on
flinch prove --spec candidate.yml --json
flinch check --ci             # run the reviewed checks in CI
flinch report <id> --at src/retry.ts:42 --reason "Zero is valid here"
```

Set `FLINCH=off` to turn Flinch off for a single session.

Checkpoints, logs and local measurements live under the Git directory. Your working tree only gains the check and its record.

## Documentation

- [Install Flinch](docs/install.md): release archives, checksums, package managers and removal.
- [Hosts and workspace trust](docs/hosts.md): what setup installs in each host and when its hooks act.
- [How Flinch works](docs/design.md): the loop, all thirteen decisions and what Flinch doesn't do.
- [Architecture decision records](docs/adr/): the context, alternatives and consequences of each decision.
- [Trust model and operating limits](docs/limitations.md): what Flinch runs on your machine, and with what access.

## Feedback

If you try Flinch, open an issue with the correction, the check Flinch chose and whether it caught the next occurrence. To propose a change, read [CONTRIBUTING.md](CONTRIBUTING.md). To report a vulnerability, follow [SECURITY.md](SECURITY.md).

## License

Flinch is licensed under the [Apache License 2.0](LICENSE).
