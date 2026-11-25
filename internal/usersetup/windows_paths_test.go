package usersetup

import (
	"errors"
	"strings"
	"testing"
)

func TestWindowsShellPath(t *testing.T) {
	const short = "C:/TOOLS~1/flinch.exe"
	for _, path := range []string{`C:\bin\flinch.exe`, "D:/flinch-2.1_bin~/flinch.exe", "D:/café١/flinch.exe"} {
		got, err := windowsShellPath(path, func(string) (string, error) {
			t.Fatal("looked up a short name for a direct path")
			return "", nil
		})
		if err != nil || got != strings.ReplaceAll(path, `\`, "/") {
			t.Errorf("%q produced %q, %v", path, got, err)
		}
	}
	for _, char := range " &%!^();,='$`" {
		path := "C:/tools" + string(char) + "directory/flinch.exe"
		got, err := windowsShellPath(path, func(input string) (string, error) {
			if input != path {
				t.Fatalf("short-name lookup for %q, want %q", input, path)
			}
			return short, nil
		})
		if err != nil || got != short {
			t.Errorf("%q produced %q, %v", path, got, err)
		}
	}
	for _, path := range []string{`\\server\flinch.exe`, `C:bin\flinch.exe`, `/tmp/flinch.exe`, "C:/R&D/flinch.exe", "C:/x:y/flinch.exe", "C:/a\n/flinch.exe"} {
		if _, err := windowsShellPath(path, func(string) (string, error) { return path, nil }); err == nil {
			t.Errorf("registered %q as its own short alias", path)
		}
	}
	if _, err := windowsShellPath("C:/two words/flinch.exe", func(string) (string, error) {
		return "", errors.New("no short path")
	}); err == nil || !strings.Contains(err.Error(), "C:/Tools/Flinch") {
		t.Fatalf("missing guidance on a volume without short paths: %v", err)
	}
}
