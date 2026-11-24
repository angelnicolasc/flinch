package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/angelnicolasc/flinch/internal/hook"
)

func TestInstalledHookDeadlineIncludesWaitingForInput(t *testing.T) {
	// Run the real CLI entry point in a child so all hosts can wait on a
	// different open stdin pipe and exercise their registered wall-clock limit.
	if event := os.Getenv("FLINCH_TEST_HOOK_DEADLINE_EVENT"); event != "" {
		os.Stderr.WriteString("ready\n")
		os.Exit(Run(context.Background(), []string{"hook", "--host", os.Getenv("FLINCH_TEST_HOOK_DEADLINE_HOST"), "--event", event}, os.Stdout, os.Stderr))
	}
	for _, host := range []hook.Host{hook.HostClaude, hook.HostCodex, hook.HostAntigravity} {
		repo := t.TempDir()
		installed, err := hook.InstallHooks(repo, host, "flinch")
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.ReadFile(filepath.Join(repo, installed.Path))
		if err != nil {
			t.Fatal(err)
		}
		var groups map[string]json.RawMessage
		if err := json.Unmarshal(file, &groups); err != nil {
			t.Fatal(err)
		}
		group, start := "hooks", "UserPromptSubmit"
		if host == hook.HostAntigravity {
			group, start = "flinch", "PreInvocation"
		}
		var events map[string]json.RawMessage
		if err := json.Unmarshal(groups[group], &events); err != nil {
			t.Fatal(err)
		}
		for _, event := range []string{start, "Stop"} {
			var handlers []struct {
				Timeout int
				Hooks   []struct{ Timeout int }
			}
			if err := json.Unmarshal(events[event], &handlers); err != nil || len(handlers) != 1 {
				t.Fatalf("invalid %s registration: %s", host, events[event])
			}
			seconds := handlers[0].Timeout
			if len(handlers[0].Hooks) > 0 {
				seconds = handlers[0].Hooks[0].Timeout
			}
			if seconds <= 0 || seconds > 60 {
				t.Fatalf("unreasonable registration for %s/%s: %d", host, event, seconds)
			}
			t.Run(string(host)+"/"+event, func(t *testing.T) {
				t.Parallel()
				limit := time.Duration(seconds) * time.Second
				ctx, cancel := context.WithTimeout(context.Background(), limit+2*time.Second)
				defer cancel()
				binary, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				cmd := exec.CommandContext(ctx, binary, "-test.run=^TestInstalledHookDeadlineIncludesWaitingForInput$")
				cmd.Env = append(os.Environ(), "FLINCH_TEST_HOOK_DEADLINE_HOST="+string(host), "FLINCH_TEST_HOOK_DEADLINE_EVENT="+event, "GORACE=atexit_sleep_ms=0")
				writer, err := cmd.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				defer writer.Close()
				ready, err := cmd.StderrPipe()
				if err != nil {
					t.Fatal(err)
				}
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				if line, err := bufio.NewReader(ready).ReadString('\n'); err != nil || line != "ready\n" {
					cmd.Wait()
					t.Fatalf("hook child failed to start: %s, %v", line, err)
				}
				started := time.Now()
				if err := cmd.Wait(); err != nil {
					t.Fatalf("%s/%s exceeded its installed %s timeout: %v", host, event, limit, err)
				}
				if elapsed := time.Since(started); elapsed >= limit {
					t.Fatalf("%s/%s returned after %s (host limit %s)", host, event, elapsed, limit)
				}
			})
		}
	}
}
