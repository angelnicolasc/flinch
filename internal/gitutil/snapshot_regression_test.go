package gitutil

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRegressionSnapshotPreservesTrackedIgnoredAndRealIndex(t *testing.T) {
	ctx := context.Background()
	repo := initTestRepo(t)
	path := filepath.Join(repo, "main.go")
	if err := os.WriteFile(path, []byte("staged contents\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Run(ctx, repo, nil, "add", "--", "main.go"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("main.go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("working contents\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(repo, ".git", "index")
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := CaptureWorkingTreeSnapshot(ctx, repo, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := FileAtRef(ctx, repo, snap, "main.go")
	if err != nil || string(got) != "working contents\n" {
		t.Fatalf("snapshot of tracked ignored file = %q, %v", got, err)
	}
	after, err := os.ReadFile(indexPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("snapshot changed real index: %v", err)
	}
}

func TestRegressionSnapshotReadsSameSizeEditWithUnchangedTimestamp(t *testing.T) {
	// Model a filesystem that reports the same timestamps for two fast writes.
	t.Setenv("GIT_CONFIG_COUNT", "2")
	t.Setenv("GIT_CONFIG_KEY_0", "core.checkStat")
	t.Setenv("GIT_CONFIG_VALUE_0", "minimal")
	t.Setenv("GIT_CONFIG_KEY_1", "core.trustctime")
	t.Setenv("GIT_CONFIG_VALUE_1", "false")
	ctx := context.Background()
	repo := initTestRepo(t)
	const old = "value || 8\n"
	const fixed = "value ?? 8\n"
	path := filepath.Join(repo, "delay.js")
	mtime := time.Unix(1700000000, 0)
	writeAtSameTime := func(data string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	writeAtSameTime(old)
	if _, _, err := Run(ctx, repo, nil, "add", "--", "delay.js"); err != nil {
		t.Fatal(err)
	}
	writeAtSameTime(fixed)
	tree, err := CaptureWorkingTreeSnapshot(ctx, repo, "")
	if err != nil {
		t.Fatal(err)
	}
	current, err := FileAtRef(ctx, repo, tree, "delay.js")
	if err != nil || string(current) != fixed {
		t.Fatalf("snapshot returned %q, want working bytes %q (%v)", current, fixed, err)
	}
	staged, _, err := Run(ctx, repo, nil, "show", ":delay.js")
	if err != nil || staged != old {
		t.Fatalf("staging changed: %q (%v)", staged, err)
	}
}

func TestRegressionPruneKeepsActiveProofTree(t *testing.T) {
	ctx := context.Background()
	repo := initTestRepo(t)
	wt, err := CreateWorktree(ctx, repo, "HEAD", "active-")
	if err != nil {
		t.Fatal(err)
	}
	defer wt.Cleanup(ctx)
	if _, err = PruneOrphanWorktrees(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "main.go")); err != nil {
		t.Fatalf("prune removed active proof tree: %v", err)
	}
}
