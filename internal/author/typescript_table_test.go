package author_test

import (
	"strings"
	"testing"
)

func TestTypeScriptTableTypeArguments(t *testing.T) {
	const table = `test.each<TYPE>([
  { name: 'zero', values: { delay: 0 }, read: () => 0 },
  { name: 'omitted', values: { delay: 8 }, read: () => 8 },
])('$name', ({ values, read }) => { expect(read()).toBe(values.delay); });
`
	for _, tc := range []struct{ name, typ string }{
		{"nested", "{ name: string, values: Record<string, number>, read: Function }"},
		{"arrow", "{ name: string, values: { delay: number }, read: () => number }"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := strings.Replace(table, "TYPE", tc.typ, 1)
			t.Run("edit_row", func(t *testing.T) {
				editProtectedCase(t, "vitest", "delay.test.ts", "zero",
					source, strings.Replace(source, "delay: 0", "delay: 1", 1), true, true)
			})
			t.Run("add_helper", func(t *testing.T) {
				editProtectedCase(t, "vitest", "delay.test.ts", "zero", source, source+"const other = 1;\n", false, false)
			})
		})
	}
}
