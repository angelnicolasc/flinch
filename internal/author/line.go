package author

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	ManagedStartMarker = "<!-- flinch:start -->"
	ManagedEndMarker   = "<!-- flinch:end -->"
)

// LineValidationResult summarizes the four checks on an agent-written managed guidance line.
type LineValidationResult struct {
	Changed       bool     `json:"changed"`
	Valid         bool     `json:"valid"`
	AddedLines    []string `json:"added_lines,omitempty"`
	SectionFull   bool     `json:"section_full,omitempty"`
	FormattingErr bool     `json:"formatting_err,omitempty"`
	Reason        string   `json:"reason,omitempty"`
}

var urlRe = regexp.MustCompile(`https?://\S+`)

// ValidateManagedLine checks an agent's edit to the shared instructions file (e.g. AGENTS.md)
// against all four constraints:
//  1. Every added line sits strictly between <!-- flinch:start --> and <!-- flinch:end -->.
//  2. The managed section stays within maxLines (default 30); if full, skip without modifying existing content.
//  3. No added line duplicates an existing line anywhere in the file.
//  4. Nothing outside the managed section changed through this path.
func ValidateManagedLine(beforeBytes, afterBytes []byte, maxLines int) LineValidationResult {
	if maxLines <= 0 {
		maxLines = 30
	}
	before := string(beforeBytes)
	after := string(afterBytes)
	if before == after {
		return LineValidationResult{Valid: true}
	}
	invalid := func(reason string) LineValidationResult {
		return LineValidationResult{Changed: true, FormattingErr: true, Reason: reason}
	}
	if beforeBytes == nil {
		return invalid("the instruction file must already exist")
	}

	lines := strings.SplitAfter(after, "\n")
	start, end := -1, -1
	for i, line := range lines {
		switch strings.TrimSpace(line) {
		case ManagedStartMarker:
			start = i
		case ManagedEndMarker:
			end = i
		}
	}
	if start < 0 || end <= start || strings.Count(after, ManagedStartMarker) != 1 || strings.Count(after, ManagedEndMarker) != 1 {
		return invalid("expected one pair of managed section markers on separate lines")
	}
	if end-start-1 > maxLines {
		return LineValidationResult{Changed: true, SectionFull: true, Reason: fmt.Sprintf("managed guidance exceeds its %d-line limit", maxLines)}
	}
	// Removing the proposed insertion must recover every original byte, including line endings.
	var added string
	for i := start + 1; i < end; i++ {
		if strings.Join(lines[:i], "")+strings.Join(lines[i+1:], "") == before {
			added = strings.TrimSpace(lines[i])
			break
		}
	}
	if added == "" || len(added) > 200 || strings.Contains(added, "```") || urlRe.MatchString(added) {
		return invalid("insert exactly one concise guidance line and preserve all existing bytes")
	}
	for _, line := range nonEmptyTrimmedLines(before) {
		if normalizeLineForDup(added) == normalizeLineForDup(line) {
			return invalid("the guidance line already exists")
		}
	}
	return LineValidationResult{Changed: true, Valid: true, AddedLines: []string{added}}
}

func nonEmptyTrimmedLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		t := strings.TrimSpace(l)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

func normalizeLineForDup(l string) string {
	s := strings.TrimSpace(l)
	s = strings.TrimLeft(s, "-*• ")
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}
