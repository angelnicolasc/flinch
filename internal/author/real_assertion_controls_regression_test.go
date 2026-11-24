package author_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/prove"
	"github.com/angelnicolasc/flinch/internal/reflex"
)

func TestRealAssertionControlsAvoidFalseProof(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "observed-broken")
	changeLine := goExample("different-lines", `import "os"`, fmt.Sprintf(`if Delay(0) == 0 { return }
	if _, err := os.Stat(%[1]q); os.IsNotExist(err) {
		if err := os.WriteFile(%[1]q, []byte("seen"), 0600); err != nil { t.Fatal(err) }
		if got := Delay(0); got != 0 { t.Fatal("explicit zero was lost") }
	} else {
		if err := os.WriteFile(%[2]q, []byte("second"), 0600); err != nil { t.Fatal(err) }
		if got := Delay(0); got != 0 { t.Fatal("explicit zero was lost") }
	}`, marker, marker+"-second"))
	changeMessage := goExample("different-diagnostic-at-same-line", `import "os"`, fmt.Sprintf(`if Delay(0) == 0 { return }
	attempt := "first"
	if _, err := os.Stat(%[1]q); err == nil {
		attempt = "second"
		if err := os.WriteFile(%[2]q, []byte("second"), 0600); err != nil { t.Fatal(err) }
	}
	if err := os.WriteFile(%[1]q, []byte("seen"), 0600); err != nil { t.Fatal(err) }
	if got := Delay(0); got != 0 { t.Fatalf("wrong zero value on %%s replay", attempt) }`, marker+"-diagnostic", marker+"-second-diagnostic"))

	setup := goExample("setup-error", "", `if err := ValidateFixture(); err != nil { t.Fatal(err) }
	if got := Delay(-1); got != 8 { t.Fatal("default changed") }`)
	setup.support = map[string]string{"setup.go": "package delay\nimport \"errors\"\nfunc ValidateFixture() error { if Delay(0) != 0 { return errors.New(\"fixture is unavailable\") }; return nil }\n"}
	empty := setup
	empty.name = "no-recognized-assertion"
	empty.newTest = strings.Replace(empty.newTest, "if got := Delay(-1); got != 8 { t.Fatal(\"default changed\") }", "", 1)

	pyFixture := pythonExample("fixture-error", "    assert delay(None) == 8\n    assert delay(0) == 0")
	pyFixture.newTest = strings.Replace(pyFixture.newTest, "def test_explicit_zero():", "def test_explicit_zero(behavior_fixture):", 1)
	pyFixture.support = map[string]string{"conftest.py": "import pytest\nfrom delay import delay\n\n@pytest.fixture\ndef behavior_fixture():\n    assert delay(0) == 0, 'fixture cannot initialize'\n"}
	pyHelper := pythonExample("external-helper", "    from assertion_helper import checked_delay\n    assert checked_delay() == 0")
	pyHelper.support = map[string]string{"assertion_helper.py": "from delay import delay\n\ndef checked_delay():\n    assert delay(0) == 0, 'failure inside helper'\n    return 0\n"}
	// testing.T.Helper makes Go attribute the fatal failure to the caller, even
	// though argument evaluation stops before require.Equal can run.
	goHelper := goExample("testify-argument-helper", `import "github.com/stretchr/testify/require"`, `require.Equal(t, 0, externalHelper(t))`)
	goHelper.support = map[string]string{"fixture_helper_test.go": `package delay
import "testing"
func externalHelper(t *testing.T) int {
	t.Helper()
	if Delay(0) != 0 { t.Fatal("fixture failed before the comparison") }
	return 0
}
`}
	examples := []assertionFixture{changeLine, changeMessage, setup, empty, goHelper, pyFixture, pyHelper}
	for _, tool := range []string{"vitest", "jest"} {
		f := jsExample(tool, "external-helper", "  expect(checkedDelay()).toBe(0);")
		helper := ""
		if tool == "vitest" {
			f.newTest = "import { checkedDelay } from './assertion-helper.js';\n" + f.newTest
			helper = "import { expect } from 'vitest';\nimport { delay } from './delay.js';\nexport function checkedDelay() { expect(delay(0)).toBe(0); return 0; }\n"
		} else {
			f.newTest = "const { checkedDelay } = require('./assertion-helper.js');\n" + f.newTest
			helper = "const { delay } = require('./delay.js');\nexports.checkedDelay = function () { expect(delay(0)).toBe(0); return 0; };\n"
		}
		f.support = map[string]string{"assertion-helper.js": helper}
		examples = append(examples, f)
	}
	for _, f := range examples {
		t.Run(f.tool+"/"+f.name, func(t *testing.T) {
			repo, broken, _ := f.repository(t)
			if f.name == "testify-argument-helper" {
				f.capture(t, repo)
			}
			fixtureWrite(t, repo, f.product, f.fixed)
			fixed, err := gitutil.CaptureWorkingTreeSnapshot(context.Background(), repo, "")
			if err != nil {
				t.Fatal(err)
			}
			neighbor := "TestOmitted"
			switch f.tool {
			case "pytest":
				neighbor = "test_delay.py::test_omitted"
			case "jest", "vitest":
				neighbor = "omitted"
			}
			spec := prove.CandidateSpec{ID: "assertion-control", Rung: reflex.RungTest, BrokenRef: broken, FixedRef: fixed,
				Artifact: reflex.Artifact{Tool: f.tool, Path: f.path, Selector: f.selector}, NeighborSelector: neighbor}
			report, err := prove.Verify(context.Background(), repo, spec, prove.Options{})
			var secondReplay string
			switch f.name {
			case "different-lines":
				secondReplay = marker + "-second"
			case "different-diagnostic-at-same-line":
				secondReplay = marker + "-second-diagnostic"
			}
			if secondReplay != "" {
				data, readErr := os.ReadFile(secondReplay)
				if readErr != nil || string(data) != "second" {
					t.Errorf("the second broken execution did not reach its failing assertion: %v", readErr)
				}
			}
			if err != nil || report.Verdict != prove.VerdictInconclusive {
				t.Fatalf("unexpected proof for control: %+v, %v", report, err)
			}
			wantReason := "unexpected_failure_signal"
			switch f.name {
			case "no-recognized-assertion":
				wantReason = "no_recognized_assertion"
			case "fixture-error":
				wantReason = "setup_error"
			}
			if string(report.Reason) != wantReason {
				t.Fatalf("%s: expected %s, got %s (%s)", f.name, wantReason, report.Reason, report.ActionableError)
			}
		})
	}
}
