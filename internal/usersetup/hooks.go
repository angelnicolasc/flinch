package usersetup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

type hookEvent struct {
	name    string
	timeout int
}

func events(host string) []hookEvent {
	start := "UserPromptSubmit"
	if host == "antigravity" {
		start = "PreInvocation"
	}
	return []hookEvent{{start, 10}, {"Stop", 30}}
}

func handler(host string, ev hookEvent, binary string) map[string]any {
	args := []string{"hook", "--host", host, "--event", ev.name, "--user"}
	return commandHandler(host, ev, binary, args)
}

func commandHandler(host string, ev hookEvent, binary string, args []string) map[string]any {
	h := map[string]any{"type": "command", "timeout": ev.timeout, "command": binary}
	if host == "claude" {
		h["args"] = args
		return h
	}
	h["command"] = posixCommand(binary, args)
	if runtime.GOOS == "windows" {
		win := filepath.ToSlash(binary) + " " + strings.Join(args, " ")
		if host == "codex" {
			h["commandWindows"] = win
		} else {
			h["command"] = win
		}
	}
	return h
}

func same(a, b any) bool {
	first, err := json.Marshal(a)
	second, otherErr := json.Marshal(b)
	return err == nil && otherErr == nil && bytes.Equal(first, second)
}

func hooksPath(host, dir string) string {
	if host == "claude" {
		return filepath.Join(dir, "settings.json")
	}
	return filepath.Join(dir, "hooks.json")
}

func (p *Plan) hostChanges(host string, old registration, entry *registration) error {
	if !outsideCheckout(hooksPath(host, entry.Dir), "") {
		return fmt.Errorf("%s user settings must be outside a checkout", host)
	}
	if host == "antigravity" {
		path := filepath.Join(entry.Dir, "plugin.json")
		raw, err := read(path)
		if err != nil {
			return err
		}
		doc, err := object(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		manifest := map[string]any{"name": "flinch", "description": "Verify repository checks when the agent receives a correction."}
		if raw != nil && doc["name"] != "flinch" {
			return fmt.Errorf("%s already belongs to another plugin", path)
		}
		if !p.remove && raw == nil {
			data, err := jsonBytes(manifest)
			if err != nil {
				return err
			}
			p.writes = append(p.writes, fileChange{path, raw, data})
			entry.Owned = appendUnique(entry.Owned, "plugin.json")
		} else if p.remove && slices.Contains(old.Owned, "plugin.json") && same(doc, manifest) {
			// Keep the manifest when other hooks or components now depend on it.
			entries, err := os.ReadDir(entry.Dir)
			if err != nil {
				return err
			}
			onlyManaged := true
			for _, f := range entries {
				if f.Name() != "hooks.json" && f.Name() != "plugin.json" {
					onlyManaged = false
				}
			}
			rawHooks, err := read(hooksPath(host, entry.Dir))
			if err != nil {
				return err
			}
			hooks, err := object(rawHooks)
			if err != nil {
				return err
			}
			if _, err := editHooks(hooks, host, old, entry, true); err != nil {
				return err
			}
			if onlyManaged && len(hooks) == 0 {
				p.writes = append(p.writes, fileChange{path, raw, nil})
			}
		}
		p.Paths = append(p.Paths, path)
	}
	path := hooksPath(host, entry.Dir)
	raw, err := read(path)
	if err != nil {
		return err
	}
	doc, err := object(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	p.Paths = append(p.Paths, path)
	for _, ev := range events(host) {
		h := handler(host, ev, entry.Binary)
		if p.remove {
			h = old.installedHandler(host, ev)
		}
		command := h["command"].(string)
		if windows, ok := h["commandWindows"].(string); ok {
			command = windows
		} else if args, ok := h["args"].([]string); ok {
			command = fmt.Sprintf("%q %s", command, strings.Join(args, " "))
		}
		data, _ := json.Marshal(h)
		p.Commands = append(p.Commands, fmt.Sprintf("%s %s: %s\n  Hook definition: %s", host, ev.name, command, data))
	}
	changed, err := editHooks(doc, host, old, entry, p.remove)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if changed {
		var data []byte
		if !p.remove || len(doc) != 0 || !slices.Contains(old.Owned, filepath.Base(path)) {
			data, err = jsonBytes(doc)
			if err != nil {
				return err
			}
		}
		p.writes = append(p.writes, fileChange{path, raw, data})
		if !p.remove && raw == nil {
			entry.Owned = appendUnique(entry.Owned, filepath.Base(path))
		}
	}
	return nil
}

func appendUnique(items []string, item string) []string {
	if slices.Contains(items, item) {
		return items
	}
	return append(items, item)
}

func sameInvocation(value any, want map[string]any) bool {
	h, ok := value.(map[string]any)
	if !ok || want == nil {
		return false
	}
	for _, key := range []string{"type", "command", "args", "commandWindows"} {
		if !same(h[key], want[key]) {
			return false
		}
	}
	return true
}

func editHooks(doc map[string]any, host string, old registration, entry *registration, remove bool) (bool, error) {
	return editHooksForEvents(doc, host, old, entry, remove, events(host))
}

func editHooksForEvents(doc map[string]any, host string, old registration, entry *registration, remove bool, selected []hookEvent) (bool, error) {
	key := "hooks"
	if host == "antigravity" {
		key = "flinch"
	}
	group, ok := doc[key].(map[string]any)
	if ok && old.Binary == "" {
		entry.KeepKeys = appendUnique(entry.KeepKeys, key)
	}
	if !ok {
		if _, exists := doc[key]; exists {
			return false, fmt.Errorf("%s must be an object", key)
		}
		if remove {
			return false, nil
		}
		group = map[string]any{}
		if host == "antigravity" {
			group["enabled"] = true
		}
		doc[key] = group
	}
	changed := false
	for _, ev := range selected {
		list, ok := group[ev.name].([]any)
		if _, exists := group[ev.name]; exists && !ok {
			return false, fmt.Errorf("%s.%s must be an array", key, ev.name)
		}
		if ok && old.Binary == "" {
			entry.KeepKeys = appendUnique(entry.KeepKeys, ev.name)
		}
		want := handler(host, ev, entry.Binary)
		var previous map[string]any
		if old.Binary != "" {
			previous = old.installedHandler(host, ev)
		}
		found := false
		ownedMatch := false
		removed := false
		managed := old.Binary != "" && !slices.Contains(old.Existing, ev.name)
		updated := make([]any, 0, len(list)+1)
		for _, item := range list {
			var nested []any
			grp, _ := item.(map[string]any)
			direct := host == "antigravity"
			if direct {
				nested = []any{item}
			} else {
				nested, ok = grp["hooks"].([]any)
				if !ok {
					updated = append(updated, item)
					continue
				}
			}
			kept := make([]any, 0, len(nested))
			modified := false
			for _, value := range nested {
				plainGroup := direct || len(grp) == 1
				if !remove && sameInvocation(value, want) {
					found = true
					if managed && same(value, previous) && same(value, want) && plainGroup {
						ownedMatch = true
					}
				} else if !removed && managed && plainGroup && same(value, previous) {
					modified = true
					removed = true
					continue
				} else if !remove && old.UserHandler && sameInvocation(value, previous) {
					// Moving a binary cannot change a customized or preexisting command.
					return false, fmt.Errorf("review the existing %s handler before changing its executable location", ev.name)
				}
				kept = append(kept, value)
			}
			if !modified {
				updated = append(updated, item)
				continue
			}
			changed = true
			if !direct && len(kept) > 0 {
				grp["hooks"] = kept
				updated = append(updated, grp)
			}
		}
		if !remove && found && !ownedMatch {
			entry.Existing = appendUnique(entry.Existing, ev.name)
		}
		if !remove && !found {
			var item any = want
			if host != "antigravity" {
				item = map[string]any{"hooks": []any{want}}
			}
			updated = append(updated, item)
			changed = true
		}
		if len(updated) > 0 || slices.Contains(old.KeepKeys, ev.name) {
			group[ev.name] = updated
		} else if len(list) > 0 {
			delete(group, ev.name)
		}
	}
	if remove && changed && !slices.Contains(old.KeepKeys, key) && (len(group) == 0 || (host == "antigravity" && len(group) == 1 && group["enabled"] == true)) {
		delete(doc, key)
	}
	return changed, nil
}

// Status checks both saved consent and the registered definitions on disk.
func Status(host string) (installed bool, detail string) {
	return hookStatus(host, events(host), true)
}

// UserHookRegistered lets a shared hook yield to the user definition for the same event.
// The host still applies the user's matchers, disable switches and review decision.
func UserHookRegistered(host, event string) bool {
	for _, candidate := range events(host) {
		if candidate.name == event {
			found, _ := hookStatus(host, []hookEvent{candidate}, false)
			return found
		}
	}
	return false
}

func hookStatus(host string, selected []hookEvent, requireEnabled bool) (installed bool, detail string) {
	_, doc, err := loadConsent()
	if err != nil {
		return false, err.Error()
	}
	dir, err := hostDir(host)
	if err != nil {
		return false, err.Error()
	}
	reg, ok := doc.Hosts[host]
	if !ok {
		return false, "user setup has not granted consent"
	}
	if reg.Dir != "" && reg.Dir != dir {
		return false, "active host configuration differs from the installed profile"
	}
	if host == "antigravity" {
		b, err := read(filepath.Join(dir, "plugin.json"))
		m, parseErr := object(b)
		if err != nil || parseErr != nil || m["name"] != "flinch" || (requireEnabled && m["disabled"] == true) {
			return false, "Antigravity plugin is missing, invalid, or disabled"
		}
	}
	path := hooksPath(host, dir)
	b, err := read(path)
	if err != nil {
		return false, err.Error()
	}
	h, err := object(b)
	if err != nil {
		return false, err.Error()
	}
	if requireEnabled && host == "claude" && h["disableAllHooks"] == true {
		return false, "Claude Code hooks are disabled"
	}
	if requireEnabled && host == "antigravity" {
		group, _ := h["flinch"].(map[string]any)
		if group["enabled"] == false {
			return false, "Antigravity hook group is disabled"
		}
	}
	if changed, err := editHooksForEvents(h, host, reg, &registration{Binary: reg.Binary}, false, selected); err != nil || changed {
		return false, "registered hooks are missing or differ in " + path
	}
	return true, "user hooks installed in " + path
}

func (p *Plan) Describe() string {
	var b strings.Builder
	action := "Install user hooks"
	if p.remove {
		action = "Remove user hooks and revoke their consent"
	}
	fmt.Fprintln(&b, action)
	fmt.Fprintln(&b, "Executable:", p.Binary)
	for _, path := range p.Paths {
		fmt.Fprintln(&b, "File:", path)
	}
	for _, command := range p.Commands {
		fmt.Fprintln(&b, command)
	}
	return b.String()
}
