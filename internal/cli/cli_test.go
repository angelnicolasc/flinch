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
