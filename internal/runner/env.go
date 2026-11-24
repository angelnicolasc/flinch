package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// FilteredEnv builds a restricted environment for candidate commands and test runners (D11, P10).
// It keeps only runtime/toolchain variables required to invoke local tools, scopes temporary
// directories outside the working tree, and omits unrelated environment variables.
func FilteredEnv(proofRoot string, extra ...string) []string {
	allowed := map[string]bool{
		"PATH":                    true,
		"PATHEXT":                 true,
		"SYSTEMROOT":              true,
		"WINDIR":                  true,
		"COMSPEC":                 true,
		"OS":                      true,
		"NUMBER_OF_PROCESSORS":    true,
		"PROCESSOR_ARCHITECTURE":  true,
		"LANG":                    true,
		"LC_ALL":                  true,
		"LC_CTYPE":                true,
		"TERM":                    true,
		"TZ":                      true,
		"GOPATH":                  true,
		"GOMODCACHE":              true,
		"GOCACHE":                 true,
		"GOROOT":                  true,
		"GOENV":                   true,
		"GOPROXY":                 true,
		"GOSUMDB":                 true,
		"CGO_ENABLED":             true,
		"NODE_PATH":               true,
		"NODE_OPTIONS":            true,
		"VIRTUAL_ENV":             true,
		"PYTHONPATH":              true,
		"PYTHONDONTWRITEBYTECODE": true,
		"PYTHONUTF8":              true,
		"PROGRAMFILES":            true,
		"PROGRAMFILES(X86)":       true,
		"COMMONPROGRAMFILES":      true,
		"COMMONPROGRAMFILES(X86)": true,
		"LOCALAPPDATA":            true,
		"APPDATA":                 true,
		"USERPROFILE":             true,
		"HOME":                    true,
		"HOMEDRIVE":               true,
		"HOMEPATH":                true,
		"XDG_CACHE_HOME":          true,
	}

	var out []string
	for _, kv := range os.Environ() {
		idx := strings.IndexByte(kv, '=')
		if idx <= 0 {
			continue
		}
		k := kv[:idx]
		upper := strings.ToUpper(k)
		if !allowed[upper] {
			continue
		}
		out = append(out, kv)
	}

	if proofRoot != "" {
		out = append(out, "FLINCH_ROOT="+proofRoot)
	}
	out = append(out, "PYTHONDONTWRITEBYTECODE=1")
	out = append(out,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"NO_COLOR=1",
	)
	if runtime.GOOS == "windows" {
		out = append(out, "PYTHONIOENCODING=utf-8")
	}
	out = append(out, extra...)
	return out
}

// runCandidate owns a private scratch directory for exactly one command.
func runCandidate(cmd *exec.Cmd, root string, extra ...string) error {
	dir, err := os.MkdirTemp("", "flinch-run-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	// Changing XDG_CACHE_HOME alone would rebuild the Go toolchain on every hook.
	if os.Getenv("GOCACHE") == "" {
		if base, err := os.UserCacheDir(); err == nil {
			extra = append(extra, "GOCACHE="+filepath.Join(base, "go-build"))
		}
	}
	// Only the golangci-lint runner supplies a persistent cache override.
	extra = append([]string{"GOLANGCI_LINT_CACHE=" + dir}, extra...)
	for _, variable := range []string{"TMP", "TEMP", "TMPDIR", "XDG_CACHE_HOME", "RUFF_CACHE_DIR", "VITE_CACHE_DIR"} {
		extra = append(extra, variable+"="+dir)
	}
	cmd.WaitDelay = 100 * time.Millisecond
	cmd.Env = FilteredEnv(root, extra...)
	return cmd.Run()
}

func golangciCacheEnv(ctx context.Context, root string) []string {
	base, err := os.UserCacheDir()
	if err != nil || !filepath.IsAbs(base) {
		return nil
	}
	cache := filepath.Join(base, "flinch", "golangci-lint")
	clone, _ := ctx.Value(repositoryKey{}).(string)
	for _, dir := range []string{root, clone} {
		if dir == "" {
			continue
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return nil
		}
		if rel, err := filepath.Rel(abs, cache); err == nil && filepath.IsLocal(rel) {
			return nil
		}
	}
	return []string{"GOLANGCI_LINT_CACHE=" + cache}
}
