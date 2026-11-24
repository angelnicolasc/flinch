package hook

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/angelnicolasc/flinch/internal/check"
	"github.com/angelnicolasc/flinch/internal/gitutil"
)

func regressionHook(t *testing.T, repo string, host Host, event string, fields map[string]any) Response {
	t.Helper()
	fields["cwd"] = repo
	if host == HostAntigravity {
		fields["conversationId"] = "regression"
	} else {
		fields["session_id"] = "regression"
	}
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return Handle(context.Background(), host, event, data, "")
}

func regressionFile(t *testing.T, repo, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func regressionCheckpoint(t *testing.T, repo string) check.TurnCheckpoint {
	t.Helper()
	state, err := gitutil.FlinchStateDir(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	root, err := gitutil.RepoRoot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	cp, ok, err := check.LoadCheckpoint(state, "regression", root)
	if err != nil || !ok {
		t.Fatalf("checkpoint missing: %v", err)
	}
	return cp
}

func TestAntigravityStopRequiresNormalIdleCompletion(t *testing.T) {
	for _, tc := range []struct {
		reason   string
		idle     bool
		failure  string
		complete bool
	}{
		{"model_stop", true, "", true}, {"model_stop", false, "", false}, {"max_steps_exceeded", true, "", false}, {"error", true, "runner failed", false}, {"model_stop", true, "runner failed", false}, {"user_cancelled", true, "", false},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			repo := initHookTestRepo(t)
			regressionHook(t, repo, HostAntigravity, "PreInvocation", map[string]any{"lastUserInput": "Change the default", "invocationNum": 0})
			regressionFile(t, repo, "retry.go", "package hookdemo\nfunc Delay(v *int) int { return 200 }\n")
			regressionHook(t, repo, HostAntigravity, "Stop", map[string]any{"terminationReason": tc.reason, "fullyIdle": tc.idle, "error": tc.failure})
			if got := regressionCheckpoint(t, repo).PreviousEndSnap != ""; got != tc.complete {
				t.Fatalf("processed stop = %v, want %v", got, tc.complete)
			}
		})
	}
}

func TestDuplicatePromptDeliveryKeepsFirstCheckpoint(t *testing.T) {
	repo := initHookTestRepo(t)
	start := map[string]any{"turn_id": "turn-123", "prompt": "Use the supplied value"}
	regressionHook(t, repo, HostCodex, "UserPromptSubmit", start)
	first := regressionCheckpoint(t, repo)
	regressionFile(t, repo, "retry.go", "package hookdemo\nfunc Delay(v *int) int { return 33 }\n")
	regressionHook(t, repo, HostCodex, "UserPromptSubmit", start)
	second := regressionCheckpoint(t, repo)
	if first.TurnID != second.TurnID || first.SnapshotSHA != second.SnapshotSHA {
		t.Fatal("duplicate prompt reset the turn baseline")
	}
}

func TestMachineTurnCannotFinishAnOlderHumanTurn(t *testing.T) {
	repo := initHookTestRepo(t)
	regressionHook(t, repo, HostClaude, "UserPromptSubmit", map[string]any{"prompt": "Read this package"})
	regressionHook(t, repo, HostClaude, "UserPromptSubmit", map[string]any{"prompt": "Poll job status", "origin": "scheduled"})
	regressionFile(t, repo, "retry.go", "package hookdemo\nfunc Delay(v *int) int { return 34 }\n")
	regressionHook(t, repo, HostClaude, "Stop", map[string]any{})
	state, err := gitutil.FlinchStateDir(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	root, err := gitutil.RepoRoot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if cp, ok, err := check.LoadCheckpoint(state, "regression", root); err != nil || (ok && cp.PreviousEndSnap != "") {
		t.Fatalf("machine stop used an older human checkpoint: %+v, %v", cp, err)
	}
}

func TestReuseNoticeDeliveredOncePerHumanTurn(t *testing.T) {
	for _, host := range []Host{HostClaude, HostCodex} {
		t.Run(string(host), func(t *testing.T) {
			repo := initHookTestRepo(t)
			regressionHook(t, repo, host, "UserPromptSubmit", map[string]any{"turn_id": "before", "prompt": "Implement retry"})
			regressionFile(t, repo, "retry.go", "package hookdemo\n\nfunc Delay(v *int) int {\n\tif v == nil || *v == 0 { return 100 };return *v\n}\n")
			regressionHook(t, repo, host, "Stop", map[string]any{"turn_id": "before"})
			regressionHook(t, repo, host, "UserPromptSubmit", map[string]any{"turn_id": "fix", "prompt": "Treat zero as an explicit value"})
			regressionFile(t, repo, "retry.go", "package hookdemo\n\nfunc Delay(v *int) int {\n\tif v == nil { return 100 };return *v\n}\n")
			p := filepath.Join(repo, "retry_test.go")
			testData, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			row := "{name: \"omitted_uses_default\", in: nil, want: 100},"
			regressionFile(t, repo, "retry_test.go", strings.Replace(string(testData), row, row+"\n\t\t{name: \"explicit_zero\", in: intPtr(0), want: 0},", 1))
			stop := map[string]any{"turn_id": "fix", "last_assistant_message": "Fixed the zero delay."}
			r := regressionHook(t, repo, host, "Stop", stop)
			var msg struct{ SystemMessage, Decision string }
			if err := json.Unmarshal(r.Stdout, &msg); err != nil || !strings.Contains(msg.SystemMessage, "explicit_zero") || msg.Decision != "" {
				t.Fatalf("expected a visible proof notice without another generation, got %q", r.Stdout)
			}
			r = regressionHook(t, repo, host, "Stop", stop)
			if strings.Contains(string(r.Stdout), "systemMessage") {
				t.Fatalf("duplicate proof notice: %s", r.Stdout)
			}
		})
	}
}

func TestInstallerPreservesForeignGroupNumbersAndTempFile(t *testing.T) {
	t.Run("Antigravity group", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.Mkdir(filepath.Join(repo, ".agents"), 0o700); err != nil {
			t.Fatal(err)
		}
		content := "{\"flinch\":{\"enabled\":false,\"Stop\":[{\"command\":\"custom\"}]}}"
		regressionFile(t, repo, ".agents/hooks.json", content)
		_, _ = InstallHooks(repo, HostAntigravity, "flinch")
		got, err := os.ReadFile(filepath.Join(repo, ".agents", "hooks.json"))
		if err != nil || string(got) != content {
			t.Fatalf("existing user group was changed: %s (%v)", got, err)
		}
	})
	t.Run("opaque settings and temporary siblings", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.Mkdir(filepath.Join(repo, ".claude"), 0o700); err != nil {
			t.Fatal(err)
		}
		regressionFile(t, repo, ".claude/settings.json", "{\"sequence\":9007199254740993}")
		regressionFile(t, repo, ".claude/settings.json.tmp", "user content")
		if _, err := InstallHooks(repo, HostClaude, "flinch"); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(filepath.Join(repo, ".claude", "settings.json"))
		tmp, _ := os.ReadFile(filepath.Join(repo, ".claude", "settings.json.tmp"))
		if !strings.Contains(string(got), "9007199254740993") || string(tmp) != "user content" {
			t.Fatalf("installer changed unrelated data: %s; temporary file %q", got, tmp)
		}
	})
}

// A host may retry delivery before the agent has even seen its continuation.
func TestAuthoringContinuationReplayKeepsOriginalHumanTurn(t *testing.T) {
	repo := initHookTestRepo(t)
	regressionHook(t, repo, HostCodex, "UserPromptSubmit", map[string]any{"prompt": "Implement the first version", "turn_id": "first"})
	regressionFile(t, repo, "retry.go", "package hookdemo\n\nfunc Delay(v *int) int { return 50 }\n")
	regressionHook(t, repo, HostCodex, "Stop", map[string]any{"turn_id": "first"})
	regressionHook(t, repo, HostCodex, "UserPromptSubmit", map[string]any{"prompt": "Correct the retry delay", "turn_id": "second"})
	regressionFile(t, repo, "retry.go", "package hookdemo\n\nfunc Delay(v *int) int { return 25 }\n")
	stop := map[string]any{"turn_id": "second", "last_assistant_message": "Adjusted the default"}
	r := regressionHook(t, repo, HostCodex, "Stop", stop)
	var reply struct{ Decision, Reason string }
	if err := json.Unmarshal(r.Stdout, &reply); err != nil || reply.Decision != "block" {
		t.Fatalf("expected authoring request, got %q", r.Stdout)
	}
	if strings.Contains(reply.Reason, "AGENTS.md") {
		t.Fatalf("offered a guide that did not exist: %s", reply.Reason)
	}
	before := regressionCheckpoint(t, repo)
	if before.TotalContinuations != 1 {
		t.Fatalf("continuation not recorded before response: %d", before.TotalContinuations)
	}
	regressionHook(t, repo, HostCodex, "Stop", stop)
	if count := regressionCheckpoint(t, repo).TotalContinuations; count != before.TotalContinuations {
		t.Fatalf("duplicate Stop spent another continuation: %d", count)
	}
	regressionHook(t, repo, HostCodex, "UserPromptSubmit", map[string]any{"turn_id": "second", "prompt": reply.Reason})
	next := regressionCheckpoint(t, repo)
	if before.TurnID != next.TurnID || before.TotalContinuations != next.TotalContinuations {
		t.Fatalf("Flinch continuation opened a new human turn: %s -> %s", before.TurnID, next.TurnID)
	}
}

func TestIdenticalHumanPromptsAfterStopOpenSeparateAntigravityTurns(t *testing.T) {
	repo := initHookTestRepo(t)
	fields := map[string]any{"lastUserInput": "Continue", "invocationNum": 0}
	regressionHook(t, repo, HostAntigravity, "PreInvocation", fields)
	first := regressionCheckpoint(t, repo)
	regressionHook(t, repo, HostAntigravity, "Stop", map[string]any{"terminationReason": "model_stop", "fullyIdle": true, "executionNum": 0})
	regressionHook(t, repo, HostAntigravity, "PreInvocation", fields)
	if second := regressionCheckpoint(t, repo); first.TurnID == second.TurnID {
		t.Fatal("a second human prompt was mistaken for a repeated model invocation")
	}
}

func TestConcurrentStopsRequestOneContinuation(t *testing.T) {
	repo := initHookTestRepo(t)
	regressionHook(t, repo, HostCodex, "UserPromptSubmit", map[string]any{"prompt": "Add retry delay", "turn_id": "first"})
	regressionFile(t, repo, "retry.go", "package hookdemo\n\nfunc Delay(v *int) int { return 50 }\n")
	regressionHook(t, repo, HostCodex, "Stop", map[string]any{"turn_id": "first"})
	regressionHook(t, repo, HostCodex, "UserPromptSubmit", map[string]any{"prompt": "Correct the delay", "turn_id": "second"})
	regressionFile(t, repo, "retry.go", "package hookdemo\n\nfunc Delay(v *int) int { return 25 }\n")
	stop, err := json.Marshal(map[string]any{"cwd": repo, "session_id": "regression", "turn_id": "second", "last_assistant_message": "Adjusted the delay"})
	if err != nil {
		t.Fatal(err)
	}
	const copies = 5
	results := make(chan Response, copies)
	start := make(chan struct{})
	for range copies {
		go func() { <-start; results <- Handle(context.Background(), HostCodex, "Stop", stop, "") }()
	}
	close(start)
	requests := 0
	for range copies {
		if r := <-results; strings.Contains(string(r.Stdout), "\"block\"") {
			requests++
		}
	}
	if requests != 1 {
		t.Fatalf("expected one repair across concurrent callbacks, got %d", requests)
	}
	if cp := regressionCheckpoint(t, repo); cp.TotalContinuations != 1 {
		t.Fatalf("expected one persisted charge, got %d", cp.TotalContinuations)
	}
}
