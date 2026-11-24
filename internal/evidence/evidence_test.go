package evidence

import (
	"testing"
	"time"
)

func TestEvaluateStatusEnforcesDualEvidenceAndAttribution(t *testing.T) {
	rec := ActivationRecord{
		ReflexID:          "unbounded-body",
		ReflexVersion:     1,
		ReflexFingerprint: "sha256:111",
		IntegratedSHA:     "abc123",
		Surface:           SurfaceCI,
		Mode:              "block",
		ArtifactPresent:   true,
		ControlDiscovered: true,
		CleanTreePassed:   true,
		NegativeControl: NegativeControlProof{
			Executed:     true,
			Failed:       true,
			ExpectedID:   "unbounded-body",
			AttributedID: "other-unrelated-lint",
		},
	}
	if st := rec.EvaluateStatus(); st != SurfaceFailed {
		t.Fatalf("expected SurfaceFailed for wrong attribution, got %s", st)
	}

	// Correct attribution but manual dispatch only -> trigger_pending.
	rec.NegativeControl.AttributedID = "unbounded-body"
	rec.TriggerWitness = TriggerWitness{
		Witnessed: true,
		Event:     "workflow_dispatch",
	}
	if st := rec.EvaluateStatus(); st != SurfaceTriggerPending {
		t.Fatalf("expected SurfaceTriggerPending for manual dispatch alone, got %s", st)
	}

	// Real PR trigger witnessed -> active.
	rec.TriggerWitness.Event = "pull_request"
	rec.TriggerWitness.WorkflowHash = "sha256:wf1"
	rec.TriggerWitness.ObservedAt = time.Now().UTC()
	rec.RequiredForMerge = true
	if st := rec.EvaluateStatus(); st != SurfaceActive {
		t.Fatalf("expected SurfaceActive after PR trigger witness, got %s", st)
	}

	// Changing workflow hash invalidates accreditation.
	if ok, _ := rec.IsValidFor("sha256:111", "sha256:wf2", "abc123"); ok {
		t.Fatalf("expected accreditation invalidation on workflow hash change")
	}
}

func TestStoreSaveAndLoad(t *testing.T) {
	store := NewStore(t.TempDir())
	saved, err := store.Save(ActivationRecord{
		ReflexID:          "r1",
		ReflexVersion:     1,
		ReflexFingerprint: "sha256:aaa",
		IntegratedSHA:     "deadbeef",
		Surface:           SurfaceAgentTurn,
		Mode:              "repair_if_supported",
		ArtifactPresent:   true,
		ControlDiscovered: true,
		CleanTreePassed:   true,
		NegativeControl: NegativeControlProof{
			Executed:     true,
			Failed:       true,
			ExpectedID:   "r1",
			AttributedID: "r1",
		},
		AllowedControl: AllowedControlProof{
			Executed: true,
			Passed:   true,
		},
	})
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	if saved.Status != SurfaceActive {
		t.Fatalf("expected SurfaceActive, got %s", saved.Status)
	}
	loaded, err := store.Load("r1", SurfaceAgentTurn)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.Status != SurfaceActive || loaded.IntegratedSHA != "deadbeef" {
		t.Fatalf("unexpected loaded record: %+v", loaded)
	}
}
