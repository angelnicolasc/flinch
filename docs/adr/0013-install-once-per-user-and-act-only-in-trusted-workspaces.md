# 0013. Install once per user and act only in trusted workspaces

- **Status:** Accepted
- **Implementation:** Implemented
- **Date:** 2026-11-24

## Context

The first install path was `flinch init --hooks <host>` in every clone. The first correction had to wait for onboarding, and the hook settings `init` wrote became project files that every teammate then needed Flinch to use.

Moving the hooks into the user's profile removes that step, but it also changes who decides where Flinch acts. Flinch runs a repository's tests and linters without the host's per-command approval. If a user hook acted everywhere, a repository the developer never trusted could get its code run on their machine. Three facts shaped the answer:
- No host passes its trust verdict to a hook, and Codex loads user hooks even in projects the developer hasn't trusted.
- Each host saves the folders the developer trusted in its own format. Claude Code Desktop and the Antigravity App and IDE can open a folder without saving a record.
- A repository controls its own settings files, so nothing inside the checkout can carry consent.

## Decision

`flinch setup --plan` lists the user files and every hook command before anything changes. The developer confirms once, either interactively or by telling the agent to run `--yes`. User hooks point at one stable executable.

Every event goes through a read-only gate before Flinch opens project settings, takes a lock or writes a Git object. The event must identify one Git checkout, the clone must not be paused, and the host's own record or the developer's explicit grant must cover it:

| Host | What authorizes the event |
| --- | --- |
| Claude Code | A saved trusted project for the clone's main checkout, in any spelling Claude writes. Or a Desktop session (`CLAUDE_CODE_ENTRYPOINT=claude-desktop`) whose `CLAUDE_PROJECT_DIR` belongs to the same clone. Or `flinch enable` in the clone. |
| Codex | A `trusted` entry for the event's exact directory, found in Codex's own lookup order. A saved profile can revoke it. Managed configuration, requirements, managed preferences or a cached organization policy keep the hook inactive. |
| Antigravity | A saved trusted workspace for the checkout, or `flinch enable` in the clone. |

The clone grant lives in the Git common directory, which a clone never receives from its remote. Codex always decides for itself, and the grant never overrides it.

The first active event in a clone shows one line in Claude Code and Codex. Antigravity has no passive channel, so `flinch doctor` reports it there. A command or script check defined by the repository needs its own approval with `flinch allow-commands`, bound to its contents. `flinch pause`, `FLINCH=off` and `flinch setup --remove` turn Flinch off for a clone, a session or the user. [Hosts and workspace trust](../hosts.md)

## Alternatives considered

- **Keep `flinch init` in every clone.** It repeats onboarding in each repository and writes hook files that the team then has to share.
- **Ask once per repository.** The host already asked whether to trust the folder. A second question adds a step to every clone and trains developers to click through it.
- **Act in every session the host starts.** Claude Code also runs user hooks where trust was withheld: under `--trust-workspace false`, in an untrusted VS Code folder, or in a session limited to user settings. Codex runs them in untrusted projects.
- **Trust the Desktop marker alone.** The variable identifies the session, not the folder, and it survives a `cd` into another checkout. It counts only for the clone of the session's project directory.
- **Reconstruct Codex's effective trust in full.** Session flags never reach the hook, and a policy that Codex fails to save to disk is invisible. Requiring the full reconstruction would leave Flinch inactive for every Codex user. Flinch follows the developer's saved trust and documents the gap.
- **Distribute catalog plugins.** Each host has its own packaging and admission rules, and some exclude plugins with lifecycle hooks.

## Consequences

After setup, a trusted repository needs no step at all, and revoking trust in the host takes effect on the next event. The Antigravity App and IDE, headless Claude sessions and independent checkouts entered from a Claude Desktop session need `flinch enable` once. When a trust signal is missing or unreadable, the hook does nothing and `flinch doctor` explains why. The desktop behavior of each signal is verified per host build, including whether a child `claude -p` session can be told apart from its Desktop parent. [Trust model and operating limits](../limitations.md)

## In the code

- [`internal/usersetup`](../../internal/usersetup/): `Prepare` builds the reviewed plan, and `Apply` writes it under a lock and rolls back on failure.
- [`internal/workspacetrust`](../../internal/workspacetrust/): `Evaluate` is the read-only gate, and `SetCloneEnabled` writes the clone grant.
- [`internal/hook/hook.go`](../../internal/hook/hook.go): `Handle` runs the gate before any state, and `handleTurnStart` shows the activation notice.
- [`internal/runner/command_trust.go`](../../internal/runner/command_trust.go): approval for repository commands, bound to their contents.
