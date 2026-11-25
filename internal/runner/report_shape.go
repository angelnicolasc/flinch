package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrUnreadableReport marks a static run whose tool claimed to finish but
// whose output is not the report Flinch can read. Callers treat it like any
// other failed run: a proof is inconclusive and enforcement fails open,
// instead of reading the empty result as a clean scan.
var ErrUnreadableReport = errors.New("the tool's report could not be read")

// checkReportShape verifies the JSON report of a tool whose exit code says it
// completed its analysis. It checks the structure each parser relies on, so a
// report whose format changed upstream can't pass as "no findings".
func checkReportShape(tool string, res LintRunResult) error {
	if res.TimedOut || (res.ExitCode != 0 && res.ExitCode != 1) {
		return nil
	}
	data := bytes.TrimSpace([]byte(res.Stdout))
	var err error
	switch tool {
	case "ruff":
		err = requireArrayOf(data, "code", "filename", "location")
	case "eslint":
		err = requireArrayOf(data, "filePath", "messages")
	case "golangci-lint":
		var report map[string]json.RawMessage
		if err = json.Unmarshal(data, &report); err == nil {
			if _, ok := report["Issues"]; !ok {
				err = errors.New(`the report has no "Issues" field`)
			}
		}
	default:
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: %w: %v", tool, ErrUnreadableReport, err)
	}
	return nil
}

func requireArrayOf(data []byte, keys ...string) error {
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		return err
	}
	for i, item := range items {
		for _, key := range keys {
			if _, ok := item[key]; !ok {
				return fmt.Errorf("entry %d has no %q field", i, key)
			}
		}
	}
	return nil
}
