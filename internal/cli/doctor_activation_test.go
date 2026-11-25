package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestClonePermissionCommandAndDiagnostics(t *testing.T) {
	isolateSetupHome(t)
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "")
	root := t.TempDir()
	if err := exec.Command("git", "init", "-q", root).Run(); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	run := func(args ...string) string {
		t.Helper()
		var out, stderr bytes.Buffer
		if rc := Run(context.Background(), args, &out, &stderr); rc != 0 {
			t.Fatalf("%v: %s", args, stderr.String())
		}
		return out.String()
	}
	run("setup", "--yes", "--host", "claude,codex,antigravity")
	for _, step := range []struct {
		action string
		state  string
	}{{"enable", "active"}, {"disable", "inactive"}} {
		message := run(step.action)
		if !strings.Contains(message, "Codex uses its own workspace trust") {
			t.Fatal(message)
		}
		diagnosis := run("doctor")
		for _, host := range []string{"claude", "codex", "antigravity"} {
			expected := step.state
			if host == "codex" {
				expected = "inactive"
			}
			if !slices.ContainsFunc(strings.Split(diagnosis, "\n"), func(line string) bool {
				return strings.HasPrefix(line, host+":") && strings.Contains(line, "| "+expected+" (")
			}) {
				t.Fatalf("after %s expected %s %s:\n%s", step.action, host, expected, diagnosis)
			}
		}
	}
}

func TestDoctorReportsCodexSubdirectoryAndOutsideGit(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	sub := filepath.Join(root, "src")
	for name, value := range map[string]string{"HOME": home, "USERPROFILE": home, "LOCALAPPDATA": home, "XDG_CONFIG_HOME": home, "CODEX_HOME": filepath.Join(home, ".codex")} {
		t.Setenv(name, value)
	}
	cmd := exec.Command("git", "init", "-q", root)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(os.Getenv("CODEX_HOME"), 0700); err != nil {
		t.Fatal(err)
	}
	key, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("CODEX_HOME"), "config.toml"), []byte("[projects."+strconv.Quote(key)+"]\ntrust_level=\"trusted\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ dir, status string }{{sub, "inactive"}, {root, "active"}} {
		t.Chdir(tc.dir)
		var out, errs bytes.Buffer
		if code := Run(context.Background(), []string{"setup", "--plan", "--host", "codex"}, &out, &errs); code != 0 {
			t.Fatalf("plan: %s", errs.String())
		}
		var diagnostic string
		for _, line := range strings.Split(out.String(), "\n") {
			lower := strings.ToLower(line)
			if strings.Contains(lower, "codex") && strings.Contains(lower, "after setup") {
				diagnostic = lower
				break
			}
		}
		if !strings.Contains(diagnostic, "after setup: "+tc.status+" (") || tc.dir == sub && !strings.Contains(diagnostic, "trusted repository root") {
			t.Fatalf("missing current-directory diagnosis: %s", out.String())
		}
	}
	for _, dir := range []string{sub, home} {
		var out, errors bytes.Buffer
		if code := Run(context.Background(), []string{"doctor", "--all", "--repo", dir}, &out, &errors); code != 0 {
			t.Fatalf("doctor: %s", errors.String())
		}
		if msg := out.String(); !strings.Contains(msg, "codex") || !strings.Contains(msg, "inactive") {
			t.Fatalf("missing hook status: %s", msg)
		}
		if dir == sub && !strings.Contains(strings.ToLower(out.String()), "repository root") {
			t.Fatalf("missing actionable subdirectory reason: %s", out.String())
		}
	}
}
