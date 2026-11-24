# 0012. Share the engine and adapt to each host's turn lifecycle

- **Status:** Accepted
- **Implementation:** Implemented
- **Date:** 2026-11-24

## Context

Claude Code, Codex and Antigravity all expose lifecycle hooks, each with its own contract:
- Claude Code and Codex fire an event when the developer submits a prompt.
- Antigravity fires `PreInvocation` before every model call, so one human request can produce several.
- At Stop, each host has its own way to send the agent back to work and to show a message.

Two details came up while building the adapters. Antigravity ends a normal turn with `model_stop`. The first adapter waited for a different value and never enforced anything. Codex sends a block reason back to the model as new input. The adapter has to treat that turn as machine-originated, not as a new human request.

## Decision

One Go engine handles all proof and enforcement. Each adapter identifies the session, opens the turn checkpoint and translates the engine's answer into the host's format. `flinch init --hooks <host>` installs the adapter in the project's hook settings and keeps existing entries, including a customized or disabled Antigravity group.

| Host | Sending the agent back at Stop | Notice for a proven check already in the fix |
| --- | --- | --- |
| Claude Code | Returns `decision: block` with a reason. | One `systemMessage`. [Claude Code hooks](https://code.claude.com/docs/en/hooks#stop-decision-control) |
| Codex | Returns `decision: block` with a reason. Codex appends it and resumes the active turn. | One `systemMessage`. [Codex hooks](https://learn.chatgpt.com/docs/hooks) · [Codex turn loop](https://github.com/openai/codex/blob/581161ab41e643d942e1e1b5f561034060bf18e3/codex-rs/core/src/session/turn.rs#L833-L866) |
| Antigravity | Returns `decision: continue` with a reason, on an idle `model_stop` with no error. | Recorded locally. [Antigravity Stop contract](https://antigravity.google/docs/hooks?tab=ide#stop) |

## Alternatives considered

- **Three engines.** The rules for proof, exceptions and budgets would be repeated in three codebases.
- **One universal hook format.** It would miss real differences in timing and message delivery. For example, treating every Antigravity model call as a new turn would move the baseline after the agent had started editing.

## Consequences

A host can change its event format without changing how Flinch proves a check. Repeated Antigravity model calls share the human turn's checkpoint. A Flinch continuation keeps its turn's budget, even when the host presents it as prompt input. The adapter tests replay each host's documented payloads and check the exact response.

## In the code

- [`internal/hook/hook.go`](../../internal/hook/hook.go): `Handle`, `handleTurnStart` and `handleStop` translate lifecycle events, and `continueResponse` writes each host's decision.
- [`internal/hook/install.go`](../../internal/hook/install.go): `InstallHooks` merges the settings.
- [`internal/cli/hook_cmd.go`](../../internal/cli/hook_cmd.go): the `flinch hook` entry point.
