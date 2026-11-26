package stats

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/angelnicolasc/flinch/internal/check"
)

func TestStatsPreservesDenominatorsAndExcludesPossibleRecurrencesFromEscapes(t *testing.T) {
	store := NewStore(t.TempDir())
	for _, ms := range []int64{100, 150, 200, 250, 900} {
		if err := store.Record(Event{Kind: EventTurnChecked, DurationMS: &ms}); err != nil {
			t.Fatal(err)
		}
	}
	_ = store.Record(Event{Kind: EventTurnIntercepted, ReflexID: "r1"})
	_ = store.Record(Event{Kind: EventConfirmedEscape, ReflexID: "r1"})
	_ = store.Record(Event{Kind: EventPossibleRecurrence, ReflexID: "r1"})
	_ = store.Record(Event{Kind: EventUnexposedSurface, ReflexID: "r1", Reason: "no plugin"})
	_ = store.Record(Event{Kind: EventCheckSkipped, ReflexID: "r1", Reason: "timeout"})
	store.RecordTurn(t.Context(), "codex", check.Result{
		DurationMS:      10000,
		PhaseMS:         map[string]int64{"test": 5000},
		SkippedControls: []check.SkippedControl{{ReflexID: "slow", Reason: "test stopped at max_test_runtime (5s); deferred to CI", TimeoutLimit: check.LimitMaxTestRuntime}},
	}, TurnDeadlineReached)

	sum, err := store.Summarize(2, 1, "test clone")
	if err != nil {
		t.Fatalf("Summarize failed: %v", err)
	}
	if sum.TurnsChecked != 5 {
		t.Fatalf("expected 5 turns checked, got %d", sum.TurnsChecked)
	}
	if sum.ConfirmedEscapes != 1 || sum.PossibleRecurrences != 1 {
		t.Fatalf("possible recurrence must stay separate from confirmed escapes: %+v", sum)
	}
	if sum.EscapeRatePer100Checked == nil || *sum.EscapeRatePer100Checked != 20.0 {
		t.Fatalf("expected 20.0 escape rate (1/5 * 100), got %v", sum.EscapeRatePer100Checked)
	}
	if sum.TurnLatencyP95MS != 900 {
		t.Fatalf("expected p95=900ms, got %d", sum.TurnLatencyP95MS)
	}
	if sum.TurnsDeferred != 1 || sum.TurnDeadlineDeferrals != 1 || sum.VerdictFraction == nil || *sum.VerdictFraction != 5.0/6 {
		t.Fatalf("wrong verdict coverage: %+v", sum)
	}
	if sum.SkippedChecks != 2 || sum.TestTimeoutsByLimit[check.LimitMaxTestRuntime] != 1 {
		t.Fatalf("missing skip reason on a deferred turn: %+v", sum)
	}
}

func TestTurnOutcomesKeepZeroDuration(t *testing.T) {
	store := NewStore(t.TempDir())
	store.RecordTurn(t.Context(), "codex", check.Result{
		SkippedControls: []check.SkippedControl{{Reason: "tool unavailable"}},
	}, "")
	store.RecordTurn(t.Context(), "codex", check.Result{}, TurnDeadlineReached)
	log, err := os.Open(store.path)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	decoder := json.NewDecoder(log)
	for _, want := range []struct {
		kind     EventKind
		duration string
	}{{EventTurnChecked, "0"}, {EventCheckSkipped, ""}, {EventTurnDeferred, "0"}} {
		var event struct {
			Kind     EventKind       `json:"kind"`
			Duration json.RawMessage `json:"duration_ms"`
		}
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		if event.Kind != want.kind || string(event.Duration) != want.duration {
			t.Fatalf("got %s with duration_ms %q, want %s with %q", event.Kind, event.Duration, want.kind, want.duration)
		}
	}
}

func TestExpiredHostBudgetLeavesNoTurnEvent(t *testing.T) {
	store := NewStore(t.TempDir())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	store.RecordTurn(ctx, "codex", check.Result{}, EnforcementFailed)
	sum, err := store.Summarize(0, 0, "")
	if err != nil || sum.LastEventAt != nil || sum.VerdictFraction != nil {
		t.Fatalf("expected empty event history: %+v, %v", sum, err)
	}
}
