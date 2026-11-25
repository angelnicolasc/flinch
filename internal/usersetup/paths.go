// Package usersetup manages user-level hook installation and its consent record.
package usersetup

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unicode/utf16"
)

var hostNames = []string{"claude", "codex", "antigravity"}

func StateDir() (string, error) {
	dir, err := os.UserConfigDir()
	if runtime.GOOS == "windows" {
		dir = os.Getenv("LOCALAPPDATA")
		err = nil
	}
	if err != nil || !filepath.IsAbs(dir) {
		return "", errors.New("cannot resolve an absolute user configuration directory")
	}
	name := "flinch"
	if runtime.GOOS == "windows" {
		name = "Flinch"
	}
	return filepath.Join(dir, name), nil
}

func hostDir(host string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return "", errors.New("cannot resolve the user home directory")
	}
	var dir string
	switch host {
	case "claude":
		dir = os.Getenv("CLAUDE_CONFIG_DIR")
		if dir == "" {
			dir = filepath.Join(home, ".claude")
		}
	case "codex":
		dir = os.Getenv("CODEX_HOME")
		if dir == "" {
			dir = filepath.Join(home, ".codex")
		}
	case "antigravity":
		dir = filepath.Join(home, ".gemini", "config", "plugins", "flinch")
	default:
		return "", fmt.Errorf("unknown host %q (choose claude, codex, or antigravity)", host)
	}
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("%s configuration directory must be absolute", host)
	}
	return filepath.Clean(dir), nil
}

func detected(host, dir string) bool {
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		return true
	}
	names := []string{host}
	if host == "antigravity" {
		names = append(names, "agy")
		home, _ := os.UserHomeDir()
		for _, p := range []string{filepath.Join(home, ".gemini", "antigravity"), filepath.Join(home, ".gemini", "antigravity-cli"), filepath.Dir(dir)} {
			if _, err := os.Stat(p); err == nil {
				return true
			}
		}
	}
	for _, name := range names {
		if _, err := exec.LookPath(name); err == nil {
			return true
		}
	}
	return false
}

func stableBinary() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	return stableBinaryFor(executable, os.Args[0])
}

func stableBinaryFor(executable, invoked string) (string, error) {
	actual, err := os.Stat(executable)
	if err != nil {
		return "", err
	}
	// A direct invocation of a package-manager version still needs a stable name.
	for dir := filepath.Dir(executable); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		switch {
		case filepath.Base(dir) == "Cellar":
			path := filepath.Join(filepath.Dir(dir), "opt", "flinch", "bin", "flinch")
			if info, err := os.Stat(path); err == nil && os.SameFile(info, actual) {
				return path, nil
			}
			return "", errors.New("cannot find the stable Homebrew flinch executable")
		case runtime.GOOS == "windows" && strings.EqualFold(filepath.Base(dir), "apps") && strings.EqualFold(filepath.Base(filepath.Dir(filepath.Dir(executable))), "flinch"):
			path := filepath.Join(filepath.Dir(dir), "shims", "flinch.exe")
			if scoopTargetMatches(path, actual) {
				return path, nil
			}
			return "", errors.New("cannot find the stable Scoop flinch shim")
		}
	}
	for _, name := range []string{"flinch", invoked} {
		path, err := exec.LookPath(name)
		if err != nil || !filepath.IsAbs(path) {
			continue
		}
		info, err := os.Stat(path)
		if err == nil && (os.SameFile(info, actual) || scoopTargetMatches(path, actual)) {
			return filepath.Clean(path), nil
		}
	}
	return executable, nil
}

func scoopTargetMatches(path string, actual os.FileInfo) bool {
	if runtime.GOOS != "windows" || !strings.EqualFold(filepath.Base(filepath.Dir(path)), "shims") {
		return false
	}
	data, err := os.ReadFile(strings.TrimSuffix(path, filepath.Ext(path)) + ".shim")
	if err != nil {
		return false
	}
	line, _, _ := strings.Cut(strings.TrimPrefix(string(data), "\ufeff"), "\n")
	_, target, ok := strings.Cut(line, "=")
	if !ok || !strings.HasPrefix(strings.TrimSpace(line), "path") {
		return false
	}
	info, err := os.Stat(strings.Trim(strings.TrimSpace(target), "\""))
	return err == nil && os.SameFile(info, actual)
}

func posixCommand(path string, args []string) string {
	return "'" + strings.ReplaceAll(path, "'", `'"'"'`) + "' " + strings.Join(args, " ")
}

var windowsLiteral = regexp.MustCompile(`^[A-Za-z]:/[\p{L}\p{Nd}._~/\-]+$`)

// A drive-qualified literal can be invoked directly by cmd, PowerShell and Git Bash.
// Keep room for the hook arguments within cmd's 8,191 UTF-16 code unit limit.
func windowsShellPath(path string, shortPath func(string) (string, error)) (string, error) {
	literal := func(path string) (string, bool) {
		path = strings.ReplaceAll(path, `\`, "/")
		return path, windowsLiteral.MatchString(path) &&
			len(utf16.Encode([]rune(path)))+len(" hook --host antigravity --event UserPromptSubmit --user") <= 8191
	}
	if direct, ok := literal(path); ok {
		return direct, nil
	}
	if short, err := shortPath(path); err == nil {
		if direct, ok := literal(short); ok {
			return direct, nil
		}
	}
	return "", fmt.Errorf("cannot register a direct Windows hook for %q: install flinch.exe in a user-owned folder with a simple drive path such as C:/Tools/Flinch, then run setup again", path)
}
