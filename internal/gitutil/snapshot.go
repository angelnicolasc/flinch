package gitutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CaptureWorkingTreeSnapshot writes the exact current working tree (tracked edits, staged edits,
// untracked non-ignored files, renames, and deletions) into Git's object store as a pure tree OID
// using a temporary index. It creates NO commits, NO refs, and NO worktree HEAD updates, so it
// never conflicts with repository identity or signing hooks (D11, P2, P10).
func CaptureWorkingTreeSnapshot(ctx context.Context, repoRoot, _ string) (string, error) {
	tmpDir, err := os.MkdirTemp("", "flinch-index-*")
	if err != nil {
		return "", fmt.Errorf("create temp index dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	env := []string{"GIT_INDEX_FILE=" + filepath.Join(tmpDir, "index")}
	if err := seedSnapshotIndex(ctx, repoRoot, env); err != nil {
		return "", err
	}
	if _, _, err := Run(ctx, repoRoot, env, "add", "-A"); err != nil {
		return "", fmt.Errorf("stage working tree into temp index: %w", err)
	}
	treeOut, _, err := Run(ctx, repoRoot, env, "write-tree")
	if err != nil {
		return "", fmt.Errorf("write snapshot tree: %w", err)
	}
	return strings.TrimSpace(treeOut), nil
}

// Seed the tracked entries, including merge stages and sparse checkout exclusions.
// A copied stat cache can make Git reuse the staged blob across a same-size edit.
func seedSnapshotIndex(ctx context.Context, repoRoot string, env []string) error {
	entries, _, err := Run(ctx, repoRoot, []string{"GIT_OPTIONAL_LOCKS=0"}, "ls-files", "-t", "--stage", "--full-name", "-z")
	if err != nil {
		return fmt.Errorf("read tracked snapshot entries: %w", err)
	}
	var indexInfo, skipped strings.Builder
	for entry := range strings.SplitSeq(entries, "\x00") {
		if entry == "" {
			continue
		}
		if len(entry) < 3 || entry[1] != ' ' {
			return fmt.Errorf("unexpected git ls-files entry")
		}
		indexInfo.WriteString(entry[2:])
		indexInfo.WriteByte(0)
		if entry[0] == 'S' {
			_, path, ok := strings.Cut(entry[2:], "\t")
			if !ok {
				return fmt.Errorf("missing path in sparse index entry")
			}
			skipped.WriteString(path)
			skipped.WriteByte(0)
		}
	}
	if indexInfo.Len() > 0 {
		if _, _, err := runWithInput(ctx, repoRoot, env, strings.NewReader(indexInfo.String()), "update-index", "-z", "--index-info"); err != nil {
			return fmt.Errorf("seed temporary index: %w", err)
		}
	}
	if skipped.Len() > 0 {
		if _, _, err := runWithInput(ctx, repoRoot, env, strings.NewReader(skipped.String()), "update-index", "--skip-worktree", "-z", "--stdin"); err != nil {
			return fmt.Errorf("restore sparse checkout entries: %w", err)
		}
	}
	return nil
}
