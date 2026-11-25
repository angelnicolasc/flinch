package doctor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/angelnicolasc/flinch/internal/config"
	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/reflex"
	"github.com/angelnicolasc/flinch/internal/runner"
)

func TestDoctorReportsCommandApprovalWithoutGrantingOrRunningIt(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	if _, _, err := gitutil.Run(ctx, repo, nil, "init"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	if runner.CommandsNeedApproval(ctx, repo, cfg, nil) {
		t.Fatal("reported a missing approval without a repository command")
	}
	cfg.Lint = []config.LintConfig{{Kind: "script", Command: "sh ./guard.sh"}}
	if err := config.Save(repo, cfg); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(repo, "guard.sh")
	if err := os.WriteFile(script, []byte("echo ran > unexpected-execution\n"), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := Run(ctx, repo, false)
	if err != nil || !report.CommandApprovalRequired {
		t.Fatalf("diagnosed unapproved command: required=%t err=%v", report.CommandApprovalRequired, err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "flinch", "command-approvals.json")); !os.IsNotExist(err) {
		t.Fatalf("diagnosis created an approval: %v", err)
	}
	if _, err := runner.EnableCommands(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if runner.CommandsNeedApproval(ctx, repo, cfg, nil) {
		t.Fatal("reported a reviewed, unchanged script")
	}
	if err := os.WriteFile(script, []byte("echo changed > unexpected-execution\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if !runner.CommandsNeedApproval(ctx, repo, cfg, nil) {
		t.Fatal("failed to report a script changed since approval")
	}
	record := reflex.Reflex{Artifact: reflex.Artifact{Tool: "command", Command: "echo record"}}
	if !runner.CommandsNeedApproval(ctx, repo, config.Default(), []reflex.Reflex{record}) {
		t.Fatal("failed to report an unapproved command in a stored check")
	}
	if _, err := os.Stat(filepath.Join(repo, "unexpected-execution")); !os.IsNotExist(err) {
		t.Fatalf("diagnosing command approval ran the script: %v", err)
	}
}

func TestDiscoverAndDiagnoseSeparatesDeterministicAndProbabilistic(t *testing.T) {
	repo := t.TempDir()
	ctx := context.Background()
	if _, _, err := gitutil.Run(ctx, repo, nil, "init"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/doc\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".github", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".github", "workflows", "ci.yml"), []byte("name: CI\non: [pull_request]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, notes := DiscoverConfig(ctx, repo)
	if len(cfg.Tests.Runners) == 0 || cfg.Tests.Runners[0].Kind != "go" {
		t.Fatalf("expected Go test runner discovered, got %+v", cfg.Tests.Runners)
	}
	if len(notes) == 0 {
		t.Fatalf("expected discovery notes")
	}
	if err := config.Save(repo, cfg); err != nil {
		t.Fatal(err)
	}

	// Add one deterministic reflex and one probabilistic context guide line.
	if _, err := reflex.Save(repo, reflex.Reflex{
		ID:         "r-det",
		Version:    1,
		Obligation: "Deterministic rule",
		Rung:       reflex.RungLint,
		Artifact:   reflex.Artifact{Tool: "go-vet", Rule: "printf"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := reflex.Save(repo, reflex.Reflex{
		ID:         "r-line",
		Version:    1,
		Obligation: "Guidance only",
		Rung:       reflex.RungLine,
		Artifact:   reflex.Artifact{Tool: "agents-md", Text: "Prefer short functions."},
	}); err != nil {
		t.Fatal(err)
	}

	rep, err := Run(ctx, repo, true)
	if err != nil {
		t.Fatalf("doctor.Run failed: %v", err)
	}
	if rep.DeterministicReflexes != 1 || rep.ProbabilisticGuides != 1 {
		t.Fatalf("expected 1 deterministic + 1 probabilistic, got %d / %d", rep.DeterministicReflexes, rep.ProbabilisticGuides)
	}
	if rep.Capabilities.MergeBlocking.State == CapEnabledAndTested {
		t.Fatalf("merge blocking should not claim enabled_and_tested before accreditation")
	}
}
