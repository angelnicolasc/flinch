package stats

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// EventKind categorizes local enforcement and recurrence observations (§8.2).
type EventKind string

const (
	EventTurnChecked        EventKind = "turn_checked"
	EventTurnIntercepted    EventKind = "turn_intercepted"
	EventCIIntercepted      EventKind = "ci_intercepted"
	EventConfirmedEscape    EventKind = "confirmed_escape"
	EventUnexposedSurface   EventKind = "unexposed_surface"
	EventPossibleRecurrence EventKind = "possible_recurrence" // excluded from confirmed escape numerator (§8.2)
	EventCheckSkipped       EventKind = "check_skipped"
)

// Event is one append-only local telemetry-free observation stored in <stateDir>/log/events.jsonl.
type Event struct {
	Timestamp  time.Time `json:"timestamp"`
	Kind       EventKind `json:"kind"`
	ReflexID   string    `json:"check_id,omitempty"`
	Surface    string    `json:"surface,omitempty"`
	DurationMS int64     `json:"duration_ms,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	CommitSHA  string    `json:"commit_sha,omitempty"`
}

// Summary reports raw counts, explicit denominators, omissions, and latency percentiles (§8.2, §9).
type Summary struct {
	Schema                  int        `json:"schema"`
	ObservedScope           string     `json:"observed_scope"`
	DeterministicReflexes   int        `json:"deterministic_checks"`
	ProbabilisticGuides     int        `json:"probabilistic_guides"`
	TurnsChecked            int        `json:"turns_checked"`
	TurnInterceptions       int        `json:"turn_interceptions"`
	CIInterceptions         int        `json:"ci_interceptions"`
	ConfirmedEscapes        int        `json:"confirmed_escapes"`
	UnexposedSurfaceEvents  int        `json:"unexposed_surface_events"`
	PossibleRecurrences     int        `json:"possible_recurrences"`
	SkippedChecks           int        `json:"skipped_checks"`
	TurnLatencyP95MS        int64      `json:"turn_latency_p95_ms"`
	EscapeRatePer100Checked *float64   `json:"escape_rate_per_100_checked,omitempty"`
	FirstEventAt            *time.Time `json:"first_event_at,omitempty"`
	LastEventAt             *time.Time `json:"last_event_at,omitempty"`
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

	f, err := os.Open(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return sum, nil
		}
		return sum, err
	}
	defer f.Close()

	var latencies []int64
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
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
			if ev.DurationMS > 0 {
				latencies = append(latencies, ev.DurationMS)
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
		}
	}
	if err := scanner.Err(); err != nil {
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

	return sum, nil
}
