package prove

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/angelnicolasc/flinch/internal/reflex"
)

func TestRecognizedAssertionLinesStayInSelectedCase(t *testing.T) {
	cases := []struct {
		tool, path, name, source string
		want                     []int
	}{
		{"go-test", "delay_test.go", "TestDelay/zero", `package sample
import ("testing"; "reflect"; "errors"; check "github.com/stretchr/testify/require")
func TestDelay(t *testing.T) {
 t.Run("other",func(t *testing.T){ check.Equal(t,0,1) })
 for _,row:=range rows {
  t.Run(row.name,func(t *testing.T){
   if err:=load(); err!=nil {t.Fatal(err)}
   if !reflect.DeepEqual(row.got,row.want) {t.Fatal("different")}
   if !errors.Is(row.err,expected) {t.Fatal("wrong error")}
   check.Equal(t,row.want,row.got)
   if diff:=cmp.Diff(row.want,row.got); diff!="" {t.Fatal(diff)}
  })
 }
}`, []int{8, 9, 10, 11}},
		{"pytest", "test_sample.py", "test_sample.py::TestDelay::test_zero[0]", `class Other:
 def test_zero(self):
  assert False
class TestDelay:
 def test_zero(self):
  # assert fixture()
  def helper():
   assert False
  assert (
   delay(0)
   == 0
  )
  with pytest.raises(ValueError):
   validate(0)
  assert delay(None) == 8
`, []int{9, 10, 11, 12, 13, 15}},
		{"jest", "delay.test.ts", "outer › zero", `describe('outer', () => {
test.each([
 {name:'zero'}, {name:'missing'},
])('$name', async row => {
  // expect(wrong()).toEqual(0)
  expect(
    delay(row.value)
  ).toBe(0)
  expect(() => validate()).toThrow()
  await expect(promise)
    .rejects
    .toThrow()
})
})
test('zero', () => { expect(5).toBe(5) })`, []int{6, 8, 9, 10, 11, 12}},
	}
	for _, c := range cases {
		t.Run(c.tool, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, c.path), []byte(c.source), 0o644); err != nil {
				t.Fatal(err)
			}
			got := findAssertionSites(root, CandidateSpec{Artifact: reflex.Artifact{Tool: c.tool, Path: c.path, Selector: c.name}})
			if !slices.Equal(got.lines, c.want) {
				t.Fatalf("lines %v, want %v", got.lines, c.want)
			}
		})
	}
}

func TestAssertionFailureUsesOriginAndStableDiagnostic(t *testing.T) {
	cases := []struct {
		tool, path, text string
		want             bool
	}{
		{"go-test", "pkg/delay_test.go", "<tree>/pkg/delay_test.go:6: got 8, want 0", true},
		{"go-test", "pkg/delay_test.go", "<tree>/other/delay_test.go:6: fixture failed", false},
		{"go-test", "pkg/delay_test.go", "<tree>/pkg/delay_test.go:6:\nError Trace: <tree>/helpers/check.go:6\n<tree>/pkg/delay_test.go:6", false},
		{"pytest", "tests/test_delay.py", "tests/test_delay.py:6: AssertionError", true},
		{"pytest", "tests/test_delay.py", "Failed: DID NOT RAISE <class 'ValueError'>\ntests/test_delay.py:6: Failed", true},
		{"pytest", "tests/test_delay.py", "tests/test_delay.py:6:\nfixture.py:3: AssertionError", false},
		{"jest", "tests/delay.test.ts", "Error: expect(received).toBe(expected)\nat Object.toBe (<tree>/tests/delay.test.ts:7:17)", true},
		{"vitest", "tests/delay.test.ts", "AssertionError: setup\nat fixture (<tree>/src/fixture.ts:8:1)\nat <tree>/tests/delay.test.ts:7:7", false},
		{"jest", "tests/delay.test.ts", "AssertionError: setup\nat helper (/deps/node_modules/custom/helpers.js:8:1)\nat <tree>/tests/delay.test.ts:7:7", false},
		// .rejects as Vitest 3.2.4 and Vitest 5.1.1 report it. Vitest 5 bundles
		// @vitest/expect into its own chunks, next to the runner.
		{"vitest", "tests/delay.test.ts", "Error: promise resolved \"undefined\" instead of rejecting\n" +
			"at _Assertion.__VITEST_REJECTS__ (file:///deps/node_modules/@vitest/expect/dist/index.js:1610:17)\n" +
			"at _Assertion.propertyGetter (file:///deps/node_modules/chai/index.js:1557:27)\nat Reflect.get (<anonymous>)\n" +
			"at Object.proxyGetter [as get] (file:///deps/node_modules/chai/index.js:1645:22)\nat <tree>/tests/delay.test.ts:7:33\n" +
			"at file:///deps/node_modules/@vitest/runner/dist/chunk-hooks.js:155:11", true},
		{"vitest", "tests/delay.test.ts", "Error: promise resolved \"undefined\" instead of rejecting\n" +
			"at _Assertion.__VITEST_REJECTS__ (file:///deps/node_modules/vitest/dist/chunks/index.DNcttN5a.js:2179:33)\n" +
			"at _Assertion.propertyGetter (file:///deps/node_modules/chai/index.js:1607:27)\nat Reflect.get (<anonymous>)\n" +
			"at Object.proxyGetter [as get] (file:///deps/node_modules/chai/index.js:1696:22)\nat <tree>/tests/delay.test.ts:7:33\n" +
			"at file:///deps/node_modules/vitest/dist/chunks/run.zBhXlcZQ.js:1695:35", true},
		// The same words thrown by the test itself, or a Vitest 5 frame that
		// isn't .resolves or .rejects, aren't a failed assertion.
		{"vitest", "tests/delay.test.ts", "Error: promise resolved \"undefined\" instead of rejecting\nat helper (<tree>/tests/delay.test.ts:7:33)", false},
		{"vitest", "tests/delay.test.ts", "Error: promise resolved \"undefined\" instead of rejecting\n" +
			"at file:///deps/node_modules/vitest/dist/chunks/run.zBhXlcZQ.js:1695:35\nat <tree>/tests/delay.test.ts:7:33", false},
	}
	for _, c := range cases {
		s := assertionSites{tool: c.tool, path: c.path, lines: []int{6, 7}}
		if _, ok := s.failure(c.text); ok != c.want {
			t.Errorf("%s: failure(%q) recognized=%v", c.tool, c.text, ok)
		}
	}
	s := assertionSites{tool: "jest", path: "case.test.js", lines: []int{1, 2}}
	one, _ := s.failure("Error: expect(received).toBe(expected)\nReceived: 8\nat <tree>/case.test.js:1:20")
	two, _ := s.failure("Error: expect(received).toBe(expected)\nReceived: 8\nat <tree>/case.test.js:2:20")
	three, _ := s.failure("Error: expect(received).toBe(expected)\nReceived: 9\nat <tree>/case.test.js:1:20")
	if one == two || one == three || one.lines != "1" {
		t.Fatal("replays must distinguish both source location and diagnostic")
	}
}

func TestGoFailureRequiresTestifyTraceAndIgnoresSeparateLogSite(t *testing.T) {
	s := assertionSites{tool: "go-test", path: "pkg/sample_test.go", lines: []int{7}, testify: []int{7}, logs: []int{6}}
	const valid = "<tree>/pkg/sample_test.go:7:\nError Trace: <tree>/pkg/sample_test.go:7\nError: Not equal"
	withLog, ok := s.failure("<tree>/pkg/sample_test.go:6: started at 12345\n" + valid)
	withoutLog, ok2 := s.failure(valid)
	if !ok || !ok2 || withLog != withoutLog {
		t.Fatal("preceding log changed assertion evidence")
	}
	for _, invalid := range []string{
		"<tree>/pkg/sample_test.go:7: setup failed",
		"<tree>/pkg/sample_test.go:7:\nError Trace: <tree>/fixture/helper.go:7\n<tree>/pkg/sample_test.go:7",
		"<tree>/pkg/sample_test.go:6: started",
	} {
		if _, ok := s.failure(invalid); ok {
			t.Fatalf("credited helper or log: %q", invalid)
		}
	}
}

func TestGoAssertionSitesNestedSubtests(t *testing.T) {
	body := []byte(`package sample
import "testing"
func TestOuter(t *testing.T) {
 t.Run("left",func(t *testing.T) {
  t.Run("row",func(t *testing.T) {
   if got!=1 { t.Fatal("left failure") }
  })
 })
 t.Run("right",func(t *testing.T) {
  t.Run("row",func(t *testing.T) {
   if got!=2 { t.Fatal("right failure") }
  })
 })
}`)
	if got := goAssertionLines(body, "TestOuter/left/row").lines; !slices.Equal(got, []int{6}) {
		t.Fatalf("left row contains %v", got)
	}
	if got := goAssertionLines(body, "TestOuter/right/row").lines; !slices.Equal(got, []int{11}) {
		t.Fatalf("right row contains %v", got)
	}
	if got := goAssertionLines(body, "TestOuter").lines; len(got) != 0 {
		t.Fatalf("parent borrowed child assertions: %v", got)
	}
}

func TestJSThrowsRegexAndNestedHelper(t *testing.T) {
	body := `test('selected', () => {
  function helper() { expect(wrong()).toEqual(7) }
  const callback = () => { expect(wrong()).toEqual(8) }
  const also = () => expect(wrong()).toEqual(9);
  expect(() => validate(0)).toThrow(/explicit[ -]zero(?:\)\/detail)?/);
})
test('elsewhere', () => { expect(false).toBe(true) })`
	for _, selector := range []string{"selected", "elsewhere"} {
		want := 5
		if selector == "elsewhere" {
			want = 7
		}
		got := jsAssertionLines(body, selector)
		slices.Sort(got)
		if got = slices.Compact(got); !slices.Equal(got, []int{want}) {
			t.Errorf("%s sites %v, want [%d]", selector, got, want)
		}
	}
}
