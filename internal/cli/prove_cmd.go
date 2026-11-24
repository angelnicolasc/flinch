package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/angelnicolasc/flinch/internal/config"
	"github.com/angelnicolasc/flinch/internal/prove"
	"github.com/angelnicolasc/flinch/internal/reflex"
	"gopkg.in/yaml.v3"
)

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func runProve(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("prove", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repoDir := fs.String("repo", "", "repository directory")
	specPath := fs.String("spec", "", "path to YAML or JSON candidate spec file")
	id := fs.String("id", "candidate", "candidate identifier")
	rung := fs.String("rung", "", "protection rung: structure | lint | test | guard")
	obligation := fs.String("obligation", "", "obligation statement")
	scope := fs.String("scope", "", "scope glob (e.g. pkg/**)")
	tool := fs.String("tool", "", "tool name (go-test, pytest, go-vet, go-build, eslint, ruff, golangci-lint, ast-grep, command)")
	artPath := fs.String("artifact-path", "", "path to rule/config/test artifact")
	rule := fs.String("rule", "", "linter rule ID")
	selector := fs.String("selector", "", "exact test or rule selector")
	command := fs.String("command", "", "command to run for command/script tool")
	expectedSig := fs.String("expected-signal", "", "expected distinguishing failure substring or regex")
	neighborSel := fs.String("neighbor", "", "legitimate neighboring test selector (required for test proofs)")
	brokenRef := fs.String("broken", "", "broken Git ref")
	fixedRef := fs.String("fixed", "WORKTREE", "fixed Git ref or WORKTREE")
	timeoutStr := fs.String("timeout", "", "verification timeout (e.g. 120s)")
	asJSON := fs.Bool("json", false, "output JSON report")
	var allowedFlags multiFlag
	fs.Var(&allowedFlags, "allowed", "allowed case mapping target_path=source_file (repeatable)")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	root, err := resolveRepoRoot(ctx, *repoDir)
	if err != nil {
		fmt.Fprintf(stderr, "flinch prove: %v\n", err)
		return 2
	}
	cfg, _, err := config.LoadOrDefault(root)
	if err != nil {
		fmt.Fprintf(stderr, "flinch prove: %v\n", err)
		return 2
	}

	var spec prove.CandidateSpec
	if *specPath != "" {
		data, readErr := os.ReadFile(*specPath)
		if readErr != nil {
			fmt.Fprintf(stderr, "flinch prove: read spec: %v\n", readErr)
			return 2
		}
		if strings.HasSuffix(strings.ToLower(*specPath), ".json") {
			if err := json.Unmarshal(data, &spec); err != nil {
				fmt.Fprintf(stderr, "flinch prove: decode JSON spec: %v\n", err)
				return 2
			}
		} else {
			if err := yaml.Unmarshal(data, &spec); err != nil {
				fmt.Fprintf(stderr, "flinch prove: decode YAML spec: %v\n", err)
				return 2
			}
		}
	}

	// CLI flags override spec file values when explicitly provided.
	if spec.ID == "" || *specPath == "" {
		spec.ID = *id
	}
	if *rung != "" {
		spec.Rung = reflex.Rung(*rung)
	}
	if *obligation != "" {
		spec.Obligation = *obligation
	}
	if *scope != "" {
		spec.Scope = *scope
	}
	if *tool != "" {
		spec.Artifact.Tool = *tool
	}
	if *artPath != "" {
		spec.Artifact.Path = *artPath
	}
	if *rule != "" {
		spec.Artifact.Rule = *rule
	}
	if *selector != "" {
		spec.Artifact.Selector = *selector
	}
	if *command != "" {
		spec.Artifact.Command = *command
	}
	if *expectedSig != "" {
		spec.Artifact.ExpectedSignal = *expectedSig
	}
	if *neighborSel != "" {
		spec.NeighborSelector = *neighborSel
	}
	if *brokenRef != "" {
		spec.BrokenRef = *brokenRef
	}
	if *fixedRef != "" && (spec.FixedRef == "" || *specPath == "") {
		spec.FixedRef = *fixedRef
	}
	if len(spec.TestSupportGlobs) == 0 {
		spec.TestSupportGlobs = cfg.Tests.Support
	}
	for _, entry := range allowedFlags {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 {
			fmt.Fprintf(stderr, "flinch prove: invalid --allowed %q (expected target_path=source_file)\n", entry)
			return 2
		}
		spec.AllowedCases = append(spec.AllowedCases, prove.AllowedCase{
			Name:       parts[1],
			TargetPath: parts[0],
			SourceFile: parts[1],
		})
	}

	timeout := cfg.TestTimeout()
	if *timeoutStr != "" {
		d, dErr := time.ParseDuration(*timeoutStr)
		if dErr != nil {
			fmt.Fprintf(stderr, "flinch prove: invalid --timeout %q: %v\n", *timeoutStr, dErr)
			return 2
		}
		timeout = d
	}

	report, err := prove.Verify(ctx, root, spec, prove.Options{Timeout: timeout})
	if err != nil {
		fmt.Fprintf(stderr, "flinch prove: %v\n", err)
		return 2
	}

	if *asJSON {
		_ = writeJSON(stdout, report)
	} else {
		fmt.Fprintf(stdout, "verdict: %s\n", report.Verdict)
		fmt.Fprintf(stdout, "candidate: %s (rung=%s, tool=%s, selector=%s)\n", report.CandidateID, report.Rung, report.Runner, report.Selector)
		fmt.Fprintf(stdout, "revisions: broken=%s fixed=%s\n", shortSHA(report.BrokenRef), shortSHA(report.FixedRef))
		if report.Reason != "" {
			fmt.Fprintf(stdout, "reason: %s\n", report.Reason)
		}
		if report.ActionableError != "" {
			fmt.Fprintf(stdout, "detail: %s\n", report.ActionableError)
		}
		if report.ObservedSignal != "" {
			fmt.Fprintf(stdout, "observed: %s\n", report.ObservedSignal)
		}
		fmt.Fprintf(stdout, "census: %d  runtime_ms: %d\n", report.Census, report.TotalRuntimeMS)
	}

	switch report.Verdict {
	case prove.VerdictVerified:
		return 0
	case prove.VerdictRefuted:
		return 1
	default:
		return 2
	}
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
