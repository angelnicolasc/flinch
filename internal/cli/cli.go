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
	case "setup":
		return runSetup(ctx, rest, stdout, stderr)
	case "enable", "disable":
		return runClonePermission(ctx, cmd, rest, stdout, stderr)
	case "allow-commands":
		return runAllowCommands(ctx, rest, stdout, stderr)
	case "init":
		return runInit(ctx, rest, stdout, stderr)
	case "prove":
		return runProve(ctx, rest, stdout, stderr)
	case "check":
		return runCheck(ctx, rest, stdout, stderr)
	case "hook":
		return runHook(ctx, rest, stdout, stderr)
	case "pause":
		return runPause(ctx, rest, stdout, stderr)
	case "resume":
		return runResume(ctx, rest, stdout, stderr)
	case "skip":
		return runSkip(ctx, rest, stdout, stderr)
	case "report":
		return runReport(ctx, rest, stdout, stderr)
	case "checkpoint":
		return runCheckpoint(ctx, rest, stdout, stderr)
	case "signal":
		return runSignal(ctx, rest, stdout, stderr)
	case "queue":
		return runQueue(ctx, rest, stdout, stderr)
	case "doctor":
		return runDoctor(ctx, rest, stdout, stderr)
	case "cleanup":
		return runCleanup(ctx, rest, stdout, stderr)
	case "stats":
		return runStats(ctx, rest, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "flinch: unknown command %q\n\n", cmd)
		printUsage(stderr)
		return 2
	}
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `flinch - turn corrections to coding agents into checks your repository runs

Usage:
  flinch <command> [flags]

Commands:
  setup       Preview, install or remove user hooks (--plan, --yes, --host, --remove)
  init        Configure a team repository (optional --shared --hooks <host>)
  enable      Grant Claude Code and Antigravity permission in this clone and its worktrees
  disable     Revoke this clone's explicit permission for Claude Code and Antigravity
  allow-commands  Approve the current contents of reviewed repository commands and scripts
  prove       Prove a candidate check against the original mistake, the fix and an allowed case
  check       Run the reviewed checks at the end of a turn (--turn) or in CI (--ci)
  hook        Entry point the host hooks call (--host <host> --event <event>)
  pause       Pause Flinch in this clone and its worktrees
  resume      Resume Flinch in this clone
  skip        Close a nominated correction with a reason
  report      Record a disputed finding without disabling the check
  doctor      Show hosts, discovered tools and local diagnostics
  cleanup     Remove local state older than retention_days
  stats       Show local check and turn counts
  version     Print the CLI version
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
