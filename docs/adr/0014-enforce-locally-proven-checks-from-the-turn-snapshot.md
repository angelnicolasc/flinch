# 0014. Enforce locally proven checks from the turn snapshot

- **Status:** Accepted
- **Implementation:** Implemented
- **Date:** 2026-11-25

## Context

A developer can correct an agent and ask for another edit before committing the fix. Loading only the trusted revision leaves the newly proven case unprotected during that edit. Loading the working copy at Stop would let the edit remove the case that should detect the regression.

## Decision

At Stop, Flinch first loads the records from the trusted revision as described in [ADR 0010](0010-keep-the-reviewed-check-in-the-comparison.md). It also reads the records present in the snapshot taken when the developer submitted the prompt. A record found only there qualifies if the clone has a verified proof for the same contract and the protected case is intact relative to the verified revision. As in ADR 0010, this comparison covers the case and its test skeleton. Shared helpers outside that region can change independently. Text with LF and CRLF line endings has the same local proof identity.

Each ID runs at most once. The trusted revision takes precedence when it contains the ID. For a checkout-only check, the opening snapshot supplies the record and protected case for the integrity comparison. The normal scope, test budget and comparison with the opening checkout still apply.

The local receipt records the revision used to verify the artifact. The local proof store indexes verified receipts by stable record so edits to a sibling case can retain the original proof. A receipt that predates the index still qualifies if it identifies the verified revision and matches the record and artifact from that revision. Receipts without a verified revision supply runtime measurements only.

## Consequences

The next turn can detect a regression or a change to the protected case before the developer commits. Discarding the uncommitted record before submitting a prompt removes it from that turn's snapshot. A linked worktree needs the matching record and artifact in its own snapshot to use the proof.

A checkout-only proof affects interactive turns. Committing the record, test and fix makes the check available to other clones and CI through the repository.
