package workspacetrust

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeGitDir(t testing.TB, dir string) {
	t.Helper()
	for _, sub := range []string{"objects", "refs"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// FuzzLinkedWorktreePointer lets the fuzzer write the three files that link a
// worktree to its repository: the worktree's .git pointer and the commondir and
// gitdir files the repository keeps for it. A second repository, the decoy,
// also has an entry for the worktree, but its backlink names another
// directory. The property: an accepted checkout always belongs to the
// repository whose backlink names this worktree, never to the decoy, so a
// forged pointer can't borrow another clone's identity, pause or grant.
func FuzzLinkedWorktreePointer(f *testing.F) {
	f.Add("gitdir: ../main/.git/worktrees/wt", "../..", "../../../wt/.git")
	f.Add("gitdir: ../decoy/.git/worktrees/wt", "../..", "../../../wt/.git")
	f.Add("gitdir: ../main/.git/worktrees/wt\n", "../..\n", "../../../wt/.git\n")
	f.Add("gitdir:../main/.git/worktrees/wt", "../..", "../../../wt/.git")
	f.Add("gitdir: ../main/.git", "..", "../wt/.git")
	f.Add("", "", "")

	base := f.TempDir()
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		f.Fatal(err)
	}
	main := filepath.Join(base, "main", ".git")
	decoy := filepath.Join(base, "decoy", ".git")
	for _, dir := range []string{main, decoy} {
		writeGitDir(f, dir)
		if err := os.MkdirAll(filepath.Join(dir, "worktrees", "wt"), 0o755); err != nil {
			f.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(decoy, "worktrees", "wt", "commondir"), []byte("../..\n"), 0o644); err != nil {
		f.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(decoy, "worktrees", "wt", "gitdir"), []byte("../../../other/.git\n"), 0o644); err != nil {
		f.Fatal(err)
	}
	worktree := filepath.Join(base, "wt")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		f.Fatal(err)
	}
	admin := filepath.Join(main, "worktrees", "wt")

	f.Fuzz(func(t *testing.T, pointer, commondir, backlink string) {
		for path, content := range map[string]string{
			filepath.Join(worktree, ".git"):   pointer,
			filepath.Join(admin, "commondir"): commondir,
			filepath.Join(admin, "gitdir"):    backlink,
		} {
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		c, err := findCheckout(context.Background(), worktree)
		if err != nil {
			return
		}
		if !samePath(c.common, main) || !samePath(c.root, worktree) {
			t.Fatalf("pointer %q, commondir %q, backlink %q: accepted root %q with common %q", pointer, commondir, backlink, c.root, c.common)
		}
	})
}
