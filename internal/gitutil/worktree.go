package gitutil

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Worktree represents a disposable detached Git worktree owned by Flinch.
type Worktree struct {
	RepoRoot string
	Path     string
	Ref      string
}

// CreateWorktree checks out ref into an isolated detached worktree under <git-common-dir>/flinch/worktrees.
func CreateWorktree(ctx context.Context, repoRoot, ref, prefix string) (*Worktree, error) {
	stateDir, err := FlinchStateDir(ctx, repoRoot)
	if err != nil {
		return nil, err
	}
	wtBase := filepath.Join(stateDir, "worktrees")
	if err := os.MkdirAll(wtBase, 0o755); err != nil {
		return nil, fmt.Errorf("create worktree base dir: %w", err)
	}
	if prefix == "" {
		prefix = "wt-"
	}
	dir, err := os.MkdirTemp(wtBase, prefix+"*")
	if err != nil {
		return nil, fmt.Errorf("allocate worktree path: %w", err)
	}
	// git worktree add requires the target directory not to exist yet (or be empty); remove the empty dir first.
	if err := os.Remove(dir); err != nil {
		return nil, fmt.Errorf("prepare worktree path: %w", err)
	}
	if _, _, err := Run(ctx, repoRoot, nil, "worktree", "add", "--detach", dir, ref); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("create worktree for %s: %w", ref, err)
	}
	return &Worktree{
		RepoRoot: repoRoot,
		Path:     dir,
		Ref:      ref,
	}, nil
}

// Cleanup removes the disposable worktree and prunes Git worktree metadata.
func (w *Worktree) Cleanup(ctx context.Context) error {
	if w == nil || w.Path == "" {
		return nil
	}
	_, _, removeErr := Run(ctx, w.RepoRoot, nil, "worktree", "remove", "--force", w.Path)
	if _, statErr := os.Stat(w.Path); statErr == nil {
		_ = os.RemoveAll(w.Path)
	}
	_, _, pruneErr := Run(ctx, w.RepoRoot, nil, "worktree", "prune")
	if removeErr != nil && pruneErr != nil {
		return removeErr
	}
	return nil
}

// ApplyPatch applies a unified diff patch inside the worktree without mutating the parent repo.
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
		return fmt.Errorf("git apply in worktree: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// WriteFile writes content to relPath inside the worktree, creating parent directories.
func (w *Worktree) WriteFile(relPath string, content []byte) error {
	target := filepath.Join(w.Path, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, content, 0o644)
}

// RemoveFile deletes relPath inside the worktree if it exists.
func (w *Worktree) RemoveFile(relPath string) error {
	target := filepath.Join(w.Path, filepath.FromSlash(relPath))
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// PruneOrphanWorktrees removes stale Flinch worktree directories and runs git worktree prune.
func PruneOrphanWorktrees(ctx context.Context, repoRoot string) (int, error) {
	stateDir, err := FlinchStateDir(ctx, repoRoot)
	if err != nil {
		return 0, err
	}
	wtBase := filepath.Join(stateDir, "worktrees")
	entries, err := os.ReadDir(wtBase)
	if err != nil {
		if os.IsNotExist(err) {
			_, _, _ = Run(ctx, repoRoot, nil, "worktree", "prune")
			return 0, nil
		}
		return 0, err
	}
	removed := 0
	for _, entry := range entries {
		p := filepath.Join(wtBase, entry.Name())
		_, _, _ = Run(ctx, repoRoot, nil, "worktree", "remove", "--force", p)
		if err := os.RemoveAll(p); err == nil {
			removed++
		}
	}
	_, _, _ = Run(ctx, repoRoot, nil, "worktree", "prune")
	return removed, nil
}
