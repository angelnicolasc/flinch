package gitutil

import (
	"path/filepath"
	"strings"
	"testing"
)

// FuzzLocalPath checks the property check records depend on: an accepted name
// always resolves strictly inside the root, and its repo-relative form maps
// back to the same path. Rejecting is always safe; accepting too much is not.
func FuzzLocalPath(f *testing.F) {
	for _, seed := range []string{
		"a.go", "dir/b.go", `dir\c.go`, "a/../b.go", "a/./b", "a//b",
		"../x", "dir/../../x", "/abs", `\abs`, "C:/x", `C:x`, `\\server\share\x`,
		"a\x00b", ".", "", "con", "nul.txt", "dir/", " spaced name.go",
	} {
		f.Add(seed)
	}
	root := f.TempDir()
	f.Fuzz(func(t *testing.T, rel string) {
		got, err := LocalPath(root, rel)
		if err != nil {
			return
		}
		slashed := strings.ReplaceAll(rel, `\`, "/")
		if strings.ContainsAny(rel, ":\x00") || strings.HasPrefix(slashed, "/") {
			t.Fatalf("accepted a volume, NUL or rooted name %q as %q", rel, got)
		}
		inner, err := filepath.Rel(root, got)
		if err != nil || !filepath.IsLocal(inner) || inner == "." {
			t.Fatalf("%q resolved outside or onto the root: %q (%q, %v)", rel, got, inner, err)
		}
		again, err := LocalPath(root, filepath.ToSlash(inner))
		if err != nil || again != got {
			t.Fatalf("%q is not stable: %q became %q (%v)", rel, got, again, err)
		}
	})
}
