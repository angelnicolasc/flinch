package reflex

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Rejection remembers a declined candidate scoped by version, scope, reason, and fingerprint.
// A later incident with distinct evidence can justify re-evaluation.
type Rejection struct {
	ID          string `yaml:"id" json:"id"`
	Version     int    `yaml:"version" json:"version"`
	Scope       string `yaml:"scope,omitempty" json:"scope,omitempty"`
	Fingerprint string `yaml:"fingerprint" json:"fingerprint"`
	Reason      string `yaml:"reason" json:"reason"`
	By          string `yaml:"by,omitempty" json:"by,omitempty"`
	At          string `yaml:"at" json:"at"`
}

// RejectedFile stores all rejections recorded in .flinch/rejected.yml.
type RejectedFile struct {
	Schema     int         `yaml:"schema" json:"schema"`
	Rejections []Rejection `yaml:"rejections" json:"rejections"`
}

// LoadRejections loads .flinch/rejected.yml, returning an empty list if absent.
func LoadRejections(repoRoot string) (RejectedFile, error) {
	path := filepath.Join(repoRoot, filepath.FromSlash(RejectedRelPath))
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RejectedFile{Schema: 1}, nil
		}
		return RejectedFile{}, fmt.Errorf("read %s: %w", path, err)
	}
	var rf RejectedFile
	if err := yaml.Unmarshal(data, &rf); err != nil {
		return RejectedFile{}, fmt.Errorf("decode %s: %w", path, err)
	}
	if rf.Schema == 0 {
		rf.Schema = 1
	}
	return rf, nil
}

// IsRejected checks whether the same candidate fingerprint, version, and scope was previously rejected.
func (rf RejectedFile) IsRejected(fingerprint string, version int, scope string) (Rejection, bool) {
	for _, r := range rf.Rejections {
		if r.Fingerprint == fingerprint && r.Version == version && r.Scope == scope {
			return r, true
		}
	}
	return Rejection{}, false
}

// AppendRejection records a new rejection in .flinch/rejected.yml.
func AppendRejection(repoRoot string, rej Rejection) error {
	rf, err := LoadRejections(repoRoot)
	if err != nil {
		return err
	}
	if rej.At == "" {
		rej.At = time.Now().UTC().Format(time.RFC3339)
	}
	if rej.Version == 0 {
		rej.Version = 1
	}
	if _, exists := rf.IsRejected(rej.Fingerprint, rej.Version, rej.Scope); !exists {
		rf.Rejections = append(rf.Rejections, rej)
	}
	path := filepath.Join(repoRoot, filepath.FromSlash(RejectedRelPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(rf)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
