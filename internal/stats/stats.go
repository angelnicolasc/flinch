package stats

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/angelnicolasc/flinch/internal/check"
)

// EventKind categorizes local enforcement and recurrence observations.
type EventKind string

const (
	EventTurnChecked        EventKind = "turn_checked"
	EventTurnDeferred       EventKind = "turn_deferred"
	EventTurnIntercepted    EventKind = "turn_intercepted"
	EventCIIntercepted      EventKind = "ci_intercepted"
	EventConfirmedEscape    EventKind = "confirmed_escape"
	EventUnexposedSurface   EventKind = "unexposed_surface"
	EventPossibleRecurrence EventKind = "possible_recurrence" // excluded from confirmed escape numerator
	EventCheckSkipped       EventKind = "check_skipped"
)

const (
	TurnDeadlineReached = "turn deadline reached before a verdict"
	EnforcementFailed   = "enforcement failed"
)

// Event is one append-only local telemetry-free observation stored in <stateDir>/log/events.jsonl.
type Event struct {
	Timestamp    time.Time        `json:"timestamp"`
	Kind         EventKind        `json:"kind"`
	ReflexID     string           `json:"check_id,omitempty"`
	Surface      string           `json:"surface,omitempty"`
	DurationMS   *int64           `json:"duration_ms,omitempty"`
	PhaseMS      map[string]int64 `json:"phase_ms,omitempty"`
	TimeoutLimit string           `json:"timeout_limit,omitempty"`
	Reason       string           `json:"reason,omitempty"`
	CommitSHA    string           `json:"commit_sha,omitempty"`
}

// Summary reports raw counts, explicit denominators, omissions, and latency percentiles.
type Summary struct {
	Schema                  int            `json:"schema"`
	ObservedScope           string         `json:"observed_scope"`
	DeterministicReflexes   int            `json:"deterministic_checks"`
	ProbabilisticGuides     int            `json:"probabilistic_guides"`
	TurnsChecked            int            `json:"turns_checked"`
	TurnsDeferred           int            `json:"turns_deferred"`
	TurnDeadlineDeferrals   int            `json:"turn_deadline_deferrals"`
	VerdictFraction         *float64       `json:"verdict_fraction,omitempty"`
	TestTimeoutsByLimit     map[string]int `json:"test_timeouts_by_limit,omitempty"`
	TurnInterceptions       int            `json:"turn_interceptions"`
	CIInterceptions         int            `json:"ci_interceptions"`
	ConfirmedEscapes        int            `json:"confirmed_escapes"`
	UnexposedSurfaceEvents  int            `json:"unexposed_surface_events"`
	PossibleRecurrences     int            `json:"possible_recurrences"`
	SkippedChecks           int            `json:"skipped_checks"`
	TurnLatencyP95MS        int64          `json:"turn_latency_p95_ms"`
	EscapeRatePer100Checked *float64       `json:"escape_rate_per_100_checked,omitempty"`
	FirstEventAt            *time.Time     `json:"first_event_at,omitempty"`
	LastEventAt             *time.Time     `json:"last_event_at,omitempty"`
}

// Store persists local enforcement events under <stateDir>/log/events.jsonl.
type Store struct {
	path string
}

// NewStore opens a local event log rooted at stateDir/log/events.jsonl.
func NewStore(stateDir string) *Store {
	return &Store{path: filepath.Join(stateDir, "log", "events.jsonl")}
}

// Record appends one Event to the local JSONL log.
func (s *Store) Record(ev Event) error {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s\n", line)
	return err
}

// RecordTurn writes one outcome and the skip reasons available at that point.
// The caller's context reserves enough time for the host to receive its reply.
func (s *Store) RecordTurn(ctx context.Context, surface string, res check.Result, deferred string) {
	if ctx.Err() != nil {
		return
	}
	kind := EventTurnChecked
	if deferred != "" {
		kind = EventTurnDeferred
	}
	_ = s.Record(Event{Kind: kind, Surface: surface, DurationMS: &res.DurationMS, PhaseMS: res.PhaseMS, Reason: deferred})
	for _, sk := range res.SkippedControls {
		if ctx.Err() != nil {
			return
		}
		_ = s.Record(Event{Kind: EventCheckSkipped, ReflexID: sk.ReflexID, Surface: surface, Reason: sk.Reason, TimeoutLimit: sk.TimeoutLimit})
	}
}

func (s *Store) readEvents(visit func(Event)) error {
	f, err := os.Open(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		visit(ev)
	}
	return scanner.Err()
}

// LastTurnsChecked returns the latest recorded turn check for each requested host.
func (s *Store) LastTurnsChecked(hosts ...string) (map[string]time.Time, error) {
	last := make(map[string]time.Time)
	err := s.readEvents(func(ev Event) {
		if ev.Kind == EventTurnChecked && slices.Contains(hosts, ev.Surface) && ev.Timestamp.After(last[ev.Surface]) {
			last[ev.Surface] = ev.Timestamp.UTC()
		}
	})
	return last, err
}

// Summarize computes a denominator-preserving Summary from the local log.
func (s *Store) Summarize(deterministicCount, probabilisticCount int, scopeLabel string) (Summary, error) {
	if scopeLabel == "" {
		scopeLabel = "local clone event log"
	}
	sum := Summary{
		Schema:                1,
		ObservedScope:         scopeLabel,
		DeterministicReflexes: deterministicCount,
		ProbabilisticGuides:   probabilisticCount,
	}
	var latencies []int64
	err := s.readEvents(func(ev Event) {
		ts := ev.Timestamp
		if sum.FirstEventAt == nil || ts.Before(*sum.FirstEventAt) {
			tCopy := ts
			sum.FirstEventAt = &tCopy
		}
		if sum.LastEventAt == nil || ts.After(*sum.LastEventAt) {
			tCopy := ts
			sum.LastEventAt = &tCopy
		}
		switch ev.Kind {
		case EventTurnChecked:
			sum.TurnsChecked++
			if ev.DurationMS != nil && *ev.DurationMS > 0 {
				latencies = append(latencies, *ev.DurationMS)
			}
		case EventTurnDeferred:
			sum.TurnsDeferred++
			if ev.Reason == TurnDeadlineReached {
				sum.TurnDeadlineDeferrals++
			}
		case EventTurnIntercepted:
			sum.TurnInterceptions++
		case EventCIIntercepted:
			sum.CIInterceptions++
		case EventConfirmedEscape:
			sum.ConfirmedEscapes++
		case EventUnexposedSurface:
			sum.UnexposedSurfaceEvents++
		case EventPossibleRecurrence:
			sum.PossibleRecurrences++
		case EventCheckSkipped:
			sum.SkippedChecks++
			if ev.TimeoutLimit != "" {
				if sum.TestTimeoutsByLimit == nil {
					sum.TestTimeoutsByLimit = make(map[string]int)
				}
				sum.TestTimeoutsByLimit[ev.TimeoutLimit]++
			}
		}
	})
	if err != nil {
		return sum, err
	}

	if len(latencies) > 0 {
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		idx := int(math.Ceil(0.95*float64(len(latencies)))) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(latencies) {
			idx = len(latencies) - 1
		}
		sum.TurnLatencyP95MS = latencies[idx]
	}

	if sum.TurnsChecked > 0 {
		rate := (float64(sum.ConfirmedEscapes) / float64(sum.TurnsChecked)) * 100.0
		sum.EscapeRatePer100Checked = &rate
	}
	if total := sum.TurnsChecked + sum.TurnsDeferred; total > 0 {
		fraction := float64(sum.TurnsChecked) / float64(total)
		sum.VerdictFraction = &fraction
	}

	return sum, nil
}
