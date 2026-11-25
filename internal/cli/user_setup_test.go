package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isolateSetupHome(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "user with spaces")
	for k, v := range map[string]string{
		"HOME": home, "USERPROFILE": home, "LOCALAPPDATA": filepath.Join(home, "Local"),
		"XDG_CONFIG_HOME": filepath.Join(home, "config"), "CODEX_HOME": filepath.Join(home, ".codex"),
		"CLAUDE_CONFIG_DIR": filepath.Join(home, ".claude"),
	} {
		t.Setenv(k, v)
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	return home
}

func setupCLI(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), append([]string{"setup"}, args...), &stdout, &stderr)
	return code, stdout.String() + stderr.String()
}

func TestUserSetupPlanAndNoninteractiveConsent(t *testing.T) {
	home := isolateSetupHome(t)
	code, out := setupCLI(t, "--plan", "--host", "claude,codex,antigravity")
	if code != 0 {
		t.Fatal(out)
	}
	for _, want := range []string{"settings.json", "hooks.json", "plugin.json", "setup.json", "allow-commands", "flinch enable", "flinch disable", "FLINCH=off", "setup --remove", "hook --host codex --event Stop", "hook --host antigravity --event PreInvocation", "hook --host claude --event UserPromptSubmit"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan omits %q: %s", want, out)
		}
	}
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	writer.Close()
	saved := os.Stdin
	os.Stdin = input
	defer func() { os.Stdin = saved; input.Close() }()
	if code, out := setupCLI(t, "--host", "claude"); code == 0 || !strings.Contains(out, "--yes") {
		t.Errorf("noninteractive setup: %d %s", code, out)
	}
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
		t.Fatalf("preview or unconfirmed install wrote to home: %v %v", entries, err)
	}
}

func TestUserSetupMergesAndRemovesOnlyItsHooks(t *testing.T) {
	home := isolateSetupHome(t)
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	const foreign = `{"customNumber":9007199254740993,"hooks":{"Stop":[{"hooks":[{"type":"command","command":"keep me"}]}]}}`
	if err := os.WriteFile(path, []byte(foreign), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--yes", "--host", "claude,codex,antigravity"}, {"--yes", "--host", "claude,codex,antigravity"}} {
		if code, out := setupCLI(t, args...); code != 0 {
			t.Fatal(out)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || !json.Valid(data) || !bytes.Contains(data, []byte("9007199254740993")) || bytes.Count(data, []byte("UserPromptSubmit")) != 2 || bytes.Count(data, []byte("keep me")) != 1 {
		t.Fatalf("merged hooks: %s, %v", data, err)
	}
	for _, want := range []string{".codex/hooks.json", ".gemini/config/plugins/flinch/plugin.json", ".gemini/config/plugins/flinch/hooks.json"} {
		if _, err := os.Stat(filepath.Join(home, want)); err != nil {
			t.Error(err)
		}
	}
	if code, out := setupCLI(t, "--remove", "--yes", "--host", "claude"); code != 0 {
		t.Fatal(out)
	}
	data, err = os.ReadFile(path)
	if err != nil || !bytes.Contains(data, []byte("keep me")) || bytes.Contains(data, []byte("--event")) {
		t.Fatalf("removed other entries or retained Flinch: %s, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "hooks.json")); err != nil {
		t.Fatal(err)
	}
}

func TestUserSetupRefusesMalformedFileBeforeApplyingAnotherHost(t *testing.T) {
	home := isolateSetupHome(t)
	bad := filepath.Join(home, ".codex", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(bad), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("{} trailing"), 0600); err != nil {
		t.Fatal(err)
	}
	if code, _ := setupCLI(t, "--yes", "--host", "claude,codex"); code == 0 {
		t.Fatal("accepted invalid JSON")
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("wrote first host before validating second: %v", err)
	}
	if data, _ := os.ReadFile(bad); string(data) != "{} trailing" {
		t.Fatalf("changed invalid input: %s", data)
	}
}

func TestInitRequiresExplicitSharedHooks(t *testing.T) {
	var out, errs bytes.Buffer
	code := Run(context.Background(), []string{"init", "--hooks", "claude", "--repo", t.TempDir()}, &out, &errs)
	if code == 0 || !strings.Contains(errs.String(), "--shared") {
		t.Fatalf("missing migration instruction: %d %s", code, errs.String())
	}
}

func TestUserSetupPreservesPreexistingFlinchHandlersAndEmptySettings(t *testing.T) {
	home := isolateSetupHome(t)
	if code, out := setupCLI(t, "--yes", "--host", "claude"); code != 0 {
		t.Fatal(out)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	initial, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var src map[string]any
	if err := json.Unmarshal(initial, &src); err != nil {
		t.Fatal(err)
	}
	settings := src["hooks"].(map[string]any)
	stopGroup := settings["Stop"].([]any)[0].(map[string]any)
	stopGroup["hooks"].([]any)[0].(map[string]any)["timeout"] = 17
	settings["UserPromptSubmit"] = []any{}
	preexisting, _ := json.Marshal(src)
	if code, out := setupCLI(t, "--yes", "--remove", "--host", "claude"); code != 0 {
		t.Fatal(out)
	}
	if err := os.WriteFile(path, preexisting, 0600); err != nil {
		t.Fatal(err)
	}
	if code, out := setupCLI(t, "--yes", "--host", "claude"); code != 0 {
		t.Fatal(out)
	}
	merged, err := os.ReadFile(path)
	if err != nil || bytes.Count(merged, []byte("--event")) != 2 {
		t.Fatalf("duplicated customized handler: %s, %v", merged, err)
	}
	if code, out := setupCLI(t, "--yes", "--remove", "--host", "claude"); code != 0 {
		t.Fatal(out)
	}
	restored, err := os.ReadFile(path)
	var got map[string]any
	if err != nil || json.Unmarshal(restored, &got) != nil {
		t.Fatal(string(restored), err)
	}
	result, _ := json.Marshal(got)
	if !bytes.Equal(result, preexisting) {
		t.Fatalf("preexisting settings changed: got %s want %s", result, preexisting)
	}
}

func TestUserSetupDiscoversStandaloneAntigravityProfile(t *testing.T) {
	home := isolateSetupHome(t)
	t.Setenv("PATH", t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".gemini", "antigravity-cli"), 0700); err != nil {
		t.Fatal(err)
	}
	code, out := setupCLI(t, "--plan")
	if code != 0 || !strings.Contains(out, "PreInvocation") || strings.Contains(out, "UserPromptSubmit") {
		t.Fatalf("standalone profile discovery: %d %s", code, out)
	}
}

func TestUserSetupLeavesMatchingManualRegistrationByteIdentical(t *testing.T) {
	home := isolateSetupHome(t)
	path := filepath.Join(home, ".codex", "hooks.json")
	if code, out := setupCLI(t, "--yes", "--host", "codex"); code != 0 {
		t.Fatal(out)
	}
	input, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if code, out := setupCLI(t, "--yes", "--remove", "--host", "codex"); code != 0 {
		t.Fatal(out)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, input); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, compact.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--yes", "--host", "codex"}, {"--yes", "--host", "codex"}, {"--yes", "--remove", "--host", "codex"}} {
		if code, out := setupCLI(t, args...); code != 0 {
			t.Fatal(out)
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, compact.Bytes()) {
			t.Fatalf("changed a preexisting registration: %s, %v", got, err)
		}
	}
}
