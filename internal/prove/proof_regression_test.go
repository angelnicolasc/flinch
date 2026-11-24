package prove

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/reflex"
)

func proofRegressionSpec(broken, fixed string) CandidateSpec {
	return CandidateSpec{
		ID: "clamp-proof", BrokenRef: broken, FixedRef: fixed, Rung: reflex.RungTest,
		Artifact:         reflex.Artifact{Tool: "go-test", Path: "calc_test.go", Selector: "TestRegressionCase", ExpectedSignal: "expected clamp"},
		NeighborSelector: "TestClamp/below_max_unchanged",
	}
}

func regressionVerdict(t *testing.T, repo string, spec CandidateSpec) VerificationReport {
	t.Helper()
	r, err := Verify(context.Background(), repo, spec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRegressionProofRejectsOutsidePaths(t *testing.T) {
	repo := t.TempDir()
	for name, configure := range map[string]func(*CandidateSpec){
		"overlay":   func(s *CandidateSpec) { s.OverlayFiles = map[string]string{"../outside": "x"} },
		"source":    func(s *CandidateSpec) { s.AllowedCases = []AllowedCase{{SourceFile: "../outside"}} },
		"target":    func(s *CandidateSpec) { s.AllowedCases = []AllowedCase{{TargetPath: "../outside"}} },
		"artifact":  func(s *CandidateSpec) { s.Artifact.Path = "../outside" },
		"absolute":  func(s *CandidateSpec) { s.Artifact.Path = filepath.Join(repo, "outside") },
		"windows":   func(s *CandidateSpec) { s.Artifact.Path = "C:\\outside" },
		"separator": func(s *CandidateSpec) { s.Artifact.Path = "..\\outside" },
	} {
		t.Run(name, func(t *testing.T) {
			s := proofRegressionSpec("missing", "missing")
			configure(&s)
			r := regressionVerdict(t, repo, s)
			if r.Verdict != VerdictInconclusive || !strings.Contains(r.ActionableError, "repository-relative") {
				t.Fatalf("expected input path validation before resolving trees, got %s: %s", r.Verdict, r.ActionableError)
			}
		})
	}
}

func TestRegressionBrokenTestsUseIndependentTrees(t *testing.T) {
	repo, broken, fixed := setupGoCalcRepo(t)
	s := proofRegressionSpec(broken, fixed)
	s.OverlayFiles = map[string]string{"scope_test.go": `package calc
import ("os"; "testing")
func TestRegressionCase(t *testing.T) {
 if _, err := os.Stat("case-ran"); err == nil { t.Fatal("dirty proof directory") }
 if err := os.WriteFile("case-ran", []byte("ran"), 0600); err != nil { t.Fatal(err) }
 if Clamp(10, 5) != 5 { t.Fatal("expected clamp") }
}`}
	if r := regressionVerdict(t, repo, s); r.Verdict != VerdictVerified {
		t.Fatalf("clean independent replays should verify: %s/%s %s", r.Verdict, r.Reason, r.ActionableError)
	}
}

func TestRegressionProofBindsExpectedSignalToEachCase(t *testing.T) {
	repo, broken, fixed := setupGoCalcRepo(t)
	s := proofRegressionSpec(broken, fixed)
	s.OverlayFiles = map[string]string{"scope_test.go": `package calc
import ("fmt"; "os"; "testing")
func TestMain(m *testing.M) {
 code := m.Run()
 fmt.Println("expected clamp")
 os.Exit(code)
}
func TestRegressionCase(t *testing.T) {
 if Clamp(10, 5) != 5 { t.Fatal("unrelated failure") }
}`}
	if r := regressionVerdict(t, repo, s); r.Verdict != VerdictInconclusive || r.Reason != ReasonUnexpectedFailureSignal {
		t.Fatalf("suite output counted for selected case: %+v", r)
	}
	counter := filepath.Join(t.TempDir(), "previous-run")
	s.OverlayFiles["scope_test.go"] = fmt.Sprintf(`package calc
import ("os"; "testing")
func TestRegressionCase(t *testing.T) {
 if Clamp(10, 5) == 5 { return }
 if _, err := os.Stat(%[1]q); err == nil { t.Fatal("unrelated second failure") }
 if err := os.WriteFile(%[1]q, []byte("observed"), 0600); err != nil { t.Fatal(err) }
 t.Fatal("expected clamp")
}`, counter)
	if r := regressionVerdict(t, repo, s); r.Verdict != VerdictInconclusive || r.Reason != ReasonUnexpectedFailureSignal {
		t.Fatalf("second run supplied unrelated failure: %+v", r)
	}
}

func TestRegressionProofRejectsStableFixtureFailureWithoutAssertionSignal(t *testing.T) {
	repo, broken, fixed := setupGoCalcRepo(t)
	s := proofRegressionSpec(broken, fixed)
	s.Artifact.ExpectedSignal = ""
	s.Artifact.Path = "scope_test.go"
	s.OverlayFiles = map[string]string{"scope_test.go": `package calc
import ("fmt"; "testing")
func loadFixture() error {
 if Clamp(10,5) == 10 { return fmt.Errorf("fixture unavailable") }
 return nil
}
func TestRegressionCase(t *testing.T) {
 if err := loadFixture(); err != nil { t.Fatal(err) }
 if got := Clamp(3, 5); got != 3 { t.Fatalf("unexpected result %d", got) }
}`}
	if r := regressionVerdict(t, repo, s); r.Verdict != VerdictInconclusive || r.Reason != ReasonUnexpectedFailureSignal {
		t.Fatalf("repeatable fixture error credited as a behavior assertion: %+v", r)
	}
}

func TestRegressionGuardRequiresNeighborAndIndependentReplays(t *testing.T) {
	repo, broken, fixed := setupGoCalcRepo(t)
	ctx := context.Background()
	state, err := gitutil.FlinchStateDir(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "commands-enabled"), []byte("enabled\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "broken-dirs")
	s := CandidateSpec{
		ID: "guard-clamp", BrokenRef: broken, FixedRef: fixed, Rung: reflex.RungGuard,
		Artifact: reflex.Artifact{Tool: "command", Command: "go run ./guard", ExpectedSignal: "clamp missing"},
		OverlayFiles: map[string]string{"guard/main.go": fmt.Sprintf(`package main
import ("fmt"; "os"; "strings")
func main() {
 b, err := os.ReadFile("calc.go"); if err != nil { panic(err) }
 if strings.Contains(string(b), "if n > max") { return }
 cwd, err := os.Getwd(); if err != nil { panic(err) }
 f, err := os.OpenFile(%q, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600); if err != nil { panic(err) }
 fmt.Fprintln(f, cwd); f.Close()
 fmt.Println("clamp missing"); os.Exit(1)
}`, logPath)},
	}
	if r := regressionVerdict(t, repo, s); r.Verdict != VerdictInconclusive || r.Reason != ReasonMissingAllowedCase {
		t.Fatalf("guard accepted without neighbor: %+v", r)
	}
	_ = os.Remove(logPath)
	s.AllowedCases = []AllowedCase{{TargetPath: "calc.go", Content: "package calc\n// if n > max: allowed boundary\n"}}
	if r := regressionVerdict(t, repo, s); r.Verdict != VerdictVerified || len(r.AllowedResults) != 1 {
		t.Fatalf("guard did not verify with a neighbor: %+v", r)
	}
	contents, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	dirs := strings.Split(strings.TrimSpace(string(contents)), "\n")
	if len(dirs) != 2 || strings.TrimSpace(dirs[0]) == strings.TrimSpace(dirs[1]) {
		t.Fatalf("expected two independent broken trees, got %q", contents)
	}
}

func TestRegressionStaticToolErrorIsInconclusive(t *testing.T) {
	repo, _, _ := setupGoCalcRepo(t)
	path := filepath.Join(repo, "calc.go")
	if err := os.WriteFile(path, []byte("package calc\nimport \"fmt\"\nfunc Clamp(n, max int) int { fmt.Printf(\"%s\", n); return n }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	broken := commitAll(t, repo, "printf mismatch")
	if err := os.WriteFile(path, []byte("package calc\nfunc Clamp(n, max int) int { return missing }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixed := commitAll(t, repo, "unusable fixed tree")
	s := CandidateSpec{
		ID: "printf", BrokenRef: broken, FixedRef: fixed, Rung: reflex.RungLint, Scope: "**",
		Artifact:     reflex.Artifact{Tool: "go-vet", Rule: "printf"},
		AllowedCases: []AllowedCase{{TargetPath: "neighbor.go", Content: "package calc\n"}},
	}
	if r := regressionVerdict(t, repo, s); r.Verdict != VerdictInconclusive {
		t.Fatalf("failed vet invocation presented as evidence: %+v", r)
	}
}
