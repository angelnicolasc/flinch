package author

import (
	"strings"
	"testing"
)

func TestGoCandidateIgnoresUnrelatedRunMethods(t *testing.T) {
	const fixed = `package cli_test
import ("bytes"; "context"; "testing"; "example.org/tool/cli")
func TestStatus(t *testing.T) {
	codes := map[string]int{"ok": 0}
	var out, errOut bytes.Buffer
	CALL
	if out.String() != "ready" { t.Fatal("wrong status") }
}
`
	for _, call := range []string{
		`if cli.Run(context.Background(), []string{"status"}, &out, &errOut) != codes["ok"] { t.Fatal("status failed") }`,
		`if cli.Run("status", codes["ok"]) != 0 { t.Fatal("status failed") }`,
	} {
		after := strings.Replace(fixed, "CALL", call, 1)
		before := strings.Replace(after, `!= "ready"`, `!= "pending"`, 1)
		got := extractGoTestCandidates("status_test.go", "**", []byte(before), []byte(after))
		if len(got) != 1 || got[0].Selector != "TestStatus" {
			t.Errorf("expected the enclosing TestStatus for %s, got %+v", call, got)
		}
	}
}
