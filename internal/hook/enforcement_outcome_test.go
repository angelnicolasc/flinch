package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/angelnicolasc/flinch/internal/config"
	"github.com/angelnicolasc/flinch/internal/stats"
)

func TestStopRecordsOneEnforcementOutcome(t *testing.T) {
	for _, tc := range []struct {
		name   string
		kind   stats.EventKind
		reason string
	}{
		{"checked", stats.EventTurnChecked, ""},
		{"deadline", stats.EventTurnDeferred, stats.TurnDeadlineReached},
		{"snapshot error", stats.EventTurnDeferred, stats.EnforcementFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := initHookTestRepo(t)
			if tc.name == "deadline" {
				cfg := config.Default()
				cfg.Loop.Deadline = "1ns"
				if err := config.Save(repo, cfg); err != nil {
					t.Fatal(err)
				}
			}
			regressionHook(t, repo, HostClaude, "UserPromptSubmit", map[string]any{"prompt": "Read this package"})
			if tc.name == "snapshot error" {
				if err := os.WriteFile(filepath.Join(repo, ".git", "index"), []byte("incomplete index"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				got := regressionHook(t, repo, HostClaude, "Stop", map[string]any{"event_id": "stop-1"})
				want := AllowResponse(HostClaude)
				if got.ExitCode != want.ExitCode || !bytes.Equal(got.Stdout, want.Stdout) {
					t.Fatalf("unexpected host response: %+v", got)
				}
			}
			data, err := os.ReadFile(filepath.Join(repo, ".git", "flinch", "log", "events.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			var event stats.Event
			if err := json.Unmarshal(data, &event); err != nil {
				t.Fatalf("expected exactly one event: %s (%v)", data, err)
			}
			_, snapshot := event.PhaseMS["snapshot"]
			if event.Kind != tc.kind || event.Reason != tc.reason || !snapshot || event.Surface != "claude" {
				t.Fatalf("unexpected turn outcome: %+v", event)
			}
		})
	}
}
