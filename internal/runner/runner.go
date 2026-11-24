package runner

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/angelnicolasc/flinch/internal/finding"
)

// TestOutcome represents the structured status of a single executed test case or subtest.
type TestOutcome string

const (
	OutcomePass  TestOutcome = "pass"
	OutcomeFail  TestOutcome = "fail"
	OutcomeSkip  TestOutcome = "skip"
	OutcomeError TestOutcome = "error"
)

// TestCaseResult captures one test or subtest result from a structured runner.
type TestCaseResult struct {
	Package    string      `json:"package,omitempty"`
	Name       string      `json:"name"`
	Outcome    TestOutcome `json:"outcome"`
	ElapsedSec float64     `json:"elapsed_sec,omitempty"`
	Output     string      `json:"output,omitempty"`
}

// TestRunResult captures the full structured outcome of a test runner invocation.
type TestRunResult struct {
	Runner        string           `json:"runner"`
	RunnerVersion string           `json:"runner_version,omitempty"`
	Command       string           `json:"command"`
	ExitCode      int              `json:"exit_code"`
	CompileOnly   bool             `json:"compile_only"`
	BuildOutput   string           `json:"build_output,omitempty"`
	Cases         []TestCaseResult `json:"cases,omitempty"`
	DurationMS    int64            `json:"duration_ms"`
	TimedOut      bool             `json:"timed_out"`
	RawOutput     string           `json:"raw_output,omitempty"`
}

// LintRunResult captures normalized findings and raw output from a linter, compiler, or command.
type LintRunResult struct {
	Tool        string            `json:"tool"`
	ToolVersion string            `json:"tool_version,omitempty"`
	Command     string            `json:"command"`
	ExitCode    int               `json:"exit_code"`
	Findings    []finding.Finding `json:"findings,omitempty"`
	DurationMS  int64             `json:"duration_ms"`
	TimedOut    bool              `json:"timed_out"`
	Stdout      string            `json:"stdout,omitempty"`
	Stderr      string            `json:"stderr,omitempty"`
}

// ToolInfo reports whether a local binary is available and its observed version string.
type ToolInfo struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
}

// ProbeTool checks if binary (or any fallback name) is installed and queries its version.
func ProbeTool(ctx context.Context, names []string, versionArgs ...string) ToolInfo {
	info := ToolInfo{Name: names[0]}
	var foundPath string
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			foundPath = p
			break
		}
	}
	if foundPath == "" {
		return info
	}
	info.Available = true
	info.Path = foundPath
	if len(versionArgs) > 0 {
		tctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(tctx, foundPath, versionArgs...)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err == nil {
			line := strings.TrimSpace(out.String())
			if idx := strings.IndexByte(line, '\n'); idx >= 0 {
				line = strings.TrimSpace(line[:idx])
			}
			info.Version = line
		}
	}
	return info
}

// DiscoverTools probes the standard toolchain used by Flinch and target repositories.
func DiscoverTools(ctx context.Context) map[string]ToolInfo {
	probes := []struct {
		key   string
		names []string
		args  []string
	}{
		{"git", []string{"git"}, []string{"--version"}},
		{"go", []string{"go"}, []string{"version"}},
		{"gh", []string{"gh"}, []string{"--version"}},
		{"ast-grep", []string{"ast-grep", "sg"}, []string{"--version"}},
		{"golangci-lint", []string{"golangci-lint"}, []string{"--version"}},
		{"eslint", []string{"eslint"}, []string{"--version"}},
		{"ruff", []string{"ruff"}, []string{"--version"}},
		{"pytest", []string{"pytest"}, []string{"--version"}},
		{"claude", []string{"claude"}, []string{"--version"}},
		{"codex", []string{"codex"}, []string{"--version"}},
		{"opencode", []string{"opencode"}, []string{"--version"}},
	}
	res := make(map[string]ToolInfo, len(probes))
	for _, p := range probes {
		info := ProbeTool(ctx, p.names, p.args...)
		info.Name = p.key
		res[p.key] = info
	}
	return res
}

func shellCommand(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd", "/C", command)
	}
	return exec.CommandContext(ctx, "sh", "-c", command)
}

func relTargetsOrAll(rootDir string, files []string) []string {
	if len(files) == 0 {
		return nil
	}
	out := make([]string, 0, len(files))
	for _, f := range files {
		clean := filepath.ToSlash(strings.TrimPrefix(filepath.Clean(f), "./"))
		if clean != "" && clean != "." {
			out = append(out, filepath.FromSlash(clean))
		}
	}
	return out
}
