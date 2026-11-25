package check

import (
	"strings"
	"testing"
)

func TestLegacyJavaScriptLeafBindsToUniqueTrustedSuite(t *testing.T) {
	const original = `describe('retry', () => {
  it('zero', () => { expect(delay(0)).toBe(0); });
});`
	const sibling = `describe('timer', () => {
  it('zero', () => { expect(timer(0)).toBe(8); });
});
`
	for _, tc := range []struct {
		name, trusted, current string
		intact                 bool
	}{
		{"added_sibling", original, sibling + original, true},
		{"edited_protected", original, sibling + strings.Replace(original, "toBe(0)", "toBe(8)", 1), false},
		{"removed_protected", original, sibling, false},
		{"ambiguous_trusted", sibling + original, sibling + original + "\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			intact, reason := compareJSTestCase("zero", []byte(tc.trusted), []byte(tc.current))
			if intact != tc.intact {
				t.Fatalf("expected intact=%t for legacy selector, got %t (%s)", tc.intact, intact, reason)
			}
		})
	}
}
