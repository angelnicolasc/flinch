package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/reflex"
	"github.com/angelnicolasc/flinch/internal/stats"
)

func TestDoctorPreservesHealthyChecksWhenEventHistoryIsUnreadable(t *testing.T) {
	isolateSetupHome(t)
	ctx := context.Background()
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if _, _, err := gitutil.Run(ctx, repo, nil, args...); err != nil {
			t.Fatal(err)
		}
	}
	git("init")
	if _, err := reflex.Save(repo, reflex.Reflex{ID: "reviewed", Version: 1, Obligation: "Check format verbs.",
		Rung: reflex.RungLint, Artifact: reflex.Artifact{Tool: "go-vet", Rule: "printf"}}); err != nil {
		t.Fatal(err)
	}
	git("add", ".flinch")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "Review check")
	state := filepath.Join(repo, ".git", "flinch")
	history := filepath.Join(state, "log", "events.jsonl")
	if err := os.MkdirAll(history, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(t *testing.T, command string, opts ...string) string {
		t.Helper()
		var out, stderr bytes.Buffer
		if rc := Run(ctx, append([]string{command, "--repo", repo}, opts...), &out, &stderr); rc != 0 {
			t.Fatalf("%s returned %d: %s", command, rc, &stderr)
		}
		return out.String()
	}
	type report struct {
		Trusted int                  `json:"trusted_checks"`
		Problem string               `json:"event_history_error"`
		Last    map[string]time.Time `json:"last_turn_checked_by_host"`
	}
	t.Run("doctor_text", func(t *testing.T) {
		out := run(t, "doctor")
		if !strings.Contains(out, "1 in the trusted revision") || !strings.Contains(out, "Event history unavailable:") {
			t.Fatalf("missing check count or history diagnostic: %s", out)
		}
	})
	for _, command := range []string{"doctor", "init"} {
		t.Run(command+"_json", func(t *testing.T) {
			out := run(t, command, "--json")
			var got report
			var target any = &got
			if command == "init" {
				target = &struct {
					Report *report `json:"report"`
				}{&got}
			}
			if err := json.Unmarshal([]byte(out), target); err != nil || got.Trusted != 1 || got.Problem == "" {
				t.Fatalf("missing check count or history diagnostic: %s (%v)", out, err)
			}
		})
	}
	t.Run("long_event", func(t *testing.T) {
		if err := os.Remove(history); err != nil {
			t.Fatal(err)
		}
		store := stats.NewStore(state)
		at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		if err := store.Record(stats.Event{Timestamp: at, Kind: stats.EventTurnChecked, Surface: "codex", Reason: strings.Repeat("x", 128<<10)}); err != nil {
			t.Fatal(err)
		}
		out := run(t, "doctor", "--json")
		var got report
		if err := json.Unmarshal([]byte(out), &got); err != nil || got.Problem != "" || !got.Last["codex"].Equal(at) {
			t.Fatalf("could not read a valid long event: %s (%v)", out, err)
		}
	})
}
