package hook

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"github.com/angelnicolasc/flinch/internal/usersetup"
)

// Incident describes the most recent hook failure that Flinch contained by
// letting the turn continue. Every field is a timestamp, a version or a value
// from a fixed list, so the record can never carry a payload, a message, a path
// or a session identifier.
type Incident struct {
	Time    time.Time `json:"time"`
	Version string    `json:"version"`
	Host    string    `json:"host"`
	Event   string    `json:"event"`
	Stage   string    `json:"stage"`
}

const incidentFile = "hook-incident.json"

var (
	incidentHosts  = []string{"claude", "codex", "antigravity"}
	incidentEvents = []string{"UserPromptSubmit", "PreInvocation", "Stop", "StopFailure"}
	incidentStages = []string{"read", "registration", "handle", "write"}
	versionPattern = regexp.MustCompile(`^[0-9A-Za-z.+-]{1,40}$`)
)

func enumerated(value string, allowed []string) string {
	if slices.Contains(allowed, value) {
		return value
	}
	return "other"
}

func (i Incident) sanitized() Incident {
	i.Time = i.Time.UTC().Truncate(time.Second)
	if !versionPattern.MatchString(i.Version) {
		i.Version = "unknown"
	}
	i.Host = enumerated(i.Host, incidentHosts)
	i.Event = enumerated(i.Event, incidentEvents)
	i.Stage = enumerated(i.Stage, incidentStages)
	return i
}

// RecordIncident keeps the latest contained hook failure for flinch doctor. It
// writes only inside an existing user installation, replaces the previous
// record atomically, and skips the write once the hook's deadline has passed.
// A failure to record is ignored: the turn always takes priority.
func RecordIncident(ctx context.Context, host, event, stage, version string) {
	if ctx.Err() != nil {
		return
	}
	dir, err := usersetup.StateDir()
	if err != nil {
		return
	}
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		return
	}
	data, err := json.Marshal(Incident{Time: time.Now(), Version: version, Host: host, Event: event, Stage: stage}.sanitized())
	if err != nil {
		return
	}
	f, err := os.CreateTemp(dir, ".hook-incident-*")
	if err != nil {
		return
	}
	name := f.Name()
	_, writeErr := f.Write(append(data, '\n'))
	if err := errors.Join(writeErr, f.Close()); err != nil {
		_ = os.Remove(name)
		return
	}
	if err := os.Rename(name, filepath.Join(dir, incidentFile)); err != nil {
		_ = os.Remove(name)
	}
}

// LastIncident reads the latest contained hook failure, if one was recorded.
// A missing, oversized or unreadable record reports none.
func LastIncident() (Incident, bool) {
	dir, err := usersetup.StateDir()
	if err != nil {
		return Incident{}, false
	}
	f, err := os.Open(filepath.Join(dir, incidentFile))
	if err != nil {
		return Incident{}, false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4<<10))
	if err != nil {
		return Incident{}, false
	}
	var incident Incident
	if json.Unmarshal(data, &incident) != nil || incident.Time.IsZero() {
		return Incident{}, false
	}
	return incident.sanitized(), true
}
