package prove

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/reflex"
)

func commitAll(t *testing.T, repo, msg string) string {
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

func setupGoCalcRepo(t *testing.T) (repo, brokenSHA, fixedSHA string) {
	t.Helper()
	repo = t.TempDir()
	ctx := context.Background()
	if _, _, err := gitutil.Run(ctx, repo, nil, "init"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/calc\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "calc.go"), []byte(`package calc

func Clamp(n, max int) int {
	// Bug: forgets to clamp values greater than max.
	return n
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	brokenSHA = commitAll(t, repo, "broken clamp")

	if err := os.WriteFile(filepath.Join(repo, "calc.go"), []byte(`package calc

func Clamp(n, max int) int {
	if n > max {
		return max
	}
	return n
}

func NewFixOnlyHelper() int { return 42 }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "calc_test.go"), []byte(`package calc

import "testing"

func TestClamp(t *testing.T) {
	t.Run("caps_above_max", func(t *testing.T) {
		if got := Clamp(10, 5); got != 5 {
			t.Fatalf("Clamp(10, 5) = %d, want 5", got)
		}
	})
	t.Run("below_max_unchanged", func(t *testing.T) {
		if got := Clamp(3, 5); got != 3 {
			t.Fatalf("Clamp(3, 5) = %d, want 3", got)
		}
	})
	t.Run("useless_always_passes", func(t *testing.T) {
		if got := Clamp(2, 5); got != 2 {
			t.Fatalf("got %d", got)
		}
	})
	t.Run("skips_on_broken", func(t *testing.T) {
		if Clamp(10, 5) != 5 {
			t.Skip("skipping when broken")
		}
	})
	t.Run("always_skipped", func(t *testing.T) {
		t.Skip("always skipped")
	})
	t.Run("always_fails", func(t *testing.T) {
		t.Fatalf("neighbor fails on fixed")
	})
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	fixedSHA = commitAll(t, repo, "fix clamp and add tests")
	return repo, brokenSHA, fixedSHA
}

func TestProveGoTestDistinguishesVerifiedRefutedSkippedAndCompileOnly(t *testing.T) {
	repo, brokenSHA, fixedSHA := setupGoCalcRepo(t)
	ctx := context.Background()

	// 1. Valid regression test with legitimate neighbor -> Verified.
	rep, err := Verify(ctx, repo, CandidateSpec{
		ID:         "clamp-caps-above-max",
		Version:    1,
		Rung:       reflex.RungTest,
		Obligation: "Clamp must cap values above max.",
		Artifact: reflex.Artifact{
			Tool:           "go-test",
			Path:           "calc_test.go",
			Selector:       "TestClamp/caps_above_max",
			ExpectedSignal: "want 5",
		},
		NeighborSelector: "TestClamp/below_max_unchanged",
		BrokenRef:        brokenSHA,
		FixedRef:         fixedSHA,
	}, Options{})
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if rep.Verdict != VerdictVerified {
		t.Fatalf("expected verified, got %s (reason=%s, err=%s)", rep.Verdict, rep.Reason, rep.ActionableError)
	}
	if len(rep.AllowedResults) != 1 || !rep.AllowedResults[0].Passed {
		t.Fatalf("expected recorded passing neighbor result, got %+v", rep.AllowedResults)
	}

	// 2. Weak test that also passes on broken tree -> Refuted (passed_on_broken).
	repRefuted, err := Verify(ctx, repo, CandidateSpec{
		ID:         "clamp-weak",
		Version:    1,
		Rung:       reflex.RungTest,
		Obligation: "Clamp must cap values above max.",
		Artifact: reflex.Artifact{
			Tool:     "go-test",
			Path:     "calc_test.go",
			Selector: "TestClamp/useless_always_passes",
		},
		NeighborSelector: "TestClamp/below_max_unchanged",
		BrokenRef:        brokenSHA,
		FixedRef:         fixedSHA,
	}, Options{})
	if err != nil {
		t.Fatalf("Verify weak failed: %v", err)
	}
	if repRefuted.Verdict != VerdictRefuted || repRefuted.Reason != ReasonPassedOnBroken {
		t.Fatalf("expected refuted/passed_on_broken, got %s/%s", repRefuted.Verdict, repRefuted.Reason)
	}

	// 3. Skip->pass test -> Inconclusive (skipped_on_broken), NEVER verified!
	repSkip, err := Verify(ctx, repo, CandidateSpec{
		ID:         "clamp-skip",
		Version:    1,
		Rung:       reflex.RungTest,
		Obligation: "Clamp must cap values above max.",
		Artifact: reflex.Artifact{
			Tool:     "go-test",
			Path:     "calc_test.go",
			Selector: "TestClamp/skips_on_broken",
		},
		NeighborSelector: "TestClamp/below_max_unchanged",
		BrokenRef:        brokenSHA,
		FixedRef:         fixedSHA,
	}, Options{})
	if err != nil {
		t.Fatalf("Verify skip failed: %v", err)
	}
	if repSkip.Verdict != VerdictInconclusive || repSkip.Reason != ReasonSkippedOnBroken {
		t.Fatalf("expected inconclusive/skipped_on_broken, got %s/%s", repSkip.Verdict, repSkip.Reason)
	}

	// 4. Test referencing fix-only API -> Inconclusive (compile_only).
	repCompile, err := Verify(ctx, repo, CandidateSpec{
		ID:         "clamp-compile-only",
		Version:    1,
		Rung:       reflex.RungTest,
		Obligation: "Test calls API added by fix.",
		Artifact: reflex.Artifact{
			Tool:     "go-test",
			Path:     "api_test.go",
			Selector: "TestNewHelper",
		},
		OverlayFiles: map[string]string{
			"api_test.go": "package calc\nimport \"testing\"\nfunc TestNewHelper(t *testing.T) { if NewFixOnlyHelper() != 42 { t.Fatal() } }\n",
		},
		NeighborSelector: "TestClamp/below_max_unchanged",
		BrokenRef:        brokenSHA,
		FixedRef:         fixedSHA,
	}, Options{})
	if err != nil {
		t.Fatalf("Verify compile-only failed: %v", err)
	}
	if repCompile.Verdict != VerdictInconclusive || repCompile.Reason != ReasonCompileOnly {
		t.Fatalf("expected inconclusive/compile_only, got %s/%s (%s)", repCompile.Verdict, repCompile.Reason, repCompile.ActionableError)
	}
}

// Regression tests:
// - Empty selector never matches every test case; returns inconclusive/missing_selector.
// - Test proof without a legitimate neighbor never returns verified; returns inconclusive/missing_allowed_case.
// - Skipped or failing neighbor prevents verification.
func TestProveTestEnforcesExactSelectorAndRealNeighborRun(t *testing.T) {
	repo, brokenSHA, fixedSHA := setupGoCalcRepo(t)
	ctx := context.Background()

	// Direct unit check on matchTestCases with empty selector.
	if matched := matchTestCases(nil, ""); len(matched) != 0 {
		t.Fatalf("empty selector must not match any cases")
	}

	// 1. Empty selector -> inconclusive/missing_selector.
	repEmpty, err := Verify(ctx, repo, CandidateSpec{
		ID:               "empty-sel",
		Version:          1,
		Rung:             reflex.RungTest,
		Obligation:       "Must specify selector.",
		Artifact:         reflex.Artifact{Tool: "go-test", Path: "calc_test.go", Selector: ""},
		NeighborSelector: "TestClamp/below_max_unchanged",
		BrokenRef:        brokenSHA,
		FixedRef:         fixedSHA,
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if repEmpty.Verdict != VerdictInconclusive || repEmpty.Reason != ReasonMissingSelector {
		t.Fatalf("expected inconclusive/missing_selector, got %s/%s", repEmpty.Verdict, repEmpty.Reason)
	}

	// 2. Missing neighbor -> inconclusive/missing_allowed_case.
	repNoNeighbor, err := Verify(ctx, repo, CandidateSpec{
		ID:         "no-neighbor",
		Version:    1,
		Rung:       reflex.RungTest,
		Obligation: "Clamp caps above max.",
		Artifact:   reflex.Artifact{Tool: "go-test", Path: "calc_test.go", Selector: "TestClamp/caps_above_max"},
		BrokenRef:  brokenSHA,
		FixedRef:   fixedSHA,
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if repNoNeighbor.Verdict != VerdictInconclusive || repNoNeighbor.Reason != ReasonMissingAllowedCase {
		t.Fatalf("expected inconclusive/missing_allowed_case without neighbor, got %s/%s", repNoNeighbor.Verdict, repNoNeighbor.Reason)
	}

	// 3. Skipped neighbor -> inconclusive/neighbor_skipped.
	repSkipNeighbor, err := Verify(ctx, repo, CandidateSpec{
		ID:               "skip-neighbor",
		Version:          1,
		Rung:             reflex.RungTest,
		Obligation:       "Clamp caps above max.",
		Artifact:         reflex.Artifact{Tool: "go-test", Path: "calc_test.go", Selector: "TestClamp/caps_above_max"},
		NeighborSelector: "TestClamp/always_skipped",
		BrokenRef:        brokenSHA,
		FixedRef:         fixedSHA,
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if repSkipNeighbor.Verdict != VerdictInconclusive || repSkipNeighbor.Reason != ReasonNeighborSkipped {
		t.Fatalf("expected inconclusive/neighbor_skipped, got %s/%s", repSkipNeighbor.Verdict, repSkipNeighbor.Reason)
	}

	// 4. Failing neighbor -> refuted/neighbor_failed.
	repFailNeighbor, err := Verify(ctx, repo, CandidateSpec{
		ID:               "fail-neighbor",
		Version:          1,
		Rung:             reflex.RungTest,
		Obligation:       "Clamp caps above max.",
		Artifact:         reflex.Artifact{Tool: "go-test", Path: "calc_test.go", Selector: "TestClamp/caps_above_max"},
		NeighborSelector: "TestClamp/always_fails",
		BrokenRef:        brokenSHA,
		FixedRef:         fixedSHA,
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if repFailNeighbor.Verdict != VerdictRefuted || repFailNeighbor.Reason != ReasonNeighborFailed {
		t.Fatalf("expected refuted/neighbor_failed, got %s/%s", repFailNeighbor.Verdict, repFailNeighbor.Reason)
	}
}

func TestProveLintWithOutOfScopeAllowedCaseReturnsInconclusive(t *testing.T) {
	repo := t.TempDir()
	ctx := context.Background()
	if _, _, err := gitutil.Run(ctx, repo, nil, "init"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/vetdemo\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "pkg", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "pkg", "app", "app.go"), []byte("package app\n\nimport \"fmt\"\n\nfunc Greet(s string) string { return fmt.Sprintf(\"%d\", s) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	brokenSHA := commitAll(t, repo, "broken printf")

	if err := os.WriteFile(filepath.Join(repo, "pkg", "app", "app.go"), []byte("package app\n\nimport \"fmt\"\n\nfunc Greet(s string) string { return fmt.Sprintf(\"%s\", s) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixedSHA := commitAll(t, repo, "fix printf")

	// Allowed case outside scope must be rejected as inconclusive.
	rep, err := Verify(ctx, repo, CandidateSpec{
		ID:         "vet-printf",
		Version:    1,
		Rung:       reflex.RungLint,
		Scope:      "pkg/app/**",
		Obligation: "Format verbs must match argument types.",
		Artifact: reflex.Artifact{
			Tool: "go-vet",
			Rule: "printf",
		},
		AllowedCases: []AllowedCase{
			{
				Name:       "wrong-dir",
				TargetPath: "other/outside.go",
				Content:    "package other\n",
			},
		},
		BrokenRef: brokenSHA,
		FixedRef:  fixedSHA,
	}, Options{})
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if rep.Verdict != VerdictInconclusive || rep.Reason != ReasonAllowedCaseOutOfScope {
		t.Fatalf("expected inconclusive/allowed_case_out_of_scope, got %s/%s (%s)", rep.Verdict, rep.Reason, rep.ActionableError)
	}

	// Same check with in-scope valid allowed case -> Verified!
	repOK, err := Verify(ctx, repo, CandidateSpec{
		ID:         "vet-printf",
		Version:    1,
		Rung:       reflex.RungLint,
		Scope:      "pkg/app/**",
		Obligation: "Format verbs must match argument types.",
		Artifact: reflex.Artifact{
			Tool: "go-vet",
			Rule: "printf",
		},
		AllowedCases: []AllowedCase{
			{
				Name:       "valid-in-scope",
				TargetPath: "pkg/app/allowed_case.go",
				Content:    "package app\n\nimport \"fmt\"\n\nfunc ValidLog(n int) string { return fmt.Sprintf(\"%d\", n) }\n",
			},
		},
		BrokenRef: brokenSHA,
		FixedRef:  fixedSHA,
	}, Options{})
	if err != nil {
		t.Fatalf("Verify valid failed: %v", err)
	}
	if repOK.Verdict != VerdictVerified {
		t.Fatalf("expected verified, got %s/%s (%s)", repOK.Verdict, repOK.Reason, repOK.ActionableError)
	}
}
