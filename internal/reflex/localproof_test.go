package reflex

import (
	"context"
	"os"
	"path/filepath"
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
