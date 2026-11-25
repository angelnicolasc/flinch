package check

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/angelnicolasc/flinch/internal/author"
	"github.com/angelnicolasc/flinch/internal/config"
	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/reflex"
)

func TestTurnEnforcesProvenCheckoutChecks(t *testing.T) {
	for _, endings := range []string{"LF", "CRLF"} {
		t.Run(endings, func(t *testing.T) {
			testProvenCheckout(t, endings == "CRLF")
		})
	}
}

func testProvenCheckout(t *testing.T, crlf bool) {
	t.Helper()
	ctx := context.Background()
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if _, _, err := gitutil.Run(ctx, repo, nil, args...); err != nil {
			t.Fatal(err)
		}
	}
	git("init")
	eolSetting := "false"
	if crlf {
		eolSetting = "true"
	}
	git("config", "core.autocrlf", eolSetting)
	write := func(root, path, content string) {
		t.Helper()
		if crlf {
			content = strings.ReplaceAll(content, "\n", "\r\n")
		}
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const product = `package retry

func RetryDelay(d *int) int {
	if d == nil {
		return 1
	}
	return *d
}
`
	const fixedTest = `package retry

import "testing"

func TestRetryDelay(t *testing.T) {
	var zero int
	_ = zero
	cases := []struct {
		name string
		d    *int
		want int
	}{
		{"omitted", nil, 1},
		{"zero", &zero, 0},
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
	const row = `{"zero", &zero, 0},`
	brokenProduct := strings.Replace(product, "d == nil", "d == nil || *d == 0", 1)
	baseTest := strings.Replace(fixedTest, "\t\t"+row+"\n", "", 1)
	changedRow := strings.Replace(fixedTest, row, `{"zero", &zero, 1},`, 1)
	write(repo, "go.mod", "module example.com/retry\n\ngo 1.26\n")
	write(repo, "retry.go", brokenProduct)
	write(repo, "retry_test.go", baseTest)
	base := commitRepo(t, repo, "Add retry delay")
	write(repo, "retry.go", product)
	write(repo, "retry_test.go", fixedTest)
	fixed, err := gitutil.CaptureWorkingTreeSnapshot(ctx, repo, "fixed")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Loop.MaxTestRuntime = "30s"
	cfg.Loop.TestBudget = "90s"
	// Proof runs against the captured tree even if the live artifact changes.
	write(repo, "retry_test.go", changedRow)
	out, err := author.ReuseFirst(ctx, repo, cfg, base, fixed, 2*time.Minute)
	if err != nil || out.Status != author.ReuseProven || out.Reflex == nil || out.Reflex.Artifact.Selector != "TestRetryDelay/zero" {
		t.Fatalf("expected a real proof for the zero row, got %+v, err=%v", out, err)
	}
	record := filepath.Join(repo, reflex.ReflexesRelDir, out.Reflex.ID+".yml")
	savedRecord, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	reset := func() {
		t.Helper()
		write(repo, "retry.go", product)
		write(repo, "retry_test.go", fixedTest)
		if err := os.WriteFile(record, savedRecord, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(t *testing.T, root string, edit func()) Result {
		t.Helper()
		opts := TurnOptions{SessionID: t.Name(), WorktreeID: root, CheckIntg: true, Deadline: 90 * time.Second}
		cp, err := CaptureCheckpoint(ctx, root, opts.SessionID, root, "", "", true)
		if err != nil || cp.SnapshotSHA == "" {
			t.Fatalf("capture checkpoint: %+v, err=%v", cp, err)
		}
		edit()
		res, err := RunTurn(ctx, root, cfg, opts)
		if err != nil || len(res.SkippedControls) != 0 {
			t.Fatalf("unexpected error or skip: %+v, err=%v", res, err)
		}
		return res
	}
	allow := func(t *testing.T, res Result) {
		t.Helper()
		if res.Decision != DecisionAllow || len(res.Findings) != 0 || len(res.IntegrityViolations) != 0 {
			t.Fatalf("expected allow, got %+v", res)
		}
	}
	productRegression := func() { write(repo, "retry.go", brokenProduct) }
	requireCase := func(t *testing.T, res Result) {
		t.Helper()
		if !res.Blocking || len(res.Findings) != 1 || res.Findings[0].ReflexID != out.Reflex.ID || res.Findings[0].Finding.Selector != "TestRetryDelay/zero" {
			t.Fatalf("expected one finding for the proven case, got %+v", res)
		}
	}
	t.Run("product", func(t *testing.T) {
		reset()
		res := run(t, repo, productRegression)
		requireCase(t, res)
		if len(res.IntegrityViolations) != 0 {
			t.Fatalf("unexpected integrity violation: %+v", res)
		}
	})
	for _, tc := range []struct {
		name string
		kind string
		edit func()
	}{
		{"rewrite", "artifact_modified", func() {
			productRegression()
			write(repo, "retry_test.go", changedRow)
		}},
		{"rename", "artifact_modified", func() { write(repo, "retry_test.go", strings.Replace(fixedTest, row, `{"zero_delay", &zero, 0},`, 1)) }},
		{"delete_record", "check_deleted", func() {
			if err := os.Remove(record); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reset()
			res := run(t, repo, tc.edit)
			if !res.Blocking || len(res.Findings) != 0 || len(res.IntegrityViolations) != 1 || res.IntegrityViolations[0].Kind != tc.kind || res.IntegrityViolations[0].ReflexID != out.Reflex.ID {
				t.Fatalf("expected %s for the proven case, got %+v", tc.kind, res)
			}
		})
	}
	t.Run("sibling", func(t *testing.T) {
		reset()
		allow(t, run(t, repo, func() {
			write(repo, "retry_test.go", strings.Replace(fixedTest, row, row+"\n\t\t{\"another_default\", nil, 1},", 1))
		}))
	})
	t.Run("discard_before_prompt", func(t *testing.T) {
		reset()
		if err := os.Remove(record); err != nil {
			t.Fatal(err)
		}
		write(repo, "retry_test.go", baseTest)
		allow(t, run(t, repo, productRegression))
	})
	t.Run("other_worktree", func(t *testing.T) {
		reset()
		other := filepath.Join(t.TempDir(), "linked")
		git("worktree", "add", "--detach", other, base)
		write(other, "retry.go", product)
		allow(t, run(t, other, func() { write(other, "retry.go", brokenProduct) }))
	})
	t.Run("product_already_broken", func(t *testing.T) {
		reset()
		productRegression()
		allow(t, run(t, repo, func() { write(repo, "retry.go", brokenProduct+"\n// Unrelated change.\n") }))
	})
	t.Run("legacy_proof_measured_on_disk", func(t *testing.T) {
		reset()
		if err := os.Remove(record); err != nil {
			t.Fatal(err)
		}
		// Older releases recorded the live file's identity after verification.
		// A verified verdict alone cannot establish which revision supplied it.
		legacy := *out.Reflex
		legacy.ID = "legacy-zero"
		path, err := reflex.Save(repo, legacy)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Remove(path) })
		allow(t, run(t, repo, productRegression))
	})
	t.Run("invalid_proof", func(t *testing.T) {
		reset()
		// The same ID and artifact cannot authorize a different contract. A
		// timing-only local measurement and a claim in the YAML are insufficient.
		changed := *out.Reflex
		changed.Obligation = "A different contract"
		changed.Proof = &reflex.ProofSummary{RuntimeMS: 1}
		if err := os.Remove(record); err != nil {
			t.Fatal(err)
		}
		if _, err := reflex.Save(repo, changed); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(record)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(record, append(b, []byte("proof:\n  verdict: verified\n")...), 0o644); err != nil {
			t.Fatal(err)
		}
		allow(t, run(t, repo, productRegression))
	})
	t.Run("different_artifact_at_open", func(t *testing.T) {
		reset()
		write(repo, "retry_test.go", changedRow)
		allow(t, run(t, repo, func() { write(repo, "retry_test.go", fixedTest) }))
	})
	t.Run("reviewed_id_wins", func(t *testing.T) {
		reset()
		commitRepo(t, repo, "Review explicit zero delay")
		requireCase(t, run(t, repo, productRegression))
		reset()
		shadow := *out.Reflex
		shadow.Obligation = "A different contract"
		if err := os.Remove(record); err != nil {
			t.Fatal(err)
		}
		if _, err := reflex.Save(repo, shadow); err != nil {
			t.Fatal(err)
		}
		res := run(t, repo, productRegression)
		requireCase(t, res)
		if res.Findings[0].Obligation != out.Reflex.Obligation || len(res.IntegrityViolations) != 1 || res.IntegrityViolations[0].Kind != "check_modified" {
			t.Fatalf("the reviewed contract lost precedence: %+v", res)
		}
	})
}
