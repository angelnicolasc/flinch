# 0006. Nominate deterministically, let the agent decide

- **Status:** Accepted
- **Implementation:** Implemented
- **Date:** 2026-11-24

## Context

"Keep the zero" could be a correction to a default, a formatting preference or a feature request. The words alone don't say which. A phrase list would also have to learn every team's vocabulary, in every language. Git can answer a narrower question without a model: did this turn rewrite something the previous turn wrote?

## Decision

Flinch compares, file by file, the lines the previous turn added with the lines the current turn removed:
- Replacing or deleting one of those lines counts. Adding a line next to it does not.
- Renames are followed back to the old path.
- If the previous turn's end snapshot differs from this turn's start snapshot, the file must have the same bytes at that boundary before line positions are compared.

The first version counted any change within three lines of the previous turn's work. Routine additions, such as a new case next to an old one, were nominated alongside real corrections. Counting only replacements and deletions keeps routine additions out.

One overlap nominates the turn. Flinch first tries a check that is already in the fix. If it asks the agent to write one, the agent decides whether the rework was a reusable correction and can skip it.

## Alternatives considered

- **Overlap by file or function.** It catches more, including ordinary work on the same function.
- **A phrase list.** Each team would have to teach Flinch its vocabulary.
- **A mandatory correction tag.** Every developer would have to classify every prompt.

## Consequences

Every nomination points to a concrete edit a reviewer can inspect, and it works the same in any language. A correction made entirely in a different file, or on a line shifted by an intervening edit, goes unseen. A nomination only opens the opportunity. Registering a check still takes a proof.

## In the code

- [`internal/author/nominate.go`](../../internal/author/nominate.go): `NominateCorrection` and `hunksOverlap` compare the snapshots.
- `TestNominationRequiresReplacingPreviouslyAddedLines`: separates an insertion next to earlier work from a rewrite of that work.
