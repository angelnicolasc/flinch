package gitutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CaptureWorkingTreeSnapshot creates an isolated, unreachable Git commit representing
// the exact current working tree (including unstaged modifications, untracked non-ignored
// files, renames, and deletions) without modifying the user's Git index, working tree, or stash.
func CaptureWorkingTreeSnapshot(ctx context.Context, repoRoot, message string) (string, error) {
	if strings.TrimSpace(message) == "" {
		message = "flinch temporary snapshot"
	}
	tmpDir, err := os.MkdirTemp("", "flinch-index-*")
	if err != nil {
		return "", fmt.Errorf("create temp index dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tmpIndex := filepath.Join(tmpDir, "index")
	env := []string{
		"GIT_INDEX_FILE=" + tmpIndex,
		"GIT_AUTHOR_NAME=flinch-snapshot",
		"GIT_AUTHOR_EMAIL=flinch-snapshot@local",
		"GIT_COMMITTER_NAME=flinch-snapshot",
		"GIT_COMMITTER_EMAIL=flinch-snapshot@local",
	}

	if _, _, err := Run(ctx, repoRoot, env, "add", "-A"); err != nil {
		return "", fmt.Errorf("stage working tree into temp index: %w", err)
	}
	treeOut, _, err := Run(ctx, repoRoot, env, "write-tree")
	if err != nil {
		return "", fmt.Errorf("write snapshot tree: %w", err)
	}
	treeSHA := strings.TrimSpace(treeOut)

	args := []string{"-c", "commit.gpgsign=false", "commit-tree", treeSHA, "-m", message}
	if headSHA, ok, err := HeadSHA(ctx, repoRoot); err == nil && ok {
		args = append(args, "-p", headSHA)
	}
	commitOut, _, err := Run(ctx, repoRoot, env, args...)
	if err != nil {
		return "", fmt.Errorf("create snapshot commit: %w", err)
	}
	return strings.TrimSpace(commitOut), nil
}
