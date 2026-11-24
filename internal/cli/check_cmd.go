package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/angelnicolasc/flinch/internal/check"
	"github.com/angelnicolasc/flinch/internal/config"
	"github.com/angelnicolasc/flinch/internal/evidence"
	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/stats"
)

func runCheck(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repoDir := fs.String("repo", "", "repository directory")
	turnMode := fs.Bool("turn", false, "run interactive agent-turn check against turn checkpoint")
	ciMode := fs.Bool("ci", false, "run shared CI check")
	accreditMode := fs.Bool("accredit", false, "verify activation on an integrated SHA with negative and allowed controls")
	baseRef := fs.String("base", "", "base Git ref for CI diff baseline")
	sessionID := fs.String("session", "default", "agent session identifier (for --turn)")
	integrity := fs.Bool("integrity", true, "check reviewed checks for edits and newly added suppressions")
	format := fs.String("format", "text", "output format: text | json | github | sarif")
	integratedRef := fs.String("ref", "HEAD", "integrated Git ref to accredit (for --accredit)")
	reflexID := fs.String("reflex", "", "optional single check ID to accredit")
	surface := fs.String("surface", string(evidence.SurfaceCI), "accreditation surface: ci | agent_turn")
	hostVersion := fs.String("host-version", "", "host/client version recorded in accreditation evidence")
	requiredMerge := fs.Bool("required-for-merge", false, "mark CI check as required by repository merge policy")
	witnessEvent := fs.String("witness-event", "", "witnessed workflow trigger event: pull_request | merge_group | workflow_dispatch")
	witnessJob := fs.String("witness-job", "", "witnessed CI job name")
	witnessRun := fs.String("witness-run", "", "witnessed CI run ID")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	root, err := resolveRepoRoot(ctx, *repoDir)
	if err != nil {
		fmt.Fprintf(stderr, "flinch check: %v\n", err)
		return 2
	}
	cfg, _, err := config.LoadOrDefault(root)
	if err != nil {
		fmt.Fprintf(stderr, "flinch check: %v\n", err)
		return 2
	}

	if *accreditMode {
		var witness *evidence.TriggerWitness
		if *witnessEvent != "" {
			witness = &evidence.TriggerWitness{
				Witnessed:  true,
				Event:      *witnessEvent,
				Job:        *witnessJob,
				RunID:      *witnessRun,
				ObservedAt: time.Now().UTC(),
			}
		}
		recs, accErr := check.Accredit(ctx, root, cfg, check.AccreditOptions{
			IntegratedRef:    *integratedRef,
			ReflexID:         *reflexID,
			Surface:          evidence.SurfaceKind(*surface),
			HostVersion:      *hostVersion,
			RequiredForMerge: *requiredMerge,
			TriggerWitness:   witness,
		})
		if accErr != nil {
			fmt.Fprintf(stderr, "flinch check --accredit: %v\n", accErr)
			return 2
		}
		if *format == "json" {
			_ = writeJSON(stdout, recs)
		} else {
			for _, rec := range recs {
				fmt.Fprintf(stdout, "%s [%s] sha=%s status=%s (%s)\n",
					rec.ReflexID, rec.Surface, shortSHA(rec.IntegratedSHA), rec.Status, rec.Summary)
			}
		}
		for _, rec := range recs {
			if rec.Status == evidence.SurfaceFailed {
				return 1
			}
		}
		return 0
	}

	var res check.Result
	if *turnMode && !*ciMode {
		res, err = check.RunTurn(ctx, root, cfg, check.TurnOptions{
			SessionID:  *sessionID,
			WorktreeID: root,
			CheckIntg:  *integrity,
		})
	} else {
		res, err = check.RunCI(ctx, root, cfg, check.CIOptions{
			BaseRef:   *baseRef,
			CheckIntg: *integrity,
		})
	}
	if err != nil {
		fmt.Fprintf(stderr, "flinch check: %v\n", err)
		return 2
	}

	// Append local telemetry-free metrics observation.
	if stateDir, sdErr := gitutil.FlinchStateDir(ctx, root); sdErr == nil {
		st := stats.NewStore(stateDir)
		if res.Surface == "turn" {
			_ = st.Record(stats.Event{Kind: stats.EventTurnChecked, Surface: "turn", DurationMS: res.DurationMS})
			if res.Blocking {
				_ = st.Record(stats.Event{Kind: stats.EventTurnIntercepted, Surface: "turn"})
			}
			for _, sk := range res.SkippedControls {
				_ = st.Record(stats.Event{Kind: stats.EventCheckSkipped, ReflexID: sk.ReflexID, Surface: "turn", Reason: sk.Reason})
			}
		} else if res.Blocking {
			_ = st.Record(stats.Event{Kind: stats.EventCIIntercepted, Surface: "ci"})
		}
	}

	switch *format {
	case "json":
		_ = writeJSON(stdout, res)
	case "github":
		fmt.Fprint(stdout, check.FormatGitHubAnnotations(res))
	case "sarif":
		b, sErr := check.FormatSARIF(res)
		if sErr != nil {
			fmt.Fprintf(stderr, "flinch check: sarif encode: %v\n", sErr)
			return 2
		}
		fmt.Fprintln(stdout, string(b))
	default:
		if res.DiagnosticMessage != "" {
			fmt.Fprintln(stdout, res.DiagnosticMessage)
		} else {
			fmt.Fprintf(stdout, "flinch check (%s): %s (findings=%d, skipped=%d, duration=%dms)\n",
				res.Surface, res.Decision, len(res.Findings), len(res.SkippedControls), res.DurationMS)
		}
	}

	if res.Blocking {
		return 1
	}
	return 0
}
