package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/angelnicolasc/flinch/internal/reflex"
	"github.com/angelnicolasc/flinch/internal/testutil"
)

func TestReportShapeSeparatesCleanFromUnreadable(t *testing.T) {
	for _, tc := range []struct {
		name, tool, stdout string
		exit               int
		unreadable         bool
	}{
		{"ruff clean", "ruff", "[]", 0, false},
		{"ruff finding", "ruff", `[{"code":"F401","filename":"a.py","location":{"row":1,"column":1}}]`, 1, false},
		{"ruff syntax error keeps a null code", "ruff", `[{"code":null,"filename":"a.py","location":{"row":1,"column":1}}]`, 1, false},
		{"ruff empty output", "ruff", "", 0, true},
		{"ruff text output", "ruff", "All checks passed!", 0, true},
		{"ruff object instead of array", "ruff", `{"diagnostics":[]}`, 1, true},
		{"ruff renamed field", "ruff", `[{"rule":"F401","filename":"a.py","location":{}}]`, 1, true},
		{"ruff usage error is judged by its exit code", "ruff", "error: bad flag", 2, false},
		{"eslint clean", "eslint", "[]", 0, false},
		{"eslint finding", "eslint", `[{"filePath":"/r/a.js","messages":[{"ruleId":"no-undef"}]}]`, 1, false},
		{"eslint truncated", "eslint", `[{"filePath":"/r/a.js","mess`, 1, true},
		{"golangci clean v1", "golangci-lint", `{"Issues":null,"Report":{}}`, 0, false},
		{"golangci clean v2", "golangci-lint", `{"Issues":[],"Report":{}}`, 0, false},
		{"golangci stats mixed into stdout", "golangci-lint", "{\"Issues\":[]}\n0 issues.", 0, true},
		{"golangci without Issues", "golangci-lint", `{"Report":{}}`, 0, true},
		{"other tools are not checked here", "go-vet", "", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkReportShape(tc.tool, LintRunResult{Stdout: tc.stdout, ExitCode: tc.exit})
			if got := errors.Is(err, ErrUnreadableReport); got != tc.unreadable {
				t.Fatalf("unreadable = %t (%v), want %t", got, err, tc.unreadable)
			}
		})
	}
}

func TestRunArtifactRejectsAnUnreadableReport(t *testing.T) {
	repo, tools := t.TempDir(), t.TempDir()
	name, script := "ruff", "#!/bin/sh\necho 'All checks passed!'\n"
	if runtime.GOOS == "windows" {
		name, script = "ruff.cmd", "@echo off\r\necho All checks passed!\r\n"
	}
	if err := os.WriteFile(filepath.Join(tools, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, err := RunArtifact(context.Background(), repo, reflex.Artifact{Tool: "ruff"}, nil, 10*time.Second)
	if !errors.Is(err, ErrUnreadableReport) {
		t.Fatalf("a text report passed as a clean scan: %v", err)
	}
}

func TestGolangciReportArgsFollowTheMajorVersion(t *testing.T) {
	v1 := golangciReportArgs("golangci-lint has version 1.64.8 built with go1.24.1 from 8b37f141 on 2025-03-17T20:41:53Z")
	v2 := golangciReportArgs("golangci-lint has version 2.6.2 built with go1.25.3 from dc16cf43 on 2025-11-14T02:47:46Z")
	unknown := golangciReportArgs("")
	if len(v1) != 3 || v1[1] != "--out-format" {
		t.Fatalf("v1 args: %v", v1)
	}
	for _, args := range [][]string{v2, unknown} {
		if len(args) != 3 || args[1] != "--output.json.path=stdout" || args[2] != "--show-stats=false" {
			t.Fatalf("v2 args: %v", args)
		}
	}
}

// TestRealGolangCILintReportsAndClears runs the installed golangci-lint, v1 or
// v2, through the production adapter on a finding and on its fix.
func TestRealGolangCILintReportsAndClears(t *testing.T) {
	bin := testutil.RequireToolEnv(t, "FLINCH_REAL_GOLANGCI_LINT")
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	repo := t.TempDir()
	write := func(src string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, "parse.go"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/lintcheck\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// errcheck reports an ignored strconv.Atoi error in both majors. v1 excludes
	// os.Remove by default (EXC0001), so that call would hide the finding there.
	write("package lintcheck\n\nimport \"strconv\"\n\nfunc Parse() {\n\tstrconv.Atoi(\"1\")\n}\n")
	res, err := RunArtifact(context.Background(), repo, reflex.Artifact{Tool: "golangci-lint"}, nil, 3*time.Minute)
	if err != nil || len(res.Findings) == 0 {
		t.Fatalf("expected an errcheck finding: %v %+v\nstdout %s\nstderr %s", err, res.Findings, res.Stdout, res.Stderr)
	}
	write("package lintcheck\n\nimport \"strconv\"\n\nfunc Parse() {\n\t_, _ = strconv.Atoi(\"1\")\n}\n")
	res, err = RunArtifact(context.Background(), repo, reflex.Artifact{Tool: "golangci-lint"}, nil, 3*time.Minute)
	if err != nil || len(res.Findings) != 0 {
		t.Fatalf("expected a clean report: %v %+v\nstdout %s\nstderr %s", err, res.Findings, res.Stdout, res.Stderr)
	}
}
