package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/angelnicolasc/flinch/internal/gitutil"
)

// Version is overridden at build time with -ldflags "-X github.com/angelnicolasc/flinch/internal/cli.Version=...".
var Version = "0.1.0-dev"

// Run executes the Flinch CLI and returns a process exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stdout)
		return 0
	}
	cmd := args[0]
	rest := args[1:]

	switch cmd {
	case "-h", "--help", "help":
		printUsage(stdout)
		return 0
	case "version", "--version":
		fmt.Fprintf(stdout, "flinch %s\n", Version)
		return 0
	case "init":
		return runInit(ctx, rest, stdout, stderr)
	case "prove":
		return runProve(ctx, rest, stdout, stderr)
	case "check":
		return runCheck(ctx, rest, stdout, stderr)
	case "checkpoint":
		return runCheckpoint(ctx, rest, stdout, stderr)
	case "signal":
		return runSignal(ctx, rest, stdout, stderr)
	case "queue":
		return runQueue(ctx, rest, stdout, stderr)
	case "doctor":
		return runDoctor(ctx, rest, stdout, stderr)
	case "stats":
		return runStats(ctx, rest, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "flinch: unknown command %q\n\n", cmd)
		printUsage(stderr)
		return 2
	}
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `flinch - deterministic verification and enforcement for repository protections

Usage:
  flinch <command> [flags]

Commands:
  init        Discover repository tools/workflows and write .flinch/flinch.toml
  prove       Verify a candidate protection against broken, fixed, and allowed cases
  check       Run active protections (--turn, --ci) or accredit an integrated SHA (--accredit)
  checkpoint  Capture a working-tree baseline for an interactive agent turn
  signal      Record a deduplicated signal into the local episode queue
  queue       Inspect, claim, release, suspend, or record deliveries on queued episodes
  doctor      Inspect installation capability matrix, accreditation freshness, and worktrees
  stats       Show local enforcement counts, denominators, omissions, and turn p95 latency
  version     Print CLI version
`)
}

func resolveRepoRoot(ctx context.Context, explicitDir string) (string, error) {
	dir := explicitDir
	if strings.TrimSpace(dir) == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	return gitutil.RepoRoot(ctx, dir)
}

func writeJSON(w io.Writer, v any) int {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return 2
	}
	return 0
}
