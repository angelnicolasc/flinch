package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/hook"
	"github.com/angelnicolasc/flinch/internal/testutil"
)

func registeredHookArgs(t *testing.T, path, group, event string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	var events map[string]json.RawMessage
	if err := json.Unmarshal(root[group], &events); err != nil {
		t.Fatal(err)
	}
	var list []json.RawMessage
	if err := json.Unmarshal(events[event], &list); err != nil || len(list) != 1 {
		t.Fatalf("%s %s: %v", path, event, err)
	}
	handler := list[0]
	if group == "hooks" {
		var matcher struct{ Hooks []json.RawMessage }
		if err := json.Unmarshal(handler, &matcher); err != nil || len(matcher.Hooks) != 1 {
			t.Fatalf("%s: %v", path, err)
		}
		handler = matcher.Hooks[0]
	}
	var command struct {
		Command, CommandWindows string
		Args                    []string
	}
	if err := json.Unmarshal(handler, &command); err != nil {
		t.Fatal(err)
	}
	if len(command.Args) != 0 {
		return command.Args
	}
	line := command.Command
	if runtime.GOOS == "windows" && command.CommandWindows != "" {
		line = command.CommandWindows
	}
	_, args, ok := strings.Cut(line, " hook ")
	if !ok {
		t.Fatalf("expected direct hook command in %s", path)
	}
	return append([]string{"hook"}, strings.Fields(args)...)
}

func deliverFixtureHook(t *testing.T, args []string, payload []byte) {
	t.Helper()
	in, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	if _, err := in.Write(payload); err != nil {
		t.Fatal(err)
	}
	if _, err := in.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = in
	defer func() { os.Stdin = old }()
	var output bytes.Buffer
	if code := Run(context.Background(), args, &output, &output); code != 0 {
		t.Fatalf("hook failed: %s", output.Bytes())
	}
}

func TestSharedAndUserHooksDeliverOncePerEvent(t *testing.T) {
	for _, host := range []hook.Host{hook.HostClaude, hook.HostCodex, hook.HostAntigravity} {
		t.Run(string(host), func(t *testing.T) {
			repo := t.TempDir()
			if _, _, err := gitutil.Run(context.Background(), repo, nil, "init"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := gitutil.Run(context.Background(), repo, nil, "-c", "user.name=fixture", "-c", "user.email=fixture@example.com", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "Initialize fixture"); err != nil {
				t.Fatal(err)
			}
			home := testutil.HookProfile(t, repo)
			shared, err := hook.InstallHooks(repo, host, "flinch")
			if err != nil {
				t.Fatal(err)
			}
			if code, msg := setupCLI(t, "--host", string(host), "--yes"); code != 0 {
				t.Fatal(msg)
			}
			userFile := map[hook.Host]string{hook.HostClaude: ".claude/settings.json", hook.HostCodex: ".codex/hooks.json", hook.HostAntigravity: ".gemini/config/plugins/flinch/hooks.json"}[host]
			group, start := "hooks", "UserPromptSubmit"
			if host == hook.HostAntigravity {
				group, start = "flinch", "PreInvocation"
			}
			t.Setenv("FLINCH_RECORD", "1")
			userPath := filepath.Join(home, filepath.FromSlash(userFile))
			sharedPath := filepath.Join(repo, shared.Path)
			params := map[string]any{"cwd": repo, "session_id": "fixture", "conversationId": "fixture", "prompt": "ordinary change", "fullyIdle": true, "terminationReason": "model_stop"}
			payload, _ := json.Marshal(params)
			wantCount := func(n int) {
				t.Helper()
				files, err := filepath.Glob(filepath.Join(repo, ".git", "flinch", "recordings", "*.json"))
				if err != nil || len(files) != n {
					t.Fatalf("expected %d effective deliveries, got %d (%v)", n, len(files), err)
				}
			}
			for i, event := range []string{start, "Stop"} {
				userArgs := registeredHookArgs(t, userPath, group, event)
				sharedArgs := registeredHookArgs(t, sharedPath, group, event)
				// Exercise either callback arriving first. The shared command is also the
				// form published before per-user setup existed.
				pair := [][]string{sharedArgs, userArgs}
				if i == 1 {
					pair = [][]string{userArgs, sharedArgs}
				}
				for _, args := range pair {
					deliverFixtureHook(t, args, payload)
				}
				wantCount(i + 1)
			}
			// Keep a filtered or disabled user start and remove only the user Stop.
			// Shared handling must honor the first choice and fill the second gap.
			data, err := os.ReadFile(userPath)
			if err != nil {
				t.Fatal(err)
			}
			var settings map[string]map[string]any
			if err := json.Unmarshal(data, &settings); err != nil {
				t.Fatal(err)
			}
			events := settings[group]
			delete(events, "Stop")
			if host == hook.HostAntigravity {
				events["enabled"] = false
			} else {
				events[start].([]any)[0].(map[string]any)["matcher"] = "fixture"
			}
			edited, err := json.Marshal(settings)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(userPath, edited, 0600); err != nil {
				t.Fatal(err)
			}
			deliverFixtureHook(t, registeredHookArgs(t, sharedPath, group, start), payload)
			wantCount(2)
			deliverFixtureHook(t, registeredHookArgs(t, sharedPath, group, "Stop"), payload)
			wantCount(3)
		})
	}
}
