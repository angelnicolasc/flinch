package pause

import (
	"testing"
	"time"
)

func TestPauseResumeExpiryAndEnvDisable(t *testing.T) {
	stateDir := t.TempDir()
	t0 := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)

	if _, active, err := Check(stateDir, t0); err != nil || active {
		t.Fatalf("expected unpaused initially, active=%v err=%v", active, err)
	}

	// Pause for 1 hour.
	st, err := Pause(stateDir, 1*time.Hour, "manual spike", t0)
	if err != nil || !st.Paused {
		t.Fatalf("Pause failed: %+v err=%v", st, err)
	}
	got, active, err := Check(stateDir, t0.Add(30*time.Minute))
	if err != nil || !active || got.Reason != "manual spike" {
		t.Fatalf("expected active pause at +30m, got %+v active=%v err=%v", got, active, err)
	}

	// After expiry, Check auto-clears pause.
	_, active, err = Check(stateDir, t0.Add(61*time.Minute))
	if err != nil || active {
		t.Fatalf("expected expired pause to clear, active=%v err=%v", active, err)
	}

	// Explicit pause + resume.
	if _, err := Pause(stateDir, 0, "indefinite", t0); err != nil {
		t.Fatal(err)
	}
	wasActive, err := Resume(stateDir, t0.Add(5*time.Minute))
	if err != nil || !wasActive {
		t.Fatalf("expected Resume wasActive=true, got %v err=%v", wasActive, err)
	}
	if _, active, _ := Check(stateDir, t0.Add(6*time.Minute)); active {
		t.Fatalf("expected unpaused after Resume")
	}

	// FLINCH=off disables for one session without touching pause.json.
	t.Setenv(EnvDisableVar, "off")
	got, active, err = Check(stateDir, t0)
	if err != nil || !active || got.Reason != "FLINCH=off" {
		t.Fatalf("expected FLINCH=off pause, got %+v active=%v err=%v", got, active, err)
	}
}
