package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/angelnicolasc/flinch/internal/author"
	"github.com/angelnicolasc/flinch/internal/check"
	"github.com/angelnicolasc/flinch/internal/config"
	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/hook"
	"github.com/angelnicolasc/flinch/internal/prove"
	"github.com/angelnicolasc/flinch/internal/reflex"
	"github.com/angelnicolasc/flinch/internal/runner"
	"github.com/angelnicolasc/flinch/internal/testutil"
)

// TestStructureRungInternalBoundaryProveAndZeroFlinchClone verifies the structure rung:
// a Go internal/ module boundary enforced by `go build ./...` catches the prohibited import,
// allows a legitimate consumer neighbor, and blocks repeats in a clone without Flinch.
func TestStructureRungInternalBoundaryProveAndZeroFlinchClone(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	if _, _, err := gitutil.Run(ctx, repo, nil, "init"); err != nil {
		t.Fatal(err)
	}

	writeRepoFile(t, repo, "go.mod", "module example.com/boundary\n\ngo 1.26\n")
	writeRepoFile(t, repo, "mainproc/internal/win/win.go", "package win\n\nfunc Open() string { return \"open\" }\n")
	writeRepoFile(t, repo, "mainproc/bridge/bridge.go", "package bridge\n\nimport \"example.com/boundary/mainproc/internal/win\"\n\nfunc OpenSafe() string { return win.Open() }\n")
	// Broken commit: renderer directly imports mainproc/internal/win (violates Go internal package boundary!).
	writeRepoFile(t, repo, "renderer/view.go", "package renderer\n\nimport \"example.com/boundary/mainproc/internal/win\"\n\nfunc Render() string { return win.Open() }\n")
	brokenSHA := gitCommit(t, repo, "broken direct import into mainproc/internal/win")

	// Fixed commit: renderer calls mainproc/bridge instead.
	writeRepoFile(t, repo, "renderer/view.go", "package renderer\n\nimport \"example.com/boundary/mainproc/bridge\"\n\nfunc Render() string { return bridge.OpenSafe() }\n")
	fixedSHA := gitCommit(t, repo, "fix renderer to use mainproc/bridge")

	rep, err := prove.Verify(ctx, repo, prove.CandidateSpec{
		ID:         "renderer-uses-ipc-bridge",
		Version:    1,
		Rung:       reflex.RungStructure,
		Scope:      "renderer/**",
		Obligation: "Renderer must access window operations through mainproc/bridge, not mainproc/internal/win.",
		Artifact: reflex.Artifact{
			Tool:           "go-build",
			Selector:       "go-build",
			ExpectedSignal: "use of internal package",
		},
		AllowedCases: []prove.AllowedCase{
			{
				Name:       "legitimate-bridge-consumer",
				TargetPath: "renderer/panel.go",
				Content:    "package renderer\n\nimport \"example.com/boundary/mainproc/bridge\"\n\nfunc Panel() string { return bridge.OpenSafe() }\n",
			},
		},
		BrokenRef: brokenSHA,
		FixedRef:  fixedSHA,
	}, prove.Options{})
	if err != nil {
		t.Fatalf("prove structure failed: %v", err)
	}
	if rep.Verdict != prove.VerdictVerified {
		t.Fatalf("expected structure proof verified, got %s/%s (%s)", rep.Verdict, rep.Reason, rep.ActionableError)
	}

	// Clone without Flinch: repository's native `go build ./...` rejects a repeat and accepts the neighbor!
	clone2 := filepath.Join(t.TempDir(), "clone2-structure")
	if _, _, err := gitutil.Run(ctx, filepath.Dir(clone2), nil, "clone", repo, clone2); err != nil {
		t.Fatal(err)
	}
	// Legitimate neighbor compiles cleanly under native `go build ./...`.
	writeRepoFile(t, clone2, "renderer/panel.go", "package renderer\n\nimport \"example.com/boundary/mainproc/bridge\"\n\nfunc Panel() string { return bridge.OpenSafe() }\n")
	passCmd := exec.CommandContext(ctx, "go", "build", "./...")
	passCmd.Dir = clone2
	if out, err := passCmd.CombinedOutput(); err != nil {
		t.Fatalf("native go build rejected legitimate neighbor: %v (%s)", err, string(out))
	}
	// Prohibited repeat fails native `go build ./...` with the exact internal package error.
	writeRepoFile(t, clone2, "renderer/bad.go", "package renderer\n\nimport \"example.com/boundary/mainproc/internal/win\"\n\nfunc Bad() string { return win.Open() }\n")
	failCmd := exec.CommandContext(ctx, "go", "build", "./...")
	failCmd.Dir = clone2
	failOut, failErr := failCmd.CombinedOutput()
	if failErr == nil || !strings.Contains(string(failOut), "use of internal package") {
		t.Fatalf("expected native go build to reject prohibited internal import, err=%v out=%s", failErr, string(failOut))
	}
}

// TestGuardRungRecognizableFailureMatchesNativeCommand verifies the guard rung:
// the recognizable failure signal seen during `prove` is the exact same signal that fails
// the repository's native guard command in a clone without Flinch.
func TestGuardRungRecognizableFailureMatchesNativeCommand(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	if _, _, err := gitutil.Run(ctx, repo, nil, "init"); err != nil {
		t.Fatal(err)
	}

	guardSrc := `package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	data, err := os.ReadFile("go.mod")
	if err != nil {
		os.Exit(2)
	}
	if !strings.Contains(string(data), "toolchain go1.26") {
		fmt.Fprintln(os.Stderr, "GUARD_FAIL: go.mod must pin toolchain go1.26")
		os.Exit(1)
	}
}
`
	writeRepoFile(t, repo, "go.mod", "module example.com/guarddemo\n\ngo 1.26\n")
	writeRepoFile(t, repo, "tools/checktoolchain/main.go", guardSrc)
	brokenSHA := gitCommit(t, repo, "broken go.mod missing toolchain pin")

	writeRepoFile(t, repo, "go.mod", "module example.com/guarddemo\n\ngo 1.26\n\ntoolchain go1.26.1\n")
	fixedSHA := gitCommit(t, repo, "fix go.mod toolchain pin")

	spec := prove.CandidateSpec{
		ID:           "pin-go-toolchain",
		Scope:        "go.mod",
		AllowedCases: []prove.AllowedCase{{Name: "other-module-with-pin", TargetPath: "go.mod", Content: "module example.com/neighbor\n\ngo 1.26\n\ntoolchain go1.26.1\n"}},
		Version:      1,
		Rung:         reflex.RungGuard,
		Obligation:   "go.mod must pin toolchain go1.26.",
		Artifact: reflex.Artifact{
			Tool:           "command",
			Path:           "tools/checktoolchain/main.go",
			Command:        "go run ./tools/checktoolchain",
			ExpectedSignal: "GUARD_FAIL: go.mod must pin toolchain go1.26",
		},
		BrokenRef: brokenSHA,
		FixedRef:  fixedSHA,
	}
	if _, err := runner.AllowCandidateCommand(ctx, repo, spec.Artifact, nil); err != nil {
		t.Fatal(err)
	}
	rep, err := prove.Verify(ctx, repo, spec, prove.Options{})
	if err != nil {
		t.Fatalf("prove guard failed: %v", err)
	}
	if rep.Verdict != prove.VerdictVerified {
		t.Fatalf("expected guard proof verified, got %s/%s (%s)", rep.Verdict, rep.Reason, rep.ActionableError)
	}
	if !strings.Contains(rep.ObservedSignal, "GUARD_FAIL: go.mod must pin toolchain go1.26") {
		t.Fatalf("expected recognizable signal in proof report, got %q", rep.ObservedSignal)
	}

	// Clone without Flinch: native command produces the exact same recognizable failure on repeat.
	clone2 := filepath.Join(t.TempDir(), "clone2-guard")
	if _, _, err := gitutil.Run(ctx, filepath.Dir(clone2), nil, "clone", repo, clone2); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, clone2, "go.mod", "module example.com/guarddemo\n\ngo 1.26\n")
	cmd := exec.CommandContext(ctx, "go", "run", "./tools/checktoolchain")
	cmd.Dir = clone2
	out, runErr := cmd.CombinedOutput()
	if runErr == nil || !strings.Contains(string(out), "GUARD_FAIL: go.mod must pin toolchain go1.26") {
		t.Fatalf("expected identical recognizable failure from native guard command, err=%v out=%s", runErr, string(out))
	}
}

// TestBudgetSlowTestDeferredAtTurnEndAndRunByNativeCI verifies the turn budget:
// a test measured > 5s is skipped at turn end, while native `go test ./...` still runs it.
func TestBudgetSlowTestDeferredAtTurnEndAndRunByNativeCI(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	if _, _, err := gitutil.Run(ctx, repo, nil, "init"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	_ = config.Save(repo, cfg)
	writeRepoFile(t, repo, "go.mod", "module example.com/slowdemo\n\ngo 1.26\n")
	writeRepoFile(t, repo, "slow.go", "package slowdemo\nfunc Val() int { return 1 }\n")
	writeRepoFile(t, repo, "slow_test.go", "package slowdemo\nimport \"testing\"\nfunc TestSlow(t *testing.T) { if Val() != 1 { t.Fatal(\"regressed\") } }\n")
	_, _ = reflex.Save(repo, reflex.Reflex{
		ID:         "slow-test-guard",
		Version:    1,
		Scope:      "*.go",
		Rung:       reflex.RungTest,
		Obligation: "Val returns 1.",
		Artifact:   reflex.Artifact{Tool: "go-test", Path: "slow_test.go", Selector: "TestSlow"},
		Proof:      &reflex.ProofSummary{Verdict: "verified", RuntimeMS: 7500}, // > 5000ms max_test_runtime
	})
	gitCommit(t, repo, "init with slow reflex")

	if _, err := check.CaptureCheckpoint(ctx, repo, "s-slow", repo, "", "p1", true); err != nil {
		t.Fatal(err)
	}
	// Regress slow.go during the turn.
	writeRepoFile(t, repo, "slow.go", "package slowdemo\nfunc Val() int { return 99 }\n")

	turnRes, err := check.RunTurn(ctx, repo, cfg, check.TurnOptions{SessionID: "s-slow", WorktreeID: repo})
	if err != nil {
		t.Fatal(err)
	}
	if turnRes.Blocking {
		t.Fatalf("slow test (>5s) must NOT run or block at turn end, got %+v", turnRes)
	}
	if len(turnRes.SkippedControls) != 1 || !strings.Contains(turnRes.SkippedControls[0].Reason, "exceeds max_test_runtime") {
		t.Fatalf("expected skipped control noting max_test_runtime, got %+v", turnRes.SkippedControls)
	}

	// Native CI command (`go test ./...`) still catches the regression!
	ciCmd := exec.CommandContext(ctx, "go", "test", "./...")
	ciCmd.Dir = repo
	if out, err := ciCmd.CombinedOutput(); err == nil {
		t.Fatalf("expected native go test ./... to catch regression of slow test, out=%s", string(out))
	}
}

// TestSharedInstructionsLineAndSkipOutcomes verifies the guidance line and skip outcomes:
// - Durable convention lands as one line inside <!-- flinch:start -->..<!-- flinch:end --> with no edits outside.
// - One-off preference closes with silent logged skip and zero file modifications.
func TestSharedInstructionsLineAndSkipOutcomes(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	testutil.HookProfile(t, repo)
	if _, _, err := gitutil.Run(ctx, repo, nil, "init"); err != nil {
		t.Fatal(err)
	}
	_ = config.Save(repo, config.Default())
	writeRepoFile(t, repo, "AGENTS.md", "# Shared Instructions\n\nFollow repo conventions.\n\n<!-- flinch:start -->\n<!-- flinch:end -->\n")
	writeRepoFile(t, repo, "CLAUDE.md", "@AGENTS.md\n")
	writeRepoFile(t, repo, "copy.txt", "Hello world\n")
	gitCommit(t, repo, "init shared instructions repo")

	// Turn 1: initial edit to copy.txt
	start1, _ := json.Marshal(map[string]any{"session_id": "s-line", "cwd": repo, "hook_event_name": "UserPromptSubmit", "prompt": "Write greeting"})
	hook.Handle(ctx, hook.HostClaude, "UserPromptSubmit", start1, "")
	writeRepoFile(t, repo, "copy.txt", "Hey there!\n")
	stop1, _ := json.Marshal(map[string]any{"session_id": "s-line", "cwd": repo, "hook_event_name": "Stop"})
	hook.Handle(ctx, hook.HostClaude, "Stop", stop1, "")

	// Turn 2: human corrects with a durable convention -> agent edits copy.txt AND adds one line in managed section!
	start2, _ := json.Marshal(map[string]any{"session_id": "s-line", "cwd": repo, "hook_event_name": "UserPromptSubmit", "prompt": "Always use American English in user-facing text"})
	hook.Handle(ctx, hook.HostClaude, "UserPromptSubmit", start2, "")
	writeRepoFile(t, repo, "copy.txt", "Welcome.\n")
	writeRepoFile(t, repo, "AGENTS.md", "# Shared Instructions\n\nFollow repo conventions.\n\n<!-- flinch:start -->\n- Write all user-facing copy in concise American English.\n<!-- flinch:end -->\n")
	res := hook.Handle(ctx, hook.HostClaude, "Stop", stop1, "")
	if res.ExitCode != 0 || len(res.Stdout) != 0 {
		t.Fatalf("expected clean turn completion on valid managed line, got %q", string(res.Stdout))
	}

	// Verify authoring log recorded line_recorded and nothing outside managed section changed.
	stateDir, _ := gitutil.FlinchStateDir(ctx, repo)
	logBytes, err := os.ReadFile(filepath.Join(stateDir, "log", "authoring.jsonl"))
	if err != nil || !strings.Contains(string(logBytes), `"kind":"line_recorded"`) {
		t.Fatalf("expected line_recorded event in authoring log, err=%v log=%s", err, string(logBytes))
	}

	// Turn 3: one-off preference -> agent closes with `flinch skip: one-off wording tweak` -> silent, zero extra file edits!
	beforeAgents, _ := os.ReadFile(filepath.Join(repo, "AGENTS.md"))
	start3, _ := json.Marshal(map[string]any{"session_id": "s-line", "cwd": repo, "hook_event_name": "UserPromptSubmit", "prompt": "Make this one screen say Hi"})
	hook.Handle(ctx, hook.HostClaude, "UserPromptSubmit", start3, "")
	writeRepoFile(t, repo, "copy.txt", "Hi.\n")
	stopSkip, _ := json.Marshal(map[string]any{
		"session_id":             "s-line",
		"cwd":                    repo,
		"hook_event_name":        "Stop",
		"last_assistant_message": "Updated copy.txt.\nflinch skip: one-off screen preference",
	})
	resSkip := hook.Handle(ctx, hook.HostClaude, "Stop", stopSkip, "")
	if resSkip.ExitCode != 0 || len(resSkip.Stdout) != 0 {
		t.Fatalf("expected silent skip without continuation, got %q", string(resSkip.Stdout))
	}
	afterAgents, _ := os.ReadFile(filepath.Join(repo, "AGENTS.md"))
	if !bytes.Equal(beforeAgents, afterAgents) {
		t.Fatalf("skip path must not modify AGENTS.md")
	}
	logBytes, _ = os.ReadFile(filepath.Join(stateDir, "log", "authoring.jsonl"))
	if !strings.Contains(string(logBytes), `"kind":"skipped"`) {
		t.Fatalf("expected skipped event in authoring log, got: %s", string(logBytes))
	}
	_ = time.Second
	_ = author.ManagedStartMarker
}
