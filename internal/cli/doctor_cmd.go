package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/angelnicolasc/flinch/internal/check"
	"github.com/angelnicolasc/flinch/internal/config"
	"github.com/angelnicolasc/flinch/internal/doctor"
	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/hook"
	"github.com/angelnicolasc/flinch/internal/runner"
	"github.com/angelnicolasc/flinch/internal/usersetup"
	"github.com/angelnicolasc/flinch/internal/workspacetrust"
)

func runDoctor(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repoDir := fs.String("repo", "", "repository directory")
	prune := fs.Bool("prune", false, "prune stale Flinch worktrees")
	all := fs.Bool("all", false, "list checkouts where a hook previously activated")
	asJSON := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cwd, err := filepath.Abs(*repoDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	root, rootErr := resolveRepoRoot(ctx, cwd)
	type hostReport struct {
		Name         string `json:"host"`
		Installed    bool   `json:"installed"`
		Active       bool   `json:"active"`
		Installation string `json:"installation"`
		Reason       string `json:"reason"`
	}
	rep := struct {
		doctor.Report
		Hosts            []hostReport   `json:"hosts"`
		LastHookIncident *hook.Incident `json:"last_hook_incident,omitempty"`
		Repositories     []string       `json:"previously_active,omitempty"`
	}{Report: doctor.Report{Schema: 1}}
	if incident, ok := hook.LastIncident(); ok {
		rep.LastHookIncident = &incident
	}
	for _, name := range []string{"claude", "codex", "antigravity"} {
		installed, detail := usersetup.Status(name)
		trust := workspacetrust.Evaluate(ctx, name, cwd, nil)
		reason := trust.Reason
		active := trust.Active && installed && usersetup.Consented(name, trust.Root)
		if trust.Active && !active {
			reason = "user hooks need setup or repair"
		}
		rep.Hosts = append(rep.Hosts, hostReport{name, installed, active, detail, reason})
	}
	if *all {
		rep.Repositories, err = hook.ActivationHistory()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
	}
	if rootErr == nil {
		rep.Report, err = doctor.Run(ctx, root, *prune)
		if err != nil {
			fmt.Fprintf(stderr, "flinch doctor: %v\n", err)
			return 2
		}
	} else if *prune {
		fmt.Fprintln(stderr, "flinch doctor --prune requires a Git checkout")
		return 2
	}
	if *asJSON {
		return writeJSON(stdout, rep)
	}
	for _, host := range rep.Hosts {
		state := "inactive"
		if host.Active {
			state = "active"
		}
		fmt.Fprintf(stdout, "%s: %s | %s (%s)\n", host.Name, host.Installation, state, host.Reason)
		if at, ok := rep.LastTurnChecked[host.Name]; ok {
			fmt.Fprintf(stdout, "  Last %s turn checked in this clone: %s\n", host.Name, at.Format(time.RFC3339Nano))
		}
	}
	if inc := rep.LastHookIncident; inc != nil {
		fmt.Fprintf(stdout, "Last contained hook failure: %s, %s %s during %s (flinch %s). The turn continued without Flinch.\n",
			inc.Time.Format(time.RFC3339), inc.Host, inc.Event, inc.Stage, inc.Version)
	}
	if *all {
		fmt.Fprintln(stdout, "Previously active checkouts:")
		for _, path := range rep.Repositories {
			fmt.Fprintf(stdout, "  %q\n", path)
		}
	}
	if rootErr != nil {
		return 0
	}

	fmt.Fprintf(stdout, "Repository: %s (head=%s, config=%v, mode=%s)\n",
		rep.RepoRoot, shortSHA(rep.HeadSHA), rep.ConfigPresent, rep.PolicyMode)
	if rep.WorkingTreeChecksError != "" {
		fmt.Fprintf(stdout, "Deterministic checks: %d in the trusted revision\n", rep.TrustedChecks)
		fmt.Fprintf(stdout, "Working-tree check counts unavailable: %s\n", rep.WorkingTreeChecksError)
	} else {
		fmt.Fprintf(stdout, "Deterministic checks: %d in the trusted revision, %d only in the working tree (%d locally proved)\n",
			rep.TrustedChecks, rep.WorkingTreeOnlyChecks, rep.LocallyProvedChecks)
		fmt.Fprintf(stdout, "Probabilistic context guides: %d\n", rep.ProbabilisticGuides)
	}
	if rep.CommandApprovalRequired {
		fmt.Fprintln(stdout, runner.CommandPermissionNotice)
	}
	if rep.UnrecognizedAssertions > 0 {
		fmt.Fprintf(stdout, "Local test candidates with no recognized assertion (no_recognized_assertion): %d\n", rep.UnrecognizedAssertions)
	}
	printCapabilityMatrix(stdout, rep.Capabilities)
	if len(rep.ReflexHealth) > 0 {
		fmt.Fprintln(stdout, "\nCheck accreditation:")
		for _, rh := range rep.ReflexHealth {
			fmt.Fprintf(stdout, "  - %s (v%d, %s): ci=%s turn=%s fresh=%v",
				rh.ID, rh.Version, rh.Rung, rh.CIStatus, rh.TurnStatus, rh.Fresh)
			if rh.StaleReason != "" {
				fmt.Fprintf(stdout, " [%s]", rh.StaleReason)
			}
			fmt.Fprintln(stdout)
		}
	}
	if *prune {
		fmt.Fprintf(stdout, "\nPruned orphan worktrees: %d\n", rep.PrunedOrphanWorktrees)
	}
	return 0
}

func printCapabilityMatrix(w io.Writer, m doctor.CapabilityMatrix) {
	fmt.Fprintln(w, "\nCapability matrix:")
	rows := []struct {
		name  string
		entry doctor.CapabilityEntry
	}{
		{"local_capture", m.LocalCapture},
		{"candidate_preparation", m.CandidatePreparation},
		{"turn_enforcement", m.TurnEnforcement},
		{"ci_observation", m.CIObservation},
		{"merge_blocking", m.MergeBlocking},
	}
	for _, r := range rows {
		fmt.Fprintf(w, "  %-22s %-20s %s\n", r.name, r.entry.State, r.entry.Detail)
	}
}

func runCleanup(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cleanup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repoDir := fs.String("repo", "", "repository directory")
	days := fs.Int("days", 0, "override policy.retention_days")
	asJSON := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	root, err := resolveRepoRoot(ctx, *repoDir)
	if err != nil {
		fmt.Fprintf(stderr, "flinch cleanup: %v\n", err)
		return 2
	}
	cfg, _, err := config.LoadOrDefault(root)
	if err != nil {
		fmt.Fprintf(stderr, "flinch cleanup: %v\n", err)
		return 2
	}
	retention := cfg.Policy.RetentionDays
	if *days > 0 {
		retention = *days
	}
	stateDir, err := gitutil.FlinchStateDir(ctx, root)
	if err != nil {
		fmt.Fprintf(stderr, "flinch cleanup: %v\n", err)
		return 2
	}
	rep, err := check.CleanupExpired(stateDir, retention, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(stderr, "flinch cleanup: %v\n", err)
		return 2
	}
	if *asJSON {
		return writeJSON(stdout, rep)
	}
	fmt.Fprintf(stdout, "Cleanup complete (retention=%dd): checkpoints=%d proof_trees=%d log_events=%d\n",
		rep.RetentionDays, rep.RemovedCheckpoints, rep.RemovedProofTrees, rep.PrunedLogEvents)
	return 0
}
