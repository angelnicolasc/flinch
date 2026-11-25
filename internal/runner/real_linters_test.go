package runner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/angelnicolasc/flinch/internal/reflex"
	"github.com/angelnicolasc/flinch/internal/testutil"
)

// runRealLinter puts an installed tool first on PATH, writes a fixture, and
// runs the production adapter on a version that has the finding and on one
// that doesn't. Each adapter has to report the first and come back clean on
// the second.
func runRealLinter(t *testing.T, env string, art reflex.Artifact, setup map[string]string, file, broken, fixed string) {
	t.Helper()
	bin := testutil.RequireToolEnv(t, env)
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	repo := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		path := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range setup {
		write(name, content)
	}
	write(file, broken)
	res, err := RunArtifact(context.Background(), repo, art, nil, 2*time.Minute)
	if err != nil || len(res.Findings) == 0 {
		t.Fatalf("expected a finding: %v %+v\nstdout %s\nstderr %s", err, res.Findings, res.Stdout, res.Stderr)
	}
	if got := res.Findings[0].Path; got != file {
		t.Fatalf("finding path %q, want %q", got, file)
	}
	write(file, fixed)
	res, err = RunArtifact(context.Background(), repo, art, nil, 2*time.Minute)
	if err != nil || len(res.Findings) != 0 {
		t.Fatalf("expected a clean run: %v %+v\nstdout %s\nstderr %s", err, res.Findings, res.Stdout, res.Stderr)
	}
}

func TestRealESLintReportsAndClears(t *testing.T) {
	runRealLinter(t, "FLINCH_REAL_ESLINT", reflex.Artifact{Tool: "eslint"},
		map[string]string{"eslint.config.cjs": `module.exports = [{ files: ["**/*.js"], rules: { "no-var": "error" } }];` + "\n"},
		"src/retry.js", "var delay = 1;\nexport { delay };\n", "const delay = 1;\nexport { delay };\n")
}

func TestRealAstGrepReportsAndClears(t *testing.T) {
	rule := "id: no-console-log\nlanguage: JavaScript\nseverity: error\nmessage: Use the logger.\nrule:\n  pattern: console.log($$$ARGS)\n"
	runRealLinter(t, "FLINCH_REAL_AST_GREP", reflex.Artifact{Tool: "ast-grep", Path: "rules/no-console-log.yml"},
		map[string]string{"rules/no-console-log.yml": rule},
		"src/retry.js", "export function retry() {\n  console.log(\"retrying\");\n}\n", "export function retry(logger) {\n  logger.info(\"retrying\");\n}\n")
}
