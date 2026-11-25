package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/angelnicolasc/flinch/internal/config"
	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/reflex"
	"github.com/angelnicolasc/flinch/internal/runner"
)

func TestCommandNoticeOncePerContent(t *testing.T) {
	repo := initHookTestRepo(t)
	cfg := config.Default()
	cfg.Policy.Mode = config.ModeObserveOnly
	if err := config.Save(repo, cfg); err != nil {
		t.Fatal(err)
	}
	r := reflex.Reflex{
		ID: "guard", Version: 1, Obligation: "Check changed inputs", Scope: "input.txt",
		Artifact: reflex.Artifact{Tool: "script", Path: "guard.sh", Command: "sh guard.sh"},
	}
	if _, err := reflex.Save(repo, r); err != nil {
		t.Fatal(err)
	}
	regressionFile(t, repo, "input.txt", "initial")
	regressionFile(t, repo, "guard.sh", "exit 0\n")
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=fixture", "-c", "user.email=fixture@example.com", "-c", "commit.gpgsign=false", "commit", "-m", "Add guard"}} {
		if _, _, err := gitutil.Run(context.Background(), repo, nil, args...); err != nil {
			t.Fatal(err)
		}
	}
	for i, tc := range []struct {
		host   Host
		script string
		notice bool
	}{
		{HostAntigravity, "exit 0\n", false},
		{HostClaude, "exit 0\n", true},
		{HostCodex, "exit 0\n", false},
		{HostClaude, "exit 0 # edited\n", true},
		{HostCodex, "exit 0\n", false},
	} {
		start := "UserPromptSubmit"
		if tc.host == HostAntigravity {
			start = "PreInvocation"
		}
		id := fmt.Sprint(i)
		regressionFile(t, repo, "guard.sh", tc.script)
		regressionHook(t, repo, tc.host, start, map[string]any{"turn_id": id, "prompt": "Change the input"})
		regressionFile(t, repo, "input.txt", "input "+id)
		got := regressionHook(t, repo, tc.host, "Stop", map[string]any{"turn_id": id, "fullyIdle": true, "terminationReason": "model_stop"})
		if strings.Contains(string(got.Stdout), runner.CommandPermissionNotice) != tc.notice {
			t.Fatalf("turn %s, expected notice %t, got %s", id, tc.notice, got.Stdout)
		}
	}
}

func TestClaudeRecordingContainsOnlyEnvironmentComparisons(t *testing.T) {
	repo := initHookTestRepo(t)
	const private = "PRIVATE-ENVIRONMENT-95472"
	for _, key := range []string{"CLAUDE_CODE_ENTRYPOINT", "CLAUDE_PROJECT_DIR", "CLAUDE_CODE_CHILD_SESSION", "CLAUDECODE", "CLAUDE_CODE_SESSION_ID"} {
		t.Setenv(key, private)
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("FLINCH_RECORD", "1")
	for i, tc := range []struct{ all, same, desktop bool }{{false, false, false}, {true, true, false}, {true, false, false}, {true, true, true}} {
		if tc.all {
			for _, key := range []string{"CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_CHILD_SESSION", "CLAUDECODE", "CLAUDE_CODE_SESSION_ID"} {
				t.Setenv(key, private)
			}
			if tc.desktop {
				t.Setenv("CLAUDE_CODE_ENTRYPOINT", "claude-desktop")
			}
			t.Setenv("CLAUDE_PROJECT_DIR", repo)
			if !tc.same {
				t.Setenv("CLAUDE_CODE_SESSION_ID", private+"other")
				t.Setenv("CLAUDE_PROJECT_DIR", t.TempDir())
			}
		}
		input, _ := json.Marshal(map[string]any{"cwd": repo, "session_id": private, "prompt": "Update the fixture", "turn_id": fmt.Sprint(i)})
		Handle(context.Background(), HostClaude, "UserPromptSubmit", input, "")
		files, _ := filepath.Glob(filepath.Join(repo, ".git", "flinch", "recordings", "*.json"))
		if len(files) != 1 {
			t.Fatalf("expected one event, got %d", len(files))
		}
		data, err := os.ReadFile(files[0])
		if err != nil {
			t.Fatal(err)
		}
		var record struct {
			Environment map[string]bool `json:"claude_environment"`
		}
		if err := json.Unmarshal(data, &record); err != nil {
			t.Fatal(err)
		}
		want := map[string]bool{"entrypoint_present": tc.all, "entrypoint_desktop": tc.desktop, "project_dir_present": tc.all, "project_dir_same_clone": tc.same, "project_dir_same_checkout": tc.same, "child_session_present": tc.all, "claudecode_present": tc.all, "session_id_matches_payload": tc.same}
		if !reflect.DeepEqual(record.Environment, want) || bytes.Contains(data, []byte(private)) {
			t.Fatalf("case %d: expected only allowlisted comparisons", i)
		}
		if err := os.Remove(files[0]); err != nil {
			t.Fatal(err)
		}
	}
}
