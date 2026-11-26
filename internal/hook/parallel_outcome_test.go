package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/angelnicolasc/flinch/internal/check"
	"github.com/angelnicolasc/flinch/internal/config"
	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/stats"
)

func TestParallelWorktreeStopsRecordOneOutcomePerTurn(t *testing.T) {
	repo := initHookTestRepo(t)
	ctx := context.Background()
	dir := t.TempDir()
	var worktrees [4]string
	for i := range worktrees {
		worktrees[i] = filepath.Join(dir, fmt.Sprintf("worker-%d", i))
		if _, _, err := gitutil.Run(ctx, repo, nil, "worktree", "add", "--detach", worktrees[i], "HEAD"); err != nil {
			t.Fatal(err)
		}
	}

	for round, kind := range []stats.EventKind{stats.EventTurnChecked, stats.EventTurnDeferred} {
		turn := fmt.Sprintf("round-%d", round)
		receipt := "stop-" + turn
		var stops [len(worktrees)][]byte
		for i, worktree := range worktrees {
			if kind == stats.EventTurnDeferred {
				cfg := config.Default()
				cfg.Loop.Deadline = "1ns"
				if err := config.Save(worktree, cfg); err != nil {
					t.Fatal(err)
				}
			}
			// Identical session and event IDs exercise the worktree part of checkpoint identity.
			regressionHook(t, worktree, HostClaude, "UserPromptSubmit", map[string]any{"turn_id": turn, "prompt": "Read this package"})
			data, err := json.Marshal(map[string]string{"cwd": worktree, "session_id": "regression", "turn_id": turn, "event_id": receipt})
			if err != nil {
				t.Fatal(err)
			}
			stops[i] = data
		}

		start := make(chan struct{})
		var group sync.WaitGroup
		for _, payload := range stops {
			group.Go(func() {
				<-start
				got := Handle(ctx, HostClaude, "Stop", payload, "")
				if got.ExitCode != 0 || len(got.Stdout) != 0 {
					t.Errorf("unexpected Stop response in %s: %+v", turn, got)
				}
			})
		}
		close(start)
		group.Wait()
		for _, worktree := range worktrees {
			cp := regressionCheckpoint(t, worktree)
			if cp.HostTurnID != turn || len(cp.StopReceipts) != 1 || !cp.StopReceipts[check.HashPrompt(receipt)] {
				t.Fatalf("missing Stop receipt in %s for %s", worktree, turn)
			}
		}

		// Replaying one of the four deliveries must leave the shared log unchanged.
		Handle(ctx, HostClaude, "Stop", stops[0], "")
		data, err := os.ReadFile(filepath.Join(repo, ".git", "flinch", "log", "events.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
		if want := (round + 1) * len(worktrees); len(lines) != want {
			t.Fatalf("%s: got %d events, want %d: %s", turn, len(lines), want, data)
		}
		for _, line := range lines[round*len(worktrees):] {
			var event stats.Event
			if err := json.Unmarshal(line, &event); err != nil {
				t.Fatal(err)
			}
			reason := ""
			if kind == stats.EventTurnDeferred {
				reason = stats.TurnDeadlineReached
			}
			_, hasSnapshot := event.PhaseMS["snapshot"]
			if event.Kind != kind || event.Reason != reason || event.Surface != string(HostClaude) || !hasSnapshot {
				t.Fatalf("%s: unexpected outcome: %+v", turn, event)
			}
		}
	}
}
