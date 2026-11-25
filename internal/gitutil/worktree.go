package gitutil

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/angelnicolasc/flinch/internal/localstate"
)

// Worktree represents an isolated materialized proof tree owned by Flinch.
// Trees are checked out via temporary index + checkout-index (no Git refs or worktree HEADs)
// so repository signing hooks such as reference-transaction are never triggered.
type Worktree struct {
	RepoRoot string
	Path     string
	Ref      string
	lease    string
	marker   string
	release  func()
}

// CreateWorktree materializes treeIsh (commit SHA, branch/tag ref, or pure tree OID from write-tree)
// into a disposable directory outside Git metadata, without creating a Git ref.
func CreateWorktree(ctx context.Context, repoRoot, treeIsh, prefix string) (*Worktree, error) {
	stateDir, err := FlinchStateDir(ctx, repoRoot)
	if err != nil {
		return nil, err
	}
	base := filepath.Join(stateDir, "proof-trees")
	if err := os.MkdirAll(base, 0o755); err != nil {
		return nil, fmt.Errorf("create proof-trees base dir: %w", err)
	}
	if prefix == "" {
		prefix = "tree-"
	}
	if !proofName.MatchString(prefix) {
		return nil, fmt.Errorf("invalid proof tree prefix %q", prefix)
	}
	leaseDir := filepath.Join(stateDir, "proof-leases")
	if err := os.MkdirAll(leaseDir, 0o700); err != nil {
		return nil, err
	}
	reservation, err := os.CreateTemp(leaseDir, "v2-"+prefix+"*")
	if err != nil {
		return nil, err
	}
	lease := reservation.Name()
	if err := reservation.Close(); err != nil {
		_ = os.Remove(lease)
		return nil, err
	}
	release, acquired, err := localstate.TryLock(lease)
	if err != nil || !acquired {
		_ = os.Remove(lease)
		return nil, fmt.Errorf("cannot acquire proof lease %s (%v)", lease, err)
	}
	// Publish an ownership marker only after the lease is held. Jest's native
	// crawler ignores .git anywhere in a path, even for an explicitly named test.
	name := filepath.Base(lease)
	marker := filepath.Join(base, name)
	if err := os.Mkdir(marker, 0o700); err != nil {
		release()
		_ = os.Remove(lease)
		return nil, err
	}
	dir := proofScratchPath(stateDir, name)
	if err := os.Mkdir(dir, 0o700); err != nil {
		_ = os.Remove(marker)
		release()
		_ = os.Remove(lease)
		return nil, err
	}
	tree := &Worktree{RepoRoot: repoRoot, Path: dir, Ref: treeIsh, lease: lease, marker: marker, release: release}
	ready := false
	defer func() {
		if !ready {
			_ = tree.Cleanup(ctx)
		}
	}()

	tmpIdxDir, err := os.MkdirTemp("", "flinch-readtree-*")
	if err != nil {
		return nil, fmt.Errorf("create temp index dir: %w", err)
	}
	defer os.RemoveAll(tmpIdxDir)

	env := []string{
		"GIT_INDEX_FILE=" + filepath.Join(tmpIdxDir, "index"),
	}

	// Verify treeIsh resolves to a valid tree object before materializing.
	if _, _, err := Run(ctx, repoRoot, env, "rev-parse", "--verify", treeIsh+"^{tree}"); err != nil {
		return nil, fmt.Errorf("resolve tree %s: %w", treeIsh, err)
	}
	if _, _, err := Run(ctx, repoRoot, env, "read-tree", treeIsh); err != nil {
		return nil, fmt.Errorf("read-tree %s: %w", treeIsh, err)
	}
	// git checkout-index requires trailing forward slash on --prefix across Windows and POSIX.
	prefixArg := filepath.ToSlash(dir)
	if !strings.HasSuffix(prefixArg, "/") {
		prefixArg += "/"
	}
	if _, _, err := Run(ctx, repoRoot, env, "checkout-index", "-a", "-f", "--prefix="+prefixArg); err != nil {
		return nil, fmt.Errorf("checkout-index %s: %w", treeIsh, err)
	}

	ready = true
	return tree, nil
}

// Cleanup removes the disposable materialized tree directory.
func (w *Worktree) Cleanup(_ context.Context) error {
	if w == nil || w.Path == "" {
		return nil
	}
	err := os.RemoveAll(w.Path)
	if err == nil && w.marker != "" {
		err = os.Remove(w.marker)
	}
	if w.release != nil {
		w.release()
		w.release = nil
		if err == nil {
			_ = os.Remove(w.lease)
		}
	}
	return err
}

// ApplyPatch applies a unified diff patch inside the materialized proof tree without mutating the parent repo.
func (w *Worktree) ApplyPatch(ctx context.Context, patch []byte) error {
	if len(bytes.TrimSpace(patch)) == 0 {
		return nil
	}
	cmd := exec.CommandContext(ctx, "git", "apply", "--whitespace=nowarn", "-")
	cmd.Dir = w.Path
	cmd.Stdin = bytes.NewReader(patch)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git apply in proof tree: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// WriteFile writes content to relPath inside the proof tree, creating parent directories.
func (w *Worktree) WriteFile(relPath string, content []byte) error {
	target, err := LocalPath(w.Path, relPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, content, 0o644)
}

// RemoveFile deletes relPath inside the proof tree if it exists.
func (w *Worktree) RemoveFile(relPath string) error {
	target, err := LocalPath(w.Path, relPath)
	if err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

var proofName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Each clone owns a separate namespace in system temp. The directory to remove
// is derived from the trusted state root and a reservation name, never read from a marker.
func proofScratchPath(stateDir, name string) string {
	identity := filepath.Clean(stateDir)
	if runtime.GOOS == "windows" {
		identity = strings.ToLower(identity)
	}
	key := sha256.Sum256([]byte(identity))
	return filepath.Join(realTempDir(), fmt.Sprintf("flinch-proof-%x-%s", key[:12], name))
}

// realTempDir is the system temp directory by its real path. A proof
// compares the paths a tool reports with the tree's own path, and tools
// report the real one: on macOS and Linux a process sees its working
// directory without the symlinks in TMPDIR (macOS's /var is one), Jest
// resolves every file, and on Windows the real path spells out the 8.3 short
// names that TEMP keeps (C:\Users\RUNNER~1).
func realTempDir() string {
	dir := os.TempDir()
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		return real
	}
	return dir
}

// PruneProofTree removes a known proof tree only while holding its owner's lease.
func PruneProofTree(stateDir, name string) (bool, error) {
	if !proofName.MatchString(name) {
		return false, fmt.Errorf("invalid proof tree name %q", name)
	}
	lease := filepath.Join(stateDir, "proof-leases", name)
	if _, err := os.Stat(lease); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	release, acquired, err := localstate.TryLock(lease)
	if err != nil || !acquired {
		return false, err
	}
	path := filepath.Join(stateDir, "proof-trees", name)
	_, statErr := os.Stat(path)
	if statErr == nil {
		if strings.HasPrefix(name, "v2-") {
			err = os.RemoveAll(proofScratchPath(stateDir, name))
		}
		if err == nil {
			// This is a marker for current trees and the full tree for earlier leases.
			err = os.RemoveAll(path)
		}
	} else if !os.IsNotExist(statErr) {
		err = statErr
	}
	release()
	if err == nil {
		_ = os.Remove(lease)
	}
	return statErr == nil && err == nil, err
}

// PruneOrphanWorktrees leaves live trees and unrecognized directories untouched.
func PruneOrphanWorktrees(ctx context.Context, repoRoot string) (int, error) {
	stateDir, err := FlinchStateDir(ctx, repoRoot)
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(filepath.Join(stateDir, "proof-trees"))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() && proofName.MatchString(entry.Name()) {
			ok, err := PruneProofTree(stateDir, entry.Name())
			if err != nil {
				return removed, err
			}
			if ok {
				removed++
			}
		}
	}
	return removed, nil
}
