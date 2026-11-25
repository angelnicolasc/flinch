package author_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/angelnicolasc/flinch/internal/cli"
	"github.com/angelnicolasc/flinch/internal/hook"
	"github.com/angelnicolasc/flinch/internal/testutil"
)

func TestMissingAssertionReachesDoctorFromBothStopPaths(t *testing.T) {
	if _, err := exec.LookPath("pytest"); err != nil {
		t.Skip("pytest not installed")
	}
	for _, path := range []string{"existing", "authored"} {
		t.Run(path, func(t *testing.T) {
			repo := t.TempDir()
			testutil.HookProfile(t, repo)
			fixtureGit(t, repo, "init")
			const baseTests = "from value import value\nimport pytest\n\ndef test_neighbor():\n    assert value >= 0\n"
			fixtureWrite(t, repo, "value.py", "value = 0\n")
			fixtureWrite(t, repo, "test_value.py", baseTests)
			fixtureCommit(t, repo, "seed value and neighbor", ".")
			const session = "correction"
			start := func(prompt string) { fixtureEvent(t, repo, hook.HostClaude, session, "UserPromptSubmit", prompt, "") }
			stop := func(last string) hook.Response {
				return fixtureEvent(t, repo, hook.HostClaude, session, "Stop", "", last)
			}
			start("Set the value.")
			fixtureWrite(t, repo, "value.py", "value = 8\n")
			stop("Set the value.")
			start("Preserve the explicit zero.")
			fixtureWrite(t, repo, "value.py", "value = 0  # preserve explicit zero\n")
			if path == "authored" {
				var request struct{ Reason string }
				if err := json.Unmarshal(stop("Preserved zero.").Stdout, &request); err != nil {
					t.Fatal(err)
				}
				for _, part := range []string{"new case", "assertion in its own body", "neighboring case"} {
					if !strings.Contains(request.Reason, part) {
						t.Errorf("authoring prompt omitted %q: %s", part, request.Reason)
					}
				}
			}
			const customCases = "\ndef test_first():\n    if value:\n        pytest.fail('zero changed')\n\ndef test_second():\n    if value:\n        pytest.fail('zero changed again')\n"
			fixtureWrite(t, repo, "test_value.py", baseTests+customCases)
			stop("Added two cases.")
			stop("Added two cases.")
			events := fixtureAuthoringEvents(t, repo)
			if len(events) == 0 {
				t.Fatal("no authoring events")
			}
			last := events[len(events)-1]
			data, err := json.Marshal(last)
			if err != nil {
				t.Fatal(err)
			}
			if last.Kind != "inconclusive_closed" || !bytes.Contains(data, []byte(`"no_recognized_assertion":2`)) {
				t.Errorf("expected the closing event to count both candidates: %s", data)
			}
			var stdout, stderr bytes.Buffer
			args := []string{"doctor", "--repo", repo}
			if code := cli.Run(context.Background(), append(args, "--json"), &stdout, &stderr); code != 0 {
				t.Fatal(stderr.String())
			}
			var report map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &report); err != nil || report["no_recognized_assertion"] != float64(2) {
				t.Errorf("doctor count = %v, error = %v", report["no_recognized_assertion"], err)
			}
			stdout.Reset()
			if code := cli.Run(context.Background(), args, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "(no_recognized_assertion): 2") {
				t.Errorf("doctor output: %s %s", stdout.String(), stderr.String())
			}
		})
	}
}
