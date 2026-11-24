//go:build linux

package hook

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/angelnicolasc/flinch/internal/gitutil"
)

func TestCannotRequestAuthoringWhenCheckpointStoreIsReadOnly(t *testing.T) {
	// Root can write through a read-only directory mode. Exercise the exact
	// same scenario as an unprivileged child when the CI container runs as root.
	const child = "FLINCH_TEST_READ_ONLY_CHECKPOINT"
	if os.Geteuid() == 0 && os.Getenv(child) == "" {
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(binary, "-test.run=^TestCannotRequestAuthoringWhenCheckpointStoreIsReadOnly$")
		cmd.Env = append(os.Environ(), child+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v", output, err)
		}
		return
	}
	if os.Geteuid() == 0 {
		if err := syscall.Setgroups(nil); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Setgid(65534); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Setuid(65534); err != nil {
			t.Fatal(err)
		}
		t.Setenv("TMPDIR", "/tmp")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	repo := initHookTestRepo(t)
	regressionHook(t, repo, HostClaude, "UserPromptSubmit", map[string]any{"prompt": "Add retry delay"})
	regressionFile(t, repo, "retry.go", "package hookdemo\n\nfunc Delay(v *int) int { return 50 }\n")
	regressionHook(t, repo, HostClaude, "Stop", map[string]any{})
	regressionHook(t, repo, HostClaude, "UserPromptSubmit", map[string]any{"prompt": "Correct the retry delay"})
	regressionFile(t, repo, "retry.go", "package hookdemo\n\nfunc Delay(v *int) int { return 25 }\n")
	state, err := gitutil.FlinchStateDir(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(state, "turns")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	r := regressionHook(t, repo, HostClaude, "Stop", map[string]any{})
	if r.ExitCode != 0 || bytes.Contains(r.Stdout, []byte("block")) {
		t.Fatalf("requested a continuation without persisting it: %s", r.Stdout)
	}
}
