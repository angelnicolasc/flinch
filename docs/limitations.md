# Trust model and operating limits

User setup grants Flinch permission to run the repository's tests and linters with your user account in an authorized checkout. The host's per-command approval does not apply to those child processes. Flinch's hook engine makes no network requests. A test or linter can use the network and access files available to your account.

## Workspace trust

Flinch checks authorization on every event before reading project settings, creating Git objects, or writing state. Missing consent, an unknown or ambiguous checkout, a clone pause, and `FLINCH=off` all leave the hook inactive. `flinch doctor` reports the current directory and `flinch doctor --all` also lists checkouts where a user hook previously captured a turn.

| Host | What authorizes the event |
| --- | --- |
| Claude Code | A positive saved trust entry for the main checkout, a Desktop session whose project directory belongs to the event's clone, or an explicit clone grant. Registered linked worktrees share the clone. An independent nested repository needs its own authorization. |
| Codex | A positive user decision for the workspace, subject to system settings and saved profile revocations. Managed configuration, system requirements, managed preferences, a cached organization policy, or failure to inspect those sources disables activation. |
| Antigravity | A saved `trustedWorkspaces` entry for the checkout or an explicit clone grant. The App and IDE can lack a saved entry. Activity is visible in `flinch doctor`. |

After reviewing a clone, run `flinch enable` there to grant access to Claude Code and Antigravity. `flinch disable` removes the grant, but trust saved in the host still applies. A clone pause takes precedence over both.

### Claude Code Desktop

The Desktop marker identifies the session, not the folder. It counts only when the event's checkout belongs to the same clone as the session's `CLAUDE_PROJECT_DIR`, so a `cd` into an independent checkout leaves that checkout inactive. A child `claude -p` process inherits the marker and has its own project directory. When both refer to the same clone, the Desktop check can activate the child. Whether a child session can be told apart from its parent is still being verified on Desktop builds. [Claude hook working directories](https://code.claude.com/docs/en/hooks#reference-scripts-by-path)

### Codex

With the default `project_root_markers = [".git"]`, Flinch looks for a saved decision in the event directory, the checkout root and the main checkout of a verified linked worktree, in that order. Only the base user configuration can grant access. A more specific system denial still applies unless the user overrides that same key. A saved profile can veto a grant. With custom markers, including an empty list, Flinch requires trust for the exact event directory. If a path alias prevents Flinch from verifying the same checkout ancestry, it also uses only the event directory. [Codex configuration layers](https://github.com/openai/codex/blob/3d86e43b075adeba2193ec5c22ac92f55afd82e6/codex-rs/config/src/loader/mod.rs#L345-L390) · [Workspace decision](https://github.com/openai/codex/blob/3d86e43b075adeba2193ec5c22ac92f55afd82e6/codex-rs/config/src/loader/mod.rs#L1124-L1149)

For each directory, Codex 0.172.0 tries the canonical path before the original spelling. Each key uses an exact match first. On Windows the lexicographically smallest key that matches under ASCII case folding wins otherwise. [Codex path lookup](https://github.com/openai/codex/blob/3d86e43b075adeba2193ec5c22ac92f55afd82e6/codex-rs/config/src/loader/mod.rs#L1621-L1659)

A directory between the event directory and the checkout root can have its own project configuration layer. If that directory contains a `.codex` directory and has a saved denial, Flinch leaves the hook inactive under an inherited grant. An empty `.codex` directory also counts. Flinch checks directory metadata and gives an exact event-directory decision precedence. [Codex project decision](https://github.com/openai/codex/blob/3d86e43b075adeba2193ec5c22ac92f55afd82e6/codex-rs/config/src/loader/mod.rs#L423-L441) · [Project layer discovery](https://github.com/openai/codex/blob/3d86e43b075adeba2193ec5c22ac92f55afd82e6/codex-rs/config/src/loader/mod.rs#L1889-L1923)

Flinch reads organization policy from the copy Codex saves on disk, under the active Codex home. A cached configuration or requirements fragment disables Flinch, even when it names the same key as a user grant. A nonempty bundle cached from a previous policy server keeps Flinch inactive too. If Codex applies a policy it could not save, Flinch can't see it. In that case, when the cache is missing or still holds an empty bundle, Flinch follows the user's saved trust. [Codex cache publication](https://github.com/openai/codex/blob/3d86e43b075adeba2193ec5c22ac92f55afd82e6/codex-rs/cloud-config/src/service.rs#L382-L406)

| User trust | Session opens in | Result when other checks pass |
| --- | --- | --- |
| Main checkout only | A subdirectory or a registered linked worktree | Active with default project markers. |
| Trusted main checkout and an untrusted worktree | That worktree or a subdirectory without its own decision | Inactive. |
| Trusted worktree and an untrusted event directory | That event directory | Inactive. |
| Exact directory, with a cached organization policy | That directory | Inactive, including when both layers use an identical key. |
| Repository root | That root | Active. |

A hook receives neither one-session flags nor the effective profile selected only for that session. Flinch follows persisted trust. To disable Flinch for a stricter session, launch the host with `FLINCH=off`. [Hook input at 0.172.0](https://github.com/openai/codex/blob/3d86e43b075adeba2193ec5c22ac92f55afd82e6/codex-rs/hooks/src/events/user_prompt_submit.rs#L81-L94)

## Local execution and storage

- **Repository commands.** Review command definitions, script files and any programs they call before granting `flinch allow-commands` in the clone. Changing the command or its directly identified script bytes invalidates the grant. Claude Code and Codex show the approval notice once per distinct content in the clone. Put shell expansions and indirect program selection in a named script and invoke it by its literal repository path. For an unregistered proof candidate, use `flinch allow-commands --spec <file>`. Tests, linters and transitive dependencies still execute with your permissions. `flinch doctor` reports pending command approvals even on hosts without a passive message channel.
- **Filesystem links.** Flinch reads the source line for a diagnostic only through the checkout root, so a reported path or link that leaves the checkout is never opened. Other artifact paths are validated lexically. Symlinks, junctions or Windows reparse points can redirect file operations. Keep artifacts, fixtures and state in ordinary directories.
- **Shared settings.** A team can distribute hook definitions through `flinch init --shared --hooks <host>`. Each developer still needs user consent and authorization for the clone. With both registrations present, the host can launch both commands. The shared command yields to the user handler for the matching event, including its host review and filters.
- **Snapshots.** Turn snapshots are ordinary Git objects. Git maintenance can prune unreferenced objects after its configured grace period.
- **Recordings.** Setting `FLINCH_RECORD=1` records active deliveries under `flinch/recordings/` in the Git common directory. Recordings use per-clone aliases for IDs, text and paths. They retain event timing, correlation and decision shape. After a trial, turn recording off, review the JSON before sharing, and remove the trial recordings. Keep the separate private recording key local.
- **Contained failures.** If Flinch itself fails inside a hook, the host receives its ordinary allow response with exit code 0 and the turn continues without Flinch. In an installed setup, Flinch keeps a record of the last such failure in your user configuration directory: the time, the Flinch version, and the host, event and stage, each from a fixed list. It never stores the event's content, an error message, a path or a session ID. `flinch doctor` shows the record.
