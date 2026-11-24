package author

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/angelnicolasc/flinch/internal/config"
	"github.com/angelnicolasc/flinch/internal/gitutil"
)

func commitSeed(t *testing.T, repo, msg string) string {
	t.Helper()
	ctx := context.Background()
	env := []string{
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	}
	if _, _, err := gitutil.Run(ctx, repo, env, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := gitutil.Run(ctx, repo, env, "-c", "commit.gpgsign=false", "commit", "-m", msg); err != nil {
		t.Fatal(err)
	}
	sha, _, err := gitutil.HeadSHA(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	return sha
}

func TestNominateCorrectionDetectsHunkAndSymbolOverlap(t *testing.T) {
	repo := t.TempDir()
	ctx := context.Background()
	if _, _, err := gitutil.Run(ctx, repo, nil, "init"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "retry.go"), []byte("package retry\n\nfunc Delay(v *int) int {\n\treturn 100\n}\n\nfunc Other() int {\n\treturn 1\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prevStart := commitSeed(t, repo, "base")

	// Turn 1: agent implements Delay with the || 0 bug.
	if err := os.WriteFile(filepath.Join(repo, "retry.go"), []byte("package retry\n\nfunc Delay(v *int) int {\n\tif v == nil || *v == 0 {\n\t\treturn 100\n\t}\n\treturn *v\n}\n\nfunc Other() int {\n\treturn 1\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prevEnd, err := gitutil.CaptureWorkingTreeSnapshot(ctx, repo, "")
	if err != nil {
		t.Fatal(err)
	}

	// Case A: Turn 2 edits an unrelated function Other() far away -> NOT nominated.
	if err := os.WriteFile(filepath.Join(repo, "retry.go"), []byte("package retry\n\nfunc Delay(v *int) int {\n\tif v == nil || *v == 0 {\n\t\treturn 100\n\t}\n\treturn *v\n}\n\nfunc Other() int {\n\treturn 2\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	otherEnd, err := gitutil.CaptureWorkingTreeSnapshot(ctx, repo, "")
	if err != nil {
		t.Fatal(err)
	}
	nomUnrelated, err := NominateCorrection(ctx, repo, prevStart, prevEnd, prevEnd, otherEnd)
	if err != nil {
		t.Fatal(err)
	}
	if nomUnrelated.Nominated {
		t.Fatalf("unrelated function edit must NOT be nominated: %+v", nomUnrelated)
	}

	// Case B: Turn 2 corrects Delay() -> NOMINATED!
	if err := os.WriteFile(filepath.Join(repo, "retry.go"), []byte("package retry\n\nfunc Delay(v *int) int {\n\tif v == nil {\n\t\treturn 100\n\t}\n\treturn *v\n}\n\nfunc Other() int {\n\treturn 1\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixEnd, err := gitutil.CaptureWorkingTreeSnapshot(ctx, repo, "")
	if err != nil {
		t.Fatal(err)
	}
	nomFix, err := NominateCorrection(ctx, repo, prevStart, prevEnd, prevEnd, fixEnd)
	if err != nil {
		t.Fatal(err)
	}
	if !nomFix.Nominated {
		t.Fatalf("expected correction overlapping Delay() to be nominated: %+v", nomFix)
	}
}

func TestReuseFirstProvesNewlyAddedGoTableRowWithoutContinuation(t *testing.T) {
	repo := t.TempDir()
	ctx := context.Background()
	if _, _, err := gitutil.Run(ctx, repo, nil, "init"); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/reuse\n\ngo 1.26\n"), 0o644)
	_ = os.WriteFile(filepath.Join(repo, "retry.go"), []byte("package reuse\n\nfunc Delay(v *int) int {\n\tif v == nil || *v == 0 {\n\t\treturn 100\n\t}\n\treturn *v\n}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(repo, "retry_test.go"), []byte(`package reuse

import "testing"

func intPtr(v int) *int { return &v }

func TestRetryDelay(t *testing.T) {
	cases := []struct {
		name string
		in   *int
		want int
	}{
		{name: "omitted_uses_default", in: nil, want: 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Delay(tc.in); got != tc.want {
				t.Fatalf("Delay = %d, want %d", got, tc.want)
			}
		})
	}
}
`), 0o644)
	brokenSHA := commitSeed(t, repo, "broken retry delay")

	// Agent fixes the bug AND adds the corrective row in the existing table test!
	_ = os.WriteFile(filepath.Join(repo, "retry.go"), []byte("package reuse\n\nfunc Delay(v *int) int {\n\tif v == nil {\n\t\treturn 100\n\t}\n\treturn *v\n}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(repo, "retry_test.go"), []byte(`package reuse

import "testing"

func intPtr(v int) *int { return &v }

func TestRetryDelay(t *testing.T) {
	cases := []struct {
		name string
		in   *int
		want int
	}{
		{name: "omitted_uses_default", in: nil, want: 100},
		{name: "explicit_zero_returns_zero", in: intPtr(0), want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Delay(tc.in); got != tc.want {
				t.Fatalf("Delay = %d, want %d", got, tc.want)
			}
		})
	}
}
`), 0o644)
	fixedSnap, err := gitutil.CaptureWorkingTreeSnapshot(ctx, repo, "")
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := ReuseFirst(ctx, repo, config.Default(), brokenSHA, fixedSnap, AuthoringBudgetCap)
	if err != nil {
		t.Fatalf("ReuseFirst failed: %v", err)
	}
	if outcome.Status != ReuseProven || outcome.Reflex == nil {
		t.Fatalf("expected ReuseProven, got %+v", outcome)
	}
	if outcome.Reflex.Artifact.Selector != "TestRetryDelay/explicit_zero_returns_zero" {
		t.Fatalf("unexpected selector: %s", outcome.Reflex.Artifact.Selector)
	}
	if outcome.Neighbor != "TestRetryDelay/omitted_uses_default" {
		t.Fatalf("unexpected neighbor: %s", outcome.Neighbor)
	}
	if _, err := os.Stat(filepath.Join(repo, ".flinch", "checks", outcome.Reflex.ID+".yml")); err != nil {
		t.Fatalf("expected saved reflex file: %v", err)
	}
}

func TestValidateManagedLineEnforcesAllFourD3Rules(t *testing.T) {
	before := "# Project\n\nKeep code simple.\n\n<!-- flinch:start -->\n- Existing rule one.\n<!-- flinch:end -->\n"

	// 1. Valid new line inside managed section.
	validAfter := "# Project\n\nKeep code simple.\n\n<!-- flinch:start -->\n- Existing rule one.\n- Always route renderer window calls through the IPC bridge.\n<!-- flinch:end -->\n"
	res := ValidateManagedLine([]byte(before), []byte(validAfter), 30)
	if !res.Changed || !res.Valid || len(res.AddedLines) != 1 {
		t.Fatalf("expected valid managed line, got %+v", res)
	}

	// 2. Modification outside managed section -> rejected!
	outsideEdit := strings.Replace(validAfter, "Keep code simple.", "Keep code very simple.", 1)
	resOut := ValidateManagedLine([]byte(before), []byte(outsideEdit), 30)
	if resOut.Valid || !resOut.FormattingErr {
		t.Fatalf("expected formatting error when editing outside managed section, got %+v", resOut)
	}

	// 3. Duplicate line -> rejected!
	dupEdit := "# Project\n\nKeep code simple.\n\n<!-- flinch:start -->\n- Existing rule one.\n- existing rule one.\n<!-- flinch:end -->\n"
	resDup := ValidateManagedLine([]byte(before), []byte(dupEdit), 30)
	if resDup.Valid || !resDup.FormattingErr {
		t.Fatalf("expected duplicate rejection, got %+v", resDup)
	}

	// 4. Section over cap -> SectionFull!
	resFull := ValidateManagedLine([]byte(before), []byte(validAfter), 1)
	if resFull.Valid || !resFull.SectionFull {
		t.Fatalf("expected SectionFull when cap exceeded, got %+v", resFull)
	}
}

func TestParseSkipFromText(t *testing.T) {
	reason, ok := ParseSkipFromText("Done.\nflinch skip: one-off wording preference for this screen")
	if !ok || reason != "one-off wording preference for this screen" {
		t.Fatalf("unexpected skip parse: ok=%v reason=%q", ok, reason)
	}
}

func TestReuseFirstProvesConfiguredLintRuleWithInScopeNeighbor(t *testing.T) {
	repo := t.TempDir()
	ctx := context.Background()
	if _, _, err := gitutil.Run(ctx, repo, nil, "init"); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(repo, "pkg", "app"), 0o755)
	_ = os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/lintreuse\n\ngo 1.26\n"), 0o644)
	_ = os.WriteFile(filepath.Join(repo, "pkg", "app", "neighbor.go"), []byte("package app\n\nimport \"fmt\"\n\nfunc Valid(n int) string { return fmt.Sprintf(\"%d\", n) }\n"), 0o644)
	_ = os.WriteFile(filepath.Join(repo, "pkg", "app", "bad.go"), []byte("package app\n\nimport \"fmt\"\n\nfunc Bad(s string) string { return fmt.Sprintf(\"%d\", s) }\n"), 0o644)
	brokenSHA := commitSeed(t, repo, "broken printf")

	_ = os.WriteFile(filepath.Join(repo, "pkg", "app", "bad.go"), []byte("package app\n\nimport \"fmt\"\n\nfunc Bad(s string) string { return fmt.Sprintf(\"%s\", s) }\n"), 0o644)
	fixedSnap, err := gitutil.CaptureWorkingTreeSnapshot(ctx, repo, "")
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Lint = []config.LintConfig{{Kind: "go-vet", Paths: []string{"**/*.go"}}}

	outcome, err := ReuseFirst(ctx, repo, cfg, brokenSHA, fixedSnap, AuthoringBudgetCap)
	if err != nil {
		t.Fatalf("ReuseFirst lint failed: %v", err)
	}
	if outcome.Status != ReuseProven || outcome.Reflex == nil {
		t.Fatalf("expected ReuseProven for configured go-vet rule, got %+v", outcome)
	}
	if outcome.Reflex.Artifact.Tool != "go-vet" || outcome.Reflex.Artifact.Rule != "printf" {
		t.Fatalf("unexpected lint reflex artifact: %+v", outcome.Reflex.Artifact)
	}
}
