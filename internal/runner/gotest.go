package runner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"time"
)

type goTestEvent struct {
	Time       time.Time `json:"Time"`
	Action     string    `json:"Action"`
	Package    string    `json:"Package"`
	Test       string    `json:"Test"`
	Elapsed    float64   `json:"Elapsed"`
	Output     string    `json:"Output"`
	ImportPath string    `json:"ImportPath"`
}

// AnchorGoTestRunPattern converts a test selector like "TestClient/rejects_unbounded"
// into an exact anchored `go test -run` regex ("^TestClient$/^rejects_unbounded$").
// If selector already begins with '^', it is returned unchanged.
func AnchorGoTestRunPattern(selector string) string {
	s := strings.TrimSpace(selector)
	if s == "" || strings.HasPrefix(s, "^") {
		return s
	}
	parts := strings.Split(s, "/")
	for i, p := range parts {
		parts[i] = "^" + regexpEscapeLiteral(p) + "$"
	}
	return strings.Join(parts, "/")
}

func regexpEscapeLiteral(s string) string {
	const meta = `\.+*?()|[]{}^$`
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(meta, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// RunGoTest executes `go test -json -count=1` and classifies every executed test/subtest
// as well as build/compilation failures (CompileOnly).
func RunGoTest(ctx context.Context, dir string, packages []string, testSelector string, timeout time.Duration) (TestRunResult, error) {
	if len(packages) == 0 {
		packages = []string{"./..."}
	}
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"test", "-json", "-fullpath", "-count=1"}
	if pattern := AnchorGoTestRunPattern(testSelector); pattern != "" {
		args = append(args, "-run", pattern)
	}
	args = append(args, packages...)

	start := time.Now()
	cmd := exec.CommandContext(tctx, "go", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := runCandidate(cmd, dir)
	elapsedMS := time.Since(start).Milliseconds()

	goInfo := ProbeTool(ctx, []string{"go"}, "version")
	res := TestRunResult{
		Runner:        "go-test",
		RunnerVersion: goInfo.Version,
		Command:       "go " + strings.Join(args, " "),
		DurationMS:    elapsedMS,
		RawOutput:     stdout.String() + stderr.String(),
	}

	if errors.Is(tctx.Err(), context.DeadlineExceeded) {
		res.TimedOut = true
		res.ExitCode = -1
		return res, nil
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else {
			return res, runErr
		}
	}

	ParseGoTestJSON(stdout.Bytes(), &res)
	if stderr.Len() > 0 {
		if strings.Contains(stderr.String(), "build failed") || strings.Contains(stderr.String(), "undefined:") || strings.Contains(stderr.String(), "cannot ") {
			res.CompileOnly = true
			res.BuildOutput = strings.TrimSpace(res.BuildOutput + "\n" + stderr.String())
		}
	}
	return res, nil
}

// ParseGoTestJSON decodes `go test -json` event streams into structured TestCaseResults.
func ParseGoTestJSON(data []byte, res *TestRunResult) {
	type key struct{ pkg, test string }
	outputs := make(map[key]*strings.Builder)
	outcomes := make(map[key]TestCaseResult)
	var order []key
	seenOrder := make(map[key]bool)
	var buildBuf strings.Builder

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var ev goTestEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		switch ev.Action {
		case "build-output":
			buildBuf.WriteString(ev.Output)
		case "build-fail":
			res.CompileOnly = true
		case "output":
			if ev.Test != "" {
				k := key{ev.Package, ev.Test}
				if outputs[k] == nil {
					outputs[k] = &strings.Builder{}
				}
				outputs[k].WriteString(ev.Output)
			}
		case "pass", "fail", "skip":
			if ev.Test == "" {
				continue
			}
			k := key{ev.Package, ev.Test}
			if !seenOrder[k] {
				seenOrder[k] = true
				order = append(order, k)
			}
			outStr := ""
			if b := outputs[k]; b != nil {
				outStr = strings.TrimSpace(b.String())
			}
			outcomes[k] = TestCaseResult{
				Package:    ev.Package,
				Name:       ev.Test,
				Outcome:    TestOutcome(ev.Action),
				ElapsedSec: ev.Elapsed,
				Output:     outStr,
			}
		}
	}
	if buildBuf.Len() > 0 {
		res.BuildOutput = strings.TrimSpace(buildBuf.String())
	}
	for _, k := range order {
		res.Cases = append(res.Cases, outcomes[k])
	}
}
