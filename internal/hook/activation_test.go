package hook

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestCodexSessionDirectoryControlsEveryHook(t *testing.T) {
	repo := initHookTestRepo(t)
	root, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "src")
	if err = os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	start := func(dir string) Response {
		return regressionHook(t, dir, HostCodex, "UserPromptSubmit", map[string]any{"prompt": "synthetic fixture"})
	}
	before := hookFootprint(t, repo)
	if r := start(sub); len(r.Stdout) != 0 || !reflect.DeepEqual(before, hookFootprint(t, repo)) {
		t.Fatal("root trust activated a subdirectory")
	}
	if got := start(root); !strings.Contains(string(got.Stdout), "active in this repo") {
		t.Fatalf("missing root activation: %s", got.Stdout)
	}
	config := filepath.Join(os.Getenv("CODEX_HOME"), "config.toml")
	if err = os.WriteFile(config, []byte("[projects."+strconv.Quote(root)+"]\ntrust_level=\"untrusted\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before = hookFootprint(t, repo)
	data, _ := json.Marshal(map[string]any{"cwd": root, "session_id": "regression"})
	got := Handle(context.Background(), HostCodex, "Stop", data, "")
	if len(got.Stdout) != 0 || !reflect.DeepEqual(before, hookFootprint(t, repo)) {
		t.Fatal("revocation did not stop the following event")
	}
}

func TestAntigravityFirstTurnKeepsClaudeNoticeAvailable(t *testing.T) {
	repo := initHookTestRepo(t)
	got := regressionHook(t, repo, HostAntigravity, "PreInvocation", map[string]any{"lastUserInput": "fixture"})
	if strings.TrimSpace(string(got.Stdout)) != "{}" {
		t.Fatalf("unexpected Antigravity response: %s", got.Stdout)
	}
	got = regressionHook(t, repo, HostClaude, "UserPromptSubmit", map[string]any{"turn_id": "human-id", "prompt": "second fixture"})
	if !strings.Contains(string(got.Stdout), "active in this repo") || strings.Contains(string(got.Stdout), "decision") {
		t.Fatalf("expected a passive first notice: %s", got.Stdout)
	}
	roots, err := ActivationHistory()
	if err != nil || len(roots) != 1 {
		t.Fatalf("previously active roots: %v %v", roots, err)
	}
	if cp := regressionCheckpoint(t, repo); cp.TotalContinuations != 0 {
		t.Fatalf("first notice consumed %d continuations", cp.TotalContinuations)
	}
}
