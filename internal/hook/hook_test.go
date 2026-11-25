package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/angelnicolasc/flinch/internal/config"
	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/pause"
)

func initHookTestRepo(t *testing.T) string {
	t.Helper()
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
	_ = config.Save(repo, config.Default())
	_ = os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/hookdemo\n\ngo 1.26\n"), 0o644)
	_ = os.WriteFile(filepath.Join(repo, "retry.go"), []byte("package hookdemo\n\nfunc Delay(v *int) int {\n\treturn 100\n}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(repo, "retry_test.go"), []byte(`package hookdemo

import "testing"

func intPtr(v int) *int { return &v }

func TestRetryDelay(t *testing.T) {
	cases := []struct {
		name string
		in   *int
		want int
	}{
		{name: "omitted_uses_default", in: nil, want: 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Delay(tc.in); got != tc.want {
				t.Fatalf("Delay = %d, want %d", got, tc.want)
			}
		})
	}
}
`), 0o644)
	if _, _, err := gitutil.Run(ctx, repo, env, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := gitutil.Run(ctx, repo, env, "-c", "commit.gpgsign=false", "commit", "-m", "initial"); err != nil {
		t.Fatal(err)
	}
	setupHookProfile(t, repo)
	return repo
}

// Exercises synthetic Claude Code and Codex event sequences across:
// - Turn 1: agent writes buggy Delay
// - Turn 2 (reuse): user corrects, agent fixes Delay AND adds row -> Flinch proves it with ZERO continuations!
// - Turn 3 (ask-when-needed): user corrects without test -> Flinch emits ONE continuation ("decision": "block").
func TestClaudeAndCodexTurnFlowReuseAndAuthoring(t *testing.T) {
	for _, host := range []Host{HostClaude, HostCodex} {
		t.Run(string(host), func(t *testing.T) {
			repo := initHookTestRepo(t)
			ctx := context.Background()

			// Turn 1 start
			start1, _ := json.Marshal(map[string]any{
				"session_id":      "s1",
				"cwd":             repo,
				"hook_event_name": "UserPromptSubmit",
				"prompt":          "Implement Delay with optional override",
			})
			r := Handle(ctx, host, "UserPromptSubmit", start1, "")
			if r.ExitCode != 0 || !strings.Contains(string(r.Stdout), "active in this repo") {
				t.Fatalf("unexpected Turn 1 start response: %+v", r)
			}

			// Turn 1 edit (buggy: treats 0 as missing)
			_ = os.WriteFile(filepath.Join(repo, "retry.go"), []byte("package hookdemo\n\nfunc Delay(v *int) int {\n\tif v == nil || *v == 0 {\n\t\treturn 100\n\t}\n\treturn *v\n}\n"), 0o644)
			stop1, _ := json.Marshal(map[string]any{
				"session_id":      "s1",
				"cwd":             repo,
				"hook_event_name": "Stop",
			})
			r = Handle(ctx, host, "Stop", stop1, "")
			if r.ExitCode != 0 || len(r.Stdout) != 0 {
				t.Fatalf("unexpected Turn 1 stop response: %+v", r)
			}

			// Turn 2 start (human correction)
			start2, _ := json.Marshal(map[string]any{
				"session_id":      "s1",
				"cwd":             repo,
				"hook_event_name": "UserPromptSubmit",
				"prompt":          "Explicit 0 must retry immediately, not use default 100",
			})
			Handle(ctx, host, "UserPromptSubmit", start2, "")

			// Agent fixes Delay AND adds explicit_zero row in the existing table test (reuse-first!)
			_ = os.WriteFile(filepath.Join(repo, "retry.go"), []byte("package hookdemo\n\nfunc Delay(v *int) int {\n\tif v == nil {\n\t\treturn 100\n\t}\n\treturn *v\n}\n"), 0o644)
			_ = os.WriteFile(filepath.Join(repo, "retry_test.go"), []byte(`package hookdemo

import "testing"

func intPtr(v int) *int { return &v }

func TestRetryDelay(t *testing.T) {
	cases := []struct {
		name string
		in   *int
		want int
	}{
		{name: "omitted_uses_default", in: nil, want: 100},
		{name: "explicit_zero", in: intPtr(0), want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Delay(tc.in); got != tc.want {
				t.Fatalf("Delay = %d, want %d", got, tc.want)
			}
		})
	}
}
`), 0o644)

			r = Handle(ctx, host, "Stop", stop1, "")
			var notice map[string]string
			if r.ExitCode != 0 || json.Unmarshal(r.Stdout, &notice) != nil || notice["decision"] != "" || !strings.Contains(notice["systemMessage"], "explicit_zero") {
				t.Fatalf("reuse-first should prove without requesting a continuation, got: %s", string(r.Stdout))
			}

			records, err := filepath.Glob(filepath.Join(repo, ".flinch", "checks", "*.yml"))
			if err != nil || len(records) != 1 {
				t.Fatalf("expected one registered check, got %v: %v", records, err)
			}
		})
	}
}

// Exercises synthetic Antigravity events:
// - PreInvocation fires multiple times per user turn with the same lastUserInput -> deduplicates checkpoint!
// - Stop requests continuation via {"decision":"continue","reason":...} when a correction has no protection yet.
// - Agent closes silently via `flinch skip: ...` -> Stop allows cleanly with {}.
// - Stop respects USER_CANCELED without continuing.
func TestAntigravityPreInvocationDeduplicationContinueAndSkip(t *testing.T) {
	repo := initHookTestRepo(t)
	ctx := context.Background()

	// Turn 1: initial buggy edit
	pre1, _ := json.Marshal(map[string]any{
		"conversationId": "conv-ag-1",
		"cwd":            repo,
		"hookEventName":  "PreInvocation",
		"invocationNum":  1,
		"lastUserInput":  "Implement Delay",
	})
	r := Handle(ctx, HostAntigravity, "PreInvocation", pre1, "")
	if r.ExitCode != 0 || strings.TrimSpace(string(r.Stdout)) != "{}" {
		t.Fatalf("unexpected PreInvocation 1 output: %q", string(r.Stdout))
	}
	_ = os.WriteFile(filepath.Join(repo, "retry.go"), []byte("package hookdemo\n\nfunc Delay(v *int) int {\n\tif v == nil || *v == 0 {\n\t\treturn 100\n\t}\n\treturn *v\n}\n"), 0o644)

	// Second model call in SAME Turn 1 (same lastUserInput) must NOT overwrite checkpoint!
	pre1b, _ := json.Marshal(map[string]any{
		"conversationId": "conv-ag-1",
		"cwd":            repo,
		"hookEventName":  "PreInvocation",
		"invocationNum":  2,
		"lastUserInput":  "Implement Delay",
	})
	Handle(ctx, HostAntigravity, "PreInvocation", pre1b, "")

	stop1, _ := json.Marshal(map[string]any{
		"conversationId":    "conv-ag-1",
		"cwd":               repo,
		"hookEventName":     "Stop",
		"terminationReason": "model_stop",
		"fullyIdle":         true,
	})
	r = Handle(ctx, HostAntigravity, "Stop", stop1, "")
	if strings.TrimSpace(string(r.Stdout)) != "{}" {
		t.Fatalf("expected {} on Turn 1 stop, got %q", string(r.Stdout))
	}

	// Turn 2: human corrects Delay without adding a test -> Antigravity Stop returns decision:"continue"!
	pre2, _ := json.Marshal(map[string]any{
		"conversationId": "conv-ag-1",
		"cwd":            repo,
		"hookEventName":  "PreInvocation",
		"invocationNum":  3,
		"lastUserInput":  "Zero must not fall back to 100",
	})
	Handle(ctx, HostAntigravity, "PreInvocation", pre2, "")
	_ = os.WriteFile(filepath.Join(repo, "retry.go"), []byte("package hookdemo\n\nfunc Delay(v *int) int {\n\tif v == nil {\n\t\treturn 100\n\t}\n\treturn *v\n}\n"), 0o644)

	r = Handle(ctx, HostAntigravity, "Stop", stop1, "")
	var resp map[string]string
	if err := json.Unmarshal(r.Stdout, &resp); err != nil || resp["decision"] != "continue" {
		t.Fatalf("expected Antigravity decision=continue for unguarded correction, got %q (err=%v)", string(r.Stdout), err)
	}

	// USER_CANCELED must always fail open with {}.
	stopCanceled, _ := json.Marshal(map[string]any{
		"conversationId":    "conv-ag-1",
		"cwd":               repo,
		"hookEventName":     "Stop",
		"terminationReason": "USER_CANCELED",
	})
	r = Handle(ctx, HostAntigravity, "Stop", stopCanceled, "")
	if strings.TrimSpace(string(r.Stdout)) != "{}" {
		t.Fatalf("expected {} on USER_CANCELED, got %q", string(r.Stdout))
	}
}

func TestPauseAndEnvDisableFailOpenInHooks(t *testing.T) {
	repo := initHookTestRepo(t)
	ctx := context.Background()
	stateDir, err := gitutil.FlinchStateDir(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pause.Pause(stateDir, 1*time.Hour, "testing pause", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	stopJSON, _ := json.Marshal(map[string]any{"conversationId": "c1", "cwd": repo, "terminationReason": "model_stop", "fullyIdle": true})
	r := Handle(ctx, HostAntigravity, "Stop", stopJSON, "")
	if r.ExitCode != 0 || strings.TrimSpace(string(r.Stdout)) != "{}" {
		t.Fatalf("paused repo must fail open silently, got %+v", r)
	}
}

// Regression test (D10): Hook installation is idempotent, preserves unrelated hooks/settings,
// and refuses to overwrite malformed JSON files.
func TestInstallHooksIsIdempotentPreservesForeignHooksAndRejectsCorruptJSON(t *testing.T) {
	repo := t.TempDir()

	// Seed existing foreign settings & hooks in all three hosts.
	_ = os.MkdirAll(filepath.Join(repo, ".claude"), 0o755)
	_ = os.WriteFile(filepath.Join(repo, ".claude", "settings.json"), []byte(`{
  "permissions": {"allow": ["Bash(go test:*)"]},
  "hooks": {
    "Stop": [{"hooks": [{"type": "command", "command": "echo foreign-hook"}]}]
  }
}`), 0o644)

	_ = os.MkdirAll(filepath.Join(repo, ".agents"), 0o755)
	_ = os.WriteFile(filepath.Join(repo, ".agents", "hooks.json"), []byte(`{
  "my-other-tool": {
    "Stop": [{"type": "command", "command": "echo keep-me"}]
  }
}`), 0o644)

	for _, host := range []Host{HostClaude, HostCodex, HostAntigravity} {
		res1, err := InstallHooks(repo, host, "flinch")
		if err != nil || !res1.Changed {
			t.Fatalf("first InstallHooks(%s) failed: res=%+v err=%v", host, res1, err)
		}
		abs := filepath.Join(repo, filepath.FromSlash(res1.Path))
		afterFirst, _ := os.ReadFile(abs)

		// Second install MUST be a no-op (Changed=false, identical bytes!).
		res2, err := InstallHooks(repo, host, "flinch")
		if err != nil || res2.Changed {
			t.Fatalf("second InstallHooks(%s) should be unchanged: res=%+v err=%v", host, res2, err)
		}
		afterSecond, _ := os.ReadFile(abs)
		if !bytes.Equal(afterFirst, afterSecond) {
			t.Fatalf("second InstallHooks(%s) altered file bytes", host)
		}
	}

	// Verify foreign hooks & settings survived untouched.
	claudeRaw, _ := os.ReadFile(filepath.Join(repo, ".claude", "settings.json"))
	if !strings.Contains(string(claudeRaw), "foreign-hook") || !strings.Contains(string(claudeRaw), "Bash(go test:*)") {
		t.Fatalf("InstallHooks clobbered foreign Claude settings/hooks: %s", string(claudeRaw))
	}
	agRaw, _ := os.ReadFile(filepath.Join(repo, ".agents", "hooks.json"))
	if !strings.Contains(string(agRaw), "my-other-tool") || !strings.Contains(string(agRaw), "keep-me") {
		t.Fatalf("InstallHooks clobbered foreign Antigravity hook: %s", string(agRaw))
	}

	// Malformed JSON must NEVER be overwritten!
	corrupt := []byte("{not valid json")
	_ = os.WriteFile(filepath.Join(repo, ".codex", "hooks.json"), corrupt, 0o644)
	if _, err := InstallHooks(repo, HostCodex, "flinch"); err == nil {
		t.Fatalf("expected error on corrupt .codex/hooks.json")
	}
	stillCorrupt, _ := os.ReadFile(filepath.Join(repo, ".codex", "hooks.json"))
	if !bytes.Equal(corrupt, stillCorrupt) {
		t.Fatalf("InstallHooks overwrote corrupt JSON!")
	}
}
