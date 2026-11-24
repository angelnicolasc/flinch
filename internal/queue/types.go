package queue

import (
	"fmt"
	"time"

	"github.com/angelnicolasc/flinch/internal/evidence"
)

// Provenance distinguishes human input from machine-generated continuations or Flinch notices (§2.2).
type Provenance string

const (
	ProvenanceHuman     Provenance = "human"
	ProvenanceSynthetic Provenance = "synthetic"
)

// EvidenceState distinguishes receiving a signal from having reproducible broken/fixed evidence (§2.1).
type EvidenceState string

const (
	EvidenceSignalReceived       EvidenceState = "signal_received"
	EvidenceAvailable            EvidenceState = "evidence_available"
	EvidencePendingCloneEvidence EvidenceState = "pending_clone_evidence"
)

// DeliveryState tracks idempotent patch publication to Git/PR targets (§2.3, §3.1).
type DeliveryState string

const (
	DeliveryPending     DeliveryState = "pending"
	DeliveryDelivered   DeliveryState = "delivered"
	DeliveryBlocked     DeliveryState = "blocked"
	DeliveryReconciling DeliveryState = "reconciling"
)

// Signal represents a single deduplicated observation from a session adapter, Git, or CI.
type Signal struct {
	ID               string        `json:"id"`
	Repo             string        `json:"repo"`
	Source           string        `json:"source"` // correction | fix | ci | review | note
	EventID          string        `json:"event_id"`
	Scope            string        `json:"scope,omitempty"`
	ObservedAt       time.Time     `json:"observed_at"`
	Provenance       Provenance    `json:"provenance"`
	Surface          string        `json:"surface,omitempty"` // claude-code | codex | opencode | git | ci
	SessionID        string        `json:"session_id,omitempty"`
	WorktreeID       string        `json:"worktree_id,omitempty"`
	Branch           string        `json:"branch,omitempty"`
	PRNumber         int           `json:"pr_number,omitempty"`
	BrokenRef        string        `json:"broken_ref,omitempty"`
	FixedRef         string        `json:"fixed_ref,omitempty"`
	EvidenceState    EvidenceState `json:"evidence_state"`
	EvidenceHash     string        `json:"evidence_hash,omitempty"`
	Summary          string        `json:"summary,omitempty"`
	Paths            []string      `json:"paths,omitempty"`
	EscapeOfReflexID string        `json:"escape_of_reflex_id,omitempty"`
}

// DedupKey uniquely identifies a source event in a repository so retries never duplicate signals.
func (s Signal) DedupKey() string {
	return fmt.Sprintf("%s|%s|%s", s.Repo, s.Source, s.EventID)
}

// Lease grants exclusive time-bounded processing rights to a worker so crashes recover cleanly (§2.3).
type Lease struct {
	WorkerID  string    `json:"worker_id"`
	ClaimedAt time.Time `json:"claimed_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Suspension records why a queued episode is paused and what event should wake it (§2.3).
type Suspension struct {
	Cause         string    `json:"cause"`
	NextTrigger   string    `json:"next_trigger"`
	LastCheckedAt time.Time `json:"last_checked_at"`
}

// Episode aggregates related signals into a single actionable work unit on the queue.
type Episode struct {
	ID               string                  `json:"id"`
	Repo             string                  `json:"repo"`
	LifecycleState   evidence.LifecycleState `json:"lifecycle_state"`
	Priority         int                     `json:"priority"` // higher drains first
	Branch           string                  `json:"branch,omitempty"`
	PRNumber         int                     `json:"pr_number,omitempty"`
	CaptureSurface   string                  `json:"capture_surface,omitempty"`
	EvidenceState    EvidenceState           `json:"evidence_state"`
	BrokenRef        string                  `json:"broken_ref,omitempty"`
	FixedRef         string                  `json:"fixed_ref,omitempty"`
	Paths            []string                `json:"paths,omitempty"`
	Summary          string                  `json:"summary,omitempty"`
	EscapeOfReflexID string                  `json:"escape_of_reflex_id,omitempty"`
	SignalIDs        []string                `json:"signal_ids"`
	CandidateVersion int                     `json:"candidate_version"`
	DeliveryState    DeliveryState           `json:"delivery_state"`
	Lease            *Lease                  `json:"lease,omitempty"`
	Suspension       *Suspension             `json:"suspension,omitempty"`
	CreatedAt        time.Time               `json:"created_at"`
	UpdatedAt        time.Time               `json:"updated_at"`
}

// DeliveryKey builds the idempotent delivery key linking repo, episode, candidate version, and target (§2.3).
func DeliveryKey(repo, episodeID string, candidateVersion int, target string) string {
	return fmt.Sprintf("%s|%s|v%d|%s", repo, episodeID, candidateVersion, target)
}

// DeliveryRecord records a completed or in-progress delivery for idempotency checks.
type DeliveryRecord struct {
	Key         string    `json:"key"`
	EpisodeID   string    `json:"episode_id"`
	Target      string    `json:"target"`
	ObservedSHA string    `json:"observed_sha,omitempty"`
	ResultSHA   string    `json:"result_sha,omitempty"`
	PRURL       string    `json:"pr_url,omitempty"`
	DeliveredAt time.Time `json:"delivered_at"`
}

// Cursor records the last persisted source watermark. Saved strictly AFTER signal persistence (§2.2).
type Cursor struct {
	Source    string    `json:"source"`
	Watermark string    `json:"watermark"`
	UpdatedAt time.Time `json:"updated_at"`
}

// IngestResult describes what happened when a signal was submitted to the queue.
type IngestResult struct {
	Signal           Signal  `json:"signal"`
	Episode          Episode `json:"episode,omitempty"`
	Duplicate        bool    `json:"duplicate"`
	IgnoredSynthetic bool    `json:"ignored_synthetic"`
	LinkedExisting   bool    `json:"linked_existing"`
}
