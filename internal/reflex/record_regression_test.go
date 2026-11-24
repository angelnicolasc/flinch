package reflex

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/angelnicolasc/flinch/internal/gitutil"
)

func TestStableRecordsPreserveHumanEditsAndRejectConflicts(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := gitutil.Run(context.Background(), dir, nil, "init"); err != nil {
		t.Fatal(err)
	}
	r := Reflex{ID: "retry-zero", Version: 1, Obligation: "Use an explicit zero.", Artifact: Artifact{Tool: "go-test", Path: "one/retry_test.go", Selector: "TestRetry/zero"}, Proof: &ProofSummary{Verdict: "verified", RuntimeMS: 25, At: "2026-01-01", ReportHash: "first-hash"}}
	p, err := Save(dir, r)
	if err != nil {
		t.Fatal(err)
	}
	if p != filepath.Join(dir, ".flinch", "checks", "retry-zero.yml") {
		t.Fatalf("record in wrong directory: %s", p)
	}
	original, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"proof:", "fingerprint:", "first-hash", "runtime_ms:", "2026-01-01"} {
		if bytes.Contains(original, []byte(key)) {
			t.Fatalf("tracked record contains volatile field %q", key)
		}
	}
	edited := append([]byte("# reviewed by the team\n"), original...)
	if err := os.WriteFile(p, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	r.Proof.RuntimeMS++
	if _, err := Save(dir, r); err != nil {
		t.Fatal(err)
	}
	r.Artifact.Path = "two/retry_test.go"
	if _, err := Save(dir, r); err == nil {
		t.Fatal("overwrote a different package's check")
	}
	got, err := os.ReadFile(p)
	if err != nil || !bytes.Equal(got, edited) {
		t.Fatalf("reviewed file changed on a repeat or collision, err=%v", err)
	}
}

func TestCheckRecordRejectsOutsideIDsAndFixtures(t *testing.T) {
	for _, tc := range []struct{ id, path string }{
		{"../outside", "test.go"}, {"dir/inside", "test.go"}, {"good", "../other/test.go"},
	} {
		r := Reflex{ID: tc.id, Version: 1, Obligation: "Check the value.", Artifact: Artifact{Tool: "go-test", Path: tc.path}}
		if err := r.Validate(); err == nil {
			t.Errorf("accepted ID %q, artifact %q", tc.id, tc.path)
		}
	}
}

func TestNewRecordCannotShadowAnExistingLegacyID(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, ".flinch", "reflexes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "legacy.yaml")
	old := []byte("id: legacy\nversion: 1\nobligation: Enforce the old check.\nartifact:\n  tool: go-vet\n  rule: printf\n")
	if err := os.WriteFile(path, old, 0o644); err != nil {
		t.Fatal(err)
	}
	candidate := Reflex{ID: "legacy", Version: 1, Obligation: "Preserve a different behavior.", Artifact: Artifact{Tool: "pytest", Path: "test_retry.py", Selector: "test_retry"}}
	if _, err := Save(repo, candidate); err == nil {
		t.Fatal("published an incompatible check over a legacy ID")
	}
	if _, err := os.Stat(filepath.Join(repo, ".flinch", "checks", "legacy.yml")); !os.IsNotExist(err) {
		t.Fatalf("unexpected canonical record: %v", err)
	}
}
