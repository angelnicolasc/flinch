package author_test

import (
	"context"
	"strings"
	"testing"

	"github.com/angelnicolasc/flinch/internal/author"
	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/reflex"
)

func TestSharedTableCallbackPrioritizesItsRow(t *testing.T) {
	const cases = `test('negative', () => { expect(delay(-1)).toBe(-1); });

test.each([
  {name: 'zero', value: 0},
])('$name', row => {
  expect(delay(row.value)).toBe(7);
});
`
	f := jsExample("vitest", "shared callback", "")
	f.oldTest += cases
	f.newTest = strings.Replace(f.oldTest, "toBe(7)", "toBe(8)", 1)
	f.fixed = strings.Replace(f.broken, "value || 8", "value == null ? 8 : Math.max(0, value)", 1)
	repo, initial, cfg := f.repository(t)
	ctx := context.Background()
	prior, err := gitutil.CaptureWorkingTreeSnapshot(ctx, repo, "use the default in the shared callback")
	if err != nil {
		t.Fatal(err)
	}
	tests := strings.Replace(f.newTest, "expect(delay(row.value)).toBe(8)", "expect(delay(row.value)).toBe(0)", 1)
	tests = strings.Replace(tests, "delay(-1)).toBe(-1)", "delay(-1)).toBe(0)", 1)
	fixtureWrite(t, repo, f.path, tests)
	fixtureWrite(t, repo, f.product, f.fixed)
	fixed, err := gitutil.CaptureWorkingTreeSnapshot(ctx, repo, "correct both delay cases")
	if err != nil {
		t.Fatal(err)
	}
	nom, err := author.NominateCorrection(ctx, repo, initial, prior, prior, fixed)
	if err != nil || !nom.Nominated {
		t.Fatalf("expected the edit to the previous callback to nominate the correction: %+v, %v", nom, err)
	}
	out, err := nom.Reuse(ctx, repo, cfg, author.AuthoringBudgetCap)
	records, readErr := reflex.LoadAll(repo)
	if err != nil || readErr != nil || out.Status != author.ReuseProven || len(records) != 1 ||
		records[0].Artifact.Selector != "zero" || out.Neighbor != "omitted" {
		t.Fatalf("expected only zero with its passing neighbor omitted, got %+v, %+v, %v, %v", records, out, err, readErr)
	}
}
