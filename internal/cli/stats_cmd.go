package cli

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/reflex"
	"github.com/angelnicolasc/flinch/internal/stats"
)

func runStats(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repoDir := fs.String("repo", "", "repository directory")
	asJSON := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	root, err := resolveRepoRoot(ctx, *repoDir)
	if err != nil {
		fmt.Fprintf(stderr, "flinch stats: %v\n", err)
		return 2
	}
	stateDir, err := gitutil.FlinchStateDir(ctx, root)
	if err != nil {
		fmt.Fprintf(stderr, "flinch stats: %v\n", err)
		return 2
	}
	reflexes, err := reflex.LoadAll(root)
	if err != nil {
		fmt.Fprintf(stderr, "flinch stats: %v\n", err)
		return 2
	}
	det, prob := 0, 0
	for _, r := range reflexes {
		if r.IsDeterministic() {
			det++
		} else {
			prob++
		}
	}

	sum, err := stats.NewStore(stateDir).Summarize(det, prob, "local clone event log")
	if err != nil {
		fmt.Fprintf(stderr, "flinch stats: %v\n", err)
		return 2
	}
	if *asJSON {
		return writeJSON(stdout, sum)
	}

	fmt.Fprintf(stdout, "Scope: %s\n", sum.ObservedScope)
	fmt.Fprintf(stdout, "Protections: %d deterministic, %d probabilistic guides\n", sum.DeterministicReflexes, sum.ProbabilisticGuides)
	fmt.Fprintf(stdout, "Turns checked: %d (p95 latency: %dms)\n", sum.TurnsChecked, sum.TurnLatencyP95MS)
	fmt.Fprintf(stdout, "Interceptions: turn=%d  ci=%d\n", sum.TurnInterceptions, sum.CIInterceptions)
	fmt.Fprintf(stdout, "Escapes: confirmed=%d  unexposed_surface=%d  possible_recurrences=%d (excluded from escape rate)\n",
		sum.ConfirmedEscapes, sum.UnexposedSurfaceEvents, sum.PossibleRecurrences)
	fmt.Fprintf(stdout, "Skipped checks: %d\n", sum.SkippedChecks)
	if sum.EscapeRatePer100Checked != nil {
		fmt.Fprintf(stdout, "Confirmed escapes per 100 checked turns: %.2f (%d/%d)\n",
			*sum.EscapeRatePer100Checked, sum.ConfirmedEscapes, sum.TurnsChecked)
	}
	return 0
}
