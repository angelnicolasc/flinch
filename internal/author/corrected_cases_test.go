package author_test

import (
	"context"
	"strings"
	"testing"

	"github.com/angelnicolasc/flinch/internal/author"
	"github.com/angelnicolasc/flinch/internal/check"
	"github.com/angelnicolasc/flinch/internal/config"
	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/reflex"
)

func correctedCaseExamples() []assertionFixture {
	goFunction := goExample("function", "", `if got := Delay(0); got != 0 { t.Fatalf("got %d, want zero", got) }`)
	goFunction.oldTest = strings.Replace(goFunction.newTest, "got != 0", "got != 8", 1)
	goSubtest := goFunction
	goSubtest.name = "subtest"
	goSubtest.selector = "TestDelay/zero"
	goSubtest.newTest = `package delay
import "testing"
func TestDelay(t *testing.T) {
	t.Run("zero", func(t *testing.T) {
		results := map[string]int{"delay": Delay(0)}
		if got := results["delay"]; got != 0 { t.Fatalf("got %d, want zero", got) }
	})
	t.Run("omitted", func(t *testing.T) { if Delay(-1) != 8 { t.Fatal("default changed") } })
}
`
	goSubtest.oldTest = strings.Replace(goSubtest.newTest, "got != 0", "got != 8", 1)
	goRenamed := goSubtest
	goRenamed.name = "renamed_first_subtest"
	goRenamed.oldTest = strings.Replace(goRenamed.oldTest, `t.Run("zero"`, `t.Run("default_zero"`, 1)
	py := pythonExample("function", "    assert delay(0) == 0")
	py.oldTest = strings.Replace(py.newTest, "delay(0) == 0", "delay(0) == 8", 1)
	fixtures := []assertionFixture{goFunction, goSubtest, goRenamed, py}
	for _, tool := range []string{"vitest", "jest"} {
		f := jsExample(tool, "test", "  expect(delay(0)).toBe(0);")
		f.oldTest = strings.Replace(f.newTest, "toBe(0)", "toBe(8)", 1)
		fixtures = append(fixtures, f)
	}
	return fixtures
}

func TestReuseProvesModifiedNamedCases(t *testing.T) {
	ctx := context.Background()
	for _, f := range correctedCaseExamples() {
		t.Run(f.tool+"/"+f.name, func(t *testing.T) {
			repo, broken, cfg := f.repository(t)
			fixtureWrite(t, repo, f.product, f.fixed)
			fixed, err := gitutil.CaptureWorkingTreeSnapshot(ctx, repo, "corrected case")
			if err != nil {
				t.Fatal(err)
			}
			out, err := author.ReuseFirst(ctx, repo, cfg, broken, fixed, author.AuthoringBudgetCap)
			records, readErr := reflex.LoadAll(repo)
			if err != nil || readErr != nil || out.Status != author.ReuseProven || out.Reflex == nil || len(records) != 1 ||
				records[0].Artifact.Selector != f.selector || out.Neighbor == "" || out.Neighbor == f.selector {
				t.Fatalf("expected only %s with a distinct neighbor: %+v, %v, %v", f.selector, out, err, readErr)
			}
		})
	}
}

func TestModifiedCaseDiscoveryIgnoresFormattingAndComments(t *testing.T) {
	ctx := context.Background()
	for _, f := range correctedCaseExamples() {
		t.Run(f.tool+"/"+f.name, func(t *testing.T) {
			// If nominated, the existing expectation would distinguish the
			// two implementations. A formatting change alone must not nominate it.
			f.oldTest = f.newTest
			switch f.tool {
			case "go-test":
				f.newTest = strings.Replace(f.newTest, "got != 0", "got  !=  /* delay */ 0", 1)
			case "pytest":
				f.newTest = strings.Replace(f.newTest, "delay(0) == 0", "delay(0)  ==  0  # delay", 1)
			default:
				f.newTest = strings.Replace(f.newTest, "toBe(0)", "toBe( /* delay */ 0 )", 1)
			}
			repo, broken, cfg := f.repository(t)
			fixtureWrite(t, repo, f.product, f.fixed)
			fixed, err := gitutil.CaptureWorkingTreeSnapshot(ctx, repo, "format only")
			if err != nil {
				t.Fatal(err)
			}
			candidates, err := author.ExtractTestCandidates(ctx, repo, cfg, broken, fixed)
			if err != nil || len(candidates) != 0 {
				t.Fatalf("formatting nominated a case: %+v, err=%v", candidates, err)
			}
		})
	}
}

func TestModifiedCaseStillRequiresBrokenFailureAndPassingNeighbor(t *testing.T) {
	ctx := context.Background()
	for _, scenario := range []string{"passes_on_broken", "failing_neighbor"} {
		t.Run(scenario, func(t *testing.T) {
			f := correctedCaseExamples()[0]
			switch scenario {
			case "passes_on_broken":
				f.newTest = strings.Replace(f.newTest, "Delay(0); got != 0", "Delay(2); got != 2", 1)
			case "failing_neighbor":
				f.oldTest = strings.Replace(f.oldTest, "Delay(-1) != 8", "Delay(-1) != 7", 1)
				f.newTest = strings.Replace(f.newTest, "Delay(-1) != 8", "Delay(-1) != 7", 1)
			}
			repo, broken, cfg := f.repository(t)
			fixtureWrite(t, repo, f.product, f.fixed)
			fixed, err := gitutil.CaptureWorkingTreeSnapshot(ctx, repo, "inconclusive correction")
			if err != nil {
				t.Fatal(err)
			}
			out, err := author.ReuseFirst(ctx, repo, cfg, broken, fixed, author.AuthoringBudgetCap)
			records, readErr := reflex.LoadAll(repo)
			if err != nil || readErr != nil || out.Status != author.ReuseInconclusive || len(records) != 0 {
				t.Fatalf("expected an attempted proof with no record, got %+v, %v, %v", out, err, readErr)
			}
		})
	}
}

func TestModifiedCaseDiscoveryPreservesTableRowsAndLiteralContents(t *testing.T) {
	const methodAndTest = `const client = { test() {}, it() {} };
client.test('zero', 5);
client.it('zero', 6);
test('omitted', () => { expect(delay(null)).toBe(8); });
test('zero', () => { expect(delay(0)).toBe(8); });
`
	for _, tc := range []struct {
		name, tool, path, before, oldText, newText, selector string
	}{
		{"method_before_test", "vitest", "delay.test.js", methodAndTest, "delay(0)).toBe(8)", "delay(0)).toBe(0)", "zero"},
		{"method_only", "jest", "delay.test.js", methodAndTest, "client.test('zero', 5)", "client.test('zero', 7)", ""},
		{"object_row", "vitest", "delay.test.js", `test.each([
  {name: 'omitted', want: 8},
  {name: 'zero', want: 8},
])('$name', ({name, want}) => { expect(delay(name)).toBe(want); });
`, "'zero', want: 8", "'zero', want: 0", "zero"},
		{"tuple_row", "jest", "delay.test.js", `test.each([
  ['omitted', 8],
  ['zero', 8],
])('%s', (name, want) => { expect(delay(name)).toBe(want); });
`, "'zero', 8", "'zero', 0", "zero"},
		{"javascript_string", "jest", "delay.test.js", `test('omitted', () => { expect(label(null)).toBe('default'); });
test('zero', () => { expect(label(0)).toBe('wait // 0'); });
`, "wait // 0", "wait //0", "zero"},
		{"python_string", "pytest", "test_delay.py", "def test_omitted():\n    assert label(None) == 'default'\n\ndef test_zero():\n    assert label(0) == 'wait # 0'\n", "wait # 0", "wait #0", "test_delay.py::test_zero"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			fixtureGit(t, repo, "init")
			fixtureWrite(t, repo, tc.path, tc.before)
			fixtureCommit(t, repo, "Add two cases", ".")
			fixtureWrite(t, repo, tc.path, strings.Replace(tc.before, tc.oldText, tc.newText, 1))
			ctx := context.Background()
			fixed, err := gitutil.CaptureWorkingTreeSnapshot(ctx, repo, "edit one case")
			if err != nil {
				t.Fatal(err)
			}
			cfg := config.Default()
			cfg.Tests.Runners = []config.TestRunner{{Kind: tc.tool}}
			got, err := author.ExtractTestCandidates(ctx, repo, cfg, "HEAD", fixed)
			if err != nil {
				t.Fatal(err)
			}
			if tc.selector == "" {
				if len(got) != 0 {
					t.Fatalf("a method call was discovered as a test: %+v", got)
				}
				return
			}
			if len(got) != 1 || got[0].Selector != tc.selector {
				t.Fatalf("expected only %s after editing one case: %+v, err=%v", tc.selector, got, err)
			}
		})
	}
}

func TestIntegrityFollowsCorrectedJavaScriptSelector(t *testing.T) {
	const path = "delay.test.js"
	const source = `describe('timer', () => {
  it('zero', () => { expect(timer(0)).toBe(8); });
});
describe('retry', () => {
  it('zero', () => { expect(delay(0)).toBe(0); });
});
`
	const table = `describe('timer', () => {
  it('delay: zero', () => { expect(timer(0)).toBe(8); });
});
describe('retry', () => {
  test.each([['zero', 0], ['omitted', 8]])('delay: %s', (name, want) => { expect(delay(name)).toBe(want); });
});
`
	for _, tc := range []struct {
		name, source, selector, corrected, prior, sibling, siblingEdit string
	}{
		{"literal", source, "retry › zero", "delay(0)).toBe(0)", "delay(0)).toBe(8)", "timer(0)).toBe(8)", "timer(0)).toBe(9)"},
		{"row", table, "retry › delay: zero", "'zero', 0", "'zero', 8", "'omitted', 8", "'omitted', 9"},
		{"callback", table, "retry › delay: zero", "delay(name)).toBe(want)", "delay(name)).toBe(8)", "timer(0)).toBe(8)", "timer(0)).toBe(9)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo := t.TempDir()
			fixtureGit(t, repo, "init")
			changed := strings.Replace(tc.source, tc.corrected, tc.prior, 1)
			fixtureWrite(t, repo, path, changed)
			fixtureCommit(t, repo, "Add delay cases", path)
			fixtureWrite(t, repo, path, tc.source)
			fixed, err := gitutil.CaptureWorkingTreeSnapshot(ctx, repo, "correct retry delay")
			if err != nil {
				t.Fatal(err)
			}
			candidates, err := author.ExtractTestCandidates(ctx, repo, config.Default(), "HEAD", fixed)
			if err != nil || len(candidates) == 0 || candidates[0].Selector != tc.selector {
				t.Fatalf("expected corrected selector %q, got %+v, %v", tc.selector, candidates, err)
			}
			cand := candidates[0]
			r := reflex.Reflex{ID: "retry-zero", Version: 1, Rung: reflex.RungTest, Scope: cand.Scope,
				Obligation: "Preserve explicit zero delay.", Artifact: reflex.Artifact{Tool: cand.Tool, Path: cand.Path, Selector: cand.Selector}}
			if _, err := reflex.Save(repo, r); err != nil {
				t.Fatal(err)
			}
			fixtureCommit(t, repo, "Protect corrected retry delay", ".")
			for _, edit := range []struct {
				name, content string
				wantViolation bool
			}{
				{"change_protected_case", changed, true},
				{"remove_protected_suite", strings.Replace(tc.source, "'retry'", "'other'", 1), true},
				{"change_sibling", strings.Replace(tc.source, tc.sibling, tc.siblingEdit, 1), false},
			} {
				t.Run(edit.name, func(t *testing.T) {
					fixtureWrite(t, repo, path, edit.content)
					got, err := check.CheckIntegrity(ctx, repo, "HEAD", []reflex.Reflex{r}, []string{path})
					if err != nil || edit.wantViolation && (len(got) != 1 || got[0].Kind != "artifact_modified") || !edit.wantViolation && len(got) != 0 {
						t.Fatalf("expected artifact_modified=%t for %q, got %+v, %v", edit.wantViolation, cand.Selector, got, err)
					}
				})
			}
		})
	}
}
