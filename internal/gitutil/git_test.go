package gitutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
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

func TestPruneAbandonedProofAfterProcessExit(t *testing.T) {
	ctx := context.Background()
	const flag = "FLINCH_TEST_ABANDONED_TREE"
	if repo := os.Getenv(flag); repo != "" {
		wt, err := CreateWorktree(ctx, repo, "HEAD", "abandoned-")
		if err != nil {
			t.Fatal(err)
		}
		fmt.Print(wt.Path)
		os.Exit(0) // Simulate a worker exiting before it can run Cleanup.
	}

	repo := initTestRepo(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestPruneAbandonedProofAfterProcessExit$")
	cmd.Env = append(os.Environ(), flag+"="+repo)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("start proof owner: %v: %s", err, output)
	}
	path := string(output)
	if _, err := os.Stat(filepath.Join(path, "main.go")); err != nil {
		t.Fatalf("expected abandoned tree before prune: %v", err)
	}
	removed, err := PruneOrphanWorktrees(ctx, repo)
	if removed != 1 || err != nil {
		t.Fatalf("prune = %d, %v", removed, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("proof directory still exists after prune: %v", err)
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

// Regression test: Snapshot capture and proof-tree materialization must succeed
// even when a repository installs a strict reference-transaction hook rejecting unsigned
// commits or worktree HEAD ref updates.
func TestSnapshotAndMaterializeRespectStrictReferenceTransactionHook(t *testing.T) {
	repo := initTestRepo(t)
	ctx := context.Background()

	// Install a reference-transaction hook that fails on any worktrees/*/HEAD update.
	hookPath := filepath.Join(repo, ".git", "hooks", "reference-transaction")
	hookScript := "#!/bin/sh\nwhile read -r old new ref; do\n  case \"$ref\" in worktrees/*/HEAD) echo \"refused $ref\" >&2; exit 1 ;; esac\ndone\n"
	if err := os.WriteFile(hookPath, []byte(hookScript), 0o755); err != nil {
		t.Fatal(err)
	}

	// Dirty the working tree with an uncommitted edit + untracked file.
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n// dirty\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "new.go"), []byte("package main\nfunc Helper() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	treeOID, err := CaptureWorkingTreeSnapshot(ctx, repo, "ignored")
	if err != nil {
		t.Fatalf("CaptureWorkingTreeSnapshot rejected by signing hook: %v", err)
	}
	wt, err := CreateWorktree(ctx, repo, treeOID, "signed-policy-")
	if err != nil {
		t.Fatalf("CreateWorktree rejected by signing hook: %v", err)
	}
	defer wt.Cleanup(ctx)

	b, err := os.ReadFile(filepath.Join(wt.Path, "new.go"))
	if err != nil || !strings.Contains(string(b), "Helper") {
		t.Fatalf("expected materialized untracked file in proof tree, err=%v", err)
	}
}
