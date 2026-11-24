package gitutil

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	env := []string{
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	}
	if _, _, err := Run(ctx, dir, env, "init"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if _, _, err := Run(ctx, dir, env, "config", "commit.gpgsign", "false"); err != nil {
		t.Fatalf("git config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Run(ctx, dir, env, "add", "."); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, _, err := Run(ctx, dir, env, "commit", "-m", "initial"); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	return dir
}

func TestCaptureWorkingTreeSnapshotPreservesIndexAndUntrackedFiles(t *testing.T) {
	repo := initTestRepo(t)
	ctx := context.Background()

	// Stage one file, modify another unstaged, and add an untracked file.
	if err := os.WriteFile(filepath.Join(repo, "staged.txt"), []byte("staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Run(ctx, repo, nil, "add", "staged.txt"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n\n// modified\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "untracked.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	beforeStatus, _, err := Run(ctx, repo, nil, "status", "--porcelain=v1")
	if err != nil {
		t.Fatal(err)
	}

	snapSHA, err := CaptureWorkingTreeSnapshot(ctx, repo, "test snapshot")
	if err != nil {
		t.Fatalf("CaptureWorkingTreeSnapshot: %v", err)
	}

	afterStatus, _, err := Run(ctx, repo, nil, "status", "--porcelain=v1")
	if err != nil {
		t.Fatal(err)
	}
	if beforeStatus != afterStatus {
		t.Fatalf("working tree/index mutated!\nbefore:\n%s\nafter:\n%s", beforeStatus, afterStatus)
	}

	// Verify snapshot contains untracked file and unstaged edit.
	untrackedBytes, err := FileAtRef(ctx, repo, snapSHA, "untracked.go")
	if err != nil || !strings.Contains(string(untrackedBytes), "package main") {
		t.Fatalf("expected untracked.go in snapshot, err=%v", err)
	}
	mainBytes, err := FileAtRef(ctx, repo, snapSHA, "main.go")
	if err != nil || !strings.Contains(string(mainBytes), "// modified") {
		t.Fatalf("expected unstaged main.go edit in snapshot, err=%v", err)
	}

	// Verify diff insertion point parsing against HEAD.
	headSHA, _, err := HeadSHA(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := DiffFileChanges(ctx, repo, headSHA, snapSHA)
	if err != nil {
		t.Fatalf("DiffFileChanges: %v", err)
	}
	foundMain := false
	for _, ch := range changes {
		if ch.Path == "main.go" {
			foundMain = true
			if len(ch.InsertionsAt) == 0 {
				t.Fatalf("expected pure insertion point for main.go, got %#v", ch)
			}
		}
	}
	if !foundMain {
		t.Fatalf("expected main.go in diff changes: %#v", changes)
	}
}

func TestWorktreeLifecycle(t *testing.T) {
	repo := initTestRepo(t)
	ctx := context.Background()
	headSHA, _, err := HeadSHA(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := CreateWorktree(ctx, repo, headSHA, "unit-")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "main.go")); err != nil {
		t.Fatalf("expected main.go in worktree: %v", err)
	}
	if err := wt.Cleanup(ctx); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatalf("expected worktree directory to be removed")
	}
}
