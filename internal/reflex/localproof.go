package reflex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/angelnicolasc/flinch/internal/gitutil"
)

// A measurement belongs to this checkout's artifact contents and reviewed contract.
func localProofPath(ctx context.Context, repoRoot string, r Reflex) (string, error) {
	fp, err := r.ComputeFingerprint(repoRoot)
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
	dir, err := gitutil.FlinchStateDir(ctx, repoRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "proofs", hex.EncodeToString(sum[:])+".json"), nil
}

func LocalProof(ctx context.Context, repoRoot string, r Reflex) (*ProofSummary, error) {
	path, err := localProofPath(ctx, repoRoot, r)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var proof ProofSummary
	if err := json.Unmarshal(data, &proof); err != nil {
		return nil, err
	}
	return &proof, nil
}

func SaveLocalProof(ctx context.Context, repoRoot string, r Reflex, proof ProofSummary) error {
	path, err := localProofPath(ctx, repoRoot, r)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".proof-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := json.NewEncoder(f).Encode(proof); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
