package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/angelnicolasc/flinch/internal/gitutil"
)

func TestCLIEndToEndInitQueueDoctorAndStats(t *testing.T) {
	repo := t.TempDir()
	ctx := context.Background()
	env := []string{
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	}
	if _, _, err := gitutil.Run(ctx, repo, env, "init"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/clidemo\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := gitutil.Run(ctx, repo, env, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := gitutil.Run(ctx, repo, env, "-c", "commit.gpgsign=false", "commit", "-m", "init"); err != nil {
		t.Fatal(err)
	}

	var out, errBuf bytes.Buffer
	code := Run(ctx, []string{"init", "--repo", repo, "--owner", "core", "--json"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("init failed (%d): %s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), ".flinch/flinch.toml") {
		t.Fatalf("unexpected init output: %s", out.String())
	}

	// Signal ingest + queue list + claim + idempotent deliver
	out.Reset()
	errBuf.Reset()
	code = Run(ctx, []string{
		"signal", "--repo", repo,
		"--source", "correction",
		"--event-id", "ev-1",
		"--pr", "27",
		"--broken", "aaa",
		"--fixed", "bbb",
		"--paths", "pkg/client.go",
		"--summary", "Bound response body reads",
		"--cursor", "cur-1",
		"--json",
	}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("signal failed (%d): %s", code, errBuf.String())
	}
	var sigResp struct {
		Episode struct {
			ID string `json:"id"`
		} `json:"episode"`
	}
	if err := json.Unmarshal(out.Bytes(), &sigResp); err != nil || sigResp.Episode.ID == "" {
		t.Fatalf("decode signal response: %v (raw=%s)", err, out.String())
	}

	out.Reset()
	errBuf.Reset()
	code = Run(ctx, []string{"queue", "claim", "--repo", repo, "--worker", "w1", "--json"}, &out, &errBuf)
	if code != 0 || !strings.Contains(out.String(), sigResp.Episode.ID) {
		t.Fatalf("queue claim failed (%d): out=%s err=%s", code, out.String(), errBuf.String())
	}

	out.Reset()
	errBuf.Reset()
	code = Run(ctx, []string{"queue", "deliver", "--repo", repo, "--episode", sigResp.Episode.ID, "--target", "pr:27", "--json"}, &out, &errBuf)
	if code != 0 || !strings.Contains(out.String(), `"already_delivered": false`) {
		t.Fatalf("queue deliver failed (%d): out=%s err=%s", code, out.String(), errBuf.String())
	}

	// Doctor and stats JSON output
	out.Reset()
	errBuf.Reset()
	code = Run(ctx, []string{"doctor", "--repo", repo, "--prune", "--json"}, &out, &errBuf)
	if code != 0 || !strings.Contains(out.String(), `"capabilities"`) {
		t.Fatalf("doctor failed (%d): out=%s err=%s", code, out.String(), errBuf.String())
	}

	out.Reset()
	errBuf.Reset()
	code = Run(ctx, []string{"stats", "--repo", repo, "--json"}, &out, &errBuf)
	if code != 0 || !strings.Contains(out.String(), `"observed_scope"`) {
		t.Fatalf("stats failed (%d): out=%s err=%s", code, out.String(), errBuf.String())
	}
}

// Regression tests (D10):
// - Running `flinch init` twice changes nothing.
// - `flinch init` never overwrites an unreadable/malformed configuration.
// - `flinch init` does not accept or offer `team_connected`.
func TestInitIsIdempotentNeverOverwritesCorruptConfigAndRejectsTeamConnected(t *testing.T) {
	repo := t.TempDir()
	ctx := context.Background()
	env := []string{
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	}
	if _, _, err := gitutil.Run(ctx, repo, env, "init"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/initdemo\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. Reject team_connected mode on init (D10).
	var out, errBuf bytes.Buffer
	code := Run(ctx, []string{"init", "--repo", repo, "--mode", "team_connected"}, &out, &errBuf)
	if code == 0 {
		t.Fatalf("expected init to reject --mode team_connected")
	}

	// 2. First init creates .flinch/flinch.toml; second init leaves exact bytes untouched and exits 0.
	out.Reset()
	errBuf.Reset()
	if code := Run(ctx, []string{"init", "--repo", repo, "--owner", "platform", "--json"}, &out, &errBuf); code != 0 {
		t.Fatalf("first init failed (%d): %s", code, errBuf.String())
	}
	cfgPath := filepath.Join(repo, ".flinch", "flinch.toml")
	firstBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	// Add a custom comment line to prove second init does not rewrite the file.
	customBytes := append([]byte("# preserved user comment\n"), firstBytes...)
	if err := os.WriteFile(cfgPath, customBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	errBuf.Reset()
	if code := Run(ctx, []string{"init", "--repo", repo, "--json"}, &out, &errBuf); code != 0 {
		t.Fatalf("second init failed (%d): %s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), `"unchanged": true`) {
		t.Fatalf("expected unchanged:true on second init, got: %s", out.String())
	}
	secondBytes, err := os.ReadFile(cfgPath)
	if err != nil || !bytes.Equal(customBytes, secondBytes) {
		t.Fatalf("second init modified existing .flinch/flinch.toml")
	}

	// 3. Corrupt/unreadable config must NEVER be overwritten, even with --force!
	corrupt := []byte("schema = 1\n[policy\n broken toml\n")
	if err := os.WriteFile(cfgPath, corrupt, 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errBuf.Reset()
	if code := Run(ctx, []string{"init", "--repo", repo, "--force"}, &out, &errBuf); code == 0 {
		t.Fatalf("expected init --force to refuse overwriting corrupt config")
	}
	afterCorrupt, err := os.ReadFile(cfgPath)
	if err != nil || !bytes.Equal(corrupt, afterCorrupt) {
		t.Fatalf("init overwrote corrupt config file!")
	}
}

func TestCLIPauseResumeSkipReportCleanupAndHooksInit(t *testing.T) {
	repo := t.TempDir()
	ctx := context.Background()
	env := []string{
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	}
	if _, _, err := gitutil.Run(ctx, repo, env, "init"); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/subcmds\n\ngo 1.26\n"), 0o644)

	var out, errBuf bytes.Buffer
	if code := Run(ctx, []string{"init", "--repo", repo, "--hooks", "claude,codex,antigravity", "--json"}, &out, &errBuf); code != 0 {
		t.Fatalf("init --hooks failed (%d): %s", code, errBuf.String())
	}
	// Second init --hooks must be a no-op across config + all 3 host hook files (D10).
	out.Reset()
	errBuf.Reset()
	if code := Run(ctx, []string{"init", "--repo", repo, "--hooks", "claude,codex,antigravity", "--json"}, &out, &errBuf); code != 0 {
		t.Fatalf("second init --hooks failed (%d): %s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), `"unchanged": true`) || strings.Contains(out.String(), `"changed": true`) {
		t.Fatalf("expected zero changes on second init --hooks, got: %s", out.String())
	}

	// Pause, check status, resume (D7)
	out.Reset()
	errBuf.Reset()
	if code := Run(ctx, []string{"pause", "--repo", repo, "--duration", "2h", "--reason", "experiment", "--json"}, &out, &errBuf); code != 0 {
		t.Fatalf("pause failed (%d): %s", code, errBuf.String())
	}
	out.Reset()
	if code := Run(ctx, []string{"pause", "--repo", repo, "--status", "--json"}, &out, &errBuf); code != 0 || !strings.Contains(out.String(), `"paused": true`) {
		t.Fatalf("pause --status failed (%d): %s", code, out.String())
	}
	out.Reset()
	if code := Run(ctx, []string{"resume", "--repo", repo, "--json"}, &out, &errBuf); code != 0 || !strings.Contains(out.String(), `"was_paused": true`) {
		t.Fatalf("resume failed (%d): %s", code, out.String())
	}

	// Skip, report, cleanup
	out.Reset()
	if code := Run(ctx, []string{"skip", "--repo", repo, "--session", "s1", "--reason", "one-off tweak", "--json"}, &out, &errBuf); code != 0 {
		t.Fatalf("skip failed (%d): %s", code, errBuf.String())
	}
	out.Reset()
	if code := Run(ctx, []string{"report", "--repo", repo, "--at", "main.go:10", "--reason", "false positive", "--json", "r1"}, &out, &errBuf); code != 0 {
		t.Fatalf("report failed (%d): %s", code, errBuf.String())
	}
	out.Reset()
	if code := Run(ctx, []string{"cleanup", "--repo", repo, "--days", "30", "--json"}, &out, &errBuf); code != 0 || !strings.Contains(out.String(), `"retention_days": 30`) {
		t.Fatalf("cleanup failed (%d): out=%s err=%s", code, out.String(), errBuf.String())
	}
}
