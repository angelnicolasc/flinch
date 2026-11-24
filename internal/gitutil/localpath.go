package gitutil

import (
	"fmt"
	"path/filepath"
	"strings"
)

// LocalPath resolves a repository-relative file name using the same restrictions on all hosts.
func LocalPath(root, rel string) (string, error) {
	name := strings.ReplaceAll(rel, "\\", "/")
	if name != "" && !strings.ContainsAny(name, ":\x00") && !strings.HasPrefix(name, "/") {
		native := filepath.FromSlash(name)
		if filepath.IsLocal(native) && filepath.Clean(native) != "." {
			return filepath.Join(root, native), nil
		}
	}
	return "", fmt.Errorf("expected a repository-relative file path, got %q", rel)
}
