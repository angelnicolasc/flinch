package runner

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type junitTestSuites struct {
	XMLName xml.Name         `xml:"testsuites"`
	Suites  []junitTestSuite `xml:"testsuite"`
}

type junitTestSuite struct {
	XMLName xml.Name        `xml:"testsuite"`
	Name    string          `xml:"name,attr"`
	Cases   []junitTestCase `xml:"testcase"`
}

type junitTestCase struct {
	ClassName string        `xml:"classname,attr"`
	Name      string        `xml:"name,attr"`
	Time      string        `xml:"time,attr"`
	Failure   *junitMessage `xml:"failure"`
	Error     *junitMessage `xml:"error"`
	Skipped   *junitMessage `xml:"skipped"`
}

type junitMessage struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Body    string `xml:",chardata"`
}

// RunPytest executes pytest with `--continue-on-collection-errors` and JUnit XML output
// so collection/import errors are reliably separated from test assertion failures and skips (§5.2).
func RunPytest(ctx context.Context, dir string, targets []string, selector string, timeout time.Duration) (TestRunResult, error) {
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	tmpDir, err := os.MkdirTemp("", "flinch-pytest-*")
	if err != nil {
		return TestRunResult{}, err
	}
	defer os.RemoveAll(tmpDir)
	xmlPath := filepath.Join(tmpDir, "junit.xml")

	args := []string{"--continue-on-collection-errors", "--junitxml=" + xmlPath}
	if selector != "" {
		if strings.Contains(selector, "::") {
			args = append(args, selector)
		} else {
			args = append(args, "-k", selector)
			args = append(args, targets...)
		}
	} else {
		args = append(args, targets...)
	}

	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(tctx, "pytest", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	elapsedMS := time.Since(start).Milliseconds()

	pyInfo := ProbeTool(ctx, []string{"pytest"}, "--version")
	res := TestRunResult{
		Runner:        "pytest",
		RunnerVersion: pyInfo.Version,
		Command:       "pytest " + strings.Join(args, " "),
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

	if data, readErr := os.ReadFile(xmlPath); readErr == nil && len(bytes.TrimSpace(data)) > 0 {
		if parseErr := ParseJUnitXML(data, &res); parseErr != nil {
			return res, fmt.Errorf("parse pytest junit xml: %w", parseErr)
		}
	} else if res.ExitCode == 2 {
		// Pytest exit code 2 indicates collection/usage error before JUnit emission.
		res.CompileOnly = true
		res.BuildOutput = strings.TrimSpace(res.RawOutput)
	}
	return res, nil
}

// ParseJUnitXML decodes either <testsuites> or standalone <testsuite> JUnit XML into res.
func ParseJUnitXML(data []byte, res *TestRunResult) error {
	var suites []junitTestSuite
	var wrapped junitTestSuites
	if err := xml.Unmarshal(data, &wrapped); err == nil && len(wrapped.Suites) > 0 {
		suites = wrapped.Suites
	} else {
		var single junitTestSuite
		if err2 := xml.Unmarshal(data, &single); err2 != nil {
			return err2
		}
		suites = []junitTestSuite{single}
	}

	for _, suite := range suites {
		for _, tc := range suite.Cases {
			elapsed, _ := strconv.ParseFloat(tc.Time, 64)
			fullName := tc.Name
			if tc.ClassName != "" && !strings.HasPrefix(tc.Name, tc.ClassName) {
				fullName = tc.ClassName + "::" + tc.Name
			}
			item := TestCaseResult{
				Package:    suite.Name,
				Name:       fullName,
				ElapsedSec: elapsed,
				Outcome:    OutcomePass,
			}
			switch {
			case tc.Skipped != nil:
				item.Outcome = OutcomeSkip
				item.Output = strings.TrimSpace(tc.Skipped.Message + "\n" + tc.Skipped.Body)
			case tc.Failure != nil:
				item.Outcome = OutcomeFail
				item.Output = strings.TrimSpace(tc.Failure.Message + "\n" + tc.Failure.Body)
			case tc.Error != nil:
				combined := strings.TrimSpace(tc.Error.Message + "\n" + tc.Error.Body)
				if isCollectionOrImportError(tc.Error.Message, combined) {
					res.CompileOnly = true
					res.BuildOutput = strings.TrimSpace(res.BuildOutput + "\n" + combined)
				}
				item.Outcome = OutcomeError
				item.Output = combined
			}
			res.Cases = append(res.Cases, item)
		}
	}
	return nil
}

func isCollectionOrImportError(msg, body string) bool {
	lowMsg := strings.ToLower(msg)
	lowBody := strings.ToLower(body)
	if strings.Contains(lowMsg, "collection failure") {
		return true
	}
	for _, marker := range []string{"importerror", "modulenotfounderror", "syntaxerror", "indentationerror"} {
		if strings.Contains(lowBody, marker) {
			return true
		}
	}
	return false
}
