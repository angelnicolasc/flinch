package author_test

import (
	"strings"
	"testing"
)

func TestJavaScriptRegexpInTestFile(t *testing.T) {
	const tests = `test('zero', () => { expect(delay(0)).toBe(0); });
test('omitted', () => { expect(delay(null)).toBe(8); });
`
	for _, tc := range []struct{ name, predicate string }{
		{"logical_and", `return a && /\(/.test(s);`},
		{"statement", `a(); /[^)]+/.test(s);`},
		{"logical_or", `return a || /don't/.test(s);`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			helper := "function matches(a, s) { " + tc.predicate + " }\n"
			t.Run("edit_case", func(t *testing.T) {
				before := helper + tests
				editProtectedCase(t, "vitest", "delay.test.js", "zero",
					before, strings.Replace(before, "toBe(0)", "toBe(8)", 1), true, true)
			})
			t.Run("add_helper", func(t *testing.T) {
				editProtectedCase(t, "vitest", "delay.test.js", "zero", tests, helper+tests, false, false)
			})
		})
	}
}
