package check

import (
	"context"
	"github.com/angelnicolasc/flinch/internal/runner"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRegressionFailedSnapshotKeepsPreexistingDirtyDebtAdvisory(t *testing.T) {
	repo, cfg := setupCheckRepo(t)
	ctx := context.Background()
	p := filepath.Join(repo, "pkg", "app", "app.go")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	dirty := strings.Replace(string(b), "return fmt.Sprintf(\"%s\", s)", "return fmt.Sprintf(\"%d\", s)", 1)
	if err = os.WriteFile(p, []byte(dirty), 0644); err != nil {
		t.Fatal(err)
	}
	// A missing snapshot is the exact checkpoint state after an unsuccessful capture.
	if _, err = CaptureCheckpoint(ctx, repo, "dirty", repo, "", "", false); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(p, []byte(dirty+"\n// independent edit\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := RunTurn(ctx, repo, cfg, TurnOptions{SessionID: "dirty", WorktreeID: repo, Deadline: 30 * time.Second})
	if err != nil || got.Blocking || len(got.Findings) > 0 {
		t.Fatalf("dirty debt counted as new without snapshot: %+v, %v", got, err)
	}
}

func TestRegressionMissingSelectorDoesNotAttributeSiblingFailure(t *testing.T) {
	cases := runner.TestRunResult{Cases: []runner.TestCaseResult{{Name: "unrelated", Outcome: runner.OutcomeFail}}}
	if matches := failingTestCases(cases, "protected"); len(matches) > 0 {
		t.Fatalf("attributed sibling failure: %v", matches)
	}
}
