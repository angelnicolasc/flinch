// Command requiredtests reads `go test -json` output on stdin, prints a summary
// of failed and skipped tests, and checks that every integration test the
// list requires for this operating system ran and passed. A test that is
// missing, skipped, renamed or failing makes the command exit 1, so a green CI
// run means the integrations Flinch promises actually ran.
//
// Usage: go test -json ./... | go run ./scripts/requiredtests -list FILE -os Linux
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

type event struct {
	Action  string
	Package string
	Test    string
	Output  string
}

type requirement struct {
	pkg, test string
}

func main() {
	list := flag.String("list", "", "file of required tests: <os|all> <package> <Test[/subtest]> per line")
	osName := flag.String("os", "", "the runner's operating system, as RUNNER_OS spells it")
	flag.Parse()
	required, err := readList(*list, *osName)
	if err == nil {
		err = check(os.Stdin, os.Stdout, required)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "requiredtests:", err)
		os.Exit(1)
	}
}

func readList(path, osName string) ([]requirement, error) {
	if path == "" || osName == "" {
		return nil, errors.New("both -list and -os are required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []requirement
	for n, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, fmt.Errorf("%s:%d: want <os|all> <package> <test>", path, n+1)
		}
		if fields[0] == "all" || strings.EqualFold(fields[0], osName) {
			out = append(out, requirement{fields[1], fields[2]})
		}
	}
	return out, nil
}

// check consumes the event stream, prints the summary and verifies the list.
func check(r io.Reader, w io.Writer, required []requirement) error {
	results := map[requirement]string{}
	outputs := map[requirement]*strings.Builder{}
	var failedPackages []string
	lines := bufio.NewScanner(r)
	lines.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for lines.Scan() {
		var ev event
		if json.Unmarshal(lines.Bytes(), &ev) != nil {
			continue
		}
		key := requirement{ev.Package, ev.Test}
		switch ev.Action {
		case "output":
			if outputs[key] == nil {
				outputs[key] = &strings.Builder{}
			}
			outputs[key].WriteString(ev.Output)
		case "pass", "fail", "skip":
			if ev.Test != "" {
				results[key] = ev.Action
			} else if ev.Action == "fail" {
				failedPackages = append(failedPackages, ev.Package)
			}
		}
	}
	if err := lines.Err(); err != nil {
		return fmt.Errorf("read go test events: %w", err)
	}

	counts := map[string]int{}
	var failed, skipped []requirement
	for key, action := range results {
		counts[action]++
		switch action {
		case "fail":
			failed = append(failed, key)
		case "skip":
			skipped = append(skipped, key)
		}
	}
	sortKeys(failed)
	sortKeys(skipped)
	fmt.Fprintf(w, "tests: %d passed, %d failed, %d skipped\n", counts["pass"], counts["fail"], counts["skip"])
	for _, key := range failed {
		fmt.Fprintf(w, "\nFAIL %s %s\n%s", key.pkg, key.test, text(outputs[key]))
	}
	for _, pkg := range failedPackages {
		fmt.Fprintf(w, "\nFAIL package %s\n%s", pkg, text(outputs[requirement{pkg, ""}]))
	}
	if len(skipped) > 0 {
		fmt.Fprintln(w, "\nskipped:")
		for _, key := range skipped {
			fmt.Fprintf(w, "  %s %s: %s\n", key.pkg, key.test, skipReason(outputs[key]))
		}
	}

	var problems []string
	for _, req := range required {
		action, ok := "", false
		for key, got := range results {
			if key.test == req.test && (key.pkg == req.pkg || strings.HasSuffix(key.pkg, "/"+req.pkg)) {
				action, ok = got, true
				break
			}
		}
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("%s %s did not run", req.pkg, req.test))
		case action != "pass":
			problems = append(problems, fmt.Sprintf("%s %s: %s", req.pkg, req.test, action))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("required integration tests did not pass:\n  %s", strings.Join(problems, "\n  "))
	}
	fmt.Fprintf(w, "\nall %d required integration tests passed\n", len(required))
	return nil
}

func sortKeys(keys []requirement) {
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].pkg != keys[j].pkg {
			return keys[i].pkg < keys[j].pkg
		}
		return keys[i].test < keys[j].test
	})
}

func text(b *strings.Builder) string {
	if b == nil {
		return ""
	}
	return b.String()
}

// skipReason returns the last line the test printed before skipping.
func skipReason(b *strings.Builder) string {
	last := ""
	for _, line := range strings.Split(text(b), "\n") {
		if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "---") && !strings.HasPrefix(t, "===") {
			last = t
		}
	}
	return last
}
