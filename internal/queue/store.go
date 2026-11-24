package queue

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/angelnicolasc/flinch/internal/evidence"
	"github.com/angelnicolasc/flinch/internal/redact"
)

const (
	PriorityEscape = 100
	PriorityOpenPR = 50
	PriorityNormal = 10
)

// Store manages durable signals, episodes, cursors, and idempotent deliveries under <stateDir>/queue.
type Store struct {
	mu  sync.Mutex
	dir string
}

type queueData struct {
	Schema     int                       `json:"schema"`
	Signals    map[string]Signal         `json:"signals"`    // keyed by DedupKey
	Episodes   map[string]Episode        `json:"episodes"`   // keyed by Episode.ID
	Deliveries map[string]DeliveryRecord `json:"deliveries"` // keyed by DeliveryKey
}

type cursorData struct {
	Schema  int               `json:"schema"`
	Cursors map[string]Cursor `json:"cursors"`
}

// NewStore opens or initializes a durable queue store under stateDir/queue.
func NewStore(stateDir string) *Store {
	return &Store{dir: filepath.Join(stateDir, "queue")}
}

func (s *Store) dataPath() string   { return filepath.Join(s.dir, "state.json") }
func (s *Store) cursorPath() string { return filepath.Join(s.dir, "cursors.json") }

func (s *Store) loadData() (queueData, error) {
	data, err := os.ReadFile(s.dataPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return queueData{
				Schema:     1,
				Signals:    make(map[string]Signal),
				Episodes:   make(map[string]Episode),
				Deliveries: make(map[string]DeliveryRecord),
			}, nil
		}
		return queueData{}, err
	}
	var q queueData
	if err := json.Unmarshal(data, &q); err != nil {
		return queueData{}, fmt.Errorf("decode queue state: %w", err)
	}
	if q.Signals == nil {
		q.Signals = make(map[string]Signal)
	}
	if q.Episodes == nil {
		q.Episodes = make(map[string]Episode)
	}
	if q.Deliveries == nil {
		q.Deliveries = make(map[string]DeliveryRecord)
	}
	return q, nil
}

func (s *Store) saveData(q queueData) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	q.Schema = 1
	b, err := json.MarshalIndent(q, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(s.dataPath(), b)
}

func (s *Store) loadCursors() (cursorData, error) {
	data, err := os.ReadFile(s.cursorPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cursorData{Schema: 1, Cursors: make(map[string]Cursor)}, nil
		}
		return cursorData{}, err
	}
	var c cursorData
	if err := json.Unmarshal(data, &c); err != nil {
		return cursorData{}, fmt.Errorf("decode cursors: %w", err)
	}
	if c.Cursors == nil {
		c.Cursors = make(map[string]Cursor)
	}
	return c, nil
}

func (s *Store) saveCursors(c cursorData) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	c.Schema = 1
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(s.cursorPath(), b)
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// IngestSignal persists a deduplicated signal, links or opens an episode, and only AFTER
// persisting the signal updates the optional source cursor watermark (§2.2).
// Synthetic signals are excluded from human correction sources.
func (s *Store) IngestSignal(sig Signal, cursorWatermark string) (IngestResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if sig.Repo == "" {
		return IngestResult{}, fmt.Errorf("signal repo is required")
	}
	if sig.Source == "" {
		return IngestResult{}, fmt.Errorf("signal source is required")
	}
	if sig.EventID == "" {
		return IngestResult{}, fmt.Errorf("signal event_id is required")
	}
	if sig.ObservedAt.IsZero() {
		sig.ObservedAt = time.Now().UTC()
	}
	if sig.Provenance == "" {
		sig.Provenance = ProvenanceHuman
	}
	if sig.EvidenceState == "" {
		if sig.BrokenRef != "" && sig.FixedRef != "" {
			sig.EvidenceState = EvidenceAvailable
		} else {
			sig.EvidenceState = EvidenceSignalReceived
		}
	}
	sig.Summary = redact.Prompt(sig.Summary, 500)
	if sig.ID == "" {
		h := sha256.Sum256([]byte(sig.DedupKey()))
		sig.ID = "sig-" + hex.EncodeToString(h[:6])
	}

	q, err := s.loadData()
	if err != nil {
		return IngestResult{}, err
	}

	dedup := sig.DedupKey()
	if existing, ok := q.Signals[dedup]; ok {
		res := IngestResult{Signal: existing, Duplicate: true}
		for _, ep := range q.Episodes {
			for _, id := range ep.SignalIDs {
				if id == existing.ID {
					res.Episode = ep
					break
				}
			}
		}
		return res, nil
	}

	// Synthetic messages (continuations, curator messages, Flinch notices) must never enter human correction episodes.
	if sig.Provenance == ProvenanceSynthetic && sig.Source == "correction" {
		q.Signals[dedup] = sig
		if err := s.saveData(q); err != nil {
			return IngestResult{}, err
		}
		if cursorWatermark != "" {
			if err := s.advanceCursorUnlocked(sig.Source, cursorWatermark); err != nil {
				return IngestResult{}, err
			}
		}
		return IngestResult{Signal: sig, IgnoredSynthetic: true}, nil
	}

	q.Signals[dedup] = sig

	// Link to an existing non-terminal episode only when revision/PR + paths overlap explicitly.
	var ep Episode
	linked := false
	for _, candidate := range q.Episodes {
		if candidate.Repo != sig.Repo {
			continue
		}
		if candidate.LifecycleState == evidence.StateIntegrated || candidate.LifecycleState == evidence.StateDismissed {
			continue
		}
		if shouldLink(candidate, sig) {
			ep = candidate
			linked = true
			break
		}
	}

	now := time.Now().UTC()
	if linked {
		ep.SignalIDs = append(ep.SignalIDs, sig.ID)
		ep.Paths = mergeUnique(ep.Paths, sig.Paths)
		if ep.BrokenRef == "" && sig.BrokenRef != "" {
			ep.BrokenRef = sig.BrokenRef
		}
		if ep.FixedRef == "" && sig.FixedRef != "" {
			ep.FixedRef = sig.FixedRef
		}
		if ep.EvidenceState != EvidenceAvailable && sig.EvidenceState == EvidenceAvailable {
			ep.EvidenceState = EvidenceAvailable
		}
		if sig.EscapeOfReflexID != "" && ep.EscapeOfReflexID == "" {
			ep.EscapeOfReflexID = sig.EscapeOfReflexID
		}
		if ep.PRNumber == 0 && sig.PRNumber > 0 {
			ep.PRNumber = sig.PRNumber
		}
		ep.Priority = computePriority(ep)
		ep.UpdatedAt = now
	} else {
		h := sha256.Sum256([]byte(sig.ID + "|" + now.Format(time.RFC3339Nano)))
		ep = Episode{
			ID:               "ep-" + hex.EncodeToString(h[:6]),
			Repo:             sig.Repo,
			LifecycleState:   evidence.StateIncident,
			Branch:           sig.Branch,
			PRNumber:         sig.PRNumber,
			CaptureSurface:   sig.Surface,
			EvidenceState:    sig.EvidenceState,
			BrokenRef:        sig.BrokenRef,
			FixedRef:         sig.FixedRef,
			Paths:            append([]string(nil), sig.Paths...),
			Summary:          sig.Summary,
			EscapeOfReflexID: sig.EscapeOfReflexID,
			SignalIDs:        []string{sig.ID},
			CandidateVersion: 1,
			DeliveryState:    DeliveryPending,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		ep.Priority = computePriority(ep)
	}

	q.Episodes[ep.ID] = ep
	if err := s.saveData(q); err != nil {
		return IngestResult{}, err
	}

	// Persist cursor strictly AFTER the signal and episode are durable on disk (§2.2).
	if cursorWatermark != "" {
		if err := s.advanceCursorUnlocked(sig.Source, cursorWatermark); err != nil {
			return IngestResult{}, err
		}
	}

	return IngestResult{
		Signal:         sig,
		Episode:        ep,
		LinkedExisting: linked,
	}, nil
}

func (s *Store) advanceCursorUnlocked(source, watermark string) error {
	c, err := s.loadCursors()
	if err != nil {
		return err
	}
	c.Cursors[source] = Cursor{
		Source:    source,
		Watermark: watermark,
		UpdatedAt: time.Now().UTC(),
	}
	return s.saveCursors(c)
}

// GetCursor returns the last persisted cursor for source.
func (s *Store) GetCursor(source string) (Cursor, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.loadCursors()
	if err != nil {
		return Cursor{}, false, err
	}
	cur, ok := c.Cursors[source]
	return cur, ok, nil
}

// ClaimNext reserves the highest-priority claimable episode with a time-bounded lease (§2.3).
// Expired leases from crashed workers are automatically reclaimed.
func (s *Store) ClaimNext(workerID string, ttl time.Duration, now time.Time) (Episode, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if workerID == "" {
		return Episode{}, false, fmt.Errorf("workerID is required")
	}
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	q, err := s.loadData()
	if err != nil {
		return Episode{}, false, err
	}

	var candidates []Episode
	for _, ep := range q.Episodes {
		if ep.LifecycleState == evidence.StateIntegrated || ep.LifecycleState == evidence.StateDismissed {
			continue
		}
		if ep.DeliveryState == DeliveryDelivered {
			continue
		}
		if ep.Suspension != nil {
			continue
		}
		if ep.EvidenceState == EvidencePendingCloneEvidence {
			continue
		}
		if ep.Lease != nil && ep.Lease.ExpiresAt.After(now) {
			continue
		}
		candidates = append(candidates, ep)
	}
	if len(candidates) == 0 {
		return Episode{}, false, nil
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Priority != candidates[j].Priority {
			return candidates[i].Priority > candidates[j].Priority
		}
		return candidates[i].CreatedAt.Before(candidates[j].CreatedAt)
	})

	chosen := candidates[0]
	chosen.Lease = &Lease{
		WorkerID:  workerID,
		ClaimedAt: now,
		ExpiresAt: now.Add(ttl),
	}
	chosen.UpdatedAt = now
	q.Episodes[chosen.ID] = chosen
	if err := s.saveData(q); err != nil {
		return Episode{}, false, err
	}
	return chosen, true, nil
}

// ReleaseLease drops an active lease held by workerID (or forces release when workerID is empty).
func (s *Store) ReleaseLease(episodeID, workerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, err := s.loadData()
	if err != nil {
		return err
	}
	ep, ok := q.Episodes[episodeID]
	if !ok {
		return fmt.Errorf("episode %s not found", episodeID)
	}
	if workerID != "" && ep.Lease != nil && ep.Lease.WorkerID != workerID {
		return fmt.Errorf("episode %s lease owned by %s, not %s", episodeID, ep.Lease.WorkerID, workerID)
	}
	ep.Lease = nil
	ep.UpdatedAt = time.Now().UTC()
	q.Episodes[episodeID] = ep
	return s.saveData(q)
}

// Suspend pauses an episode with explicit cause and next trigger so it can resume automatically (§2.3).
func (s *Store) Suspend(episodeID, cause, nextTrigger string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, err := s.loadData()
	if err != nil {
		return err
	}
	ep, ok := q.Episodes[episodeID]
	if !ok {
		return fmt.Errorf("episode %s not found", episodeID)
	}
	now := time.Now().UTC()
	ep.Lease = nil
	ep.Suspension = &Suspension{
		Cause:         cause,
		NextTrigger:   nextTrigger,
		LastCheckedAt: now,
	}
	ep.UpdatedAt = now
	q.Episodes[episodeID] = ep
	return s.saveData(q)
}

// Resume clears suspension on an episode when its triggering condition occurs.
func (s *Store) Resume(episodeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, err := s.loadData()
	if err != nil {
		return err
	}
	ep, ok := q.Episodes[episodeID]
	if !ok {
		return fmt.Errorf("episode %s not found", episodeID)
	}
	ep.Suspension = nil
	ep.UpdatedAt = time.Now().UTC()
	q.Episodes[episodeID] = ep
	return s.saveData(q)
}

// UpdateLifecycle transitions an episode to a new lifecycle state.
func (s *Store) UpdateLifecycle(episodeID string, state evidence.LifecycleState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, err := s.loadData()
	if err != nil {
		return err
	}
	ep, ok := q.Episodes[episodeID]
	if !ok {
		return fmt.Errorf("episode %s not found", episodeID)
	}
	ep.LifecycleState = state
	ep.UpdatedAt = time.Now().UTC()
	q.Episodes[episodeID] = ep
	return s.saveData(q)
}

// RecordDelivery records an idempotent delivery outcome for a given DeliveryKey (§2.3).
// Returns alreadyDelivered=true without duplicating when the key already exists.
func (s *Store) RecordDelivery(rec DeliveryRecord) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec.Key == "" {
		return false, fmt.Errorf("delivery key is required")
	}
	q, err := s.loadData()
	if err != nil {
		return false, err
	}
	if _, exists := q.Deliveries[rec.Key]; exists {
		return true, nil
	}
	if rec.DeliveredAt.IsZero() {
		rec.DeliveredAt = time.Now().UTC()
	}
	q.Deliveries[rec.Key] = rec
	if ep, ok := q.Episodes[rec.EpisodeID]; ok {
		ep.DeliveryState = DeliveryDelivered
		ep.LifecycleState = evidence.StateProposed
		ep.Lease = nil
		ep.UpdatedAt = time.Now().UTC()
		q.Episodes[rec.EpisodeID] = ep
	}
	if err := s.saveData(q); err != nil {
		return false, err
	}
	return false, nil
}

// ListEpisodes returns all episodes sorted by priority desc then creation time asc.
func (s *Store) ListEpisodes() ([]Episode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, err := s.loadData()
	if err != nil {
		return nil, err
	}
	out := make([]Episode, 0, len(q.Episodes))
	for _, ep := range q.Episodes {
		out = append(out, ep)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func computePriority(ep Episode) int {
	p := PriorityNormal
	if ep.PRNumber > 0 {
		p += PriorityOpenPR
	}
	if ep.EscapeOfReflexID != "" {
		p += PriorityEscape
	}
	return p
}

func shouldLink(ep Episode, sig Signal) bool {
	if ep.BrokenRef != "" && sig.BrokenRef != "" && ep.BrokenRef == sig.BrokenRef &&
		ep.FixedRef != "" && sig.FixedRef != "" && ep.FixedRef == sig.FixedRef &&
		pathsOverlap(ep.Paths, sig.Paths) {
		return true
	}
	if ep.PRNumber > 0 && sig.PRNumber == ep.PRNumber && pathsOverlap(ep.Paths, sig.Paths) {
		return true
	}
	return false
}

func pathsOverlap(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	set := make(map[string]struct{}, len(a))
	for _, p := range a {
		set[p] = struct{}{}
	}
	for _, p := range b {
		if _, ok := set[p]; ok {
			return true
		}
	}
	return false
}

func mergeUnique(a, b []string) []string {
	set := make(map[string]struct{}, len(a)+len(b))
	var out []string
	for _, p := range append(append([]string(nil), a...), b...) {
		if p == "" {
			continue
		}
		if _, ok := set[p]; !ok {
			set[p] = struct{}{}
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}
