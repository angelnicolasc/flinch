package reflex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/angelnicolasc/flinch/internal/gitutil"
)

func TestLocalProofRecognizesDiskAndLegacyCRLFTimingsAtSnapshot(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	if _, _, err := gitutil.Run(ctx, repo, nil, "init"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := gitutil.Run(ctx, repo, nil, "config", "core.autocrlf", "true"); err != nil {
		t.Fatal(err)
	}
	const crlf = "package retry\r\n"
	r := Reflex{ID: "retry", Version: 1, Obligation: "Preserve retry behavior.", Artifact: Artifact{Tool: "go-test", Path: "retry_test.go", Selector: "TestRetry"}}
	if err := os.WriteFile(filepath.Join(repo, r.Artifact.Path), []byte(crlf), 0o644); err != nil {
		t.Fatal(err)
	}
	snapshot, err := gitutil.CaptureWorkingTreeSnapshot(ctx, repo, "CRLF checkout")
	if err != nil {
		t.Fatal(err)
	}
	stateDir, err := gitutil.FlinchStateDir(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(stateDir, "proofs"), 0o700); err != nil {
		t.Fatal(err)
	}
	for id, raw := range map[string]string{"legacy-lf": "package retry\n", "legacy-crlf": crlf} {
		legacy := r
		legacy.ID = id
		path, err := localProofPath(stateDir, legacy, []byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{"verdict":"verified","runtime_ms":5,"report_hash":"prior-report"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, ref := range []string{"", snapshot} {
			proof, err := LocalProof(ctx, repo, legacy, ref)
			if err != nil || proof == nil || proof.RuntimeMS != 5 || proof.VerifiedForCheckout() {
				t.Fatalf("legacy timing for %s at %q: %+v, err=%v", id, ref, proof, err)
			}
		}
	}
	if err := SaveLocalTiming(ctx, repo, r, 3, snapshot); err != nil {
		t.Fatal(err)
	}
	timing, err := LocalProof(ctx, repo, r, snapshot)
	if err != nil || timing == nil || timing.RuntimeMS != 3 || timing.VerifiedForCheckout() {
		t.Fatalf("calibration acquired proof status: %+v, err=%v", timing, err)
	}

	r.Proof = &ProofSummary{Verdict: "verified", ReportHash: "verified-report", RuntimeMS: 2}
	start, done := make(chan struct{}), make(chan error, 2)
	go func() {
		<-start
		_, err := SaveContext(ctx, repo, r, snapshot)
		done <- err
	}()
	go func() {
		<-start
		done <- SaveLocalTiming(ctx, repo, r, 4, snapshot)
	}()
	close(start)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if err := SaveLocalTiming(ctx, repo, r, 7, snapshot); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"", snapshot} {
		proof, err := LocalProof(ctx, repo, r, ref)
		if err != nil || !proof.VerifiedForCheckout() || proof.VerifiedRef != snapshot || proof.RuntimeMS != 7 || proof.ReportHash != r.Proof.ReportHash {
			t.Fatalf("lost the verified source after calibration at %q: %+v, err=%v", ref, proof, err)
		}
	}
}

func TestRecordIndexPreservesConcurrentProofsOfDifferentArtifacts(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	if _, _, err := gitutil.Run(ctx, repo, nil, "init"); err != nil {
		t.Fatal(err)
	}
	r := Reflex{ID: "delay", Version: 1, Artifact: Artifact{Path: "cases.txt"}}
	snapshot := func(text string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, r.Artifact.Path), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		ref, err := gitutil.CaptureWorkingTreeSnapshot(ctx, repo, "cases")
		if err != nil {
			t.Fatal(err)
		}
		return ref
	}
	first, second := snapshot("first\n"), snapshot("second\n")
	start, done := make(chan struct{}), make(chan error, 2)
	for _, ref := range []string{first, second} {
		go func() {
			<-start
			done <- saveLocalProof(ctx, repo, r, ProofSummary{Verdict: "verified"}, ref)
		}()
	}
	close(start)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	state, err := gitutil.FlinchStateDir(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(state, "proofs", "by-record"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("expected both verified artifacts in the record index: %v, %v", entries, err)
	}
	intact := func(_ Reflex, original, current []byte) (bool, string) {
		return strings.HasPrefix(string(current), string(original)), ""
	}
	var index LocalProofIndex
	for text, want := range map[string]string{"first\nsibling\n": first, "second\nsibling\n": second} {
		proof, err := index.Verified(ctx, repo, r, snapshot(text), intact)
		if err != nil || !proof.VerifiedForCheckout() || proof.VerifiedRef != want {
			t.Fatalf("lookup for %q lost proof at %s: %+v, %v", text, want, proof, err)
		}
	}
}
