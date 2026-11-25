package author_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/angelnicolasc/flinch/internal/check"
	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/hook"
	"github.com/angelnicolasc/flinch/internal/reflex"
	"github.com/angelnicolasc/flinch/internal/testutil"
)

func TestCorrectionProtectsNextTurnWithoutCommit(t *testing.T) {
	for _, scenario := range []string{"product", "rewrite_and_exhaust", "repair_then_green", "nomination_hunk", "renamed_hunk", "explicit_skip"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			repo := t.TempDir()
			testutil.HookProfile(t, repo)
			fixtureGit(t, repo, "init")
			repo, err := gitutil.RepoRoot(ctx, repo)
			if err != nil {
				t.Fatal(err)
			}
			const fixed = `package retry

func RetryDelay(d *int) int {
	if d == nil {
		return 1
	}
	return *d
}
`
			const omitted = `{"omitted", nil, 1},`
			const zero = `{"zero", new(int), 0},`
			const table = `package retry

import "testing"

func TestRetryDelay(t *testing.T) {
	cases := []struct {
		name string
		d    *int
		want int
	}{
		{"omitted", nil, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RetryDelay(tc.d); got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
}
`
			broken := strings.Replace(fixed, "d == nil", "d == nil || *d == 0", 1)
			rows := omitted
			base := table
			if scenario == "nomination_hunk" {
				// Both changed expectations would pass proof. Only the zero row
				// was inserted by B. The unchanged row separates the two hunks.
				broken = strings.Replace(broken, "*d == 0", "*d <= 0", 1)
				base = strings.Replace(base, "cases :=", "negative := -1\n\tcases :=", 1)
				rows += "\n\t\t{\"negative\", &negative, 1},\n\t\t{\"other_default\", nil, 1},"
				base = strings.Replace(base, omitted, rows, 1)
			}
			wrong := strings.Replace(zero, ", 0}", ", 1}", 1)
			if scenario == "renamed_hunk" {
				wrong = strings.Replace(wrong, `"zero"`, `"default_zero"`, 1)
			}
			tableB := strings.Replace(base, rows, rows+"\n\t\t"+wrong, 1)
			tableC := strings.Replace(tableB, wrong, zero, 1)
			if scenario == "nomination_hunk" {
				tableC = strings.Replace(tableC, `{"negative", &negative, 1}`, `{"negative", &negative, -1}`, 1)
			}
			if scenario == "renamed_hunk" {
				// Both names are new in C. Only zero replaces a row added by B.
				tableC = strings.Replace(tableC, omitted, "{\"additional_zero\", new(int), 0},\n\t\t"+omitted, 1)
			}
			fixtureWrite(t, repo, "go.mod", "module example.com/retry\n\ngo 1.26\n")
			fixtureWrite(t, repo, "retry.go", broken)
			fixtureWrite(t, repo, "retry_test.go", base)
			fixtureCommit(t, repo, "Add retry delay", ".")
			head := fixtureGit(t, repo, "rev-parse", "HEAD")
			host := hook.HostClaude
			if scenario == "rewrite_and_exhaust" {
				host = hook.HostCodex
			}
			session := "retry"
			prompt := func(message string) { fixtureEvent(t, repo, host, session, "UserPromptSubmit", message, "") }
			stop := func(message string) hook.Response { return fixtureEvent(t, repo, host, session, "Stop", "", message) }
			records := func() []reflex.Reflex {
				t.Helper()
				records, err := reflex.LoadAll(repo)
				if err != nil {
					t.Fatal(err)
				}
				return records
			}
			prompt("Cover an explicit zero delay using the default.")
			fixtureWrite(t, repo, "retry_test.go", tableB)
			if b := stop("Covered the default."); len(b.Stdout) != 0 || len(records()) != 0 {
				t.Fatalf("B unexpectedly requested work or registered a check: %s", b.Stdout)
			}
			prompt("An explicit zero should retry immediately. Keep the default when omitted.")
			fixtureWrite(t, repo, "retry.go", fixed)
			testPath := "retry_test.go"
			if scenario == "renamed_hunk" {
				testPath = "delay_test.go"
				if err := os.Rename(filepath.Join(repo, "retry_test.go"), filepath.Join(repo, testPath)); err != nil {
					t.Fatal(err)
				}
			}
			fixtureWrite(t, repo, testPath, tableC)
			if scenario == "explicit_skip" {
				skip := stop("flinch skip: a one-time preference")
				events := fixtureAuthoringEvents(t, repo)
				if len(skip.Stdout) != 0 || len(records()) != 0 || len(events) != 1 || events[0].Kind != "skipped" {
					t.Fatalf("explicit skip registered a check or requested work: %s, %+v", skip.Stdout, events)
				}
				return
			}
			proved := stop("Preserved explicit zero and updated its expectation.")
			var notice struct{ SystemMessage, Decision string }
			if err := json.Unmarshal(proved.Stdout, &notice); err != nil || notice.Decision != "" || !strings.Contains(notice.SystemMessage, "TestRetryDelay/zero") {
				t.Fatalf("C did not prove the corrected row on its first Stop: %s, %v", proved.Stdout, err)
			}
			registered := records()
			if len(registered) != 1 || registered[0].Artifact.Path != testPath || registered[0].Artifact.Selector != "TestRetryDelay/zero" || len(registered[0].Cases.Allowed) != 1 || registered[0].Cases.Allowed[0] != "TestRetryDelay/omitted" {
				t.Fatalf("C registered the wrong case or neighbor: %+v", registered)
			}
			stateDir, err := gitutil.FlinchStateDir(ctx, repo)
			if err != nil {
				t.Fatal(err)
			}
			checkpoint := func() check.TurnCheckpoint {
				t.Helper()
				cp, ok, err := check.LoadCheckpoint(stateDir, session, repo)
				if err != nil || !ok {
					t.Fatalf("missing turn checkpoint: %v", err)
				}
				return cp
			}
			if cp := checkpoint(); !cp.Closed || cp.TotalContinuations != 0 || cp.AuthoringAttempted {
				t.Fatalf("C consumed an authoring continuation: %+v", cp)
			}
			if scenario == "nomination_hunk" || scenario == "renamed_hunk" {
				return
			}
			prompt("Simplify retry delay so nonpositive values use the default.")
			fixtureWrite(t, repo, "retry.go", strings.Replace(fixed, "d == nil", "d == nil || *d <= 0", 1))
			requireBlock := func(last, reason string) {
				t.Helper()
				answer := stop(last)
				var response struct{ Decision, Reason string }
				if err := json.Unmarshal(answer.Stdout, &response); err != nil || response.Decision != "block" || !strings.Contains(response.Reason, reason) {
					t.Fatalf("expected a repair for %q, got %s, %v", reason, answer.Stdout, err)
				}
			}
			switch scenario {
			case "product":
				requireBlock("Changed only the implementation.", "regression test TestRetryDelay/zero failed")
			case "repair_then_green":
				events := fixtureAuthoringEvents(t, repo)
				requireBlock("Changed nonpositive delays to use the default.", "regression test TestRetryDelay/zero failed")
				// The repair preserves zero and still changes a line introduced in C.
				// Its new negative case fails on C and would otherwise be reusable.
				fixtureWrite(t, repo, "retry.go", strings.Replace(fixed, "d == nil", "d == nil || *d < 0", 1))
				fixtureWrite(t, repo, testPath, strings.Replace(tableC, zero, zero+"\n\t\t{\"negative\", func() *int { n := -1; return &n }(), 1},", 1))
				if answer := stop("Repaired zero and covered negative delays."); len(answer.Stdout) != 0 {
					t.Fatalf("a successful repair reopened authoring: %s", answer.Stdout)
				}
				cp := checkpoint()
				if !cp.Closed || cp.TotalContinuations != 1 || cp.AuthoringAttempted || len(fixtureAuthoringEvents(t, repo)) != len(events) || len(records()) != 1 {
					t.Fatalf("the repair nominated or registered another case: %+v", cp)
				}
			case "rewrite_and_exhaust":
				fixtureWrite(t, repo, "retry_test.go", tableB)
				events := fixtureAuthoringEvents(t, repo)
				requireBlock("Updated the implementation and its expectation.", "protected table test case TestRetryDelay/zero was modified")
				requireBlock("Keeping the new expectation.", "protected table test case TestRetryDelay/zero was modified")
				if answer := stop("Leaving both changes as requested."); len(answer.Stdout) != 0 {
					t.Fatalf("expected a clean exit at the continuation limit, got %s", answer.Stdout)
				}
				cp := checkpoint()
				if !cp.Closed || cp.TotalContinuations != 2 || cp.AuthoringAttempted || len(fixtureAuthoringEvents(t, repo)) != len(events) {
					t.Fatalf("an exhausted turn reopened authoring: %+v", cp)
				}
				// The stable ID and YAML can be identical even if the inverse
				// artifact has been proven. It would leave a second local receipt.
				proofs, err := filepath.Glob(filepath.Join(stateDir, "proofs", "*.json"))
				if err != nil || len(proofs) != 1 || len(records()) != 1 {
					t.Fatalf("unexpected proof after the reverse edit: %v, err=%v", proofs, err)
				}
			}
			if got := fixtureGit(t, repo, "rev-parse", "HEAD"); got != head {
				t.Fatalf("turns moved HEAD from %s to %s", head, got)
			}
		})
	}
}
