package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/angelnicolasc/flinch/internal/reflex"
)

func TestGolangciLintReusesOnlyItsCacheOutsideTheCheckout(t *testing.T) {
	repo, tools, userCache := t.TempDir(), t.TempDir(), t.TempDir()
	name := "golangci-lint"
	probe := "#!/bin/sh\nprintf '%s\\n%s\\n' \"$GOLANGCI_LINT_CACHE\" \"$TMPDIR\"\n"
	switch runtime.GOOS {
	case "windows":
		t.Setenv("LOCALAPPDATA", userCache)
		name += ".cmd"
		probe = "@echo off\r\necho %GOLANGCI_LINT_CACHE%\r\necho %TMPDIR%\r\n"
	case "darwin":
		t.Setenv("HOME", userCache)
	default:
		t.Setenv("XDG_CACHE_HOME", userCache)
	}
	if err := os.WriteFile(filepath.Join(tools, name), []byte(probe), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	base, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(base, "flinch", "golangci-lint")
	run := func(ctx context.Context) (cache, scratch string) {
		t.Helper()
		// The probe prints paths instead of a JSON report, so the run reports
		// ErrUnreadableReport. The environment it saw is still in Stdout.
		result, err := RunArtifact(ctx, repo, reflex.Artifact{Tool: "golangci-lint"}, nil, 5*time.Second)
		if (err != nil && !errors.Is(err, ErrUnreadableReport)) || result.ExitCode != 0 {
			t.Fatalf("cache probe: %+v, %v", result, err)
		}
		paths := strings.Split(strings.TrimSpace(strings.ReplaceAll(result.Stdout, "\r", "")), "\n")
		if len(paths) != 2 || paths[0] == "" || paths[1] == "" {
			t.Fatalf("unexpected probe output %q", result.Stdout)
		}
		if _, err := os.Stat(paths[1]); !os.IsNotExist(err) {
			t.Fatalf("command scratch directory survived: %s (%v)", paths[1], err)
		}
		return paths[0], paths[1]
	}
	first, scratch1 := run(context.Background())
	second, scratch2 := run(context.Background())
	if first != want || second != want || scratch1 == scratch2 {
		t.Fatalf("cache and scratch: first %q %q, second %q %q; want persistent %q", first, scratch1, second, scratch2, want)
	}
	// A user cache located in the source clone must stay disposable during proof.
	cache, scratch := run(WithCommandRepository(context.Background(), userCache))
	if cache != scratch {
		t.Fatalf("cache inside the source checkout: %q", cache)
	}
}
