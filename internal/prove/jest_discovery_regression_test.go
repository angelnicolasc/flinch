package prove

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/reflex"
	"github.com/angelnicolasc/flinch/internal/testutil"
)

func TestRegressionRealJestDiscoversProofAndHonorsProjectExcludes(t *testing.T) {
	bin := testutil.RequireToolEnv(t, "FLINCH_REAL_JEST")
	deps := filepath.Dir(filepath.Dir(bin))
	lock, err := os.ReadFile(filepath.Join(filepath.Dir(deps), "package-lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repo := t.TempDir()
	if _, _, err := gitutil.Run(ctx, repo, nil, "init"); err != nil {
		t.Fatal(err)
	}
	write := func(path, content string) {
		t.Helper()
		path = filepath.Join(repo, path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore", "node_modules\n")
	write("package.json", "{\"private\":true,\"devDependencies\":{\"jest\":\"29.7.0\"}}\n")
	write("package-lock.json", string(lock))
	write("jest.config.cjs", "module.exports = { testPathIgnorePatterns: ['/excluded/'] }\n")
	write("delay.js", "module.exports = value => value || 8\n")
	if err := os.Symlink(deps, filepath.Join(repo, "node_modules")); err != nil {
		t.Fatal(err)
	}
	broken := commitAll(t, repo, "add delay function")
	write("delay.js", "module.exports = value => value ?? 8\n")
	tests := "test('zero', () => { expect(delay(0)).toBe(0) })\ntest('omitted', () => { expect(delay(undefined)).toBe(8) })\n"
	write("delay.test.js", "const delay = require('./delay')\n"+tests)
	write("excluded/delay.test.js", "const delay = require('../delay')\n"+tests)
	spec := CandidateSpec{ID: "zero-delay", Rung: reflex.RungTest, BrokenRef: broken, FixedRef: "WORKTREE",
		Artifact: reflex.Artifact{Tool: "jest", Path: "delay.test.js", Selector: "zero"}, NeighborSelector: "omitted"}
	report, err := Verify(ctx, repo, spec, Options{})
	if err != nil || report.Verdict != VerdictVerified {
		t.Fatalf("Jest could not prove its discovered test: %s/%s %s (%v)", report.Verdict, report.Reason, report.ActionableError, err)
	}
	spec.Artifact.Path = "excluded/delay.test.js"
	report, err = Verify(ctx, repo, spec, Options{})
	if err != nil || report.Verdict != VerdictInconclusive || report.Reason != ReasonSelectorNotExecuted {
		t.Fatalf("proof bypassed the project's Jest exclude: %s/%s (%v)", report.Verdict, report.Reason, err)
	}
}
