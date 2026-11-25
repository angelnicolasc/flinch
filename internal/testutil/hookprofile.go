// Package testutil prepares isolated profiles for hook integration tests.
package testutil

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/angelnicolasc/flinch/internal/usersetup"
)

// HookProfile records user-level installation and explicit trust for repo in a temporary home.
func HookProfile(t *testing.T, repo string) string {
	t.Helper()
	// Candidate Go commands must reuse the process cache after HOME and LOCALAPPDATA change.
	if os.Getenv("GOCACHE") == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("GOCACHE", filepath.Join(cache, "go-build"))
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "local"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("FLINCH", "")
	t.Setenv("FLINCH_RECORD", "")
	write := func(path string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeJSON := func(path string, value any) {
		t.Helper()
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		write(path, body)
	}
	state, err := usersetup.StateDir()
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	hosts := map[string]any{}
	for _, host := range []string{"claude", "codex", "antigravity"} {
		hosts[host] = map[string]string{"binary": exe}
	}
	writeJSON(filepath.Join(state, "setup.json"), map[string]any{"version": 1, "hosts": hosts})
	repo, err = filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	writeJSON(filepath.Join(home, ".claude.json"), map[string]any{"projects": map[string]any{filepath.ToSlash(repo): map[string]bool{"hasTrustDialogAccepted": true}}})
	key := repo
	if runtime.GOOS == "windows" {
		key = strings.ToLower(repo)
	}
	write(filepath.Join(home, ".codex", "config.toml"), []byte("[projects."+strconv.Quote(key)+"]\ntrust_level=\"trusted\"\n"))
	writeJSON(filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"), map[string][]string{"trustedWorkspaces": {repo}})
	return home
}
