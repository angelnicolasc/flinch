package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/angelnicolasc/flinch/internal/gitutil"
)

const (
	CurrentSchema = 1
	ConfigRelPath = ".flinch/flinch.toml"
)

// PreparationMode controls how Flinch prepares candidate protections.
type PreparationMode string

const (
	ModeTeamConnected PreparationMode = "team_connected"
	ModeLocalAuto     PreparationMode = "local_auto"
	ModeObserveOnly   PreparationMode = "observe_only"
)

// Config represents the repository-level Flinch policy checked into .flinch/flinch.toml.
// Secrets must never be stored in this file; they are resolved from the execution environment.
type Config struct {
	Schema       int                `toml:"schema" json:"schema"`
	Policy       PolicyConfig       `toml:"policy" json:"policy"`
	Sources      SourcesConfig      `toml:"sources" json:"sources"`
	Executor     ExecutorConfig     `toml:"executor" json:"executor"`
	Loop         LoopConfig         `toml:"loop" json:"loop"`
	Severity     SeverityConfig     `toml:"severity" json:"severity"`
	ContextGuide ContextGuideConfig `toml:"context_guide" json:"context_guide"`
	Tests        TestsConfig        `toml:"tests" json:"tests"`
	Lint         []LintConfig       `toml:"lint,omitempty" json:"lint,omitempty"`
	Surfaces     SurfacesConfig     `toml:"surfaces" json:"surfaces"`
}

// PolicyConfig defines repository authorization, privacy, and retention boundaries.
type PolicyConfig struct {
	Mode                PreparationMode `toml:"mode" json:"mode"`
	Owner               string          `toml:"owner,omitempty" json:"owner,omitempty"`
	AuthorizedBranches  []string        `toml:"authorized_branches,omitempty" json:"authorized_branches,omitempty"`
	AllowPRWrites       bool            `toml:"allow_pr_writes" json:"allow_pr_writes"`
	DraftPRFallback     bool            `toml:"draft_pr_fallback" json:"draft_pr_fallback"`
	RetentionDays       int             `toml:"retention_days" json:"retention_days"`
	AllowRemoteEvidence bool            `toml:"allow_remote_evidence" json:"allow_remote_evidence"`
	StorePromptText     bool            `toml:"store_prompt_text" json:"store_prompt_text"`
}

// SourcesConfig defines which signal sources are enabled for incident capture.
type SourcesConfig struct {
	Corrections              bool   `toml:"corrections" json:"corrections"`
	Fixes                    bool   `toml:"fixes" json:"fixes"`
	CI                       bool   `toml:"ci" json:"ci"`
	Review                   bool   `toml:"review" json:"review"`
	RetrospectiveTranscripts bool   `toml:"retrospective_transcripts" json:"retrospective_transcripts"`
	CatchUp                  string `toml:"catch_up" json:"catch_up"`
}

// ExecutorConfig identifies the authorized non-interactive preparation executor and trust base.
type ExecutorConfig struct {
	Provider            string `toml:"provider,omitempty" json:"provider,omitempty"`
	Model               string `toml:"model,omitempty" json:"model,omitempty"`
	MaxCandidatesPerDay int    `toml:"max_candidates_per_day" json:"max_candidates_per_day"`
	TrustedRef          string `toml:"trusted_ref,omitempty" json:"trusted_ref,omitempty"`
}

// LoopConfig defines budgets and continuation limits for interactive agent turns.
type LoopConfig struct {
	Deadline                   string `toml:"deadline" json:"deadline"`
	StaticTargetPOSIX          string `toml:"static_target_posix" json:"static_target_posix"`
	StaticTargetWindows        string `toml:"static_target_windows" json:"static_target_windows"`
	TestBudget                 string `toml:"test_budget" json:"test_budget"`
	MaxTestRuntime             string `toml:"max_test_runtime" json:"max_test_runtime"`
	MaxContinuationsPerFinding int    `toml:"max_continuations_per_finding" json:"max_continuations_per_finding"`
	MaxContinuationsPerTurn    int    `toml:"max_continuations_per_turn" json:"max_continuations_per_turn"`
}

// SeverityConfig defines debt thresholds for CI blocking recommendations.
type SeverityConfig struct {
	FixNowMax int `toml:"fix_now_max" json:"fix_now_max"`
}

// ContextGuideConfig configures probabilistic instructions in AGENTS.md (excluded from deterministic counts).
type ContextGuideConfig struct {
	File     string `toml:"file" json:"file"`
	MaxLines int    `toml:"max_lines" json:"max_lines"`
}

// TestsConfig configures test verification and support file classification.
type TestsConfig struct {
	Timeout string       `toml:"timeout" json:"timeout"`
	Support []string     `toml:"support,omitempty" json:"support,omitempty"`
	Runners []TestRunner `toml:"runner,omitempty" json:"runner,omitempty"`
}

// TestRunner binds a test runner kind to file globs in the repository.
type TestRunner struct {
	Kind    string   `toml:"kind" json:"kind"`
	Paths   []string `toml:"paths,omitempty" json:"paths,omitempty"`
	Command string   `toml:"command,omitempty" json:"command,omitempty"`
}

// LintConfig binds a repository linter or structural analyzer.
type LintConfig struct {
	Kind    string   `toml:"kind" json:"kind"`
	Config  string   `toml:"config,omitempty" json:"config,omitempty"`
	Command string   `toml:"command,omitempty" json:"command,omitempty"`
	Paths   []string `toml:"paths,omitempty" json:"paths,omitempty"`
}

// SurfacesConfig describes expected CI workflow and merge queue surfaces.
type SurfacesConfig struct {
	CIWorkflow string `toml:"ci_workflow,omitempty" json:"ci_workflow,omitempty"`
	CIJob      string `toml:"ci_job,omitempty" json:"ci_job,omitempty"`
	MergeQueue bool   `toml:"merge_queue" json:"merge_queue"`
}

// Default returns a Config populated with Flinch v3.2 baseline defaults.
func Default() Config {
	return Config{
		Schema: CurrentSchema,
		Policy: PolicyConfig{
			Mode:                ModeLocalAuto,
			AuthorizedBranches:  []string{"main"},
			AllowPRWrites:       false,
			DraftPRFallback:     true,
			RetentionDays:       30,
			AllowRemoteEvidence: false,
		},
		Sources: SourcesConfig{
			Corrections:              true,
			Fixes:                    true,
			CI:                       true,
			Review:                   false,
			RetrospectiveTranscripts: false,
			CatchUp:                  "7d",
		},
		Executor: ExecutorConfig{
			MaxCandidatesPerDay: 10,
			TrustedRef:          "HEAD",
		},
		Loop: LoopConfig{
			Deadline:                   "10s",
			StaticTargetPOSIX:          "1s",
			StaticTargetWindows:        "2s",
			TestBudget:                 "20s",
			MaxTestRuntime:             "5s",
			MaxContinuationsPerFinding: 1,
			MaxContinuationsPerTurn:    2,
		},
		Severity: SeverityConfig{
			FixNowMax: 10,
		},
		ContextGuide: ContextGuideConfig{
			File:     "AGENTS.md",
			MaxLines: 30,
		},
		Tests: TestsConfig{
			Timeout: "300s",
			Support: []string{
				"**/testdata/**",
				"**/conftest.py",
				"**/__snapshots__/**",
				"**/__mocks__/**",
				"**/fixtures/**",
				"**/*.golden",
			},
		},
	}
}

// Load reads and validates .flinch/flinch.toml from repoRoot.
// If the file does not exist, it returns os.ErrNotExist wrapped with context.
func Load(repoRoot string) (Config, error) {
	path := filepath.Join(repoRoot, filepath.FromSlash(ConfigRelPath))
	return readFile(path, RepositoryDefaults(repoRoot))
}

// LoadOrDefault applies repository configuration over the tools discovered in this checkout.
func LoadOrDefault(repoRoot string) (Config, bool, error) {
	defaults := RepositoryDefaults(repoRoot)
	cfg, err := readFile(filepath.Join(repoRoot, filepath.FromSlash(ConfigRelPath)), defaults)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return defaults, false, nil
		}
		return Config{}, false, err
	}
	return cfg, true, nil
}

// LoadFile reads and validates a TOML policy file at path.
func LoadFile(path string) (Config, error) {
	return readFile(path, Default())
}

func readFile(path string, cfg Config) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return Config{}, fmt.Errorf("decode config %s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			keys = append(keys, k.String())
		}
		return Config{}, fmt.Errorf("config %s contains unknown keys: %s", path, strings.Join(keys, ", "))
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate config %s: %w", path, err)
	}
	return cfg, nil
}

// Save writes cfg atomically to .flinch/flinch.toml under repoRoot.
func Save(repoRoot string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	path := filepath.Join(repoRoot, filepath.FromSlash(ConfigRelPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	if err := enc.Encode(cfg); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	return writeConfig(path, buf.Bytes())
}

// Validate checks schema bounds, allowed enum values, and duration syntax.
func (c Config) Validate() error {
	if c.Schema != CurrentSchema {
		return fmt.Errorf("unsupported schema version %d (expected %d)", c.Schema, CurrentSchema)
	}
	switch c.Policy.Mode {
	case ModeTeamConnected, ModeLocalAuto, ModeObserveOnly:
	default:
		return fmt.Errorf("invalid policy.mode %q", c.Policy.Mode)
	}
	if c.Policy.RetentionDays < 1 {
		return fmt.Errorf("policy.retention_days must be >= 1, got %d", c.Policy.RetentionDays)
	}
	if _, err := ParseDuration(c.Sources.CatchUp); err != nil {
		return fmt.Errorf("sources.catch_up: %w", err)
	}
	for name, raw := range map[string]string{
		"loop.deadline":              c.Loop.Deadline,
		"loop.static_target_posix":   c.Loop.StaticTargetPOSIX,
		"loop.static_target_windows": c.Loop.StaticTargetWindows,
		"loop.test_budget":           c.Loop.TestBudget,
		"loop.max_test_runtime":      c.Loop.MaxTestRuntime,
		"tests.timeout":              c.Tests.Timeout,
	} {
		d, err := ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if d <= 0 {
			return fmt.Errorf("%s must be > 0, got %s", name, raw)
		}
	}
	if c.Loop.MaxContinuationsPerFinding < 1 {
		return fmt.Errorf("loop.max_continuations_per_finding must be >= 1")
	}
	if c.Loop.MaxContinuationsPerTurn < 1 {
		return fmt.Errorf("loop.max_continuations_per_turn must be >= 1")
	}
	if c.Severity.FixNowMax < 0 {
		return fmt.Errorf("severity.fix_now_max must be >= 0")
	}
	if c.ContextGuide.File != "" {
		if _, err := gitutil.LocalPath(".", c.ContextGuide.File); err != nil {
			return fmt.Errorf("context_guide.file: %w", err)
		}
	}
	if c.ContextGuide.MaxLines < 1 {
		return fmt.Errorf("context_guide.max_lines must be >= 1")
	}
	for i, r := range c.Tests.Runners {
		switch r.Kind {
		case "go", "pytest", "vitest", "jest", "junit":
		default:
			return fmt.Errorf("tests.runner[%d].kind %q is not supported", i, r.Kind)
		}
	}
	for i, l := range c.Lint {
		switch l.Kind {
		case "ast-grep", "golangci-lint", "eslint", "ruff", "go-vet", "go-build", "command", "script":
		default:
			return fmt.Errorf("lint[%d].kind %q is not supported", i, l.Kind)
		}
	}
	return nil
}

// StaticTargetDuration returns the static latency target for the current OS.
func (c Config) StaticTargetDuration() time.Duration {
	raw := c.Loop.StaticTargetPOSIX
	if runtime.GOOS == "windows" {
		raw = c.Loop.StaticTargetWindows
	}
	d, err := ParseDuration(raw)
	if err != nil {
		if runtime.GOOS == "windows" {
			return 2 * time.Second
		}
		return time.Second
	}
	return d
}

// LoopDeadline returns the unified deadline for interactive turn enforcement.
func (c Config) LoopDeadline() time.Duration {
	d, err := ParseDuration(c.Loop.Deadline)
	if err != nil {
		return 10 * time.Second
	}
	return d
}

// TestTimeout returns the configured per-test-invocation timeout.
func (c Config) TestTimeout() time.Duration {
	d, err := ParseDuration(c.Tests.Timeout)
	if err != nil {
		return 300 * time.Second
	}
	return d
}

// ParseDuration parses Go durations plus day suffixes (for example "7d", "30d").
func ParseDuration(raw string) (time.Duration, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if strings.HasSuffix(s, "d") && !strings.HasSuffix(s, "nd") {
		numStr := strings.TrimSuffix(s, "d")
		days, err := strconv.Atoi(numStr)
		if err != nil {
			return 0, fmt.Errorf("invalid day duration %q: %w", raw, err)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", raw, err)
	}
	return d, nil
}
