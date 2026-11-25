package pause

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EnvDisableVar disables Flinch capture and hooks for one session when set to "off".
const EnvDisableVar = "FLINCH"

// State represents an active pause stored under <git-common-dir>/flinch/pause.json
// so it applies across the local clone and all its worktrees.
type State struct {
	Paused    bool       `json:"paused"`
	StartedAt time.Time  `json:"started_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Reason    string     `json:"reason,omitempty"`
}

// Interval records a completed or active pause interval for local transparency.
// While hooks are off, the number of turns in that interval is explicitly unknown.
type Interval struct {
	StartedAt    time.Time  `json:"started_at"`
	EndedAt      *time.Time `json:"ended_at,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	Reason       string     `json:"reason,omitempty"`
	TurnsUnknown bool       `json:"turns_unknown"`
}

// DisabledByEnv reports whether FLINCH=off is set for the current process/session.
func DisabledByEnv() bool {
	val := strings.ToLower(strings.TrimSpace(os.Getenv(EnvDisableVar)))
	return val == "off" || val == "0" || val == "false"
}

func statePath(stateDir string) string {
	return filepath.Join(stateDir, "pause.json")
}

func intervalLogPath(stateDir string) string {
	return filepath.Join(stateDir, "log", "pauses.jsonl")
}

// Pause activates a local clone pause until resumed or duration expires.
func Pause(stateDir string, duration time.Duration, reason string, now time.Time) (State, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	st := State{
		Paused:    true,
		StartedAt: now,
		Reason:    strings.TrimSpace(reason),
	}
	if duration > 0 {
		exp := now.Add(duration)
		st.ExpiresAt = &exp
	}
	if err := saveState(stateDir, st); err != nil {
		return State{}, err
	}
	_ = appendInterval(stateDir, Interval{
		StartedAt:    st.StartedAt,
		ExpiresAt:    st.ExpiresAt,
		Reason:       st.Reason,
		TurnsUnknown: true,
	})
	return st, nil
}

// Resume clears any active pause and logs the completed interval end time.
func Resume(stateDir string, now time.Time) (bool, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	st, active, err := Check(stateDir, now)
	if err != nil {
		return false, err
	}
	if err := os.Remove(statePath(stateDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if active {
		end := now
		_ = appendInterval(stateDir, Interval{
			StartedAt:    st.StartedAt,
			EndedAt:      &end,
			ExpiresAt:    st.ExpiresAt,
			Reason:       st.Reason,
			TurnsUnknown: true,
		})
	}
	return active, nil
}

// Check inspects whether Flinch is currently paused (either via FLINCH=off or pause.json).
// Expired pauses are automatically cleared and recorded.
func Check(stateDir string, now time.Time) (State, bool, error) {
	if DisabledByEnv() {
		return State{Paused: true, Reason: "FLINCH=off"}, true, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	data, err := os.ReadFile(statePath(stateDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return State{}, false, nil
		}
		return State{}, false, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}, false, err
	}
	if !st.Paused {
		return State{}, false, nil
	}
	if st.ExpiresAt != nil && !now.Before(*st.ExpiresAt) {
		// Pause expired: clear file and record closed interval.
		_ = os.Remove(statePath(stateDir))
		end := *st.ExpiresAt
		_ = appendInterval(stateDir, Interval{
			StartedAt:    st.StartedAt,
			EndedAt:      &end,
			ExpiresAt:    st.ExpiresAt,
			Reason:       st.Reason,
			TurnsUnknown: true,
		})
		return State{}, false, nil
	}
	return st, true, nil
}

func saveState(stateDir string, st State) error {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	p := statePath(stateDir)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func appendInterval(stateDir string, iv Interval) error {
	p := intervalLogPath(stateDir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(iv)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s\n", b)
	return err
}
