package workspacetrust

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func linkedCheckout(t *testing.T, root string) string {
	t.Helper()
	git(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "Seed")
	linked := filepath.Join(t.TempDir(), "worktree")
	git(t, root, "worktree", "add", "--detach", linked, "HEAD")
	return linked
}

func TestCodexWindowsCanonicalSpelling(t *testing.T) {
	const simple = `Z:\Work\Repo`
	const prefix = `\\?\`
	if got := codexDiskPath(prefix + simple); got != simple {
		t.Fatalf("ordinary DOS path: %q", got)
	}
	for _, raw := range []string{
		prefix + `UNC\server\share\Repo`,
		prefix + `Z:\work\NUL.txt`,
		prefix + `Z:\LPT9.. .txt\repo`,
		prefix + `Z:\name.\repo`,
		prefix + `Z:\name \repo`,
		prefix + `Z:\` + strings.Repeat("abcdefghij\\", 24),
	} {
		if got := codexDiskPath(raw); got != raw {
			t.Fatalf("removed verbatim prefix from %q", raw)
		}
	}
	const unc = `\\?\UNC\server\share\Repo`
	const mapped = `Z:\Repo`
	projects := codexProjects{unc: {"untrusted"}, mapped: {"trusted"}}
	keys := codexKeys(codexDiskPath(unc), mapped, true)
	if selected, _ := projects.lookup(keys, true); selected.Trust != "untrusted" {
		t.Fatalf("mapped drive took precedence over canonical UNC: %+v", selected)
	}
}

func TestClaudeDesktopProjectClone(t *testing.T) {
	_, root := sandbox(t)
	ctx := context.Background()
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "claude-desktop")
	linked := linkedCheckout(t, root)
	nested := filepath.Join(root, "independent")
	sub := filepath.Join(root, "src")
	for _, path := range []string{nested, sub} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	git(t, nested, "init", "--quiet")
	unrelatedLinked := linkedCheckout(t, nested)
	fake := t.TempDir()
	meta, err := os.ReadFile(filepath.Join(linked, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(fake, ".git"), string(meta))
	for _, tc := range []struct {
		name, project, cwd string
		want               bool
	}{
		{"root", root, root, true},
		{"ordinary subdirectory", root, sub, true},
		{"app worktree", root, linked, true},
		{"project points inside a worktree", linked, sub, true},
		{"nested checkout", root, nested, false},
		{"worktree of another clone", root, unrelatedLinked, false},
		{"forged event worktree", root, fake, false},
		{"forged project worktree", fake, linked, false},
		{"missing project", "", root, false},
		{"relative project", ".", root, false},
		{"project outside Git", t.TempDir(), root, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLAUDE_PROJECT_DIR", tc.project)
			if got := Evaluate(ctx, "claude", tc.cwd, nil); got.Active != tc.want {
				t.Fatalf("Desktop trust: %+v", got)
			}
		})
	}
	t.Setenv("CLAUDE_PROJECT_DIR", root)
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "sdk-cli")
	if got := Evaluate(ctx, "claude", root, nil); got.Active {
		t.Fatalf("generic SDK entrypoint granted trust: %+v", got)
	}
}

func TestClonePermissionAndPause(t *testing.T) {
	_, root := sandbox(t)
	ctx := context.Background()
	linked := linkedCheckout(t, root)
	assert := func(granted bool) {
		t.Helper()
		for _, cwd := range []string{root, linked} {
			for _, host := range []string{"claude", "antigravity", "codex"} {
				if got := Evaluate(ctx, host, cwd, nil); got.Active != (granted && host != "codex") {
					t.Fatalf("%s in %s, clone grant %t: %+v", host, cwd, granted, got)
				}
			}
		}
	}
	write(t, filepath.Join(root, "flinch", cloneMarker), "")
	assert(false)
	if err := SetCloneEnabled(ctx, linked, true); err != nil {
		t.Fatal(err)
	}
	assert(true)
	cloneState := filepath.Join(root, ".git", "flinch")
	if st, err := os.Lstat(filepath.Join(cloneState, cloneMarker)); err != nil || !st.Mode().IsRegular() {
		t.Fatalf("clone grant: %v %v", st, err)
	}
	pause := filepath.Join(cloneState, "pause.json")
	write(t, pause, `{"paused":true}`)
	assert(false)
	if err := os.Remove(pause); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(os.Getenv("CODEX_HOME"), "config.toml"), record(root, "trusted"))
	if err := os.Mkdir(filepath.Join(os.Getenv("CODEX_HOME"), "cloud-config-bundle-cache.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if got := Evaluate(ctx, "codex", root, nil); got.Active {
		t.Fatalf("clone grant overrode a managed Codex source: %+v", got)
	}
	if err := SetCloneEnabled(ctx, root, false); err != nil {
		t.Fatal(err)
	}
	assert(false)
}

func TestCodexTrustLookupOrder(t *testing.T) {
	const physical, alias = `Z:\Work\Repo`, `Z:\link`
	keys := codexKeys(physical, alias, true)
	for _, tc := range []struct {
		name    string
		records codexProjects
		want    codexTrustLevel
	}{
		{"canonical exact wins", codexProjects{`z:\work\repo`: {"trusted"}, physical: {"untrusted"}}, "trusted"},
		{"canonical denial wins", codexProjects{`z:\work\repo`: {"untrusted"}, physical: {"trusted"}}, "untrusted"},
		{"smallest folded key wins", codexProjects{`Z:\WORK\Repo`: {"trusted"}, physical: {"untrusted"}}, "trusted"},
		{"table without trust level ignored", codexProjects{`z:\work\repo`: {}, physical: {"trusted"}}, "trusted"},
		{"folded physical before original exact", codexProjects{physical: {"untrusted"}, `z:\link`: {"trusted"}}, "untrusted"},
		{"original after physical miss", codexProjects{alias: {"trusted"}}, "trusted"},
		{"preserve stored separators", codexProjects{`z:/work/repo`: {"trusted"}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := tc.records.lookup(keys, true)
			if got.Trust != tc.want {
				t.Fatalf("selected %q, want %q", got.Trust, tc.want)
			}
		})
	}
	projects := codexProjects{`z:\é`: {"trusted"}}
	if _, found := projects.lookup(codexKeys(`Z:\É`, `Z:\É`, true), true); found {
		t.Fatal("Codex Windows folded a non-ASCII character")
	}
}
