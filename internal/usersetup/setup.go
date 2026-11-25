package usersetup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/angelnicolasc/flinch/internal/localstate"
)

type registration struct {
	Binary      string   `json:"binary"`
	UserHandler bool     `json:"user_handler,omitempty"`
	Dir         string   `json:"directory,omitempty"`
	Owned       []string `json:"created_files,omitempty"`
	Existing    []string `json:"existing_events,omitempty"`
	KeepKeys    []string `json:"existing_keys,omitempty"`
}

type consent struct {
	Version int                     `json:"version"`
	Hosts   map[string]registration `json:"hosts"`
}

// Plan contains the exact file operations reviewed before consent.
type Plan struct {
	Hosts    []string
	Commands []string
	Paths    []string
	Binary   string
	remove   bool
	writes   []fileChange
	record   fileChange
}

type fileChange struct {
	path          string
	before, after []byte // nil means the path is absent
}

func object(data []byte) (map[string]any, error) {
	if data == nil {
		return map[string]any{}, nil
	}
	var result map[string]any
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := d.Decode(&result); err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("expected a JSON object")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, errors.New("trailing JSON data")
	}
	return result, nil
}

func read(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return os.ReadFile(path)
}

func jsonBytes(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	return append(data, '\n'), err
}

// physical resolves existing parent symlinks even when the last path is new.
func physical(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("expected an absolute path")
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if _, err := os.Lstat(path); err == nil || !os.IsNotExist(err) {
		return "", fmt.Errorf("cannot resolve %s", path)
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", fmt.Errorf("cannot resolve root %s", path)
	}
	base, err := physical(parent)
	return filepath.Join(base, filepath.Base(path)), err
}

func outsideCheckout(path, repo string) bool {
	p, err := physical(path)
	if err != nil {
		return false
	}
	if repo != "" {
		root, err := physical(repo)
		if err != nil {
			return false
		}
		rel, err := filepath.Rel(root, p)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return false
		}
	}
	for dir := filepath.Dir(p); ; dir = filepath.Dir(dir) {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); !os.IsNotExist(err) && !inHomebrewCellar(dir, p) {
			return false
		}
		if dir == filepath.Dir(dir) {
			return true
		}
	}
}

// inHomebrewCellar reports whether path is inside the Cellar of a Homebrew
// repository at dir. On Apple Silicon, Homebrew's prefix /opt/homebrew is
// also its own Git checkout, and every installed formula lives in its
// Cellar, which that checkout doesn't track. Any other path in a checkout,
// or a Cellar in a repository that isn't Homebrew's, still counts as inside.
func inHomebrewCellar(dir, path string) bool {
	rel, err := filepath.Rel(filepath.Join(dir, "Cellar"), path)
	if err != nil || !filepath.IsLocal(rel) {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, "Library", "Homebrew"))
	return err == nil && info.IsDir()
}

func loadConsent() (fileChange, consent, error) {
	doc := consent{Version: 1, Hosts: make(map[string]registration)}
	dir, err := StateDir()
	record := fileChange{path: filepath.Join(dir, "setup.json")}
	if err != nil {
		return record, doc, err
	}
	if !outsideCheckout(record.path, "") {
		return record, doc, fmt.Errorf("user consent path is inside a checkout or cannot be resolved: %s", record.path)
	}
	record.before, err = read(record.path)
	if err == nil && record.before != nil {
		doc = consent{}
		err = json.Unmarshal(record.before, &doc)
		if err == nil && (doc.Version != 1 || doc.Hosts == nil) {
			err = errors.New("unrecognized setup consent record")
		}
	}
	return record, doc, err
}

// Consented reads permission granted by user setup for one host.
func Consented(host, repoRoot string) bool {
	record, doc, err := loadConsent()
	entry, found := doc.Hosts[host]
	return err == nil && found && slices.Contains(hostNames, host) && filepath.IsAbs(entry.Binary) && outsideCheckout(record.path, repoRoot)
}

func Prepare(selection string, remove bool) (*Plan, error) {
	record, doc, err := loadConsent()
	if err != nil {
		return nil, fmt.Errorf("read setup consent: %w", err)
	}
	bin, err := stableBinary()
	if err != nil {
		return nil, err
	}
	if !remove && !outsideCheckout(bin, "") {
		return nil, fmt.Errorf("install the executable at a stable user path outside a checkout before running setup: %s", bin)
	}
	plan := &Plan{record: record, Binary: bin, remove: remove}
	var selected []string
	if selection != "" {
		for _, raw := range strings.Split(selection, ",") {
			host := strings.ToLower(strings.TrimSpace(raw))
			if !slices.Contains(hostNames, host) {
				return nil, fmt.Errorf("unknown host %q (choose claude, codex, or antigravity)", raw)
			}
			if !slices.Contains(selected, host) {
				selected = append(selected, host)
			}
		}
	}
	for _, host := range hostNames {
		if len(selected) > 0 && !slices.Contains(selected, host) {
			continue
		}
		dir, err := hostDir(host)
		if err != nil {
			return nil, err
		}
		old, installed := doc.Hosts[host]
		if len(selected) == 0 && !installed && (remove || !detected(host, dir)) {
			continue
		}
		plan.Hosts = append(plan.Hosts, host)
		if installed && old.Dir != "" && dir != old.Dir {
			if !remove {
				return nil, fmt.Errorf("%s is configured in %s; remove that installation before changing its location", host, old.Dir)
			}
			dir = old.Dir
		}
		if remove && !installed {
			continue
		}
		entry := registration{Binary: bin, UserHandler: true, Dir: dir, Owned: slices.Clone(old.Owned), KeepKeys: slices.Clone(old.KeepKeys)}
		if remove {
			entry.Binary = old.Binary
		} else {
			entry.Binary, err = registeredBinary(host, bin)
			if err != nil {
				return nil, err
			}
		}
		if err := plan.hostChanges(host, old, &entry); err != nil {
			return nil, err
		}
		if remove {
			delete(doc.Hosts, host)
		} else {
			doc.Hosts[host] = entry
		}
	}
	if len(plan.Hosts) == 0 && !remove {
		return nil, errors.New("no supported host detected; select one with --host claude, codex, or antigravity")
	}
	plan.Paths = append(plan.Paths, record.path)
	if len(doc.Hosts) > 0 {
		plan.record.after, err = jsonBytes(doc)
		if err == nil && record.before != nil {
			var original consent
			if json.Unmarshal(record.before, &original) == nil && same(original, doc) {
				plan.record.after = record.before
			}
		}
	}
	if len(plan.writes) > 0 || !bytes.Equal(plan.record.before, plan.record.after) {
		plan.Paths = append(plan.Paths, filepath.Join(filepath.Dir(record.path), "setup.lock"))
	}
	return plan, err
}

func (p *Plan) Apply() error {
	if bytes.Equal(p.record.before, p.record.after) && len(p.writes) == 0 {
		return nil
	}
	dir := filepath.Dir(p.record.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	release, locked, err := localstate.TryLock(filepath.Join(dir, "setup.lock"))
	if err != nil {
		return err
	}
	if !locked {
		return errors.New("another setup is in progress; run setup again")
	}
	defer release()
	changes := append([]fileChange(nil), p.writes...)
	// Revoke consent before detaching hooks. Grant consent only after installing them.
	if p.remove {
		changes = append([]fileChange{p.record}, changes...)
	} else {
		changes = append(changes, p.record)
	}
	for _, change := range changes {
		if err := change.checkOriginal(); err != nil {
			return err
		}
	}
	var completed []fileChange
	for _, change := range changes {
		if bytes.Equal(change.before, change.after) {
			continue
		}
		if err := change.apply(); err != nil {
			for _, prev := range slices.Backward(completed) {
				current, readErr := read(prev.path)
				if readErr == nil && bytes.Equal(current, prev.after) {
					err = errors.Join(err, (fileChange{prev.path, prev.after, prev.before}).apply())
				}
			}
			return err
		}
		completed = append(completed, change)
	}
	return nil
}

func (change fileChange) apply() error {
	if !outsideCheckout(change.path, "") {
		return fmt.Errorf("refusing to alter user settings inside a checkout: %s", change.path)
	}
	if err := change.checkOriginal(); err != nil {
		return err
	}
	if change.after == nil {
		return os.Remove(change.path)
	}
	dir := filepath.Dir(change.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".flinch-setup-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if old, statErr := os.Stat(change.path); statErr == nil {
		err = file.Chmod(old.Mode().Perm())
	}
	if err == nil {
		_, err = file.Write(change.after)
	}
	closeErr := file.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return err
	}
	if err := change.checkOriginal(); err != nil {
		return err
	}
	return os.Rename(file.Name(), change.path)
}

func (change fileChange) checkOriginal() error {
	current, err := read(change.path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, change.before) || (current == nil) != (change.before == nil) {
		return fmt.Errorf("%s changed after the plan was prepared; run setup again", change.path)
	}
	return nil
}
