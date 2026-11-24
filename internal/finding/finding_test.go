package finding

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/angelnicolasc/flinch/internal/gitutil"
)

func TestEnrichResolvesGoEnclosingSymbol(t *testing.T) {
	dir := t.TempDir()
	src := `package client

import "io"

type Client struct{}

func (c *Client) Fetch(r io.Reader) ([]byte, error) {
	return io.ReadAll(r)
}
`
	if err := os.WriteFile(filepath.Join(dir, "client.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	enriched := Enrich(dir, []Finding{
		{Tool: "custom", Rule: "unbounded-read", Path: "client.go", StartLine: 8, Message: "unbounded read"},
	})
	if enriched[0].Symbol != "(*Client).Fetch" {
		t.Fatalf("expected (*Client).Fetch, got %q", enriched[0].Symbol)
	}
	if enriched[0].Context != "return io.ReadAll(r)" {
		t.Fatalf("unexpected context: %q", enriched[0].Context)
	}
}

func TestNewFindingsDetectsCountPreservingSwapAndRespectsLineShiftsAndRenames(t *testing.T) {
	baseline := []Finding{
		{Rule: "r1", Path: "pkg/a.go", StartLine: 10, Symbol: "Fetch", Context: "io.ReadAll(respA.Body)"},
		{Rule: "r1", Path: "pkg/old.go", StartLine: 5, Symbol: "Legacy", Context: "io.ReadAll(legacy.Body)"},
	}
	current := []Finding{
		// Same count in Fetch (1), but swapped respA -> respB! Must be flagged as NEW.
		{Rule: "r1", Path: "pkg/a.go", StartLine: 18, Symbol: "Fetch", Context: "io.ReadAll(respB.Body)"},
		// Renamed file + line shift of pre-existing finding: must NOT be flagged as new.
		{Rule: "r1", Path: "pkg/new.go", StartLine: 25, Symbol: "Legacy", Context: "io.ReadAll(legacy.Body)"},
	}
	renames := map[string]string{"pkg/new.go": "pkg/old.go"}

	added := NewFindings(baseline, current, renames)
	if len(added) != 1 {
		t.Fatalf("expected exactly 1 new finding, got %d: %#v", len(added), added)
	}
	if added[0].Context != "io.ReadAll(respB.Body)" {
		t.Fatalf("expected swapped expression to be flagged, got %#v", added[0])
	}
}

func TestMatchIncidentSitesHandlesPureInsertionFixes(t *testing.T) {
	changes := []gitutil.FileChange{
		{
			Path:         "handler.ts",
			Status:       "M",
			InsertionsAt: []int{2},
			AddedLines:   []gitutil.LineRange{{Start: 3, End: 3}},
		},
	}
	broken := []Finding{
		{Rule: "auth-check", Path: "handler.ts", StartLine: 1, EndLine: 4, Symbol: "handle", Context: "export function handle(req) {"},
	}
	res := MatchIncidentSites(changes, broken, nil)
	if len(res.MatchedBroken) != 1 {
		t.Fatalf("expected broken finding to match insertion point, got %#v", res)
	}
	if len(res.RemainingFixed) != 0 {
		t.Fatalf("expected 0 remaining fixed findings, got %#v", res.RemainingFixed)
	}
}
