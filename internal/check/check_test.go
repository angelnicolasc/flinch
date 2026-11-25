package check

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/angelnicolasc/flinch/internal/config"
	"github.com/angelnicolasc/flinch/internal/evidence"
	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/reflex"
	"github.com/angelnicolasc/flinch/internal/runner"
)

func TestTurnReportsWhenCurrentOrBaselineCommandNeedsApproval(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	if _, _, err := gitutil.Run(ctx, repo, nil, "init"); err != nil {
		t.Fatal(err)
	}
	check := reflex.Reflex{
		ID: "guard", Version: 1, Obligation: "Report a sample finding", Scope: "guard.txt",
		Artifact: reflex.Artifact{Tool: "command", Path: "guard.txt", Command: "echo guard.txt:1:1: sample finding"},
	}
	if _, err := reflex.Save(repo, check); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, "guard.txt")
	if err := os.WriteFile(path, []byte("baseline\n"), 0600); err != nil {
		t.Fatal(err)
	}
	commitRepo(t, repo, "Add guard")
	opts := TurnOptions{SessionID: "approval", WorktreeID: repo, Deadline: 30 * time.Second}
	if _, err := CaptureCheckpoint(ctx, repo, opts.SessionID, repo, "edit guard", "p-1", true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("current\n"), 0600); err != nil {
		t.Fatal(err)
	}
	requireNotice := func() {
		t.Helper()
		got, err := RunTurn(ctx, repo, config.Default(), opts)
		if err != nil || got.Blocking || len(got.Findings) != 0 || len(got.SkippedControls) != 1 ||
			got.SkippedControls[0].Reason != runner.CommandPermissionNotice {
			t.Fatalf("missing advisory approval notice: %+v err=%v", got, err)
		}
	}
	requireNotice()
	if _, err := runner.EnableCommands(ctx, repo); err != nil {
		t.Fatal(err)
	}
	// The current check can now report a finding, but the baseline still has unapproved bytes.
	requireNotice()
}

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

// Regression test: If baseline worktree creation or baseline scan fails,
// Flinch must fail open and NEVER treat pre-existing debt as new findings.
func TestTurnCheckFailsOpenWhenBaselineComparisonFails(t *testing.T) {
	repo, cfg := setupCheckRepo(t)
	ctx := context.Background()

	cp, err := CaptureCheckpoint(ctx, repo, "sess-base-fail", repo, "", "p-base", false)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt snapshot ref so baseline tree materialization fails.
	stateDir, err := gitutil.FlinchStateDir(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	cp.SnapshotSHA = "000000000000000000000000000000000000dead"
	if err := SaveCheckpoint(stateDir, cp); err != nil {
		t.Fatal(err)
	}

	// Make a clean edit to app.go while Legacy still holds pre-existing debt.
	cleanEdit := "package app\n\nimport \"fmt\"\n\nfunc Legacy(s string) string {\n\treturn fmt.Sprintf(\"%d\", s)\n}\n\nfunc Clean(s string) string {\n\treturn fmt.Sprintf(\"clean %s\", s)\n}\n"
	if err := os.WriteFile(filepath.Join(repo, "pkg", "app", "app.go"), []byte(cleanEdit), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := RunTurn(ctx, repo, cfg, TurnOptions{SessionID: "sess-base-fail", WorktreeID: repo, CheckIntg: false})
	if err != nil {
		t.Fatalf("RunTurn failed: %v", err)
	}
	if res.Blocking || len(res.Findings) != 0 {
		t.Fatalf("expected fail-open without blocking pre-existing debt when baseline fails, got %+v", res)
	}
	if len(res.SkippedControls) == 0 {
		t.Fatalf("expected skipped control recording baseline failure")
	}
}

// Regression test: A test reflex that was already failing at checkpoint never blocks;
// once passing at checkpoint and broken during the turn, it blocks.
func TestTurnCheckPreExistingFailingTestDoesNotBlock(t *testing.T) {
	repo := t.TempDir()
	ctx := context.Background()
	if _, _, err := gitutil.Run(ctx, repo, nil, "init"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	if err := config.Save(repo, cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/testbase\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Initial commit already has a failing test.
	if err := os.WriteFile(filepath.Join(repo, "retry.go"), []byte("package retry\n\nfunc Delay(v *int) int {\n\tif v == nil || *v == 0 {\n\t\treturn 100\n\t}\n\treturn *v\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "retry_test.go"), []byte("package retry\n\nimport \"testing\"\n\nfunc TestDelay(t *testing.T) {\n\tt.Run(\"explicit_zero\", func(t *testing.T) {\n\t\tz := 0\n\t\tif got := Delay(&z); got != 0 {\n\t\t\tt.Fatalf(\"got %d, want 0\", got)\n\t\t}\n\t})\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := reflex.Save(repo, reflex.Reflex{
		ID:         "retry-zero",
		Version:    1,
		Scope:      "*.go",
		Rung:       reflex.RungTest,
		Obligation: "Explicit zero delay must remain zero.",
		Artifact: reflex.Artifact{
			Tool:     "go-test",
			Path:     "retry_test.go",
			Selector: "TestDelay/explicit_zero",
		},
		Proof: &reflex.ProofSummary{Verdict: "verified", RuntimeMS: 200},
		Surfaces: reflex.Surfaces{
			AgentTurn: &reflex.TurnSurface{Mode: "repair_if_supported"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	commitRepo(t, repo, "initial with pre-existing failing test")

	// Checkpoint while test is ALREADY failing, then touch retry.go without fixing it.
	if _, err := CaptureCheckpoint(ctx, repo, "sess-pre", repo, "", "p1", true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "retry.go"), []byte("package retry\n\n// comment\nfunc Delay(v *int) int {\n\tif v == nil || *v == 0 {\n\t\treturn 100\n\t}\n\treturn *v\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resPre, err := RunTurn(ctx, repo, cfg, TurnOptions{SessionID: "sess-pre", WorktreeID: repo, CheckIntg: false})
	if err != nil {
		t.Fatalf("RunTurn pre-existing fail: %v", err)
	}
	if resPre.Blocking || len(resPre.Findings) != 0 {
		t.Fatalf("pre-existing failing test must NOT block turn, got %+v", resPre)
	}

	// Fix retry.go and commit so test passes at baseline.
	if err := os.WriteFile(filepath.Join(repo, "retry.go"), []byte("package retry\n\nfunc Delay(v *int) int {\n\tif v == nil {\n\t\treturn 100\n\t}\n\treturn *v\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitRepo(t, repo, "fix retry zero")

	// New checkpoint where test passes, then regress retry.go during the turn -> MUST block!
	if _, err := CaptureCheckpoint(ctx, repo, "sess-reg", repo, "", "p2", true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "retry.go"), []byte("package retry\n\nfunc Delay(v *int) int {\n\tif v == nil || *v == 0 {\n\t\treturn 100\n\t}\n\treturn *v\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resReg, err := RunTurn(ctx, repo, cfg, TurnOptions{SessionID: "sess-reg", WorktreeID: repo, CheckIntg: false})
	if err != nil {
		t.Fatalf("RunTurn regression: %v", err)
	}
	if !resReg.Blocking || len(resReg.Findings) != 1 {
		t.Fatalf("newly broken test MUST block turn, got %+v", resReg)
	}
}

// Regression test: The unified deadline covers startup and fails open on expiration.
func TestTurnCheckDeadlineCoversStartup(t *testing.T) {
	repo, cfg := setupCheckRepo(t)
	ctx := context.Background()

	res, err := RunTurn(ctx, repo, cfg, TurnOptions{
		SessionID:  "sess-timeout",
		WorktreeID: repo,
		Deadline:   1 * time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("expected fail-open nil error on startup deadline expiry, got err=%v", err)
	}
	if res.Blocking || res.Decision != DecisionAllowTimeout {
		t.Fatalf("expected allow_timeout non-blocking decision, got %+v", res)
	}
}

// Regression test: The 20s per-turn test budget persists across continuations and does not reset.
func TestTurnCheckLogicalTurnTestBudgetPersistsAcrossContinuations(t *testing.T) {
	repo := t.TempDir()
	ctx := context.Background()
	if _, _, err := gitutil.Run(ctx, repo, nil, "init"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Loop.TestBudget = "100ms"
	if err := config.Save(repo, cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/budget\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package a\nfunc V() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "a_test.go"), []byte("package a\nimport \"testing\"\nfunc TestV(t *testing.T) { if V() != 1 { t.Fatal() } }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := reflex.Save(repo, reflex.Reflex{
		ID:         "r-v",
		Version:    1,
		Scope:      "*.go",
		Rung:       reflex.RungTest,
		Obligation: "V returns 1.",
		Artifact:   reflex.Artifact{Tool: "go-test", Path: "a_test.go", Selector: "TestV"},
		Proof:      &reflex.ProofSummary{Verdict: "verified", RuntimeMS: 50},
	}); err != nil {
		t.Fatal(err)
	}
	commitRepo(t, repo, "init")

	cp, err := CaptureCheckpoint(ctx, repo, "sess-bud", repo, "", "p", true)
	if err != nil {
		t.Fatal(err)
	}
	stateDir, err := gitutil.FlinchStateDir(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	cp.TestBudgetUsedMS = 120 // Exceeds 100ms budget from prior continuation
	if err := SaveCheckpoint(stateDir, cp); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package a\nfunc V() int { return 2 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := RunTurn(ctx, repo, cfg, TurnOptions{SessionID: "sess-bud", WorktreeID: repo})
	if err != nil {
		t.Fatalf("RunTurn budget: %v", err)
	}
	if res.Blocking || len(res.SkippedControls) == 0 || !strings.Contains(res.SkippedControls[0].Reason, "budget exhausted") {
		t.Fatalf("expected skipped test reflex due to exhausted logical turn budget, got %+v", res)
	}
}

// Regression test: Deleting .flinch/checks/<id>.yml must be detected as check_deleted.
func TestIntegrityDetectsDeletedReflexRecord(t *testing.T) {
	repo, cfg := setupCheckRepo(t)
	ctx := context.Background()

	if _, err := CaptureCheckpoint(ctx, repo, "sess-del", repo, "", "p-del", true); err != nil {
		t.Fatal(err)
	}
	reflexPath := filepath.Join(repo, reflex.ReflexesRelDir, "vet-printf.yml")
	if err := os.Remove(reflexPath); err != nil {
		t.Fatal(err)
	}

	res, err := RunTurn(ctx, repo, cfg, TurnOptions{SessionID: "sess-del", WorktreeID: repo, CheckIntg: true})
	if err != nil {
		t.Fatalf("RunTurn after reflex delete: %v", err)
	}
	if !res.Blocking || len(res.IntegrityViolations) == 0 {
		t.Fatalf("expected blocking check_deleted violation, got %+v", res)
	}
	foundDeleted := false
	for _, v := range res.IntegrityViolations {
		if v.Kind == "check_deleted" && v.ReflexID == "vet-printf" {
			foundDeleted = true
		}
	}
	if !foundDeleted {
		t.Fatalf("expected check_deleted for vet-printf, got %+v", res.IntegrityViolations)
	}
}

// Regression test: Integrity compares only the protected case in a table-driven test.
// Adding a new unrelated row passes; editing the protected row or weakening assertions fails.
func TestIntegrityTableTestAllowsNewRowFlagsProtectedRowOrAssertionChange(t *testing.T) {
	repo := t.TempDir()
	ctx := context.Background()
	if _, _, err := gitutil.Run(ctx, repo, nil, "init"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	if err := config.Save(repo, cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/tbl\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	baseTest := `package tbl

import "testing"

func TestRetryDelay(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{name: "explicit_zero", in: 0, want: 0},
		{name: "omitted_default", in: -1, want: 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.in == 0 && tc.want != 0 {
				t.Fatalf("want 0")
			}
		})
	}
}
`
	if err := os.WriteFile(filepath.Join(repo, "retry_test.go"), []byte(baseTest), 0o644); err != nil {
		t.Fatal(err)
	}
	r := reflex.Reflex{
		ID:         "retry-zero",
		Version:    1,
		Scope:      "*.go",
		Rung:       reflex.RungTest,
		Obligation: "Explicit zero delay remains zero.",
		Artifact:   reflex.Artifact{Tool: "go-test", Path: "retry_test.go", Selector: "TestRetryDelay/explicit_zero"},
	}
	if _, err := reflex.Save(repo, r); err != nil {
		t.Fatal(err)
	}
	commitRepo(t, repo, "initial table test")

	// 1. Add a new unrelated row to the SAME table -> 0 integrity violations!
	withNewRow := strings.Replace(baseTest,
		`{name: "omitted_default", in: -1, want: 100},`,
		"{name: \"omitted_default\", in: -1, want: 100},\n\t\t{name: \"custom_50\", in: 50, want: 50},",
		1,
	)
	if err := os.WriteFile(filepath.Join(repo, "retry_test.go"), []byte(withNewRow), 0o644); err != nil {
		t.Fatal(err)
	}
	viols, err := CheckIntegrity(ctx, repo, "HEAD", []reflex.Reflex{r}, []string{"retry_test.go"})
	if err != nil {
		t.Fatalf("CheckIntegrity with new row: %v", err)
	}
	if len(viols) != 0 {
		t.Fatalf("adding unrelated table row must NOT trigger integrity violation, got %+v", viols)
	}

	// 2. Modify the protected row itself -> MUST trigger artifact_modified!
	mutatedRow := strings.Replace(baseTest,
		`{name: "explicit_zero", in: 0, want: 0},`,
		`{name: "explicit_zero", in: 0, want: 100},`,
		1,
	)
	if err := os.WriteFile(filepath.Join(repo, "retry_test.go"), []byte(mutatedRow), 0o644); err != nil {
		t.Fatal(err)
	}
	viols, err = CheckIntegrity(ctx, repo, "HEAD", []reflex.Reflex{r}, []string{"retry_test.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(viols) == 0 || viols[0].Kind != "artifact_modified" {
		t.Fatalf("modifying protected row must trigger artifact_modified, got %+v", viols)
	}

	// 3. Keep protected row intact, but weaken assertion in loop body -> MUST trigger artifact_modified!
	weakenedLoop := strings.Replace(baseTest, `t.Fatalf("want 0")`, `t.Log("ignored")`, 1)
	if err := os.WriteFile(filepath.Join(repo, "retry_test.go"), []byte(weakenedLoop), 0o644); err != nil {
		t.Fatal(err)
	}
	viols, err = CheckIntegrity(ctx, repo, "HEAD", []reflex.Reflex{r}, []string{"retry_test.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(viols) == 0 || viols[0].Kind != "artifact_modified" {
		t.Fatalf("weakening assertion loop must trigger artifact_modified, got %+v", viols)
	}
}

// Regression tests:
// - Default checkpoint stores no prompt text on disk, only PromptHash and identifiers.
// - CleanupExpired enforces retention_days across checkpoints, proof trees, and local event logs.
func TestCheckpointStoresNoPromptTextByDefaultAndRetentionCleanupApplies(t *testing.T) {
	repo, _ := setupCheckRepo(t)
	ctx := context.Background()

	secretPrompt := "Use the IPC bridge, not direct main import; token=ghp_1234567890abcdefghij1234567890"
	cp, err := CaptureCheckpoint(ctx, repo, "sess-priv", repo, secretPrompt, "pid-1", false)
	if err != nil {
		t.Fatalf("CaptureCheckpoint: %v", err)
	}
	if cp.Prompt != "" {
		t.Fatalf("default checkpoint must store empty prompt text, got %q", cp.Prompt)
	}
	if cp.PromptHash == "" {
		t.Fatalf("expected non-empty PromptHash")
	}

	stateDir, err := gitutil.FlinchStateDir(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(checkpointPath(stateDir, "sess-priv", repo))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "IPC bridge") || strings.Contains(string(raw), "ghp_") {
		t.Fatalf("checkpoint file on disk leaked prompt text: %s", string(raw))
	}

	// Seed an expired checkpoint, an expired proof tree, and an expired + fresh log line.
	oldTime := time.Now().UTC().Add(-45 * 24 * time.Hour)
	oldCP := cp
	oldCP.SessionID = "sess-old"
	oldCP.StartedAt = oldTime
	if err := SaveCheckpoint(stateDir, oldCP); err != nil {
		t.Fatal(err)
	}
	oldTree := filepath.Join(stateDir, "proof-trees", "stale-tree")
	if err := os.MkdirAll(oldTree, 0o755); err != nil {
		t.Fatal(err)
	}
	leases := filepath.Join(stateDir, "proof-leases")
	if err := os.MkdirAll(leases, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(leases, "stale-tree"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(oldTree, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	logDir := filepath.Join(stateDir, "log")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	freshTime := time.Now().UTC().Add(-2 * time.Hour)
	logLines := `{"timestamp":"` + oldTime.Format(time.RFC3339) + `","kind":"turn_checked"}` + "\n" +
		`{"timestamp":"` + freshTime.Format(time.RFC3339) + `","kind":"turn_checked"}` + "\n"
	if err := os.WriteFile(filepath.Join(logDir, "events.jsonl"), []byte(logLines), 0o644); err != nil {
		t.Fatal(err)
	}

	cl, err := CleanupExpired(stateDir, 30, time.Now().UTC())
	if err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}
	if cl.RemovedCheckpoints != 1 || cl.RemovedProofTrees != 1 || cl.PrunedLogEvents != 1 {
		t.Fatalf("expected 1 removed checkpoint, 1 proof tree, 1 log event; got %+v", cl)
	}
	// Verify fresh checkpoint and fresh log event survive!
	if _, ok, _ := LoadCheckpoint(stateDir, "sess-priv", repo); !ok {
		t.Fatalf("fresh checkpoint should not have been removed")
	}
	remainingLog, _ := os.ReadFile(filepath.Join(logDir, "events.jsonl"))
	if strings.Contains(string(remainingLog), oldTime.Format(time.RFC3339)) || !strings.Contains(string(remainingLog), freshTime.Format(time.RFC3339)) {
		t.Fatalf("unexpected remaining log after retention cleanup: %s", string(remainingLog))
	}
}
