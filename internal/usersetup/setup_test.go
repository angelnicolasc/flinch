package usersetup

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0700); err != nil {
		t.Fatal(err)
	}
}

func TestExecutableUpdateKeepsCodexDefinition(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stable Windows shim is exercised by TestWindowsScoopUpgrade")
	}
	prefix := filepath.Join(t.TempDir(), "brew prefix")
	stable := filepath.Join(prefix, "opt", "flinch")
	if err := os.MkdirAll(filepath.Dir(stable), 0700); err != nil {
		t.Fatal(err)
	}
	var previous []byte
	for _, version := range []string{"0.9.1", "0.9.2"} {
		cell := filepath.Join(prefix, "Cellar", "flinch", version)
		bin := filepath.Join(cell, "bin", "flinch")
		writeFixture(t, bin, []byte(version))
		if err := os.Symlink(cell, stable); err != nil {
			t.Fatal(err)
		}
		path, err := stableBinaryFor(bin, bin)
		if err != nil || path != filepath.Join(stable, "bin", "flinch") {
			t.Fatalf("upgrade selected %q: %v", path, err)
		}
		h := handler("codex", hookEvent{"Stop", 30}, path)
		data, _ := jsonBytes(h)
		if previous != nil && !bytes.Equal(data, previous) {
			t.Fatalf("updating changed the Codex hook: %s to %s", previous, data)
		}
		previous = data
		if err := os.Remove(stable); err != nil {
			t.Fatal(err)
		}
	}
}

// On Apple Silicon, Homebrew's prefix /opt/homebrew is its own Git checkout.
// A formula in its Cellar is outside any project checkout; anything else in
// a checkout is still inside one.
func TestHomebrewCellarInItsOwnRepositoryIsOutsideACheckout(t *testing.T) {
	homebrew := func(t *testing.T, prefix string) string {
		t.Helper()
		for _, dir := range []string{".git", filepath.Join("Library", "Homebrew")} {
			if err := os.MkdirAll(filepath.Join(prefix, dir), 0700); err != nil {
				t.Fatal(err)
			}
		}
		bin := filepath.Join(prefix, "Cellar", "flinch", "0.1.2", "bin", "flinch")
		writeFixture(t, bin, []byte("flinch"))
		return bin
	}
	prefix := t.TempDir()
	bin := homebrew(t, prefix)
	if !outsideCheckout(bin, "") {
		t.Fatalf("a Homebrew keg counted as inside a checkout: %s", bin)
	}
	tracked := filepath.Join(prefix, "Library", "Homebrew", "flinch")
	writeFixture(t, tracked, []byte("flinch"))
	if outsideCheckout(tracked, "") {
		t.Fatalf("a file in Homebrew's own checkout counted as outside it: %s", tracked)
	}
	t.Run("stable link", func(t *testing.T) {
		stable := filepath.Join(prefix, "opt", "flinch")
		if err := os.MkdirAll(filepath.Dir(stable), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Dir(filepath.Dir(bin)), stable); err != nil {
			t.Skipf("symlinks unavailable here: %v", err)
		}
		path, err := stableBinaryFor(bin, bin)
		if err != nil || path != filepath.Join(stable, "bin", "flinch") || !outsideCheckout(path, "") {
			t.Fatalf("setup can't register the stable Homebrew link %q: %v", path, err)
		}
	})
	t.Run("ordinary checkout", func(t *testing.T) {
		project := t.TempDir()
		if err := os.Mkdir(filepath.Join(project, ".git"), 0700); err != nil {
			t.Fatal(err)
		}
		cellar := filepath.Join(project, "Cellar", "flinch", "0.1.2", "bin", "flinch")
		writeFixture(t, cellar, []byte("flinch"))
		if outsideCheckout(cellar, "") {
			t.Fatalf("a Cellar in a checkout that isn't Homebrew's counted as outside it: %s", cellar)
		}
	})
	t.Run("homebrew inside a project", func(t *testing.T) {
		project := t.TempDir()
		if err := os.Mkdir(filepath.Join(project, ".git"), 0700); err != nil {
			t.Fatal(err)
		}
		nested := homebrew(t, filepath.Join(project, "homebrew"))
		if outsideCheckout(nested, "") {
			t.Fatalf("a Homebrew prefix inside a project counted as outside it: %s", nested)
		}
	})
}

func TestConsentRejectsCheckoutControlledFiles(t *testing.T) {
	checkout := t.TempDir()
	if err := os.Mkdir(filepath.Join(checkout, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", checkout)
	t.Setenv("APPDATA", checkout)
	t.Setenv("LOCALAPPDATA", checkout)
	t.Setenv("XDG_CONFIG_HOME", checkout)
	dir, err := StateDir()
	if err != nil {
		t.Fatal(err)
	}
	const data = `{"version":1,"hosts":{"claude":{"binary":"/tmp/flinch"}}}`
	writeFixture(t, filepath.Join(dir, "setup.json"), []byte(data))
	if Consented("claude", checkout) {
		t.Fatal("a checkout enabled its own hooks")
	}
}

func TestPlanRechecksAndCanRetryAfterConcurrentEdit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("LOCALAPPDATA", home)
	claude := filepath.Join(home, "claude")
	codex := filepath.Join(home, "codex")
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	t.Setenv("CODEX_HOME", codex)
	p, err := Prepare("claude,codex", false)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(codex, "hooks.json")
	writeFixture(t, path, []byte(`{"keep":true}`))
	if err := p.Apply(); err == nil {
		t.Fatal("overwrote a concurrent edit")
	}
	if _, err := os.Stat(claude); !os.IsNotExist(err) {
		t.Fatalf("partially installed Claude: %v", err)
	}
	if Consented("codex", t.TempDir()) {
		t.Fatal("granted consent for a failed plan")
	}
	p, err = Prepare("claude,codex", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(b, []byte(`"keep": true`)) {
		t.Fatal(string(b), err)
	}
	if ok, reason := Status("codex"); !ok {
		t.Fatal(reason)
	}
}

func TestFailedSecondWriteRestoresFirstFileAndWithholdsConsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("LOCALAPPDATA", home)
	claude := filepath.Join(home, "claude")
	codex := filepath.Join(home, "codex")
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	t.Setenv("CODEX_HOME", codex)
	initial := []byte(`{ "keep" : true }`)
	settings := filepath.Join(claude, "settings.json")
	writeFixture(t, settings, initial)
	p, err := Prepare("claude,codex", false)
	if err != nil {
		t.Fatal(err)
	}
	// The second destination becomes project-controlled between review and apply.
	gitMarker := filepath.Join(codex, ".git")
	writeFixture(t, gitMarker, []byte("gitdir: elsewhere"))
	if err := p.Apply(); err == nil {
		t.Fatal("expected unsafe second write to fail")
	}
	if got, err := os.ReadFile(settings); err != nil || !bytes.Equal(got, initial) {
		t.Fatalf("first file was not restored: %s %v", got, err)
	}
	if Consented("claude", t.TempDir()) {
		t.Fatal("partial installation granted consent")
	}
	if err := os.Remove(gitMarker); err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(); err != nil {
		t.Fatal(err)
	}
	if !Consented("claude", t.TempDir()) {
		t.Fatal("retry failed to grant consent")
	}
}

func TestSetupUpgradesUnmarkedUserHooks(t *testing.T) {
	for _, host := range hostNames {
		t.Run(host, func(t *testing.T) {
			const bin = "C:/Tools/Flinch/flinch.exe"
			key := "hooks"
			group := map[string]any{}
			if host == "antigravity" {
				key = "flinch"
				group["enabled"] = true
			}
			for _, event := range events(host) {
				var item any = previewHandler(host, event, bin)
				if host != "antigravity" {
					item = map[string]any{"hooks": []any{item}}
				}
				group[event.name] = []any{item}
			}
			doc := map[string]any{key: group}
			previous := registration{Binary: bin}
			entry := registration{Binary: bin, UserHandler: true}
			changed, err := editHooks(doc, host, previous, &entry, false)
			if err != nil || !changed {
				t.Fatalf("upgrade: %t, %v", changed, err)
			}
			for _, event := range events(host) {
				want := handler(host, event, bin)
				var item any = want
				if host != "antigravity" {
					item = map[string]any{"hooks": []any{item}}
				}
				if !same(group[event.name], []any{item}) {
					t.Fatalf("unexpected %s definition after upgrade: %v", event.name, group[event.name])
				}
			}
			if _, err := editHooks(doc, host, entry, &entry, true); err != nil || len(doc) != 0 {
				t.Fatalf("upgraded hooks remained after removal: %v, %v", doc, err)
			}
		})
	}
}

func TestSetupKeepsCustomizedPreviewAndPreexistingCurrentHandlers(t *testing.T) {
	const host = "codex"
	const bin = "C:/Tools/Flinch/flinch.exe"
	old := registration{Binary: bin}
	first, stop := events(host)[0], events(host)[1]
	custom := previewHandler(host, first, bin)
	custom["timeout"] = 3
	group := func(h map[string]any) any { return map[string]any{"hooks": []any{h}} }
	current := group(handler(host, stop, bin))
	hooks := map[string]any{
		first.name: []any{group(custom)},
		stop.name:  []any{group(previewHandler(host, stop, bin)), current},
	}
	doc := map[string]any{"hooks": hooks}
	for i := 0; i < 2; i++ {
		updated := registration{Binary: bin, UserHandler: true}
		if _, err := editHooks(doc, host, old, &updated, false); err != nil {
			t.Fatal(err)
		}
		start := []any{group(custom), group(handler(host, first, bin))}
		if !same(hooks[first.name], start) || !same(hooks[stop.name], []any{current}) {
			t.Fatalf("upgrade changed or duplicated user definitions: %v", hooks)
		}
		old = updated
	}
	if _, err := editHooks(doc, host, old, &old, true); err != nil {
		t.Fatal(err)
	}
	if !same(hooks[first.name], []any{group(custom)}) || !same(hooks[stop.name], []any{current}) {
		t.Fatalf("removal changed user definitions: %v", hooks)
	}
}

func TestPlanShowsExactHookScopeDuringMigration(t *testing.T) {
	const host = "codex"
	bin := filepath.Join(t.TempDir(), "flinch.exe")
	dir := t.TempDir()
	for _, remove := range []bool{false, true} {
		old := registration{Binary: bin, Dir: dir}
		entry := registration{Binary: bin, Dir: dir, UserHandler: true}
		p := &Plan{remove: remove}
		if err := p.hostChanges(host, old, &entry); err != nil {
			t.Fatal(err)
		}
		for i, event := range events(host) {
			h := handler(host, event, bin)
			if remove {
				h = previewHandler(host, event, bin)
			}
			definition, _ := json.Marshal(h)
			oneLine, _, _ := strings.Cut(p.Commands[i], "\n")
			if strings.Contains(oneLine, "--user") == remove || !strings.Contains(p.Commands[i], string(definition)) {
				t.Fatalf("remove=%v shows a different command: %s", remove, p.Commands[i])
			}
		}
	}
}
