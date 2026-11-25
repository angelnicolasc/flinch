package testutil

import (
	"os"
	"os/exec"
	"testing"
)

// RequireTool returns the path of a tool an integration test runs for real.
// On a contributor's machine a missing tool skips the test. With
// FLINCH_REQUIRE_TOOLS=1, which CI sets, a missing tool fails it instead, so a
// green run means every integration actually ran.
func RequireTool(t testing.TB, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		missingTool(t, name+" is not on PATH")
	}
	return path
}

// RequireToolEnv returns the binary an environment variable names, with the
// same policy as RequireTool.
func RequireToolEnv(t testing.TB, env string) string {
	t.Helper()
	bin := os.Getenv(env)
	if bin == "" {
		missingTool(t, "set "+env+" to the installed binary")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("%s names %q: %v", env, bin, err)
	}
	return bin
}

func missingTool(t testing.TB, reason string) {
	t.Helper()
	if os.Getenv("FLINCH_REQUIRE_TOOLS") == "1" || os.Getenv("FLINCH_ASSERTION_DIAGNOSTICS") != "" {
		t.Fatal("a required integration tool is missing: " + reason)
	}
	t.Skip(reason)
}
