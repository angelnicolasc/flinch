# 0004. Run proofs in ref-free trees

- **Status:** Accepted
- **Implementation:** Implemented
- **Date:** 2026-11-24

## Context

The agent usually finishes a fix before anyone stages or commits it. `HEAD` may still hold the bug, and the index may hold an older draft. Each replay needs the exact bytes the agent just tested, in a clean directory. The developer's staging area, branch and signing setup must stay untouched.

## Decision

Flinch builds a temporary index from the real index's tracked entries and sparse-checkout flags. Then it runs `git add -A` and [`git write-tree`](https://git-scm.com/docs/git-write-tree) against it, while the developer's own index stays exactly as it was. For each replay, `git read-tree` and `git checkout-index` write the tree into a private directory under the system temp folder. No commit, ref or linked worktree is created.

Three details came from runs that went wrong:
- **Ignored tracked files.** An empty temporary index dropped tracked files that also match `.gitignore`. Seeding it from the real index keeps them.
- **Same-size edits.** A copied index carries file timestamps, so a same-size edit made within one clock tick could resolve to the staged bytes. Flinch keeps the entries and drops the cached stat data, so Git rereads the file.
- **Jest discovery.** Jest's crawler skips everything under `.git/`, so proof trees inside the Git directory found no tests. They now live in the system temp folder, where Jest discovers them with the project's own settings.

Before exposing a proof directory, Flinch takes an exclusive lease in the Git common directory and holds it until cleanup. `flinch doctor --prune` only reclaims directories whose lease is free. A proof that is still running keeps its tree.

## Alternatives considered

- **A temporary commit with a linked checkout.** It adds signing and ref updates to every replay, and some repositories reject both through hooks or policy.
- **A plain directory copy.** It needs its own ignore logic and drags installed dependencies into every proof.

## Consequences

Flinch can prove an uncommitted fix while another session works in the same clone. Each replay costs one checkout of the tree. The developer's checkout, staging and history stay untouched.

## In the code

- [`internal/gitutil/snapshot.go`](../../internal/gitutil/snapshot.go): `CaptureWorkingTreeSnapshot` captures the tree.
- [`internal/gitutil/worktree.go`](../../internal/gitutil/worktree.go): `CreateWorktree`, `Cleanup` and `PruneProofTree` manage each replay directory.
- [`internal/localstate/`](../../internal/localstate/): `TryLock` provides the lease on Unix and Windows.
