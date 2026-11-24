package check

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/angelnicolasc/flinch/internal/config"
	"github.com/angelnicolasc/flinch/internal/evidence"
	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/reflex"
)

func commitRepo(t *testing.T, repo, msg string) string {
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

func setupCheckRepo(t *testing.T) (string, config.Config) {
	t.Helper()
	repo := t.TempDir()
	ctx := context.Background()
	if _, _, err := gitutil.Run(ctx, repo, nil, "init"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	if err := config.Save(repo, cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/checkdemo\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "pkg", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".flinch", "cases", "vet-printf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "pkg", "app", "app.go"), []byte("package app\n\nimport \"fmt\"\n\nfunc Legacy(s string) string {\n\treturn fmt.Sprintf(\"%d\", s)\n}\n\nfunc Clean(s string) string {\n\treturn fmt.Sprintf(\"%s\", s)\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".flinch", "cases", "vet-printf", "broken.go"), []byte("package app\n\nimport \"fmt\"\n\nfunc BrokenCase(s string) string {\n\treturn fmt.Sprintf(\"%d\", s)\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".flinch", "cases", "vet-printf", "allowed.go"), []byte("package app\n\nimport \"fmt\"\n\nfunc AllowedCase(n int) string {\n\treturn fmt.Sprintf(\"%d\", n)\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := reflex.Reflex{
		ID:         "vet-printf",
		Version:    1,
		Owner:      "core",
		Scope:      "pkg/app/**",
		Rung:       reflex.RungLint,
		Obligation: "Format verbs must match argument types.",
		Artifact: reflex.Artifact{
			Tool:    "go-vet",
			Rule:    "printf",
			FixHint: "Use %s for strings and %d for integers.",
		},
		Cases: reflex.Cases{
			TargetPath: "pkg/app/flinch_case.go",
			Broken:     ".flinch/cases/vet-printf/broken.go",
			Allowed:    reflex.StringList{".flinch/cases/vet-printf/allowed.go"},
		},
		Surfaces: reflex.Surfaces{
			PullRequest: &reflex.PRSurface{Job: "ci-vet", Mode: "block"},
			AgentTurn:   &reflex.TurnSurface{Mode: "repair_if_supported"},
		},
	}
	if _, err := reflex.Save(repo, r); err != nil {
		t.Fatal(err)
	}
	commitRepo(t, repo, "initial with pre-existing debt in Legacy")
	return repo, cfg
}

func TestTurnCheckIgnoresPreExistingDebtBlocksNewViolationAndEnforcesLoopBudget(t *testing.T) {
	repo, cfg := setupCheckRepo(t)
	ctx := context.Background()

	// Capture checkpoint BEFORE editing (Legacy already has pre-existing printf debt).
	if _, err := CaptureCheckpoint(ctx, repo, "sess-1", repo, "edit Clean function", "p-1", true); err != nil {
		t.Fatalf("CaptureCheckpoint: %v", err)
	}

	// Edit Clean cleanly without touching Legacy: pre-existing debt in Legacy must NOT block!
	cleanEdit := "package app\n\nimport \"fmt\"\n\nfunc Legacy(s string) string {\n\treturn fmt.Sprintf(\"%d\", s)\n}\n\nfunc Clean(s string) string {\n\treturn fmt.Sprintf(\"hello %s\", s)\n}\n"
	if err := os.WriteFile(filepath.Join(repo, "pkg", "app", "app.go"), []byte(cleanEdit), 0o644); err != nil {
		t.Fatal(err)
	}
	resClean, err := RunTurn(ctx, repo, cfg, TurnOptions{SessionID: "sess-1", WorktreeID: repo, CheckIntg: true})
	if err != nil {
		t.Fatalf("RunTurn clean: %v", err)
	}
	if resClean.Blocking || len(resClean.Findings) != 0 {
		t.Fatalf("pre-existing debt should not block clean edit: %+v", resClean)
	}

	// Now introduce a NEW printf bug in Clean: MUST block on attempt 1!
	badEdit := "package app\n\nimport \"fmt\"\n\nfunc Legacy(s string) string {\n\treturn fmt.Sprintf(\"%d\", s)\n}\n\nfunc Clean(s string) string {\n\treturn fmt.Sprintf(\"hello %d\", s)\n}\n"
	if err := os.WriteFile(filepath.Join(repo, "pkg", "app", "app.go"), []byte(badEdit), 0o644); err != nil {
		t.Fatal(err)
	}
	resBad, err := RunTurn(ctx, repo, cfg, TurnOptions{SessionID: "sess-1", WorktreeID: repo, CheckIntg: true})
	if err != nil {
		t.Fatalf("RunTurn bad: %v", err)
	}
	if !resBad.Blocking || resBad.Decision != DecisionBlock || len(resBad.Findings) != 1 {
		t.Fatalf("expected block with 1 new finding, got %+v", resBad)
	}
	if !strings.Contains(resBad.DiagnosticMessage, "Use %s for strings") {
		t.Fatalf("expected fix hint in diagnostic message: %s", resBad.DiagnosticMessage)
	}

	// Repeating the exact same violation hits per-finding continuation budget (default 1) -> fail-open!
	resRepeat, err := RunTurn(ctx, repo, cfg, TurnOptions{SessionID: "sess-1", WorktreeID: repo, CheckIntg: true})
	if err != nil {
		t.Fatalf("RunTurn repeat: %v", err)
	}
	if resRepeat.Blocking || resRepeat.Decision != DecisionAllowBudgetExhausted {
		t.Fatalf("expected allow_budget_exhausted on repeated identical violation, got %+v", resRepeat)
	}
}

func TestIntegrityDetectsSuppressionAndReflexModification(t *testing.T) {
	repo, cfg := setupCheckRepo(t)
	ctx := context.Background()

	if _, err := CaptureCheckpoint(ctx, repo, "sess-int", repo, "fix", "p-2", true); err != nil {
		t.Fatal(err)
	}
	// Agent tries to add a nolint suppression comment and tamper with the reflex YAML.
	suppressed := "package app\n\nimport \"fmt\"\n\nfunc Legacy(s string) string {\n\treturn fmt.Sprintf(\"%d\", s) //nolint:printf\n}\n\nfunc Clean(s string) string {\n\treturn fmt.Sprintf(\"%s\", s)\n}\n"
	if err := os.WriteFile(filepath.Join(repo, "pkg", "app", "app.go"), []byte(suppressed), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := RunTurn(ctx, repo, cfg, TurnOptions{SessionID: "sess-int", WorktreeID: repo, CheckIntg: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Blocking || len(res.IntegrityViolations) == 0 {
		t.Fatalf("expected integrity violation on suppression comment, got %+v", res)
	}
	if res.IntegrityViolations[0].Kind != "suppression_added" {
		t.Fatalf("expected suppression_added, got %+v", res.IntegrityViolations)
	}
}

func TestAccreditVerifiesIntegratedSHAAndSeparatesTriggerPendingFromActive(t *testing.T) {
	repo, cfg := setupCheckRepo(t)
	ctx := context.Background()

	// Clean up pre-existing debt so the integrated SHA itself is clean.
	cleanApp := "package app\n\nimport \"fmt\"\n\nfunc Clean(s string) string {\n\treturn fmt.Sprintf(\"%s\", s)\n}\n"
	if err := os.WriteFile(filepath.Join(repo, "pkg", "app", "app.go"), []byte(cleanApp), 0o644); err != nil {
		t.Fatal(err)
	}
	sha := commitRepo(t, repo, "clean integrated state")

	// 1. Without PR trigger witness -> trigger_pending (never claims full CI active yet!).
	recs, err := Accredit(ctx, repo, cfg, AccreditOptions{
		IntegratedRef: sha,
		Surface:       evidence.SurfaceCI,
	})
	if err != nil {
		t.Fatalf("Accredit failed: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 accreditation record, got %d", len(recs))
	}
	if recs[0].Status != evidence.SurfaceTriggerPending {
		t.Fatalf("expected trigger_pending without PR witness, got %s (%s)", recs[0].Status, recs[0].Summary)
	}
	if !recs[0].NegativeControl.Failed || recs[0].NegativeControl.AttributedID != "printf" {
		t.Fatalf("expected negative control attributed to printf, got %+v", recs[0].NegativeControl)
	}
	if !recs[0].AllowedControl.Passed {
		t.Fatalf("expected allowed control pass, got %+v", recs[0].AllowedControl)
	}

	// 2. With real pull_request trigger witness -> active!
	recsActive, err := Accredit(ctx, repo, cfg, AccreditOptions{
		IntegratedRef:    sha,
		Surface:          evidence.SurfaceCI,
		RequiredForMerge: true,
		TriggerWitness: &evidence.TriggerWitness{
			Witnessed: true,
			Event:     "pull_request",
			Job:       "ci-vet",
			RunID:     "run-42",
		},
	})
	if err != nil {
		t.Fatalf("Accredit with witness failed: %v", err)
	}
	if recsActive[0].Status != evidence.SurfaceActive {
		t.Fatalf("expected active with PR witness, got %s (%s)", recsActive[0].Status, recsActive[0].Summary)
	}
}
