// Package workspacetrust checks host-owned trust before a hook can access project settings or state.
package workspacetrust

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/angelnicolasc/flinch/internal/pause"
)

// Status identifies the authorized checkout and explains the decision without changing it.
type Status struct {
	Root, CommonDir, Reason                           string
	Active                                            bool
	ClaudeProjectSameClone, ClaudeProjectSameCheckout bool
}

// Evaluate checks the current host record and clone pause on every call.
func Evaluate(ctx context.Context, host, cwd string, workspacePaths []string) Status {
	if pause.DisabledByEnv() {
		return Status{Reason: "FLINCH is off for this session"}
	}
	if host == "antigravity" && cwd == "" && len(workspacePaths) > 0 {
		cwd = workspacePaths[0]
	}
	c, err := findCheckout(ctx, cwd)
	if err != nil {
		return Status{Reason: err.Error()}
	}
	status := Status{Root: c.root, CommonDir: c.common}
	if len(workspacePaths) > 0 && !mountedCheckout(ctx, c, workspacePaths) {
		status.Reason = "the host did not identify one mounted worktree"
		return status
	}
	switch host {
	case "claude":
		project, err := findCheckout(ctx, os.Getenv("CLAUDE_PROJECT_DIR"))
		if err == nil {
			status.ClaudeProjectSameClone = sameDirectory(project.common, c.common)
			status.ClaudeProjectSameCheckout = status.ClaudeProjectSameClone && sameDirectory(project.root, c.root)
		}
		desktop := os.Getenv("CLAUDE_CODE_ENTRYPOINT") == "claude-desktop" && status.ClaudeProjectSameClone
		status.Active = desktop || claudeTrusted(c) || cloneEnabled(c)
		status.Reason = "Claude Code has no saved trust or matching Desktop project for this clone. Review it and run flinch enable to grant access"
	case "codex":
		status.Reason = codexReason(ctx, c, cwd)
		status.Active = status.Reason == ""
	case "antigravity":
		status.Active = antigravityTrusted(c) || cloneEnabled(c)
		status.Reason = "Antigravity has no saved workspace trust. Review this clone and run flinch enable to grant access"
	default:
		status.Reason = "unrecognized host"
	}
	if !status.Active {
		return status
	}
	if err := ctx.Err(); err != nil {
		status.Active = false
		status.Reason = err.Error()
		return status
	}
	status.Reason = "the workspace is trusted"
	if reason := pauseReason(c.common); reason != "" {
		status.Active = false
		status.Reason = reason
	} else if err := c.verifyGit(ctx); err != nil {
		status.Active = false
		status.Reason = err.Error()
	}
	return status
}

func sameDirectory(a, b string) bool {
	if a == b {
		return true
	}
	first, err := os.Stat(a)
	if err != nil {
		return false
	}
	second, err := os.Stat(b)
	return err == nil && os.SameFile(first, second)
}

func mountedCheckout(ctx context.Context, c checkout, paths []string) bool {
	matched := false
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			return false
		}
		root, err := filepath.EvalSymlinks(path)
		if err != nil || !isDir(root) {
			return false
		}
		mount, err := findCheckout(ctx, root)
		if err != nil || !samePath(mount.root, c.root) {
			return false
		}
		matched = matched || within(root, c.cwd)
	}
	return matched
}

func readProfile(path string, c checkout) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("the user profile path is unavailable")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	if within(c.root, resolved) || within(filepath.Dir(c.common), resolved) {
		return nil, errors.New("the checkout cannot supply its own workspace trust")
	}
	return readRegular(resolved, 16<<20)
}

func claudeTrusted(c checkout) bool {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		dir, _ = os.UserHomeDir()
	}
	data, err := readProfile(filepath.Join(dir, ".claude.json"), c)
	if err != nil {
		return false
	}
	var doc struct {
		Projects map[string]struct {
			Accepted bool `json:"hasTrustDialogAccepted"`
		} `json:"projects"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return false
	}
	root := filepath.Dir(c.common)
	for path, project := range doc.Projects {
		if project.Accepted && sameWorkspacePath(path, root) {
			return true
		}
	}
	return false
}

// Claude Code and Antigravity have written Windows paths with either separator.
func sameWorkspacePath(stored, physical string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(windowsWorkspacePath(stored), windowsWorkspacePath(physical))
	}
	return stored == physical
}

func windowsWorkspacePath(path string) string {
	path = strings.ReplaceAll(path, "/", `\`)
	if len(path) >= 8 && strings.EqualFold(path[:8], `\\?\UNC\`) {
		return `\\` + path[8:]
	}
	return strings.TrimPrefix(path, `\\?\`)
}

func antigravityTrusted(c checkout) bool {
	home, _ := os.UserHomeDir()
	data, err := readProfile(filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"), c)
	var doc struct {
		Workspaces []string `json:"trustedWorkspaces"`
	}
	if err == nil && json.Unmarshal(data, &doc) == nil {
		for _, path := range doc.Workspaces {
			if sameWorkspacePath(path, c.root) {
				return true
			}
		}
	}
	return false
}

const cloneMarker = "enabled"

func cloneEnabled(c checkout) bool {
	dir := filepath.Join(c.common, "flinch")
	if !isDir(dir) {
		return false
	}
	info, err := os.Lstat(filepath.Join(dir, cloneMarker))
	return err == nil && info.Mode().IsRegular()
}

// SetCloneEnabled grants or revokes this clone's Claude Code and Antigravity permission.
func SetCloneEnabled(ctx context.Context, root string, enabled bool) error {
	c, err := findCheckout(ctx, root)
	if err != nil {
		return err
	}
	if err := c.verifyGit(ctx); err != nil {
		return err
	}
	dir := filepath.Join(c.common, "flinch")
	if enabled {
		if err = os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	info, err := os.Lstat(dir)
	if !enabled && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("invalid clone state directory")
	}
	path := filepath.Join(dir, cloneMarker)
	if !enabled {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("invalid clone permission file")
		}
		return os.Remove(path)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		if info, e := os.Lstat(path); e == nil && info.Mode().IsRegular() {
			return nil
		}
		return errors.New("invalid clone permission file")
	}
	if err != nil {
		return err
	}
	return f.Close()
}

// A read-only inspection also works in doctor. The regular pause command owns cleanup.
func pauseReason(common string) string {
	dir := filepath.Join(common, "flinch")
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil || !info.IsDir() {
		return "the clone state directory is unavailable"
	}
	data, err := readRegular(filepath.Join(dir, "pause.json"), 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	var state pause.State
	if err != nil || json.Unmarshal(data, &state) != nil {
		return "the clone pause status is unreadable"
	}
	if state.Paused && (state.ExpiresAt == nil || time.Now().Before(*state.ExpiresAt)) {
		return "Flinch is paused in this clone"
	}
	return ""
}

func codexHome() string {
	if path := os.Getenv("CODEX_HOME"); path != "" {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex")
}

func safeProfileName(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, "/\\:\x00") && filepath.Base(value) == value
}
