package finding

import (
	"path/filepath"
	"strings"

	"github.com/angelnicolasc/flinch/internal/gitutil"
)

// NewFindings returns findings present in current that were not already present in baseline.
// Matching uses (rule/selector, path, enclosing symbol, normalized context) multiset comparison,
// so line shifts do not create false positives, while count-preserving branch swaps inside the
// same function DO surface as new findings.
// renameMap maps currentPath -> baselinePath so renamed files preserve baseline debt identity.
func NewFindings(baseline, current []Finding, renameMap map[string]string) []Finding {
	baseCounts := make(map[string]int, len(baseline))
	for _, f := range baseline {
		baseCounts[f.OccurrenceKey()]++
	}

	var added []Finding
	for _, f := range current {
		lookup := f
		if oldPath, ok := renameMap[f.Path]; ok && oldPath != "" {
			lookup.Path = oldPath
		}
		key := lookup.OccurrenceKey()
		if baseCounts[key] > 0 {
			baseCounts[key]--
			continue
		}
		added = append(added, f)
	}
	return added
}

// IncidentMatchResult summarizes pairing between broken-tree and fixed-tree findings
// across the changed files of an incident.
type IncidentMatchResult struct {
	MatchedBroken   []Finding `json:"matched_broken,omitempty"`
	RemainingFixed  []Finding `json:"remaining_fixed,omitempty"`
	Ambiguous       bool      `json:"ambiguous"`
	AmbiguousReason string    `json:"ambiguous_reason,omitempty"`
}

// MatchIncidentSites pairs broken-tree findings overlapping the incident diff against
// corresponding sites in the fixed tree, tracking file renames, insertion points, and
// occurrence identities.
func MatchIncidentSites(changes []gitutil.FileChange, brokenFindings, fixedFindings []Finding) IncidentMatchResult {
	res := IncidentMatchResult{}
	if len(changes) == 0 {
		return res
	}

	changeByBrokenPath := make(map[string]gitutil.FileChange, len(changes))
	brokenToFixedPath := make(map[string]string, len(changes))
	fixedToChange := make(map[string]gitutil.FileChange, len(changes))

	for _, ch := range changes {
		bPath := ch.Path
		if ch.Status == "R" && ch.OldPath != "" {
			bPath = ch.OldPath
		}
		bPath = normalizeRel(bPath)
		fPath := normalizeRel(ch.Path)
		changeByBrokenPath[bPath] = ch
		brokenToFixedPath[bPath] = fPath
		fixedToChange[fPath] = ch
	}

	// 1. Identify broken-tree findings tied to the incident change (removed lines, insertion points, or deleted/modified file hunks).
	for _, bf := range brokenFindings {
		bPath := normalizeRel(bf.Path)
		ch, ok := changeByBrokenPath[bPath]
		if !ok {
			continue
		}
		if overlapsBrokenChange(bf, ch) {
			res.MatchedBroken = append(res.MatchedBroken, bf)
		}
	}

	// 2. Check fixed-tree findings at the corresponding sites.
	// Build a count of fixed-tree findings mapped back to broken coordinates by (rule, symbol, context)
	// and also detect any findings directly overlapping added lines of the fix.
	fixedBySite := make(map[string][]Finding)
	for _, ff := range fixedFindings {
		fPath := normalizeRel(ff.Path)
		ch, ok := fixedToChange[fPath]
		if !ok {
			continue
		}
		if overlapsAddedLines(ff, ch) {
			res.RemainingFixed = append(res.RemainingFixed, ff)
			continue
		}
		key := siteKey(ff.EffectiveID(), fPath, ff.Symbol, ff.Context)
		fixedBySite[key] = append(fixedBySite[key], ff)
	}

	// Verify each matched broken finding disappeared in the corresponding fixed file/site.
	brokenSiteCounts := make(map[string]int)
	for _, bf := range res.MatchedBroken {
		fPath := brokenToFixedPath[normalizeRel(bf.Path)]
		key := siteKey(bf.EffectiveID(), fPath, bf.Symbol, bf.Context)
		brokenSiteCounts[key]++
	}

	// Count total broken occurrences per site across the whole broken file to detect
	// ambiguous duplicate expressions where pairing cannot distinguish which copy disappeared.
	totalBrokenSiteCounts := make(map[string]int)
	for _, bf := range brokenFindings {
		fPath, ok := brokenToFixedPath[normalizeRel(bf.Path)]
		if !ok {
			continue
		}
		key := siteKey(bf.EffectiveID(), fPath, bf.Symbol, bf.Context)
		totalBrokenSiteCounts[key]++
	}

	for key, matchedCount := range brokenSiteCounts {
		fixedList := fixedBySite[key]
		if len(fixedList) == 0 {
			continue
		}
		totalBroken := totalBrokenSiteCounts[key]
		// Expected surviving copies outside the fixed hunk = totalBroken - matchedCount.
		expectedSurviving := totalBroken - matchedCount
		if len(fixedList) > expectedSurviving {
			// The fixed tree still has the finding at the same (rule, file, symbol, context)!
			res.RemainingFixed = append(res.RemainingFixed, fixedList...)
		} else if expectedSurviving > 0 && len(fixedList) == expectedSurviving {
			// Identical duplicate expressions exist in the same function; verify unambiguously only if count strictly dropped.
			if totalBroken-len(fixedList) != matchedCount {
				res.Ambiguous = true
				res.AmbiguousReason = "duplicate identical findings in same enclosing symbol make site pairing ambiguous"
			}
		}
	}

	return res
}

func overlapsBrokenChange(f Finding, ch gitutil.FileChange) bool {
	if ch.Status == "D" {
		return true
	}
	start := f.StartLine
	end := f.EndLine
	if end < start {
		end = start
	}
	for _, r := range ch.RemovedLines {
		if start <= r.End && end >= r.Start {
			return true
		}
	}
	for _, ins := range ch.InsertionsAt {
		// Pure insertion at line N in broken file: match findings spanning or immediately adjacent to insertion point.
		if ins == 0 && start <= 2 {
			return true
		}
		if start <= ins+1 && end >= ins {
			return true
		}
	}
	return false
}

func overlapsAddedLines(f Finding, ch gitutil.FileChange) bool {
	start := f.StartLine
	end := f.EndLine
	if end < start {
		end = start
	}
	for _, r := range ch.AddedLines {
		if start <= r.End && end >= r.Start {
			return true
		}
	}
	return false
}

func siteKey(id, path, symbol, ctx string) string {
	return id + "|" + path + "|" + symbol + "|" + NormalizeContext(ctx)
}

func normalizeRel(p string) string {
	return filepath.ToSlash(strings.TrimPrefix(filepath.Clean(p), "./"))
}
