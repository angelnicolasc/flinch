package workspacetrust

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", args[0], err, b)
	}
}

func sandbox(t *testing.T) (home, root string) {
	t.Helper()
	home, root = t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "")
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("FLINCH", "")
	git(t, root, "init", "--quiet")
	var err error
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return home, root
}

func setClaude(t *testing.T, home, root string, trusted bool) {
	t.Helper()
	b, err := json.Marshal(map[string]any{"projects": map[string]any{filepath.ToSlash(root): map[string]bool{"hasTrustDialogAccepted": trusted}}})
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(home, ".claude.json"), string(b))
}

func TestClaudeUsesExactRegisteredCheckout(t *testing.T) {
	home, root := sandbox(t)
	ctx := context.Background()
	lookup := func(path string) Status { return Evaluate(ctx, "claude", path, nil) }
	if lookup(root).Active {
		t.Fatal("missing trust file enabled hook")
	}
	for _, entry := range []struct {
		path    string
		trusted bool
	}{{root, false}, {filepath.Dir(root), true}, {root, true}} {
		setClaude(t, home, entry.path, entry.trusted)
		if got := lookup(root); got.Active != (entry.path == root && entry.trusted) {
			t.Fatalf("unexpected trust: %+v", got)
		}
	}
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	git(t, nested, "init", "--quiet")
	if lookup(nested).Active {
		t.Fatal("outer checkout authorized a nested repository")
	}
	git(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "Seed")
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, root, "worktree", "add", "--detach", linked, "HEAD")
	if got := lookup(linked); !got.Active || got.Root == root || got.CommonDir != filepath.Join(root, ".git") {
		t.Fatalf("registered worktree: %+v", got)
	}
	fake := t.TempDir()
	meta, err := os.ReadFile(filepath.Join(linked, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(fake, ".git"), string(meta))
	if lookup(fake).Active {
		t.Fatal("borrowed worktree metadata granted trust")
	}
	write(t, filepath.Join(home, ".claude.json"), "{")
	if lookup(root).Active {
		t.Fatal("malformed JSON authorized checkout")
	}
}

func record(path, level string) string {
	return "[projects." + strconv.Quote(path) + "]\ntrust_level = " + strconv.Quote(level) + "\n"
}

func TestCodexRequiresSavedWorkspaceTrust(t *testing.T) {
	_, root := sandbox(t)
	source := filepath.Join(os.Getenv("CODEX_HOME"), "config.toml")
	sub := filepath.Join(root, "src")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	lookup := func(path string) Status { return Evaluate(context.Background(), "codex", path, nil) }
	for _, value := range []string{"", "untrusted", "trusted"} {
		write(t, source, record(root, value))
		if got := lookup(root); got.Active != (value == "trusted") {
			t.Fatalf("%q: %+v", value, got)
		}
		if got := lookup(sub); got.Active != (value == "trusted") {
			t.Fatalf("subdirectory with root trust %q: %+v", value, got)
		}
	}
	write(t, source, record(root, "trusted")+record(sub, "untrusted"))
	if lookup(sub).Active {
		t.Fatal("negative subdirectory record ignored")
	}
	write(t, source, record(sub, "trusted"))
	if got := lookup(sub); !got.Active || got.Root != root {
		t.Fatalf("exact subdirectory: %+v", got)
	}
	// A lower layer may deny another path, but cannot replace an explicit value for this key.
	write(t, filepath.Join(root, ".codex", "config.toml"), record(root, "trusted"))
	if lookup(root).Active {
		t.Fatal("repository config granted trust")
	}
	write(t, source, "profile = \"work\"\n"+record(sub, "trusted"))
	profile := filepath.Join(os.Getenv("CODEX_HOME"), "work.config.toml")
	write(t, profile, record(sub, "untrusted"))
	if lookup(sub).Active {
		t.Fatal("saved profile revocation ignored")
	}
	write(t, profile, record(sub, "trusted"))
	if !lookup(sub).Active {
		t.Fatal("positive base and profile record rejected")
	}
	write(t, source, "profile = \"work\"\n")
	if lookup(sub).Active {
		t.Fatal("profile alone granted trust")
	}
}

func TestCodexManagedSources(t *testing.T) {
	for _, platform := range []string{"linux", "darwin", "windows"} {
		dir := t.TempDir()
		system, home := filepath.Join(dir, "system"), filepath.Join(dir, "home")
		paths := managedPaths(platform, filepath.Join(system, "requirements.toml"), home)
		expected := []string{filepath.Join(system, "requirements.toml")}
		if platform != "windows" {
			expected = append(expected, filepath.Join(system, "managed_config.toml"), filepath.Join(home, "managed_config.toml"))
		}
		for _, name := range expected {
			write(t, name, "")
			if got := managedReason(paths, nil); got == "" {
				t.Fatalf("%s %s did not disable hooks", platform, name)
			}
			if err := os.Remove(name); err != nil {
				t.Fatal(err)
			}
		}
		if got := managedReason(paths, func() (bool, error) { return true, nil }); got == "" {
			t.Fatal("MDM key ignored")
		}
		if got := managedReason(paths, func() (bool, error) { return false, fmt.Errorf("unavailable") }); got == "" {
			t.Fatal("MDM error ignored")
		}
	}
}

func TestCodexWindowsRequirementsLocation(t *testing.T) {
	base := t.TempDir()
	custom := filepath.Join(base, "admin", "policy.toml")
	relocated := filepath.Join(base, "enterprise-data")
	known := filepath.Join(relocated, "OpenAI", "Codex", "requirements.toml")
	fallback := filepath.Join(`C:\ProgramData`, "OpenAI", "Codex", "requirements.toml")
	t.Setenv("ProgramData", filepath.Join(base, "ignored-env"))
	for _, tc := range []struct {
		name, value, want      string
		registryErr, folderErr error
	}{
		{"absolute registry value", custom, custom, nil, nil},
		{"registry home shorthand", `~\policy.toml`, filepath.Join(base, "policy.toml"), nil, nil},
		{"relative registry value", "relative.toml", "", nil, nil},
		{"empty registry value", "", "", nil, nil},
		{"embedded NUL", custom + "\x00extra", "", nil, nil},
		{"relocated known folder", "", known, os.ErrNotExist, nil},
		{"unavailable registry", "", known, os.ErrPermission, nil},
		{"known folder unavailable", "", fallback, os.ErrNotExist, os.ErrNotExist},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			path, err := resolveWindowsRequirementsPath(tc.value, tc.registryErr, func() (string, error) {
				called = true
				return relocated, tc.folderErr
			}, func() (string, error) { return base, nil })
			if called != (tc.registryErr != nil) || path != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("policy path %q, error %v, known folder queried %t", path, err, called)
			}
		})
	}
	write(t, custom, "")
	path, err := resolveWindowsRequirementsPath(custom, nil, nil, nil)
	if err != nil || managedReason(managedPaths("windows", path, base), nil) == "" {
		t.Fatalf("redirected requirements file did not disable Codex: %v", err)
	}
}

func TestAntigravitySourcesAndWorkspaceIdentity(t *testing.T) {
	home, root := sandbox(t)
	ctx := context.Background()
	settings := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	lookup := func(cwd string, paths ...string) Status { return Evaluate(ctx, "antigravity", cwd, paths) }
	if lookup("", root).Active {
		t.Fatal("empty trust registry authorized workspace")
	}
	raw, _ := json.Marshal(map[string][]string{"trustedWorkspaces": {root}})
	write(t, settings, string(raw))
	if !lookup("", root).Active {
		t.Fatal("single registered workspace rejected")
	}
	if lookup("", root, home).Active || lookup(home, root).Active {
		t.Fatal("ambiguous workspace authorized")
	}
	write(t, settings, "{}")
	if lookup(root).Active {
		t.Fatal("revoked workspace still active")
	}
}

func TestInertChecksLeaveDirectoriesUntouched(t *testing.T) {
	home, root := sandbox(t)
	ctx := context.Background()
	for _, host := range []string{"codex", "claude", "antigravity"} {
		for _, path := range []string{"", home, root} {
			if got := Evaluate(ctx, host, path, nil); got.Active {
				t.Fatalf("%s/path: %+v", host, got)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "flinch")); !os.IsNotExist(err) {
		t.Fatalf("inactive state directory: %v", err)
	}
	setClaude(t, home, root, true)
	t.Setenv("FLINCH", "off")
	if Evaluate(ctx, "claude", root, nil).Active {
		t.Fatal("session disable ignored")
	}
	t.Setenv("FLINCH", "")
	write(t, filepath.Join(root, ".git", "flinch", "pause.json"), `{"paused":true}`)
	if Evaluate(ctx, "claude", root, nil).Active {
		t.Fatal("pause ignored")
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "flinch", "log")); !os.IsNotExist(err) {
		t.Fatal("evaluating pause wrote an event")
	}
}

func TestWindowsTrustPathSpelling(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path aliases")
	}
	home, root := sandbox(t)
	ctx := context.Background()
	native, slash := filepath.FromSlash(root), filepath.ToSlash(root)
	if native == slash {
		t.Fatal("fixture is missing a path separator")
	}
	for _, records := range []map[string]bool{
		{native: true}, {slash: true}, {native: true, slash: false}, {native: false, slash: true},
		{`\\?\` + native: true},
	} {
		projects := map[string]any{}
		for path, trust := range records {
			projects[path] = map[string]bool{"hasTrustDialogAccepted": trust}
		}
		data, err := json.Marshal(map[string]any{"projects": projects})
		if err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(home, ".claude.json"), string(data))
		if status := Evaluate(ctx, "claude", root, nil); !status.Active {
			t.Fatalf("Claude projects %v: %+v", records, status)
		}
	}
	// Codex Desktop preserves the original casing. An absent cache leaves this
	// entry active even if storing a newly fetched policy failed (docs/limitations.md).
	mixed := strings.ToUpper(native[:1]) + native[1:]
	write(t, filepath.Join(os.Getenv("CODEX_HOME"), "config.toml"), record(mixed, "trusted"))
	if status := Evaluate(ctx, "codex", root, nil); !status.Active {
		t.Fatalf("original-case Codex user key with no cloud cache: %+v", status)
	}
	data, err := json.Marshal(map[string]any{"trustedWorkspaces": []string{slash}})
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"), string(data))
	if status := Evaluate(ctx, "antigravity", root, nil); !status.Active {
		t.Fatalf("Antigravity slash spelling: %+v", status)
	}
}

func TestCodexCloudCacheAndUserTrust(t *testing.T) {
	_, root := sandbox(t)
	sub := filepath.Join(root, "child")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	cacheDir := os.Getenv("CODEX_HOME")
	cache := filepath.Join(cacheDir, "cloud-config-bundle-cache.json")
	namespaced := filepath.Join(cacheDir, "managed-config-"+strings.Repeat("0", 64)+".json")
	empty := []map[string]string{}
	fragment := func(contents string) []map[string]string {
		return []map[string]string{{"id": "policy", "name": "policy", "contents": contents}}
	}
	bundle := func(config, requirements []map[string]string) string {
		b, err := json.Marshal(map[string]any{
			"signature": "synthetic",
			"signed_payload": map[string]any{
				"version": 1, "bundle": map[string]any{
					"config_toml":       map[string]any{"enterprise_managed": config},
					"requirements_toml": map[string]any{"enterprise_managed": requirements},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	for _, path := range []string{"auth.json", "managed-config-unrelated.json"} {
		// Neither file is a cache. A directory forces an error if Flinch opens it.
		if err := os.MkdirAll(filepath.Join(cacheDir, path), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name, cwd, localKey, cachePath, cacheData string
		active                                    bool
	}{
		// A failed cloud cache write is indistinguishable from this user-only
		// record. Flinch follows the saved trust as documented in limitations.md.
		{"absent cache and original user spelling", root, root, "", "", true},
		{"empty bundle at root", root, root, cache, bundle(empty, empty), true},
		{"trusted root and session in subdirectory", sub, root, cache, bundle(empty, empty), true},
		{"cloud and user have identical keys", root, root, cache, bundle(fragment(record(root, "untrusted")), empty), false},
		{"cloud and user have different spellings", root, root, cache, bundle(fragment(record(strings.ToLower(root), "untrusted")), empty), false},
		{"namespaced requirements", root, root, namespaced, bundle(empty, fragment("allow_login = false")), false},
		{"empty namespaced bundle", root, root, namespaced, bundle(empty, empty), true},
		{"malformed cache", root, root, cache, "{", false},
		{"missing bundle", root, root, cache, `{"signed_payload":{"version":1}}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.cachePath != "" {
				write(t, test.cachePath, test.cacheData)
				t.Cleanup(func() {
					if err := os.Remove(test.cachePath); err != nil {
						t.Error(err)
					}
				})
			}
			write(t, filepath.Join(cacheDir, "config.toml"), record(test.localKey, "trusted"))
			if got := Evaluate(context.Background(), "codex", test.cwd, nil); got.Active != test.active {
				t.Fatalf("cloud cache and saved trust: %+v", got)
			}
		})
	}
	if err := os.Mkdir(cache, 0700); err != nil {
		t.Fatal(err)
	}
	if got := Evaluate(context.Background(), "codex", root, nil); got.Active || !strings.Contains(got.Reason, "organization") {
		t.Fatalf("unreadable cache: %+v", got)
	}
}

func TestWorkspacePathsAgreeForEveryHost(t *testing.T) {
	_, root := sandbox(t)
	ctx := context.Background()
	sub := filepath.Join(root, "src")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	git(t, other, "init", "--quiet")
	for _, host := range []string{"claude", "codex", "antigravity"} {
		status := Evaluate(ctx, host, sub, []string{root, other})
		if status.Active || !strings.Contains(status.Reason, "one mounted worktree") {
			t.Fatalf("%s accepted conflicting mounts: %+v", host, status)
		}
	}
	if err := SetCloneEnabled(ctx, root, true); err != nil {
		t.Fatal(err)
	}
	if status := Evaluate(ctx, "antigravity", sub, []string{root, sub}); !status.Active {
		t.Fatalf("same checkout: %+v", status)
	}
	if status := Evaluate(ctx, "antigravity", "", []string{sub, root}); !status.Active {
		t.Fatalf("same checkout without cwd: %+v", status)
	}
}

func TestBareRepositoryInsideTrustedCheckoutStaysInactive(t *testing.T) {
	home, root := sandbox(t)
	setClaude(t, home, root, true)
	bare := filepath.Join(root, "archive.git")
	git(t, root, "init", "--bare", bare)
	if status := Evaluate(context.Background(), "claude", bare, nil); status.Active {
		t.Fatalf("bare repo inherited trust from outer checkout: %+v", status)
	}
}

func TestCheckoutCannotSupplyItsOwnTrustProfile(t *testing.T) {
	_, root := sandbox(t)
	profile := filepath.Join(root, "profile")
	setClaude(t, profile, root, true)
	t.Setenv("CLAUDE_CONFIG_DIR", profile)
	write(t, filepath.Join(profile, "config.toml"), record(root, "trusted"))
	t.Setenv("CODEX_HOME", profile)
	for _, host := range []string{"claude", "codex"} {
		if status := Evaluate(context.Background(), host, root, nil); status.Active {
			t.Fatalf("repo supplied %s trust: %+v", host, status)
		}
	}
}

func TestGitRedirectionCannotCrossTrustBoundary(t *testing.T) {
	home, root := sandbox(t)
	ctx := context.Background()
	setClaude(t, home, root, true)
	other := t.TempDir()
	git(t, other, "init", "--quiet")
	for name, value := range map[string]string{
		"GIT_DIR": filepath.Join(other, ".git"), "GIT_COMMON_DIR": filepath.Join(other, ".git"),
		"GIT_WORK_TREE": other, "GIT_INDEX_FILE": filepath.Join(other, "index"),
		"GIT_OBJECT_DIRECTORY": filepath.Join(other, ".git", "objects"), "GIT_ALTERNATE_OBJECT_DIRECTORIES": filepath.Join(other, ".git", "objects"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, value)
			if got := Evaluate(ctx, "claude", root, nil); got.Active {
				t.Fatalf("Git override authorized a different checkout: %+v", got)
			}
			if err := SetCloneEnabled(ctx, root, true); err == nil {
				t.Fatal("wrote through an ambiguous checkout")
			}
		})
	}
	git(t, root, "config", "core.worktree", other)
	if got := Evaluate(ctx, "claude", root, nil); got.Active {
		t.Fatalf("redirected working tree: %+v", got)
	}
	for _, path := range []string{root, other} {
		if _, err := os.Stat(filepath.Join(path, ".git", "flinch")); !os.IsNotExist(err) {
			t.Fatalf("redirected state: %v", err)
		}
	}
}

func TestCodexExactSubdirectoryTrustTakesPrecedence(t *testing.T) {
	_, root := sandbox(t)
	cwd := filepath.Join(root, "src")
	if err := os.Mkdir(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(os.Getenv("CODEX_HOME"), "config.toml"), record(root, "untrusted")+record(cwd, "trusted"))
	if got := Evaluate(context.Background(), "codex", cwd, nil); !got.Active {
		t.Fatalf("explicit cwd trust: %+v", got)
	}
}

func TestCodexLinkedWorkspaceDecisions(t *testing.T) {
	_, root := sandbox(t)
	worktree := linkedCheckout(t, root)
	sub := filepath.Join(worktree, "src")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(worktree, "independent")
	git(t, worktree, "init", "--quiet", nested)
	base := filepath.Join(os.Getenv("CODEX_HOME"), "config.toml")
	rootGrant := record(root, "trusted")
	for _, tc := range []struct {
		name, cwd, config string
		active            bool
	}{
		{"main checkout grants worktree", worktree, rootGrant, true},
		{"main checkout grants worktree subdirectory", sub, rootGrant, true},
		{"worktree denies its subdirectory", sub, rootGrant + record(worktree, "untrusted"), false},
		{"worktree grant precedes main denial", sub, record(root, "untrusted") + record(worktree, "trusted"), true},
		{"cwd denial precedes worktree grant", sub, record(worktree, "trusted") + record(sub, "untrusted"), false},
		{"cwd grant precedes worktree denial", sub, record(worktree, "untrusted") + record(sub, "trusted"), true},
		{"outer checkout cannot grant a nested repo", nested, rootGrant + record(worktree, "trusted"), false},
		{"explicit default markers allow inheritance", sub, "project_root_markers = ['.git']\n" + rootGrant, true},
		{"custom markers require exact trust", sub, "project_root_markers = ['project.toml', '.git']\n" + rootGrant, false},
		{"empty markers require exact trust", sub, "project_root_markers = []\n" + rootGrant, false},
		{"exact grant with custom markers", sub, "project_root_markers = []\n" + record(sub, "trusted"), true},
		{"invalid markers disable activation", sub, "project_root_markers = true\n" + record(sub, "trusted"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			write(t, base, tc.config)
			if got := Evaluate(context.Background(), "codex", tc.cwd, nil); got.Active != tc.active {
				t.Fatalf("active = %t, want %t (%s)", got.Active, tc.active, got.Reason)
			}
		})
	}
	selected := "profile = 'work'\n" + rootGrant
	deny := record(worktree, "untrusted")
	for _, tc := range []struct{ name, embedded, file string }{
		{"standalone profile denies worktree", "", deny},
		{"standalone profile uses custom markers", "", "project_root_markers = []\n"},
		{"embedded profile denies worktree", strings.Replace(deny, "[projects.", "[profiles.work.projects.", 1), ""},
		{"embedded profile uses custom markers", "[profiles.work]\nproject_root_markers = []\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			write(t, base, selected+tc.embedded)
			if tc.file != "" {
				path := filepath.Join(filepath.Dir(base), "work.config.toml")
				write(t, path, tc.file)
				t.Cleanup(func() { _ = os.Remove(path) })
			}
			if got := Evaluate(context.Background(), "codex", sub, nil); got.Active {
				t.Fatalf("profile authorized inherited trust: %+v", got)
			}
		})
	}
}

func TestCodexSystemLayerWorkspaceTrust(t *testing.T) {
	_, root := sandbox(t)
	sub := filepath.Join(root, "src")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	c, err := findCheckout(context.Background(), sub)
	if err != nil {
		t.Fatal(err)
	}
	rootGrant := codexLayer{Projects: codexProjects{root: {"trusted"}}}
	subGrant := codexLayer{Projects: codexProjects{sub: {"trusted"}}}
	subDeny := codexLayer{Projects: codexProjects{sub: {"untrusted"}}}
	markers := codexLayer{ProjectRootMarkers: []string{"go.mod"}}
	for _, tc := range []struct {
		name         string
		system, user codexLayer
		active       bool
	}{
		{"system cannot grant", rootGrant, codexLayer{}, false},
		{"specific system denial precedes inherited user grant", subDeny, rootGrant, false},
		{"user overrides same system key", subDeny, subGrant, true},
		{"system custom markers disable inheritance", markers, rootGrant, false},
		{"system custom markers keep exact user trust", markers, subGrant, true},
		{"user default markers override system", markers, codexLayer{Projects: rootGrant.Projects, ProjectRootMarkers: []string{".git"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if reason := codexSavedReason(c, sub, tc.system, tc.user, nil); (reason == "") != tc.active {
				t.Fatalf("want active %t, got reason %q", tc.active, reason)
			}
		})
	}
}

func TestCodexInheritedGrantChecksEnclosingProjectLayers(t *testing.T) {
	_, root := sandbox(t)
	worker := linkedCheckout(t, root)
	project := filepath.Join(worker, "packages")
	cwd := filepath.Join(project, "service")
	if err := os.MkdirAll(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	c, err := findCheckout(context.Background(), cwd)
	if err != nil {
		t.Fatal(err)
	}
	mainGrant := codexLayer{Projects: codexProjects{root: {"trusted"}}}
	layerDenial := codexLayer{Projects: codexProjects{project: {"untrusted"}}}
	user := codexLayer{Projects: codexProjects{root: {"trusted"}, project: {"untrusted"}}}
	if reason := codexSavedReason(c, cwd, codexLayer{}, user, nil); reason != "" {
		t.Fatalf("directory without a project layer vetoed trust: %s", reason)
	}
	if err := os.Mkdir(filepath.Join(project, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name         string
		system, user codexLayer
		profiles     []codexLayer
		active       bool
	}{
		{"user denies enclosing layer", codexLayer{}, user, nil, false},
		{"system denies enclosing layer", layerDenial, mainGrant, nil, false},
		{"profile denies enclosing layer", codexLayer{}, mainGrant, []codexLayer{layerDenial}, false},
		{"empty layer inherits main grant", codexLayer{}, mainGrant, nil, true},
		{"exact user decision wins", codexLayer{}, codexLayer{Projects: codexProjects{cwd: {"trusted"}, project: {"untrusted"}}}, nil, true},
		{"user overrides same system key", layerDenial, codexLayer{Projects: codexProjects{root: {"trusted"}, project: {"trusted"}}}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if reason := codexSavedReason(c, cwd, tc.system, tc.user, tc.profiles); (reason == "") != tc.active {
				t.Fatalf("want active %t, got reason %q", tc.active, reason)
			}
		})
	}
}

func TestCodexTrustFieldNamesAreCaseSensitive(t *testing.T) {
	_, root := sandbox(t)
	worktree := linkedCheckout(t, root)
	source := filepath.Join(os.Getenv("CODEX_HOME"), "config.toml")
	grant := record(root, "trusted")
	for _, tc := range []struct {
		name, config string
		active       bool
	}{
		{"exact spelling", grant, true},
		{"capitalized project table", strings.Replace(grant, "[projects.", "[Projects.", 1), false},
		{"capitalized trust field", strings.Replace(grant, "trust_level", "Trust_Level", 1), false},
		{"inline trust field", "projects = {" + strconv.Quote(root) + " = { Trust_Level = 'trusted' }}", false},
		{"ambiguous marker spelling", "Project_Root_Markers = ['.git']\n" + grant, false},
		{"unknown unrelated section", "[editor]\nProjects = 'example'\n" + grant, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			write(t, source, tc.config)
			if got := Evaluate(context.Background(), "codex", worktree, nil); got.Active != tc.active {
				t.Fatalf("want active %t, got %+v", tc.active, got)
			}
		})
	}
}

func TestWorktreeGitDirPreservesFilenameWhitespace(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".git")
	want := "worktrees/feature\u00a0"
	write(t, path, "gitdir: "+want+"\r\n")
	got, err := readWorktreeGitDir(path)
	if err != nil || got != want {
		t.Fatalf("gitdir = %q, %v; want %q", got, err, want)
	}
}

func TestCodexWorkspaceAliasAncestry(t *testing.T) {
	_, root := sandbox(t)
	sub := filepath.Join(root, "src")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	alias := filepath.Join(parent, "checkout")
	if err := os.Symlink(root, alias); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("creating a directory symlink requires Windows Developer Mode or privilege: %v", err)
		}
		t.Fatal(err)
	}
	path := filepath.Join(os.Getenv("CODEX_HOME"), "config.toml")
	write(t, path, record(alias, "trusted"))
	if got := Evaluate(context.Background(), "codex", filepath.Join(alias, "src"), nil); !got.Active {
		t.Fatalf("lost original project root spelling: %+v", got)
	}
	childAlias := filepath.Join(parent, "source")
	if err := os.Symlink(sub, childAlias); err != nil {
		t.Fatal(err)
	}
	write(t, path, record(root, "trusted"))
	if got := Evaluate(context.Background(), "codex", childAlias, nil); got.Active {
		t.Fatalf("event path did not traverse the trusted checkout root: %+v", got)
	}
}
