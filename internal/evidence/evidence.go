package evidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// LifecycleState tracks a protection candidate from initial capture through integration (§3.2).
type LifecycleState string

const (
	StateSignal     LifecycleState = "signal"
	StateIncident   LifecycleState = "incident"
	StateCandidate  LifecycleState = "candidate"
	StateVerified   LifecycleState = "verified"
	StateProposed   LifecycleState = "proposed"
	StateApproved   LifecycleState = "approved"
	StateIntegrated LifecycleState = "integrated"
	StateDismissed  LifecycleState = "dismissed"
)

// SurfaceStatus describes observed protection accreditation on a concrete execution surface (§3.2).
type SurfaceStatus string

const (
	SurfaceActive         SurfaceStatus = "active"
	SurfaceTriggerPending SurfaceStatus = "trigger_pending"
	SurfaceWarning        SurfaceStatus = "warning"
	SurfaceSkipped        SurfaceStatus = "skipped"
	SurfaceFailed         SurfaceStatus = "failed"
	SurfaceUnknown        SurfaceStatus = "unknown"
)

// SurfaceKind identifies where enforcement is accredited.
type SurfaceKind string

const (
	SurfaceCI        SurfaceKind = "ci"
	SurfaceAgentTurn SurfaceKind = "agent_turn"
)

// NegativeControlProof verifies that introducing the prohibited pattern in a disposable
// copy fails the real configured command and attributes the failure to the expected rule/selector ID.
type NegativeControlProof struct {
	Executed     bool   `json:"executed"`
	Failed       bool   `json:"failed"`
	ExpectedID   string `json:"expected_id"`
	AttributedID string `json:"attributed_id,omitempty"`
	Detail       string `json:"detail,omitempty"`
}

// AllowedControlProof verifies that a legitimate in-scope case passes under the same command.
type AllowedControlProof struct {
	Executed bool   `json:"executed"`
	Passed   bool   `json:"passed"`
	Detail   string `json:"detail,omitempty"`
}

// TriggerWitness records that a real pull_request or merge_group event executed the job with matching filters.
// A manual workflow_dispatch run alone does not satisfy PR trigger accreditation (§3.2, §10.2).
type TriggerWitness struct {
	Witnessed    bool      `json:"witnessed"`
	Event        string    `json:"event,omitempty"` // pull_request | merge_group | workflow_dispatch
	WorkflowPath string    `json:"workflow_path,omitempty"`
	WorkflowHash string    `json:"workflow_hash,omitempty"`
	Job          string    `json:"job,omitempty"`
	RunID        string    `json:"run_id,omitempty"`
	ObservedAt   time.Time `json:"observed_at,omitempty"`
}

// ActivationRecord stores verifiable accreditation evidence for one reflex on one surface at an integrated SHA.
type ActivationRecord struct {
	Schema            int                  `json:"schema"`
	ReflexID          string               `json:"reflex_id"`
	ReflexVersion     int                  `json:"reflex_version"`
	ReflexFingerprint string               `json:"reflex_fingerprint"`
	CandidateHash     string               `json:"candidate_hash,omitempty"`
	IntegratedSHA     string               `json:"integrated_sha"`
	MergeStyle        string               `json:"merge_style,omitempty"` // merge | squash | rebase | cherry_pick
	Surface           SurfaceKind          `json:"surface"`
	HostVersion       string               `json:"host_version,omitempty"`
	Runner            string               `json:"runner,omitempty"`
	Command           string               `json:"command,omitempty"`
	Job               string               `json:"job,omitempty"`
	Filters           []string             `json:"filters,omitempty"`
	Mode              string               `json:"mode,omitempty"` // block | warn | repair_if_supported | advise
	RequiredForMerge  bool                 `json:"required_for_merge"`
	ArtifactPresent   bool                 `json:"artifact_present"`
	ControlDiscovered bool                 `json:"control_discovered"`
	CleanTreePassed   bool                 `json:"clean_tree_passed"`
	NegativeControl   NegativeControlProof `json:"negative_control"`
	AllowedControl    AllowedControlProof  `json:"allowed_control"`
	TriggerWitness    TriggerWitness       `json:"trigger_witness,omitempty"`
	Status            SurfaceStatus        `json:"status"`
	Summary           string               `json:"summary,omitempty"`
	RecordedAt        time.Time            `json:"recorded_at"`
}

// EvaluateStatus derives the honest SurfaceStatus from the collected evidence.
// A generic green run without negative-control attribution never accredits activation.
func (r *ActivationRecord) EvaluateStatus() SurfaceStatus {
	if !r.ArtifactPresent || !r.ControlDiscovered {
		r.Status = SurfaceFailed
		r.Summary = "artifact missing or control not discovered at integrated SHA"
		return r.Status
	}
	if !r.CleanTreePassed {
		r.Status = SurfaceFailed
		r.Summary = "clean integrated tree did not pass configured check"
		return r.Status
	}
	if !r.NegativeControl.Executed || !r.NegativeControl.Failed {
		r.Status = SurfaceFailed
		r.Summary = "negative control did not fail in disposable copy"
		return r.Status
	}
	if r.NegativeControl.ExpectedID != "" && r.NegativeControl.AttributedID != r.NegativeControl.ExpectedID {
		r.Status = SurfaceFailed
		r.Summary = fmt.Sprintf("negative control failure attributed to %q, expected %q", r.NegativeControl.AttributedID, r.NegativeControl.ExpectedID)
		return r.Status
	}
	if r.AllowedControl.Executed && !r.AllowedControl.Passed {
		r.Status = SurfaceFailed
		r.Summary = "legitimate allowed case failed under configured check"
		return r.Status
	}

	if r.Mode == "warn" || r.Mode == "advise" {
		r.Status = SurfaceWarning
		r.Summary = "verified in advisory/warning mode (non-blocking)"
		return r.Status
	}

	if r.Surface == SurfaceCI {
		if !r.TriggerWitness.Witnessed || (r.TriggerWitness.Event != "pull_request" && r.TriggerWitness.Event != "merge_group") {
			r.Status = SurfaceTriggerPending
			r.Summary = "artifact verified on integrated SHA; PR/merge_group trigger witness pending"
			return r.Status
		}
		if r.RequiredForMerge {
			r.Status = SurfaceActive
			r.Summary = "active in CI (required check for merge)"
		} else {
			r.Status = SurfaceActive
			r.Summary = "active in CI (informational job; not marked required for merge)"
		}
		return r.Status
	}

	r.Status = SurfaceActive
	r.Summary = "active on agent turn surface"
	return r.Status
}

// IsValidFor checks whether this accreditation still applies to the given fingerprint, workflow hash, and SHA.
func (r ActivationRecord) IsValidFor(reflexFingerprint, workflowHash, integratedSHA string) (bool, string) {
	if reflexFingerprint != "" && r.ReflexFingerprint != "" && r.ReflexFingerprint != reflexFingerprint {
		return false, "reflex artifact or configuration changed since accreditation"
	}
	if integratedSHA != "" && r.IntegratedSHA != "" && r.IntegratedSHA != integratedSHA {
		return false, "integrated SHA changed since accreditation"
	}
	if r.Surface == SurfaceCI && workflowHash != "" && r.TriggerWitness.WorkflowHash != "" && r.TriggerWitness.WorkflowHash != workflowHash {
		return false, "CI workflow or filters changed since trigger witness"
	}
	return true, ""
}

// Store persists ActivationRecords under <stateDir>/evidence.
type Store struct {
	dir string
}

// NewStore returns an activation evidence store rooted at stateDir/evidence.
func NewStore(stateDir string) *Store {
	return &Store{dir: filepath.Join(stateDir, "evidence")}
}

func (s *Store) recordPath(reflexID string, surface SurfaceKind) string {
	safe := strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(reflexID)
	return filepath.Join(s.dir, fmt.Sprintf("%s-%s.json", safe, surface))
}

// Save evaluates status and writes the record atomically.
func (s *Store) Save(rec ActivationRecord) (ActivationRecord, error) {
	if rec.Schema == 0 {
		rec.Schema = 1
	}
	if rec.RecordedAt.IsZero() {
		rec.RecordedAt = time.Now().UTC()
	}
	rec.EvaluateStatus()

	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return ActivationRecord{}, fmt.Errorf("create evidence dir: %w", err)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return ActivationRecord{}, err
	}
	path := s.recordPath(rec.ReflexID, rec.Surface)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return ActivationRecord{}, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return ActivationRecord{}, err
	}
	return rec, nil
}

// Load reads a single accreditation record, returning os.ErrNotExist if absent.
func (s *Store) Load(reflexID string, surface SurfaceKind) (ActivationRecord, error) {
	data, err := os.ReadFile(s.recordPath(reflexID, surface))
	if err != nil {
		return ActivationRecord{}, err
	}
	var rec ActivationRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return ActivationRecord{}, err
	}
	return rec, nil
}

// List returns all saved accreditation records sorted by reflex ID and surface.
func (s *Store) List() ([]ActivationRecord, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []ActivationRecord
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var rec ActivationRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ReflexID != out[j].ReflexID {
			return out[i].ReflexID < out[j].ReflexID
		}
		return out[i].Surface < out[j].Surface
	})
	return out, nil
}
