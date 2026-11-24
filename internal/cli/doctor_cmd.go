package cli

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/angelnicolasc/flinch/internal/doctor"
)

func runDoctor(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repoDir := fs.String("repo", "", "repository directory")
	prune := fs.Bool("prune", false, "prune stale Flinch worktrees")
	asJSON := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	root, err := resolveRepoRoot(ctx, *repoDir)
	if err != nil {
		fmt.Fprintf(stderr, "flinch doctor: %v\n", err)
		return 2
	}
	rep, err := doctor.Run(ctx, root, *prune)
	if err != nil {
		fmt.Fprintf(stderr, "flinch doctor: %v\n", err)
		return 2
	}

	if *asJSON {
		return writeJSON(stdout, rep)
	}

	fmt.Fprintf(stdout, "Repository: %s (head=%s, config=%v, mode=%s)\n",
		rep.RepoRoot, shortSHA(rep.HeadSHA), rep.ConfigPresent, rep.PolicyMode)
	fmt.Fprintf(stdout, "Protections: %d deterministic, %d probabilistic context guides\n",
		rep.DeterministicReflexes, rep.ProbabilisticGuides)
	printCapabilityMatrix(stdout, rep.Capabilities)
	if len(rep.ReflexHealth) > 0 {
		fmt.Fprintln(stdout, "\nReflex accreditation:")
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
