package author

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"

	"github.com/angelnicolasc/flinch/internal/gitutil"
)

// Nomination captures deterministic evidence that a turn reworked code edited in the prior agent turn.
type Nomination struct {
	Nominated          bool     `json:"nominated"`
	Reason             string   `json:"reason,omitempty"`
	OverlappingFiles   []string `json:"overlapping_files,omitempty"`
	OverlappingSymbols []string `json:"overlapping_symbols,omitempty"`
	BrokenRef          string   `json:"broken_ref,omitempty"`
	FixedRef           string   `json:"fixed_ref,omitempty"`
	// Keyed by the current path, including when a correction renames the file.
	overlaps map[string][]gitutil.DiffHunk
}

// NominateCorrection implements the deterministic nomination rule:
// a human message follows an agent turn that edited files (prevStart -> prevEnd),
// and the new turn's edits (currStart -> currEnd) overlap the earlier edits by hunk
// at a line that the previous turn actually added. File overlap locates candidates only.
func NominateCorrection(ctx context.Context, repoRoot, prevStart, prevEnd, currStart, currEnd string) (Nomination, error) {
	if prevStart == "" || prevEnd == "" || currStart == "" || currEnd == "" {
		return Nomination{Reason: "missing prior or current turn snapshots"}, nil
	}
	if prevStart == prevEnd {
		return Nomination{Reason: "previous turn made no file changes"}, nil
	}
	if currStart == currEnd {
		return Nomination{Reason: "current turn made no file changes"}, nil
	}

	prevChanges, err := gitutil.DiffFileChanges(ctx, repoRoot, prevStart, prevEnd)
	if err != nil {
		return Nomination{}, err
	}
	currChanges, err := gitutil.DiffFileChanges(ctx, repoRoot, currStart, currEnd)
	if err != nil {
		return Nomination{}, err
	}
	if len(prevChanges) == 0 || len(currChanges) == 0 {
		return Nomination{Reason: "no diff hunks in prior or current turn"}, nil
	}

	prevByPath := make(map[string]gitutil.FileChange, len(prevChanges))
	for _, ch := range prevChanges {
		if isIgnoredForNomination(ch.Path) {
			continue
		}
		prevByPath[ch.Path] = ch
	}

	var overlapFiles []string
	overlaps := make(map[string][]gitutil.DiffHunk)
	for _, curr := range currChanges {
		if isIgnoredForNomination(curr.Path) || len(curr.RemovedLines) == 0 {
			continue
		}
		path := curr.Path
		if curr.OldPath != "" {
			path = curr.OldPath
		}
		prev, ok := prevByPath[path]
		if !ok {
			continue
		}
		hunks := overlappingHunks(prev.AddedLines, curr.Hunks)
		if len(hunks) == 0 {
			continue
		}
		// The line coordinates refer to the same file contents on the two middle trees.
		// Other edits between turns can shift those coordinates.
		if prevEnd != currStart {
			before, firstErr := gitutil.FileAtRef(ctx, repoRoot, prevEnd, path)
			after, secondErr := gitutil.FileAtRef(ctx, repoRoot, currStart, path)
			if firstErr != nil || secondErr != nil || !bytes.Equal(before, after) {
				continue
			}
		}
		overlapFiles = append(overlapFiles, curr.Path)
		overlaps[curr.Path] = hunks
	}

	if len(overlapFiles) == 0 {
		return Nomination{Reason: "no lines added by the previous turn were changed or removed"}, nil
	}

	return Nomination{
		Nominated:        true,
		Reason:           "current turn changed or removed lines added by the previous turn",
		OverlappingFiles: overlapFiles,
		BrokenRef:        currStart,
		FixedRef:         currEnd,
		overlaps:         overlaps,
	}, nil
}

func overlappingHunks(previousAdditions []gitutil.LineRange, current []gitutil.DiffHunk) []gitutil.DiffHunk {
	var overlaps []gitutil.DiffHunk
	for _, hunk := range current {
		// Git reports each side in line order. Only additions still in this hunk can intersect it.
		for len(previousAdditions) > 0 && previousAdditions[0].End < hunk.Before.Start {
			previousAdditions = previousAdditions[1:]
		}
		for _, added := range previousAdditions {
			if added.Start > hunk.Before.End {
				break
			}
			overlaps = append(overlaps, gitutil.DiffHunk{
				Before: gitutil.LineRange{Start: max(added.Start, hunk.Before.Start), End: min(added.End, hunk.Before.End)},
				After:  hunk.After,
			})
		}
	}
	return overlaps
}

func (nom Nomination) candidatePriority(c DiscoveredTestCandidate) int {
	for _, h := range nom.overlaps[c.Path] {
		// Existing cases have exact coordinates in the tree where the overlap was measured.
		if c.beforeLines.Start > 0 {
			if c.beforeLines.Overlaps(h.Before) {
				return 2
			}
			continue
		}
		// A replacement with a new selector only has coordinates on the added side of the hunk.
		if c.afterLines.Overlaps(h.After) {
			return 1
		}
	}
	return 0
}

func isIgnoredForNomination(relPath string) bool {
	clean := filepath.ToSlash(strings.TrimPrefix(relPath, "./"))
	return strings.HasPrefix(clean, ".flinch/") ||
		strings.HasPrefix(clean, ".agents/") ||
		strings.HasPrefix(clean, ".claude/") ||
		strings.HasPrefix(clean, ".codex/")
}
