package author_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/angelnicolasc/flinch/internal/author"
	"github.com/angelnicolasc/flinch/internal/config"
	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/runner"
)

type assertionFixture struct {
	name, tool, path, product, broken, fixed, oldTest, newTest, selector string
	support                                                              map[string]string
}

const testifyFixtureMod = `module example.com/assertionfixture

go 1.26

require (
	github.com/google/go-cmp v0.7.0
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
`

const testifyFixtureSum = `github.com/davecgh/go-spew v1.1.1 h1:vj9j/u1bqnvCEfJOwUhtlOARqs3+rkHYY13jYWTU97c=
github.com/davecgh/go-spew v1.1.1/go.mod h1:J7Y8YcW2NihsgmVo/mv3lAwl/skON4iLHjSsI+c5H38=
github.com/google/go-cmp v0.7.0 h1:wk8382ETsv4JYUZwIsn6YpYiWiBsYLSJiTsyBybVuN8=
github.com/google/go-cmp v0.7.0/go.mod h1:pXiqmnSA92OHEEa9HXL2W4E7lf9JzCmGVUdgjX3N/iU=
github.com/pmezard/go-difflib v1.0.0 h1:4DBwDE0NGyQoBHbLQYPwSUPoCMWR5BEzIk/f1lZbAQM=
github.com/pmezard/go-difflib v1.0.0/go.mod h1:iKH77koFhYxTK1pcRnkKkqfTogsbg7gZNVY4sRDYZ/4=
github.com/stretchr/testify v1.11.1 h1:7s2iGBzp5EwR7/aIZr8ao5+dra3wiQyKjjFuvgVKu7U=
github.com/stretchr/testify v1.11.1/go.mod h1:wZwfW3scLgRK+23gO65QZefKpKQRnfz6sD981Nm4B6U=
gopkg.in/yaml.v3 v3.0.1 h1:fxVm/GzAzEWqLHuvctI91KS9hhNmmWOoWu0XTYJS7CA=
gopkg.in/yaml.v3 v3.0.1/go.mod h1:K4uyk7z7BCEPqu6E+C64Yfv1cQ7kz7rIZviUmN+EgEM=
`

func goExample(name, imports, body string) assertionFixture {
	broken := `package delay
import "errors"
var ErrZero = errors.New("explicit zero")
func Delay(n int) int {
	if n <= 0 { return 8 }
	return n
}
func Validate(n int) error {
	if Delay(n) == 0 { return ErrZero }
	return nil
}
`
	neighbor := "func TestOmitted(t *testing.T) { if Delay(-1) != 8 { t.Fatal(\"expected the default\") } }\n"
	header := "package delay\nimport \"testing\"\n"
	return assertionFixture{name: name, tool: "go-test", path: "delay_test.go", product: "delay.go",
		broken: broken, fixed: strings.Replace(broken, "n <= 0", "n < 0", 1),
		oldTest: header + neighbor, newTest: header + imports + "\n" + neighbor + "\nfunc TestExplicitZero(t *testing.T) {\n" + body + "\n}\n", selector: "TestExplicitZero"}
}

func pythonExample(name, body string) assertionFixture {
	broken := "def delay(value):\n    return value or 8\n\ndef validate(value):\n    if delay(value) == 0:\n        raise ValueError('explicit zero')\n"
	before := "import pytest\nfrom delay import delay, validate\n\ndef test_omitted():\n    assert delay(None) == 8\n"
	return assertionFixture{name: name, tool: "pytest", product: "delay.py", path: "test_delay.py", broken: broken,
		fixed: strings.Replace(broken, "value or 8", "8 if value is None else value", 1), oldTest: before,
		newTest: before + "\ndef test_explicit_zero():\n" + body + "\n", selector: "test_delay.py::test_explicit_zero"}
}

func jsExample(tool, name, body string) assertionFixture {
	broken := `function delay(value) { return value || 8; }
function validate(value) { if (delay(value) === 0) throw new Error('explicit zero'); }
async function validateAsync(value) { validate(value); }
`
	before := ""
	if tool == "vitest" {
		broken += "export {delay, validate, validateAsync};\n"
		before = "import { test, expect } from 'vitest';\nimport { delay, validate, validateAsync } from './delay.js';\n"
	} else {
		broken += "module.exports = {delay, validate, validateAsync};\n"
		before = "const { delay, validate, validateAsync } = require('./delay.js');\n"
	}
	before += "test('omitted', () => { expect(delay(undefined)).toBe(8); });\n"
	return assertionFixture{name: name, tool: tool, product: "delay.js", path: "delay.test.js", broken: broken,
		fixed: strings.Replace(broken, "value || 8", "value ?? 8", 1), oldTest: before,
		newTest: before + "\ntest('explicit zero', async () => {\n" + body + "\n});\n", selector: "explicit zero"}
}

func recognizedExamples() []assertionFixture {
	examples := []assertionFixture{
		goExample("testify-require", `import "github.com/stretchr/testify/require"`, `require.Equal(t, 0, Delay(0))`),
		goExample("testify-assert", `import "github.com/stretchr/testify/assert"`, `assert.Equal(t, 0, Delay(0))`),
		goExample("reflect-deep-equal", `import "reflect"`, `got, want := []int{Delay(0)}, []int{0}
	if !reflect.DeepEqual(got, want) { t.Fatalf("got %v, want %v", got, want) }`),
		goExample("errors-is", `import "errors"`, `if !errors.Is(Validate(0), ErrZero) { t.Fatal("expected explicit-zero error") }`),
		goExample("two-comparisons", "", `if got := Delay(-1); got != 8 { t.Fatal("default changed") }
	if got := Delay(0); got != 0 { t.Fatalf("zero became %d", got) }`),
		goExample("cmp-diff", `import "github.com/google/go-cmp/cmp"`, `if diff := cmp.Diff(0, Delay(0)); diff != "" { t.Fatalf("unexpected delay (-want +got):\n%s", diff) }`),
		pythonExample("two-asserts", "    assert delay(None) == 8\n    assert delay(0) == 0"),
		pythonExample("raises", "    with pytest.raises(ValueError, match='explicit zero'):\n        validate(0)"),
		pythonExample("multiline", "    assert (\n        delay(0)\n        == 0\n    )"),
	}
	row := goExample("table-row", "", "")
	row.oldTest = `package delay
import "testing"
func TestDelay(t *testing.T) {
	for _, tc := range []struct{name string; n, want int}{
		{"omitted", -1, 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if Delay(-1) != 8 { t.Fatal("default changed") }
			if got := Delay(tc.n); got != tc.want { t.Fatalf("got %d, want %d", got, tc.want) }
		})
	}
}
`
	row.newTest = strings.Replace(row.oldTest, "{\"omitted\", -1, 8},", "{\"omitted\", -1, 8},\n\t\t{\"zero\", 0, 0},", 1)
	row.selector = "TestDelay/zero"
	examples = append(examples, row)
	for _, tool := range []string{"vitest", "jest"} {
		regex := jsExample(tool, "multiline-throws-regexp", `  expect(() => validate(-1)).toThrow(/config\/retry\)$/);
  expect(
    () => validate(0)
  ).toThrow(/config\/retry\)$/);`)
		// Both implementations reject negative delays. The regression concerns zero.
		regex.broken = strings.Replace(regex.broken, "delay(value) === 0", "value < 0 || delay(value) === 0", 1)
		regex.broken = strings.Replace(regex.broken, "explicit zero", "invalid delay (config/retry)", 1)
		regex.fixed = strings.Replace(regex.broken, "value || 8", "value ?? 8", 1)
		examples = append(examples,
			regex,
			jsExample(tool, "two-expects", "  expect(delay(undefined)).toBe(8);\n  expect(delay(0)).toBe(0);"),
			jsExample(tool, "multiline", "  expect(\n    delay(0)\n  ).toBe(0);"),
			jsExample(tool, "throws", "  expect(() => validate(0)).toThrow('explicit zero');"),
			jsExample(tool, "await-rejects", "  await expect(validateAsync(0)).rejects.toThrow('explicit zero');"),
		)
	}
	return examples
}

func (f assertionFixture) repository(t *testing.T) (repo, brokenRef string, cfg config.Config) {
	t.Helper()
	repo = t.TempDir()
	fixtureGit(t, repo, "init")
	cfg = config.Default()
	fixtureWrite(t, repo, ".gitignore", "node_modules\n__pycache__/\n.pytest_cache/\n")
	switch f.tool {
	case "go-test":
		fixtureWrite(t, repo, "go.mod", testifyFixtureMod)
		fixtureWrite(t, repo, "go.sum", testifyFixtureSum)
	case "pytest":
		if _, err := exec.LookPath("pytest"); err != nil {
			if os.Getenv("FLINCH_ASSERTION_DIAGNOSTICS") != "" {
				t.Fatal("pytest is required to capture real runner diagnostics")
			}
			t.Skip("pytest not installed")
		}
		fixtureWrite(t, repo, "pytest.ini", "[pytest]\npythonpath = .\n")
	case "jest", "vitest":
		bin := os.Getenv("FLINCH_REAL_" + strings.ToUpper(f.tool))
		if bin == "" {
			if os.Getenv("FLINCH_ASSERTION_DIAGNOSTICS") != "" {
				t.Fatalf("%s is required to capture real runner diagnostics", f.tool)
			}
			t.Skip("set FLINCH_REAL_" + strings.ToUpper(f.tool) + " to its installed binary")
		}
		if _, err := os.Stat(bin); err != nil {
			t.Fatal(err)
		}
		deps := filepath.Dir(filepath.Dir(bin))
		lock, err := os.ReadFile(filepath.Join(filepath.Dir(deps), "package-lock.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(deps, filepath.Join(repo, "node_modules")); err != nil {
			t.Fatal(err)
		}
		fixtureWrite(t, repo, "package-lock.json", string(lock))
		pkg := map[string]any{"private": true}
		if f.tool == "vitest" {
			pkg["type"] = "module"
		}
		data, err := json.Marshal(pkg)
		if err != nil {
			t.Fatal(err)
		}
		fixtureWrite(t, repo, "package.json", string(data)+"\n")
		cfg.Tests.Runners = []config.TestRunner{{Kind: f.tool}}
	}
	for name, data := range f.support {
		fixtureWrite(t, repo, name, data)
	}
	fixtureWrite(t, repo, f.product, f.broken)
	fixtureWrite(t, repo, f.path, f.oldTest)
	fixtureCommit(t, repo, "seed behavior and neighbor", ".")
	brokenRef = strings.TrimSpace(fixtureGit(t, repo, "rev-parse", "HEAD"))
	fixtureWrite(t, repo, f.path, f.newTest)
	return repo, brokenRef, cfg
}

func (f assertionFixture) capture(t *testing.T, repo string) {
	t.Helper()
	dir := os.Getenv("FLINCH_ASSERTION_DIAGNOSTICS")
	if dir == "" {
		return
	}
	ctx := context.Background()
	var run runner.TestRunResult
	var err error
	targets := []string{f.path}
	switch f.tool {
	case "go-test":
		run, err = runner.RunGoTest(ctx, repo, []string{"."}, f.selector, time.Minute)
	case "pytest":
		run, err = runner.RunPytest(ctx, repo, targets, f.selector, time.Minute)
	case "vitest":
		run, err = runner.RunVitest(ctx, repo, repo, targets, f.selector, time.Minute)
	case "jest":
		run, err = runner.RunJest(ctx, repo, repo, targets, f.selector, time.Minute)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var source strings.Builder
	for i, line := range strings.Split(f.newTest, "\n") {
		fmt.Fprintf(&source, "%03d %s\n", i+1, line)
	}
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, f.tool+"-"+f.name+".txt")
	if err := os.WriteFile(path, append([]byte(source.String()+"\n"), data...), 0o644); err != nil {
		t.Fatal(err)
	}
	hasFailed := false
	for _, c := range run.Cases {
		if c.Outcome == runner.OutcomeFail {
			hasFailed = true
		}
	}
	if run.ExitCode != 1 || !hasFailed {
		t.Fatalf("fixture did not reproduce its ordinary failure; inspect %s", path)
	}
}

func TestReuseRecognizesRealAssertionForms(t *testing.T) {
	for _, f := range recognizedExamples() {
		t.Run(f.tool+"/"+f.name, func(t *testing.T) {
			repo, broken, cfg := f.repository(t)
			f.capture(t, repo)
			fixtureWrite(t, repo, f.product, f.fixed)
			fixed, err := gitutil.CaptureWorkingTreeSnapshot(context.Background(), repo, "")
			if err != nil {
				t.Fatal(err)
			}
			out, err := author.ReuseFirst(context.Background(), repo, cfg, broken, fixed, author.AuthoringBudgetCap)
			if err != nil || out.Status != author.ReuseProven || out.Reflex == nil || out.Report == nil {
				t.Fatalf("expected proven %s, got %s: %s, %v", f.selector, out.Status, out.Reason, err)
			}
			if out.Reflex.Artifact.Selector != f.selector || out.Neighbor == "" || len(out.Report.AllowedResults) == 0 || !out.Report.AllowedResults[0].Passed {
				t.Fatalf("unexpected selector or absent permitted neighbor: %+v", out)
			}
		})
	}
}
