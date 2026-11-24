package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/reflex"
	"github.com/angelnicolasc/flinch/internal/runner"
)

func TestRegressionInitMinimalConfigAndCloneTrust(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	if _, _, err := gitutil.Run(ctx, repo, nil, "init"); err != nil {
		t.Fatal(err)
	}
	artifact := reflex.Artifact{Tool: "command", Command: "echo authorized"}
	if _, err := runner.RunArtifact(ctx, repo, artifact, nil, time.Second); err == nil {
		t.Error("repository command ran before this clone was enabled")
	}
	var out, errs bytes.Buffer
	if code := Run(ctx, []string{"init", "--repo", repo}, &out, &errs); code != 0 {
		t.Fatal(errs.String())
	}
	if !strings.Contains(strings.ToLower(out.String()), "command") {
		t.Error("first init should explain command trust")
	}
	cfgPath := filepath.Join(repo, ".flinch", "flinch.toml")
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Error("default init wrote repository configuration")
	}
	if result, err := runner.RunArtifact(ctx, repo, artifact, nil, time.Second); err != nil || result.ExitCode != 0 {
		t.Fatalf("enabled command: %v, %+v", err, result)
	}
	other := t.TempDir()
	if _, _, err := gitutil.Run(ctx, other, nil, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunArtifact(ctx, other, artifact, nil, time.Second); err == nil {
		t.Error("command trust transferred to another repo")
	}
	if code := Run(ctx, []string{"init", "--repo", other, "--owner", "platform"}, &out, &errs); code != 0 {
		t.Fatal(errs.String())
	}
	data, err := os.ReadFile(filepath.Join(other, ".flinch", "flinch.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "platform") || strings.Contains(string(data), "retention_days") {
		t.Errorf("expected only team override, got %s", data)
	}
}

func TestRegressionCustomVetAnalyzerNeedsCloneEnrollment(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	if _, _, err := gitutil.Run(ctx, repo, nil, "init"); err != nil {
		t.Fatal(err)
	}
	art := reflex.Artifact{Tool: "go-vet", Command: "go vet -vettool=./team-analyzer"}
	if _, err := runner.RunArtifact(ctx, repo, art, nil, time.Second); err == nil || !strings.Contains(err.Error(), "enrollment") {
		t.Fatalf("repository analyzer was admitted before enrollment: %v", err)
	}
}
