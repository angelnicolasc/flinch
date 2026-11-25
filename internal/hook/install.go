package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// InstallResult summarizes an idempotent hook installation for one host.
type InstallResult struct {
	Host    Host   `json:"host"`
	Path    string `json:"path"`
	Changed bool   `json:"changed"`
}

// InstallHooks installs or verifies Flinch hooks for host inside repoRoot:
//   - Running twice changes nothing (idempotent).
//   - Preserves hooks and settings belonging to other tools.
//   - Never overwrites a JSON file it cannot read or parse.
func InstallHooks(repoRoot string, host Host, flinchCmd string) (InstallResult, error) {
	if strings.TrimSpace(flinchCmd) == "" {
		flinchCmd = "flinch"
	}
	switch host {
	case HostClaude:
		return installClaudeOrCodexHooks(repoRoot, host, ".claude/settings.json", flinchCmd)
	case HostCodex:
		return installClaudeOrCodexHooks(repoRoot, host, ".codex/hooks.json", flinchCmd)
	case HostAntigravity:
		return installAntigravityHooks(repoRoot, flinchCmd)
	default:
		return InstallResult{}, fmt.Errorf("unsupported host %q (supported: claude, codex, antigravity)", host)
	}
}

func installClaudeOrCodexHooks(repoRoot string, host Host, relPath, flinchCmd string) (InstallResult, error) {
	absPath := filepath.Join(repoRoot, filepath.FromSlash(relPath))
	rootObj, origBytes, err := loadExistingJSONObject(absPath)
	if err != nil {
		return InstallResult{Host: host, Path: relPath}, err
	}

	hooksObj, ok := rootObj["hooks"].(map[string]any)
	if !ok {
		if _, exists := rootObj["hooks"]; exists {
			return InstallResult{Host: host, Path: relPath}, fmt.Errorf("%s: existing \"hooks\" field is not a JSON object; refusing to overwrite", relPath)
		}
		hooksObj = make(map[string]any)
		rootObj["hooks"] = hooksObj
	}

	events := []struct {
		name    string
		timeout int
	}{
		{"UserPromptSubmit", startTimeoutSeconds},
		{"Stop", stopTimeoutSeconds},
	}

	changed := false
	for _, ev := range events {
		cmdStr := fmt.Sprintf("%s hook --host %s --event %s", flinchCmd, host, ev.name)
		if added, mergeErr := ensureMatcherHookEntry(hooksObj, ev.name, cmdStr, ev.timeout); mergeErr != nil {
			return InstallResult{Host: host, Path: relPath}, fmt.Errorf("%s: %w", relPath, mergeErr)
		} else if added {
			changed = true
		}
	}

	if !changed && len(origBytes) > 0 {
		return InstallResult{Host: host, Path: relPath, Changed: false}, nil
	}
	if err := writeJSONAtomic(absPath, rootObj); err != nil {
		return InstallResult{Host: host, Path: relPath}, err
	}
	return InstallResult{Host: host, Path: relPath, Changed: true}, nil
}

func ensureMatcherHookEntry(hooksObj map[string]any, eventName, cmdStr string, timeout int) (bool, error) {
	var list []any
	if raw, exists := hooksObj[eventName]; exists {
		arr, ok := raw.([]any)
		if !ok {
			return false, fmt.Errorf("existing hooks.%s is not a JSON array; refusing to overwrite", eventName)
		}
		list = arr
	}

	// Check whether an identical Flinch hook command is already registered.
	for _, item := range list {
		if entryHasCommand(item, cmdStr) {
			return false, nil
		}
	}

	entry := map[string]any{
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": cmdStr,
				"timeout": timeout,
			},
		},
	}
	hooksObj[eventName] = append(list, entry)
	return true, nil
}

func entryHasCommand(item any, wantCmd string) bool {
	m, ok := item.(map[string]any)
	if !ok {
		return false
	}
	if c, ok := m["command"].(string); ok && strings.TrimSpace(c) == wantCmd {
		return true
	}
	if nested, ok := m["hooks"].([]any); ok {
		for _, sub := range nested {
			if sm, ok := sub.(map[string]any); ok {
				if c, ok := sm["command"].(string); ok && strings.TrimSpace(c) == wantCmd {
					return true
				}
			}
		}
	}
	return false
}

// installAntigravityHooks merges a single named "flinch" hook group into .agents/hooks.json
// following Antigravity's documented schema (PreInvocation and Stop flat handler arrays).
func installAntigravityHooks(repoRoot, flinchCmd string) (InstallResult, error) {
	const relPath = ".agents/hooks.json"
	absPath := filepath.Join(repoRoot, filepath.FromSlash(relPath))
	rootObj, origBytes, err := loadExistingJSONObject(absPath)
	if err != nil {
		return InstallResult{Host: HostAntigravity, Path: relPath}, err
	}

	preCmd := fmt.Sprintf("%s hook --host antigravity --event PreInvocation", flinchCmd)
	stopCmd := fmt.Sprintf("%s hook --host antigravity --event Stop", flinchCmd)

	desired := map[string]any{
		"enabled": true,
		"PreInvocation": []any{
			map[string]any{
				"type":    "command",
				"command": preCmd,
				"timeout": startTimeoutSeconds,
			},
		},
		"Stop": []any{
			map[string]any{
				"type":    "command",
				"command": stopCmd,
				"timeout": stopTimeoutSeconds,
			},
		},
	}

	if existing, ok := rootObj["flinch"]; ok {
		eb, _ := json.Marshal(existing)
		db, _ := json.Marshal(desired)
		if bytes.Equal(eb, db) && len(origBytes) > 0 {
			return InstallResult{Host: HostAntigravity, Path: relPath, Changed: false}, nil
		}
		return InstallResult{Host: HostAntigravity, Path: relPath}, fmt.Errorf("%s: the existing flinch hook group differs from the requested settings; keeping its configuration", relPath)
	}

	rootObj["flinch"] = desired
	if err := writeJSONAtomic(absPath, rootObj); err != nil {
		return InstallResult{Host: HostAntigravity, Path: relPath}, err
	}
	return InstallResult{Host: HostAntigravity, Path: relPath, Changed: true}, nil
}

func loadExistingJSONObject(absPath string) (map[string]any, []byte, error) {
	data, err := os.ReadFile(absPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return make(map[string]any), nil, nil
		}
		return nil, nil, fmt.Errorf("read %s: %w", absPath, err)
	}
	var obj map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&obj); err != nil {
		return nil, nil, fmt.Errorf("parse %s (refusing to overwrite unreadable config): %w", absPath, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, nil, fmt.Errorf("%s contains trailing JSON data", absPath)
	}
	if obj == nil {
		return nil, nil, fmt.Errorf("%s must contain a JSON object (refusing to overwrite)", absPath)
	}
	return obj, data, nil
}

func writeJSONAtomic(absPath string, obj map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(absPath), ".flinch-hooks-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), absPath)
}
