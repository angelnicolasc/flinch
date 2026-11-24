package reflex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v3"
)

const (
	ReflexesRelDir  = ".flinch/reflexes"
	CasesRelDir     = ".flinch/cases"
	RejectedRelPath = ".flinch/rejected.yml"
)

// Rung classifies the strength tier of a protection.
type Rung string

const (
	RungStructure Rung = "structure"
	RungLint      Rung = "lint"
	RungTest      Rung = "test"
	RungGuard     Rung = "guard"
	RungLine      Rung = "line"
)

// StringList allows a YAML field to be decoded from either a single scalar string or a sequence of strings.
type StringList []string

func (s *StringList) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		var single string
		if err := value.Decode(&single); err != nil {
			return err
		}
		if strings.TrimSpace(single) == "" {
			*s = nil
		} else {
			*s = []string{single}
		}
		return nil
	case yaml.SequenceNode:
		var list []string
		if err := value.Decode(&list); err != nil {
			return err
		}
		*s = list
		return nil
	default:
		return fmt.Errorf("expected string or list of strings, got YAML kind %d", value.Kind)
	}
}

func (s StringList) MarshalYAML() (any, error) {
	if len(s) == 1 {
		return s[0], nil
	}
	return []string(s), nil
}

// Reflex represents a reviewed protection record stored under .flinch/reflexes/<id>.yml.
// Surfaces expresses reviewed intent only; observed activation state is stored separately
// in activation evidence bound to an integrated SHA.
type Reflex struct {
	ID          string          `yaml:"id" json:"id"`
	Version     int             `yaml:"version" json:"version"`
	Owner       string          `yaml:"owner,omitempty" json:"owner,omitempty"`
	Scope       string          `yaml:"scope,omitempty" json:"scope,omitempty"`
	Exclude     []string        `yaml:"exclude,omitempty" json:"exclude,omitempty"`
	Rung        Rung            `yaml:"rung,omitempty" json:"rung,omitempty"`
	Obligation  string          `yaml:"obligation" json:"obligation"`
	Artifact    Artifact        `yaml:"artifact" json:"artifact"`
	Cases       Cases           `yaml:"cases,omitempty" json:"cases,omitempty"`
	Surfaces    Surfaces        `yaml:"surfaces,omitempty" json:"surfaces,omitempty"`
	Incidents   []IncidentRef   `yaml:"incidents,omitempty" json:"incidents,omitempty"`
	Proof       *ProofSummary   `yaml:"proof,omitempty" json:"proof,omitempty"`
	Fingerprint string          `yaml:"fingerprint,omitempty" json:"fingerprint,omitempty"`
	Decided     *DecisionRecord `yaml:"decided,omitempty" json:"decided,omitempty"`
	FilePath    string          `yaml:"-" json:"file_path,omitempty"`
}

// Artifact identifies the native repository check, test, command, or guidance entry.
type Artifact struct {
	Tool           string   `yaml:"tool" json:"tool"`
	Path           string   `yaml:"path,omitempty" json:"path,omitempty"`
	Rule           string   `yaml:"rule,omitempty" json:"rule,omitempty"`
	Selector       string   `yaml:"selector,omitempty" json:"selector,omitempty"`
	Command        string   `yaml:"command,omitempty" json:"command,omitempty"`
	ExpectedSignal string   `yaml:"expected_signal,omitempty" json:"expected_signal,omitempty"`
	When           []string `yaml:"when,omitempty" json:"when,omitempty"`
	FixHint        string   `yaml:"fix_hint,omitempty" json:"fix_hint,omitempty"`
	Text           string   `yaml:"text,omitempty" json:"text,omitempty"`
}

// Cases points to minimal reviewed fixtures proving failure on broken code,
// pass on fixed code, and pass on at least one legitimate near-miss case.
type Cases struct {
	TargetPath string     `yaml:"target_path,omitempty" json:"target_path,omitempty"`
	Broken     string     `yaml:"broken,omitempty" json:"broken,omitempty"`
	Fixed      string     `yaml:"fixed,omitempty" json:"fixed,omitempty"`
	Allowed    StringList `yaml:"allowed,omitempty" json:"allowed,omitempty"`
}

// Surfaces records intended enforcement surfaces (not observed runtime activation).
type Surfaces struct {
	PullRequest *PRSurface   `yaml:"pull_request,omitempty" json:"pull_request,omitempty"`
	AgentTurn   *TurnSurface `yaml:"agent_turn,omitempty" json:"agent_turn,omitempty"`
}

// PRSurface declares intended CI enforcement on pull requests and merge queues.
type PRSurface struct {
	Job        string `yaml:"job,omitempty" json:"job,omitempty"`
	Mode       string `yaml:"mode,omitempty" json:"mode,omitempty"` // block | warn | disabled
	MergeQueue bool   `yaml:"merge_queue,omitempty" json:"merge_queue,omitempty"`
}

// TurnSurface declares intended interactive turn enforcement.
type TurnSurface struct {
	Mode         string `yaml:"mode,omitempty" json:"mode,omitempty"` // repair_if_supported | advise | disabled
	MaxRuntimeMS int    `yaml:"max_runtime_ms,omitempty" json:"max_runtime_ms,omitempty"`
}

// IncidentRef links a reflex back to the originating correction, fix, PR, or CI episode.
type IncidentRef struct {
	Source    string `yaml:"source,omitempty" json:"source,omitempty"`
	EpisodeID string `yaml:"episode_id,omitempty" json:"episode_id,omitempty"`
	PR        int    `yaml:"pr,omitempty" json:"pr,omitempty"`
	Commit    string `yaml:"commit,omitempty" json:"commit,omitempty"`
	Broken    string `yaml:"broken,omitempty" json:"broken,omitempty"`
	Fixed     string `yaml:"fixed,omitempty" json:"fixed,omitempty"`
}

// ProofSummary stores concise metadata from the latest verification report.
type ProofSummary struct {
	Verdict       string `yaml:"verdict" json:"verdict"`
	At            string `yaml:"at,omitempty" json:"at,omitempty"`
	Census        int    `yaml:"census" json:"census"`
	RuntimeMS     int64  `yaml:"runtime_ms,omitempty" json:"runtime_ms,omitempty"`
	RunnerVersion string `yaml:"runner_version,omitempty" json:"runner_version,omitempty"`
	ReportHash    string `yaml:"report_hash,omitempty" json:"report_hash,omitempty"`
}

// DecisionRecord records human review authority for the reflex.
type DecisionRecord struct {
	By string `yaml:"by,omitempty" json:"by,omitempty"`
	At string `yaml:"at,omitempty" json:"at,omitempty"`
	PR int    `yaml:"pr,omitempty" json:"pr,omitempty"`
}

// EffectiveRung returns the explicit rung or infers it from artifact.tool.
func (r Reflex) EffectiveRung() Rung {
	if r.Rung != "" {
		return r.Rung
	}
	switch r.Artifact.Tool {
	case "go-build", "import-linter", "depguard", "dependency-cruiser":
		return RungStructure
	case "eslint", "ruff", "golangci-lint", "ast-grep", "go-vet":
		return RungLint
	case "go-test", "pytest", "vitest", "jest", "junit":
		return RungTest
	case "command", "script":
		return RungGuard
	case "agents-md":
		return RungLine
	default:
		return RungGuard
	}
}

// IsDeterministic reports whether this reflex is an executable deterministic protection.
// Context guidance lines (agents-md / rung line) are probabilistic and excluded from deterministic counts.
func (r Reflex) IsDeterministic() bool {
	return r.EffectiveRung() != RungLine && r.Artifact.Tool != "agents-md"
}

// MatchesPath reports whether relPath (slash-normalized, repo-relative) falls within the reflex scope.
func (r Reflex) MatchesPath(relPath string) bool {
	clean := filepath.ToSlash(strings.TrimPrefix(relPath, "./"))
	for _, ex := range r.Exclude {
		if ok, err := doublestar.Match(filepath.ToSlash(ex), clean); err == nil && ok {
			return false
		}
	}
	if strings.TrimSpace(r.Scope) == "" {
		return true
	}
	ok, err := doublestar.Match(filepath.ToSlash(r.Scope), clean)
	return err == nil && ok
}

// ExpectedID returns the primary identifier used to attribute diagnostics to this reflex.
func (r Reflex) ExpectedID() string {
	if r.Artifact.Selector != "" {
		return r.Artifact.Selector
	}
	if r.Artifact.Rule != "" {
		return r.Artifact.Rule
	}
	return r.ID
}

// Validate checks required fields and allowed values.
func (r Reflex) Validate() error {
	if strings.TrimSpace(r.ID) == "" {
		return fmt.Errorf("reflex id is required")
	}
	if r.Version < 1 {
		return fmt.Errorf("reflex %q version must be >= 1, got %d", r.ID, r.Version)
	}
	if strings.TrimSpace(r.Obligation) == "" {
		return fmt.Errorf("reflex %q obligation is required", r.ID)
	}
	if strings.TrimSpace(r.Artifact.Tool) == "" {
		return fmt.Errorf("reflex %q artifact.tool is required", r.ID)
	}
	if r.Surfaces.PullRequest != nil {
		switch r.Surfaces.PullRequest.Mode {
		case "", "block", "warn", "disabled":
		default:
			return fmt.Errorf("reflex %q invalid pull_request.mode %q", r.ID, r.Surfaces.PullRequest.Mode)
		}
	}
	if r.Surfaces.AgentTurn != nil {
		switch r.Surfaces.AgentTurn.Mode {
		case "", "repair_if_supported", "advise", "disabled":
		default:
			return fmt.Errorf("reflex %q invalid agent_turn.mode %q", r.ID, r.Surfaces.AgentTurn.Mode)
		}
	}
	if r.EffectiveRung() == RungLine {
		if strings.TrimSpace(r.Artifact.Text) == "" {
			return fmt.Errorf("reflex %q line artifact requires artifact.text", r.ID)
		}
		if len(r.Artifact.Text) > 200 || strings.ContainsAny(r.Artifact.Text, "\r\n") {
			return fmt.Errorf("reflex %q line artifact must be a single line <= 200 chars", r.ID)
		}
	}
	return nil
}

// ComputeFingerprint calculates a deterministic SHA-256 hash covering the reflex contract
// and, when present on disk under repoRoot, the referenced artifact file bytes.
func (r Reflex) ComputeFingerprint(repoRoot string) (string, error) {
	payload := struct {
		ID          string   `json:"id"`
		Version     int      `json:"version"`
		Scope       string   `json:"scope"`
		Exclude     []string `json:"exclude"`
		Rung        Rung     `json:"rung"`
		Obligation  string   `json:"obligation"`
		Artifact    Artifact `json:"artifact"`
		ArtifactSHA string   `json:"artifact_sha,omitempty"`
	}{
		ID:         r.ID,
		Version:    r.Version,
		Scope:      r.Scope,
		Exclude:    r.Exclude,
		Rung:       r.EffectiveRung(),
		Obligation: r.Obligation,
		Artifact:   r.Artifact,
	}

	if repoRoot != "" && r.Artifact.Path != "" {
		artPath := filepath.Join(repoRoot, filepath.FromSlash(r.Artifact.Path))
		data, err := os.ReadFile(artPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("read artifact %s: %w", r.Artifact.Path, err)
		}
		if err == nil {
			sum := sha256.Sum256(data)
			payload.ArtifactSHA = hex.EncodeToString(sum[:])
		}
	}

	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// LoadFile reads and validates a single reflex YAML file.
func LoadFile(path string) (Reflex, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Reflex{}, fmt.Errorf("read reflex %s: %w", path, err)
	}
	var r Reflex
	if err := yaml.Unmarshal(data, &r); err != nil {
		return Reflex{}, fmt.Errorf("decode reflex %s: %w", path, err)
	}
	if r.Version == 0 {
		r.Version = 1
	}
	if err := r.Validate(); err != nil {
		return Reflex{}, fmt.Errorf("validate reflex %s: %w", path, err)
	}
	r.FilePath = path
	return r, nil
}

// LoadAll reads all reflex YAML files under .flinch/reflexes sorted by ID.
func LoadAll(repoRoot string) ([]Reflex, error) {
	dir := filepath.Join(repoRoot, filepath.FromSlash(ReflexesRelDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read reflexes directory: %w", err)
	}
	var out []Reflex
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		r, err := LoadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Save writes a reflex record atomically to .flinch/reflexes/<id>.yml.
func Save(repoRoot string, r Reflex) (string, error) {
	if r.Version == 0 {
		r.Version = 1
	}
	if err := r.Validate(); err != nil {
		return "", err
	}
	dir := filepath.Join(repoRoot, filepath.FromSlash(ReflexesRelDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create reflexes dir: %w", err)
	}
	path := filepath.Join(dir, r.ID+".yml")
	data, err := yaml.Marshal(r)
	if err != nil {
		return "", fmt.Errorf("marshal reflex %s: %w", r.ID, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", fmt.Errorf("write temp reflex: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("commit reflex: %w", err)
	}
	return path, nil
}
