package queue

import (
	"testing"
	"time"
)

func TestIngestDeduplicatesFiltersSyntheticAndUpdatesCursor(t *testing.T) {
	store := NewStore(t.TempDir())

	// Synthetic continuation signal must not create an episode.
	synRes, err := store.IngestSignal(Signal{
		Repo:       "angelnicolasc/graymatter",
		Source:     "correction",
		EventID:    "turn-syn-1",
		Provenance: ProvenanceSynthetic,
		Summary:    "flinch continuation notice",
	}, "cursor-1")
	if err != nil {
		t.Fatalf("IngestSignal synthetic: %v", err)
	}
	if !synRes.IgnoredSynthetic {
		t.Fatalf("expected synthetic correction signal to be ignored")
	}
	eps, err := store.ListEpisodes()
	if err != nil || len(eps) != 0 {
		t.Fatalf("expected 0 episodes after synthetic signal, got %d (err=%v)", len(eps), err)
	}

	// Real human signal creates episode and persists cursor after saving.
	res1, err := store.IngestSignal(Signal{
		Repo:       "angelnicolasc/graymatter",
		Source:     "correction",
		EventID:    "turn-2",
		Provenance: ProvenanceHuman,
		PRNumber:   27,
		BrokenRef:  "aaa111",
		FixedRef:   "bbb222",
		Paths:      []string{"pkg/client/http.go"},
		Summary:    "Limit HTTP response body reads",
	}, "cursor-2")
	if err != nil {
		t.Fatalf("IngestSignal human: %v", err)
	}
	if res1.Duplicate || res1.Episode.ID == "" {
		t.Fatalf("unexpected ingest result: %+v", res1)
	}

	// Retrying the exact same event deduplicates cleanly.
	resDup, err := store.IngestSignal(Signal{
		Repo:       "angelnicolasc/graymatter",
		Source:     "correction",
		EventID:    "turn-2",
		Provenance: ProvenanceHuman,
	}, "cursor-2")
	if err != nil || !resDup.Duplicate {
		t.Fatalf("expected duplicate on retry, got %+v (err=%v)", resDup, err)
	}

	cur, ok, err := store.GetCursor("correction")
	if err != nil || !ok || cur.Watermark != "cursor-2" {
		t.Fatalf("expected cursor-2, got %+v (ok=%v err=%v)", cur, ok, err)
	}
}

func TestPriorityClaimLeaseExpiryAndIdempotentDelivery(t *testing.T) {
	store := NewStore(t.TempDir())
	t0 := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)

	// Normal local episode
	_, err := store.IngestSignal(Signal{
		Repo:      "repo/a",
		Source:    "fix",
		EventID:   "evt-normal",
		BrokenRef: "b1",
		FixedRef:  "f1",
		Paths:     []string{"a.go"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	// Escape of active reflex on open PR (must drain first!)
	escRes, err := store.IngestSignal(Signal{
		Repo:             "repo/a",
		Source:           "ci",
		EventID:          "evt-escape",
		PRNumber:         55,
		BrokenRef:        "b2",
		FixedRef:         "f2",
		Paths:            []string{"pkg/memory/consolidate.go"},
		EscapeOfReflexID: "unbounded-response-body-read",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	// Pending clone evidence episode (must not be claimed until evidence arrives)
	_, err = store.IngestSignal(Signal{
		Repo:          "repo/a",
		Source:        "correction",
		EventID:       "evt-local-only",
		EvidenceState: EvidencePendingCloneEvidence,
		Paths:         []string{"b.go"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	ep, ok, err := store.ClaimNext("worker-1", 1*time.Minute, t0)
	if err != nil || !ok {
		t.Fatalf("ClaimNext failed: ok=%v err=%v", ok, err)
	}
	if ep.ID != escRes.Episode.ID {
		t.Fatalf("expected escape episode %s first, got %s", escRes.Episode.ID, ep.ID)
	}

	// After lease expiry, worker-2 reclaims the same episode cleanly.
	ep2, ok, err := store.ClaimNext("worker-2", 1*time.Minute, t0.Add(2*time.Minute))
	if err != nil || !ok || ep2.ID != escRes.Episode.ID {
		t.Fatalf("expected worker-2 to reclaim expired lease on %s, got %+v (ok=%v err=%v)", escRes.Episode.ID, ep2, ok, err)
	}

	// Idempotent delivery prevents duplicate PR writes.
	key := DeliveryKey(ep2.Repo, ep2.ID, ep2.CandidateVersion, "pr:55")
	dup, err := store.RecordDelivery(DeliveryRecord{Key: key, EpisodeID: ep2.ID, Target: "pr:55", ObservedSHA: "sha1", ResultSHA: "sha2"})
	if err != nil || dup {
		t.Fatalf("expected first delivery to succeed without dup, dup=%v err=%v", dup, err)
	}
	dup2, err := store.RecordDelivery(DeliveryRecord{Key: key, EpisodeID: ep2.ID, Target: "pr:55", ObservedSHA: "sha1", ResultSHA: "sha2"})
	if err != nil || !dup2 {
		t.Fatalf("expected second delivery with same key to be deduplicated, dup=%v err=%v", dup2, err)
	}
}
