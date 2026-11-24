package doctor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"github.com/angelnicolasc/flinch/internal/config"
	"github.com/angelnicolasc/flinch/internal/evidence"
	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/reflex"
	"github.com/angelnicolasc/flinch/internal/runner"
)

// CapabilityState mirrors §2.1's verifiable onboarding matrix states.
type CapabilityState string

const (
	CapEnabledAndTested  CapabilityState = "enabled_and_tested"
	CapAvailableUntested CapabilityState = "available_untested"
	CapUnavailable       CapabilityState = "unavailable"
)

// CapabilityEntry explains one cell in the installation matrix.
type CapabilityEntry struct {
	State  CapabilityState `json:"state"`
	Detail string          `json:"detail"`
}

// CapabilityMatrix summarizes what this clone and repository can actually guarantee (§2.1).
type CapabilityMatrix struct {
	LocalCapture         CapabilityEntry `json:"local_capture"`
	CandidatePreparation CapabilityEntry `json:"candidate_preparation"`
	TurnEnforcement      CapabilityEntry `json:"turn_enforcement"`
	CIObservation        CapabilityEntry `json:"ci_observation"`
	MergeBlocking        CapabilityEntry `json:"merge_blocking"`
}

// ReflexHealth summarizes the accreditation and freshness of one deterministic reflex.
type ReflexHealth struct {
	ID            string                 `json:"id"`
	Version       int                    `json:"version"`
	Rung          reflex.Rung            `json:"rung"`
	Deterministic bool                   `json:"deterministic"`
	CIStatus      evidence.SurfaceStatus `json:"ci_status"`
	TurnStatus    evidence.SurfaceStatus `json:"turn_status"`
	AccreditedSHA string                 `json:"accredited_sha,omitempty"`
	Fresh         bool                   `json:"fresh"`
	StaleReason   string                 `json:"stale_reason,omitempty"`
}

// Report is the structured diagnostic output of `flinch doctor` and `flinch init`.
type Report struct {
	Schema                int                        `json:"schema"`
	RepoRoot              string                     `json:"repo_root"`
	ConfigPresent         bool                       `json:"config_present"`
	PolicyMode            config.PreparationMode     `json:"policy_mode"`
	HeadSHA               string                     `json:"head_sha,omitempty"`
	Tools                 map[string]runner.ToolInfo `json:"tools"`
	Workflows             []string                   `json:"workflows,omitempty"`
	ContextFiles          []string                   `json:"context_files,omitempty"`
	Capabilities          CapabilityMatrix           `json:"capabilities"`
	DeterministicReflexes int                        `json:"deterministic_reflexes"`
	ProbabilisticGuides   int                        `json:"probabilistic_guides"`
	ReflexHealth          []ReflexHealth             `json:"reflex_health,omitempty"`
	PrunedOrphanWorktrees int                        `json:"pruned_orphan_worktrees,omitempty"`
}

// DiscoverConfig inspects repoRoot and builds an initial .flinch/flinch.toml based on detected tools.
func DiscoverConfig(ctx context.Context, repoRoot string) (config.Config, []string) {
	cfg := config.Default()
	tools := runner.DiscoverTools(ctx)
	var notes []string

	if exists(filepath.Join(repoRoot, "go.mod")) {
		cfg.Tests.Runners = append(cfg.Tests.Runners, config.TestRunner{
			Kind:  "go",
			Paths: []string{"**/*.go"},
		})
		if tools["golangci-lint"].Available && (exists(filepath.Join(repoRoot, ".golangci.yml")) || exists(filepath.Join(repoRoot, ".golangci.yaml"))) {
			cfg.Lint = append(cfg.Lint, config.LintConfig{Kind: "golangci-lint", Paths: []string{"**/*.go"}})
		} else {
			cfg.Lint = append(cfg.Lint, config.LintConfig{Kind: "go-vet", Paths: []string{"**/*.go"}})
		}
		notes = append(notes, "Detected Go module (go.mod): enabled go test runner and go-vet.")
	}

	if exists(filepath.Join(repoRoot, "pyproject.toml")) || exists(filepath.Join(repoRoot, "pytest.ini")) {
		cfg.Tests.Runners = append(cfg.Tests.Runners, config.TestRunner{
			Kind:  "pytest",
			Paths: []string{"**/*.py"},
		})
		if tools["ruff"].Available {
			cfg.Lint = append(cfg.Lint, config.LintConfig{Kind: "ruff", Paths: []string{"**/*.py"}})
		}
		notes = append(notes, "Detected Python project: enabled pytest runner.")
	}

	if exists(filepath.Join(repoRoot, "package.json")) {
		if exists(filepath.Join(repoRoot, "eslint.config.mjs")) || exists(filepath.Join(repoRoot, "eslint.config.js")) || exists(filepath.Join(repoRoot, ".eslintrc.json")) {
			cfg.Lint = append(cfg.Lint, config.LintConfig{Kind: "eslint", Paths: []string{"**/*.ts", "**/*.tsx", "**/*.js", "**/*.jsx"}})
			notes = append(notes, "Detected ESLint configuration.")
		}
	}

	if tools["ast-grep"].Available {
		cfg.Lint = append(cfg.Lint, config.LintConfig{Kind: "ast-grep"})
	}

	workflows := findWorkflows(repoRoot)
	if len(workflows) > 0 {
		cfg.Surfaces.CIWorkflow = workflows[0]
		notes = append(notes, "Detected GitHub Actions workflow: "+workflows[0])
	}

	if exists(filepath.Join(repoRoot, "AGENTS.md")) && exists(filepath.Join(repoRoot, "CLAUDE.md")) {
		notes = append(notes, "Both AGENTS.md and CLAUDE.md are present; verify host context loading during installation.")
	}

	return cfg, notes
}

// Run inspects repoRoot, computes the capability matrix, evaluates reflex accreditation freshness,
// and optionally prunes orphan Flinch worktrees (§2.1, §3.2, §9).
func Run(ctx context.Context, repoRoot string, pruneWorktrees bool) (Report, error) {
	cfg, cfgPresent, err := config.LoadOrDefault(repoRoot)
	if err != nil {
		return Report{}, err
	}
	tools := runner.DiscoverTools(ctx)
	headSHA, _, _ := gitutil.HeadSHA(ctx, repoRoot)
	workflows := findWorkflows(repoRoot)
	wfHash := hashFiles(repoRoot, workflows)

	rep := Report{
		Schema:        1,
		RepoRoot:      repoRoot,
		ConfigPresent: cfgPresent,
		PolicyMode:    cfg.Policy.Mode,
		HeadSHA:       headSHA,
		Tools:         tools,
		Workflows:     workflows,
		ContextFiles:  findContextFiles(repoRoot),
	}

	if pruneWorktrees {
		if n, pruneErr := gitutil.PruneOrphanWorktrees(ctx, repoRoot); pruneErr == nil {
			rep.PrunedOrphanWorktrees = n
		}
	}

	stateDir, _ := gitutil.FlinchStateDir(ctx, repoRoot)
	var evStore *evidence.Store
	if stateDir != "" {
		evStore = evidence.NewStore(stateDir)
	}

	reflexes, _ := reflex.LoadAll(repoRoot)
	hasActiveCI := false
	hasRequiredMergeBlock := false
	hasActiveTurn := false

	for _, r := range reflexes {
		if r.IsDeterministic() {
			rep.DeterministicReflexes++
		} else {
			rep.ProbabilisticGuides++
		}
		rh := ReflexHealth{
			ID:            r.ID,
			Version:       r.Version,
			Rung:          r.EffectiveRung(),
			Deterministic: r.IsDeterministic(),
			CIStatus:      evidence.SurfaceUnknown,
			TurnStatus:    evidence.SurfaceUnknown,
		}
		if r.IsDeterministic() && evStore != nil {
			fp, _ := r.ComputeFingerprint(repoRoot)
			if ciRec, err := evStore.Load(r.ID, evidence.SurfaceCI); err == nil {
				rh.CIStatus = ciRec.Status
				rh.AccreditedSHA = ciRec.IntegratedSHA
				ok, reason := ciRec.IsValidFor(fp, wfHash, headSHA)
				rh.Fresh = ok
				rh.StaleReason = reason
				if ok && ciRec.Status == evidence.SurfaceActive {
					hasActiveCI = true
					if ciRec.RequiredForMerge {
						hasRequiredMergeBlock = true
					}
				}
			}
			if turnRec, err := evStore.Load(r.ID, evidence.SurfaceAgentTurn); err == nil {
				rh.TurnStatus = turnRec.Status
				if ok, _ := turnRec.IsValidFor(fp, "", headSHA); ok && turnRec.Status == evidence.SurfaceActive {
					hasActiveTurn = true
				}
			}
		}
		rep.ReflexHealth = append(rep.ReflexHealth, rh)
	}

	rep.Capabilities = buildCapabilityMatrix(cfg, cfgPresent, tools, workflows, hasActiveCI, hasRequiredMergeBlock, hasActiveTurn)
	return rep, nil
}

func buildCapabilityMatrix(
	cfg config.Config,
	cfgPresent bool,
	tools map[string]runner.ToolInfo,
	workflows []string,
	hasActiveCI, hasRequiredMergeBlock, hasActiveTurn bool,
) CapabilityMatrix {
	var m CapabilityMatrix

	// 1. Local capture
	hasHost := tools["claude"].Available || tools["codex"].Available || tools["opencode"].Available
	if !tools["git"].Available {
		m.LocalCapture = CapabilityEntry{State: CapUnavailable, Detail: "git binary not found in PATH"}
	} else if cfgPresent && hasHost {
		m.LocalCapture = CapabilityEntry{State: CapAvailableUntested, Detail: "policy configured and at least one supported agent host detected; run adapter contract test to mark enabled_and_tested"}
	} else if hasHost {
		m.LocalCapture = CapabilityEntry{State: CapAvailableUntested, Detail: "git and supported agent host detected; run `flinch init` to configure capture"}
	} else {
		m.LocalCapture = CapabilityEntry{State: CapAvailableUntested, Detail: "git capture available; no local agent host detected on PATH"}
	}

	// 2. Candidate preparation
	if cfg.Policy.Mode == config.ModeObserveOnly {
		m.CandidatePreparation = CapabilityEntry{State: CapUnavailable, Detail: "disabled by policy.mode = observe_only"}
	} else if hasHost && cfgPresent {
		m.CandidatePreparation = CapabilityEntry{State: CapAvailableUntested, Detail: "authorized mode=" + string(cfg.Policy.Mode) + "; run controlled end-to-end check to verify"}
	} else {
		m.CandidatePreparation = CapabilityEntry{State: CapUnavailable, Detail: "requires initialized .flinch/flinch.toml and an authorized executor"}
	}

	// 3. Turn enforcement
	if hasActiveTurn {
		m.TurnEnforcement = CapabilityEntry{State: CapEnabledAndTested, Detail: "at least one deterministic reflex accredited on agent_turn surface"}
	} else if hasHost && cfgPresent {
		m.TurnEnforcement = CapabilityEntry{State: CapAvailableUntested, Detail: "agent host detected; no reflex accredited on agent_turn yet"}
	} else {
		m.TurnEnforcement = CapabilityEntry{State: CapUnavailable, Detail: "no supported agent host configured for turn enforcement"}
	}

	// 4. CI observation
	if hasActiveCI {
		m.CIObservation = CapabilityEntry{State: CapEnabledAndTested, Detail: "CI execution and PR/merge_group trigger witness recorded on integrated SHA"}
	} else if tools["gh"].Available && len(workflows) > 0 {
		m.CIObservation = CapabilityEntry{State: CapAvailableUntested, Detail: "gh CLI and GitHub Actions workflow detected; run `flinch check --accredit` and witness a PR run"}
	} else {
		m.CIObservation = CapabilityEntry{State: CapUnavailable, Detail: "requires GitHub workflow and gh CLI (or configured CI provider)"}
	}

	// 5. Merge blocking
	if hasRequiredMergeBlock {
		m.MergeBlocking = CapabilityEntry{State: CapEnabledAndTested, Detail: "accredited CI check marked required for merge"}
	} else if len(workflows) > 0 {
		m.MergeBlocking = CapabilityEntry{State: CapAvailableUntested, Detail: "workflow detected; branch protection / required status check not yet verified"}
	} else {
		m.MergeBlocking = CapabilityEntry{State: CapUnavailable, Detail: "no CI workflow detected to enforce merge blocking"}
	}

	return m
}

func findWorkflows(repoRoot string) []string {
	dir := filepath.Join(repoRoot, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml") {
			out = append(out, filepath.ToSlash(filepath.Join(".github", "workflows", name)))
		}
	}
	return out
}

func findContextFiles(repoRoot string) []string {
	var out []string
	for _, name := range []string{"AGENTS.md", "CLAUDE.md", "CODEX.md"} {
		if exists(filepath.Join(repoRoot, name)) {
			out = append(out, name)
		}
	}
	return out
}

func hashFiles(repoRoot string, relPaths []string) string {
	if len(relPaths) == 0 {
		return ""
	}
	h := sha256.New()
	for _, rel := range relPaths {
		if data, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel))); err == nil {
			h.Write([]byte(rel))
			h.Write(data)
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
