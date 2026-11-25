package author_test

import (
	"context"
	"strings"
	"testing"

	"github.com/angelnicolasc/flinch/internal/check"
	"github.com/angelnicolasc/flinch/internal/reflex"
)

func TestAmbiguousJavaScriptSelectorExplainsSuiteName(t *testing.T) {
	const path = "delay.test.js"
	const source = `describe('timer', () => { it('zero', () => { expect(timer(0)).toBe(8); }); });
describe('retry', () => { it('zero', () => { expect(delay(0)).toBe(0); }); });
`
	repo := t.TempDir()
	fixtureGit(t, repo, "init")
	fixtureWrite(t, repo, path, source)
	record := reflex.Reflex{ID: "zero-delay", Version: 1, Rung: reflex.RungTest, Scope: "**",
		Obligation: "Preserve the delay.", Artifact: reflex.Artifact{Tool: "vitest", Path: path, Selector: "zero"}}
	if _, err := reflex.Save(repo, record); err != nil {
		t.Fatal(err)
	}
	fixtureCommit(t, repo, "Protect zero by its leaf name", ".")
	fixtureWrite(t, repo, path, source+"const other = 1;\n")
	got, err := check.CheckIntegrity(context.Background(), repo, "HEAD", []reflex.Reflex{record}, []string{path})
	if err != nil || len(got) != 1 || got[0].Kind != "artifact_modified" ||
		!strings.Contains(got[0].Detail, "full") || !strings.Contains(got[0].Detail, "suite") {
		t.Fatalf("expected a finding directing the developer to the full suite-qualified name, got %+v, %v", got, err)
	}
}
