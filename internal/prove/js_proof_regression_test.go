package prove

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/reflex"
	"github.com/angelnicolasc/flinch/internal/testutil"
)

func TestRegressionRealJestDiscoversProofAndHonorsProjectExcludes(t *testing.T) {
	repo, spec := jsDelayRepo(t, "jest")
	ctx := context.Background()
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

// jsDelayRepo commits a delay function that drops an explicit zero, then
// leaves the fix and a test for it in the worktree, using the real Jest or
// Vitest install named by FLINCH_REAL_JEST or FLINCH_REAL_VITEST. The Jest
// project also holds a copy of the test under excluded/, which its config
// ignores.
func jsDelayRepo(t *testing.T, tool string) (string, CandidateSpec) {
	t.Helper()
	bin := testutil.RequireToolEnv(t, "FLINCH_REAL_"+strings.ToUpper(tool))
	deps := filepath.Dir(filepath.Dir(bin))
	lock, err := os.ReadFile(filepath.Join(filepath.Dir(deps), "package-lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if _, _, err := gitutil.Run(context.Background(), repo, nil, "init"); err != nil {
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
	write("package-lock.json", string(lock))
	if err := os.Symlink(deps, filepath.Join(repo, "node_modules")); err != nil {
		t.Fatal(err)
	}
	spec := CandidateSpec{ID: "zero-delay", Rung: reflex.RungTest, FixedRef: "WORKTREE",
		Artifact: reflex.Artifact{Tool: tool, Selector: "zero"}, NeighborSelector: "omitted"}
	switch tool {
	case "jest":
		write("package.json", "{\"private\":true,\"devDependencies\":{\"jest\":\"29.7.0\"}}\n")
		write("jest.config.cjs", "module.exports = { testPathIgnorePatterns: ['/excluded/'] }\n")
		write("delay.js", "module.exports = value => value || 8\n")
		spec.BrokenRef = commitAll(t, repo, "add delay function")
		write("delay.js", "module.exports = value => value ?? 8\n")
		tests := "test('zero', () => { expect(delay(0)).toBe(0) })\ntest('omitted', () => { expect(delay(undefined)).toBe(8) })\n"
		write("delay.test.js", "const delay = require('./delay')\n"+tests)
		write("excluded/delay.test.js", "const delay = require('../delay')\n"+tests)
		spec.Artifact.Path = "delay.test.js"
	case "vitest":
		write("package.json", "{\"private\":true,\"type\":\"module\",\"devDependencies\":{\"vitest\":\"3.2.4\"}}\n")
		write("delay.ts", "export const delay = (value?: number) => value || 8;\n")
		spec.BrokenRef = commitAll(t, repo, "add delay function")
		write("delay.ts", "export const delay = (value?: number) => value ?? 8;\n")
		write("delay.test.ts", "import { expect, test } from 'vitest';\nimport { delay } from './delay';\n"+
			"test('zero', () => { expect(delay(0)).toBe(0); });\ntest('omitted', () => { expect(delay()).toBe(8); });\n")
		spec.Artifact.Path = "delay.test.ts"
	default:
		t.Fatalf("no delay fixture for %s", tool)
	}
	return repo, spec
}
