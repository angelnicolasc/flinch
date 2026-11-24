package stats

import (
	"testing"
)

func TestStatsPreservesDenominatorsAndExcludesPossibleRecurrencesFromEscapes(t *testing.T) {
	store := NewStore(t.TempDir())
	for _, ms := range []int64{100, 150, 200, 250, 900} {
		if err := store.Record(Event{Kind: EventTurnChecked, DurationMS: ms}); err != nil {
			t.Fatal(err)
		}
	}
	_ = store.Record(Event{Kind: EventTurnIntercepted, ReflexID: "r1"})
	_ = store.Record(Event{Kind: EventConfirmedEscape, ReflexID: "r1"})
	_ = store.Record(Event{Kind: EventPossibleRecurrence, ReflexID: "r1"})
	_ = store.Record(Event{Kind: EventUnexposedSurface, ReflexID: "r1", Reason: "no plugin"})
	_ = store.Record(Event{Kind: EventCheckSkipped, ReflexID: "r1", Reason: "timeout"})

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
}
