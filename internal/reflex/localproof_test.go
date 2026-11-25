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
	if err := SaveLocalProof(ctx, repo, r, ProofSummary{Verdict: "verified", RuntimeMS: 5}, ""); err != nil {
		t.Fatal(err)
	}
	// A second ID has only the raw-CRLF key an earlier release wrote.
	legacy := r
	legacy.ID = "legacy"
	stateDir, err := gitutil.FlinchStateDir(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	path, err := localProofPath(stateDir, legacy, []byte(crlf))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"verdict":"verified","runtime_ms":5}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, check := range []Reflex{r, legacy} {
		proof, err := LocalProof(ctx, repo, check, snapshot)
		if err != nil || proof == nil || proof.Verdict != "verified" || proof.RuntimeMS != 5 {
			t.Fatalf("missing proof for %s at the LF snapshot: %+v, err=%v", check.ID, proof, err)
		}
	}
}
