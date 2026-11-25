package reflex

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/localstate"
)

// LocalProofReceipt keeps the verified revision in the clone's receipt, outside the check YAML.
type LocalProofReceipt struct {
	ProofSummary
	VerifiedRef string `json:"verified_ref,omitempty"`
}

// VerifiedForCheckout requires a proof saved against the revision the verifier actually ran.
func (p *LocalProofReceipt) VerifiedForCheckout() bool {
	return p != nil && p.Verdict == "verified" && p.VerifiedRef != ""
}

// Git may store LF for text checked out with CRLF. Use the same bytes for a
// proof made on disk and one looked up against a captured tree.
func localProofArtifact(ctx context.Context, repoRoot string, r Reflex, artifactRef string) ([]byte, error) {
	data, err := r.artifactBytes(ctx, repoRoot, artifactRef)
	if bytes.Contains(data, []byte("\r\n")) {
		data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	}
	return data, err
}

func localProofPath(stateDir string, r Reflex, artifact []byte) (string, error) {
	fp, err := r.fingerprint(artifact)
	if err != nil {
		return "", err
	}
	// Keep the established accreditation fingerprint while binding this local
	// measurement to all stable record fields, including cases and surfaces.
	r.Proof, r.Fingerprint, r.FilePath = nil, "", ""
	key, err := json.Marshal(struct {
		ArtifactFingerprint string
		Record              Reflex
	}{fp, r})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(key)
	return filepath.Join(stateDir, "proofs", hex.EncodeToString(sum[:])+".json"), nil
}

// LocalProof reads the measurement of this record and the artifact at artifactRef.
// An empty reference reads the working copy.
func LocalProof(ctx context.Context, repoRoot string, r Reflex, artifactRef string) (*LocalProofReceipt, error) {
	artifact, err := localProofArtifact(ctx, repoRoot, r, artifactRef)
	if err != nil {
		return nil, err
	}
	stateDir, err := gitutil.FlinchStateDir(ctx, repoRoot)
	if err != nil {
		return nil, err
	}
	return exactLocalProof(stateDir, r, artifact)
}

func exactLocalProof(stateDir string, r Reflex, artifact []byte) (*LocalProofReceipt, error) {
	proof, err := readLocalProof(stateDir, r, artifact)
	if err != nil || proof != nil || !bytes.Contains(artifact, []byte("\n")) {
		return proof, err
	}
	// Earlier releases hashed the raw working copy. Its LF key already
	// matches; also recognize a receipt written for a CRLF checkout.
	return readLocalProof(stateDir, r, bytes.ReplaceAll(artifact, []byte("\n"), []byte("\r\n")))
}

func readLocalProof(stateDir string, r Reflex, artifact []byte) (*LocalProofReceipt, error) {
	path, err := localProofPath(stateDir, r, artifact)
	if err != nil {
		return nil, err
	}
	return readReceipt(path)
}

func readReceipt(path string) (*LocalProofReceipt, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var proof LocalProofReceipt
	if err := json.Unmarshal(data, &proof); err != nil {
		return nil, err
	}
	return &proof, nil
}

// LocalProofIndex serves one checkout inspection. Its first inexact lookup
// reads the index directory once, including receipts that predate the index.
type LocalProofIndex struct {
	byRecord map[string][]string
	previous []string
	receipts map[string]*LocalProofReceipt
}

func (idx *LocalProofIndex) load(dir string) {
	store := os.DirFS(dir)
	indexed, _ := fs.Glob(store, "by-record/*.json")
	receipts, _ := fs.Glob(store, "*.json")
	idx.byRecord = make(map[string][]string)
	idx.receipts = make(map[string]*LocalProofReceipt)
	seen := make(map[string]bool)
	for _, entry := range indexed {
		record, name, ok := strings.Cut(filepath.Base(entry), "_")
		if ok {
			idx.byRecord[record] = append(idx.byRecord[record], name)
			seen[name] = true
		}
	}
	for _, name := range receipts {
		if !seen[name] {
			idx.previous = append(idx.previous, name)
		}
	}
}

// Verified finds a local proof for this record whose protected case is intact.
func (idx *LocalProofIndex) Verified(ctx context.Context, repoRoot string, r Reflex, ref string, intact func(Reflex, []byte, []byte) (bool, string)) (*LocalProofReceipt, error) {
	current, err := localProofArtifact(ctx, repoRoot, r, ref)
	if err != nil {
		return nil, err
	}
	stateDir, err := gitutil.FlinchStateDir(ctx, repoRoot)
	if err != nil {
		return nil, err
	}
	exact, err := exactLocalProof(stateDir, r, current)
	if err != nil || exact.VerifiedForCheckout() {
		return exact, err
	}
	if r.Artifact.Path == "" || current == nil {
		return nil, nil
	}
	dir := filepath.Join(stateDir, "proofs")
	if idx.byRecord == nil {
		idx.load(dir)
	}
	key, err := localProofPath(stateDir, r, nil)
	if err != nil {
		return nil, err
	}
	for _, name := range slices.Concat(idx.byRecord[filepath.Base(key)], idx.previous) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		proof, seen := idx.receipts[name]
		if !seen {
			proof, _ = readReceipt(filepath.Join(dir, name))
			idx.receipts[name] = proof
		}
		if !proof.VerifiedForCheckout() {
			continue
		}
		original, err := localProofArtifact(ctx, repoRoot, r, proof.VerifiedRef)
		if err != nil {
			continue
		}
		// Bind even a pre-index receipt to the complete stable record.
		path, err := localProofPath(stateDir, r, original)
		if err != nil || filepath.Base(path) != name {
			continue
		}
		if ok, _ := intact(r, original, current); ok {
			return proof, nil
		}
	}
	return nil, nil
}

// SaveLocalTiming records a bounded runtime without changing the evidence that established a check.
func SaveLocalTiming(ctx context.Context, repoRoot string, r Reflex, runtimeMS int64, artifactRef string) error {
	return saveLocalReceipt(ctx, repoRoot, r, LocalProofReceipt{ProofSummary: ProofSummary{RuntimeMS: runtimeMS}}, artifactRef)
}

func saveLocalProof(ctx context.Context, repoRoot string, r Reflex, proof ProofSummary, verifiedRef string) error {
	receipt := LocalProofReceipt{ProofSummary: proof}
	if proof.Verdict == "verified" && verifiedRef != "" {
		revision, err := gitutil.ResolveRef(ctx, repoRoot, verifiedRef)
		if err != nil {
			return err
		}
		verifiedRef = revision
		receipt.VerifiedRef = revision
	}
	return saveLocalReceipt(ctx, repoRoot, r, receipt, verifiedRef)
}

func saveLocalReceipt(ctx context.Context, repoRoot string, r Reflex, receipt LocalProofReceipt, artifactRef string) error {
	artifact, err := localProofArtifact(ctx, repoRoot, r, artifactRef)
	if err != nil {
		return err
	}
	stateDir, err := gitutil.FlinchStateDir(ctx, repoRoot)
	if err != nil {
		return err
	}
	path, err := localProofPath(stateDir, r, artifact)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// A calibration may have started before a concurrent proof finished. Merge
	// under the writer lock so the timing update cannot replace that proof.
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		release, acquired, err := localstate.TryLock(filepath.Join(dir, "write.lock"))
		if err != nil {
			return err
		}
		if acquired {
			defer release()
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
	if !receipt.VerifiedForCheckout() {
		existing, err := readLocalProof(stateDir, r, artifact)
		if err != nil {
			return err
		}
		if existing != nil && (existing.VerifiedForCheckout() || receipt.Verdict == "") {
			existing.RuntimeMS = receipt.RuntimeMS
			receipt = *existing
		}
	}
	f, err := os.CreateTemp(dir, ".proof-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := json.NewEncoder(f).Encode(receipt); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if receipt.VerifiedForCheckout() {
		key, err := localProofPath(stateDir, r, nil)
		if err != nil {
			return err
		}
		indexDir := filepath.Join(dir, "by-record")
		if err := os.MkdirAll(indexDir, 0o700); err != nil {
			return err
		}
		name := filepath.Base(key) + "_" + filepath.Base(path)
		if err := os.WriteFile(filepath.Join(indexDir, name), nil, 0o600); err != nil {
			return err
		}
	}
	return os.Rename(f.Name(), path)
}
