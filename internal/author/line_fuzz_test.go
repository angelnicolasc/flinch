package author

import (
	"strings"
	"testing"
)

// FuzzManagedLineInsertion builds each edit independently of the validator:
// one line inserted inside the managed section of a guide, in that guide's
// own line ending. A valid line must be accepted as exactly that line, and
// the same edit plus any change outside the section must be rejected.
func FuzzManagedLineInsertion(f *testing.F) {
	f.Add("# Guide\n", "Keep retry delays explicit.", uint8(0), false, uint8(0))
	f.Add("# Guide\r\nIntro\r\n", "Prefer bridge calls in the renderer.", uint8(1), true, uint8(3))
	f.Add("", "x", uint8(0), false, uint8(0))
	f.Add("# Guide", "Use the bridge.", uint8(0), false, uint8(1))
	f.Fuzz(func(t *testing.T, head, line string, existing uint8, crlf bool, outside uint8) {
		nl := "\n"
		if crlf {
			nl = "\r\n"
		}
		if strings.ContainsAny(head, "\r") || strings.Contains(head, "flinch:") || strings.ContainsAny(line, "\r\n") || strings.Contains(line, "flinch:") {
			return
		}
		head = strings.ReplaceAll(head, "\n", nl)
		if head != "" && !strings.HasSuffix(head, nl) {
			head += nl
		}
		var section []string
		for i := 0; i < int(existing%4); i++ {
			section = append(section, "- existing rule "+string(rune('A'+i))+nl)
		}
		build := func(rows []string) string {
			return head + ManagedStartMarker + nl + strings.Join(rows, "") + ManagedEndMarker + nl
		}
		before := build(section)
		pos := 0
		if len(section) > 0 {
			pos = int(existing) % (len(section) + 1)
		}
		rows := append(append(append([]string{}, section[:pos]...), line+nl), section[pos:]...)
		after := build(rows)

		trimmed := strings.TrimSpace(line)
		valid := trimmed != "" && len(trimmed) <= 200 && !strings.Contains(trimmed, "```") && !urlRe.MatchString(trimmed)
		for _, l := range nonEmptyTrimmedLines(before) {
			if normalizeLineForDup(trimmed) == normalizeLineForDup(l) {
				valid = false
			}
		}
		got := ValidateManagedLine([]byte(before), []byte(after), 30)
		if valid != (got.Valid && got.Changed) {
			t.Fatalf("line %q in %q: valid=%t, got %+v", line, after, valid, got)
		}
		if valid && (len(got.AddedLines) != 1 || got.AddedLines[0] != trimmed) {
			t.Fatalf("accepted %q as %q", trimmed, got.AddedLines)
		}

		if outside%2 == 1 && valid {
			tampered := after + "tampered" + nl
			if res := ValidateManagedLine([]byte(before), []byte(tampered), 30); res.Valid && res.Changed {
				t.Fatalf("accepted a change outside the managed section: %q", tampered)
			}
		}
	})
}
