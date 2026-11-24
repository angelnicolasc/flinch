package prove

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/angelnicolasc/flinch/internal/gitutil"
)

// assertionSites contains source locations in the selected case, fixed before any runner starts.
type assertionSites struct {
	tool          string
	path          string
	lines         []int
	testify, logs []int
}

func findAssertionSites(root string, spec CandidateSpec) assertionSites {
	path, err := gitutil.LocalPath(root, spec.Artifact.Path)
	if err != nil {
		return assertionSites{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return assertionSites{}
	}
	s := assertionSites{tool: spec.Artifact.Tool, path: filepath.ToSlash(filepath.Clean(spec.Artifact.Path))}
	switch s.tool {
	case "go", "go-test":
		goSites := goAssertionLines(data, spec.Artifact.Selector)
		s.lines, s.testify, s.logs = goSites.lines, goSites.testify, goSites.logs
	case "pytest":
		s.lines = pythonAssertionLines(string(data), spec.Artifact.Selector)
	case "vitest", "jest":
		s.lines = jsAssertionLines(string(data), spec.Artifact.Selector)
	}
	slices.Sort(s.lines)
	s.lines = slices.Compact(s.lines)
	return s
}

func (s assertionSites) description() string {
	if len(s.lines) == 0 {
		return ""
	}
	var lines []string
	for _, line := range s.lines {
		lines = append(lines, strconv.Itoa(line))
	}
	return s.path + ":" + strings.Join(lines, ",")
}

type assertionFailure struct {
	lines      string
	diagnostic string
}

var (
	goDiagnosticFrame  = regexp.MustCompile(`^(.+\.go):([0-9]+):(?:\s|$)`)
	goTraceFrame       = regexp.MustCompile(`^(.+\.go):([0-9]+)$`)
	pytestTerminal     = regexp.MustCompile(`^(.+\.py):([0-9]+):\s*([[:alnum:]_.]+)\s*$`)
	jsFrame            = regexp.MustCompile(`(?:^at |\()([^()]*?\.[cm]?[jt]sx?):([0-9]+):[0-9]+\)?$`)
	jsAssertionFailure = regexp.MustCompile(`(?im)(?:\bAssertionError\b|expect\(received\)\.)`)
	terminalEscape     = regexp.MustCompile("\x1b" + `\[[0-9;]*m`)
)

func (s assertionSites) sourceLine(name, number string) (int, bool) {
	name = strings.TrimPrefix(name, "file://")
	name = strings.TrimPrefix(strings.TrimPrefix(name, "/<tree>/"), "<tree>/")
	if name != s.path {
		return 0, false
	}
	line, err := strconv.Atoi(number)
	return line, err == nil && slices.Contains(s.lines, line)
}

// failure identifies the originating frame, never a caller further down the stack.
func (s assertionSites) failure(diagnostic string) (assertionFailure, bool) {
	if len(s.lines) == 0 {
		return assertionFailure{}, false
	}
	lines := strings.Split(diagnostic, "\n")
	switch s.tool {
	case "go", "go-test":
		return s.goFailure(lines)
	case "pytest":
		// JUnit ends its failure traceback with the source and exception type.
		m := pytestTerminal.FindStringSubmatch(strings.ReplaceAll(lines[len(lines)-1], "\\", "/"))
		if m == nil || (m[3] != "AssertionError" && !(m[3] == "Failed" && strings.Contains(diagnostic, "DID NOT RAISE"))) {
			return assertionFailure{}, false
		}
		_, ok := s.sourceLine(m[1], m[2])
		return assertionFailure{m[2], diagnostic}, ok
	case "vitest", "jest":
		promiseFailure := strings.HasPrefix(diagnostic, "Error: promise ") && (strings.Contains(diagnostic, " instead of rejecting") || strings.Contains(diagnostic, " instead of resolving")) && strings.Contains(diagnostic, "/node_modules/@vitest/expect/")
		if !jsAssertionFailure.MatchString(diagnostic) && !promiseFailure {
			return assertionFailure{}, false
		}
		endMessage := len(lines)
		for i, text := range lines {
			if !strings.HasPrefix(text, "at ") {
				continue
			}
			if i < endMessage {
				endMessage = i
			}
			text = strings.ReplaceAll(text, "\\", "/")
			if isJSRunnerFrame(text) {
				continue
			}
			m := jsFrame.FindStringSubmatch(text)
			if m == nil {
				return assertionFailure{}, false
			}
			_, ok := s.sourceLine(m[1], m[2])
			return assertionFailure{m[2], strings.Join(lines[:endMessage], "\n")}, ok
		}
	}
	return assertionFailure{}, false
}

func (s assertionSites) goFailure(output []string) (assertionFailure, bool) {
	var origins, message []string
	line := 0
	logging, needsTrace := false, false
	for _, text := range output {
		frame := strings.ReplaceAll(text, "\\", "/")
		if m := goDiagnosticFrame.FindStringSubmatch(frame); m != nil {
			if needsTrace {
				return assertionFailure{}, false
			}
			n, _ := strconv.Atoi(m[2])
			if strings.TrimPrefix(m[1], "<tree>/") != s.path {
				return assertionFailure{}, false
			}
			logging = slices.Contains(s.logs, n)
			if logging && slices.Contains(s.lines, n) {
				return assertionFailure{}, false
			}
			if !logging {
				if !slices.Contains(s.lines, n) {
					return assertionFailure{}, false
				}
				line = n
				origins = append(origins, m[2])
				needsTrace = slices.Contains(s.testify, n)
			}
		}
		if logging || line == 0 {
			continue
		}
		if trace, ok := strings.CutPrefix(frame, "Error Trace:"); ok {
			m := goTraceFrame.FindStringSubmatch(strings.TrimSpace(trace))
			if m == nil {
				return assertionFailure{}, false
			}
			origin, ok := s.sourceLine(m[1], m[2])
			if !ok || origin != line {
				return assertionFailure{}, false
			}
			needsTrace = false
		}
		message = append(message, text)
	}
	return assertionFailure{strings.Join(origins, ","), strings.Join(message, "\n")}, len(origins) > 0 && !needsTrace
}

func isJSRunnerFrame(text string) bool {
	if strings.Contains(text, "node:internal/") || text == "at Reflect.get (<anonymous>)" {
		return true
	}
	for _, pkg := range []string{"expect/", "@jest/", "jest-", "@vitest/", "vitest/", "chai/", "tinyspy/"} {
		if strings.Contains(text, "/node_modules/"+pkg) {
			return true
		}
	}
	return false
}

func (s assertionSites) matchFailure(output, expected string) (assertionFailure, bool) {
	if output == "" || (expected != "" && !matchesExpectedSignal(output, expected)) {
		return assertionFailure{}, false
	}
	failure, located := s.failure(output)
	if !located && expected != "" && len(s.lines) == 0 {
		// Explicit signals support custom test harnesses with their own diagnostics.
		return assertionFailure{diagnostic: output}, true
	}
	return failure, located
}

func unsupportedAssertions(selector string) string {
	return fmt.Sprintf("no recognized behavior assertion in selected case %q", selector)
}
