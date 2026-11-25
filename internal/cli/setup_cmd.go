package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/angelnicolasc/flinch/internal/usersetup"
	"github.com/angelnicolasc/flinch/internal/workspacetrust"
)

func runSetup(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	preview := fs.Bool("plan", false, "show user files, hook commands, and consent without writing")
	yes := fs.Bool("yes", false, "apply the displayed setup without an interactive confirmation")
	remove := fs.Bool("remove", false, "remove Flinch user hooks and revoke their consent")
	host := fs.String("host", "", "limit setup to these comma-separated hosts: claude,codex,antigravity")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "flinch setup: unexpected positional arguments")
		return 2
	}
	plan, err := usersetup.Prepare(*host, *remove)
	if err != nil {
		fmt.Fprintln(stderr, "flinch setup:", err)
		return 2
	}
	fmt.Fprint(stdout, plan.Describe())
	if !*remove {
		cwd, _ := os.Getwd()
		for _, name := range plan.Hosts {
			local := workspacetrust.Evaluate(ctx, name, cwd, nil)
			var surface, action string
			switch name {
			case "claude":
				surface = "Claude Code CLI and Desktop"
				action = " A Desktop session can also authorize its clone and registered worktrees."
			case "codex":
				surface = "Codex CLI and Desktop"
				action = " Review the registered hooks in Codex."
				if local.Root == "" {
					if reason := workspacetrust.ManagedCodexReason(); reason != "" {
						local.Active = false
						local.Reason = reason
					}
				}
			case "antigravity":
				surface = "Antigravity CLI, App and IDE"
			}
			state := "inactive"
			if local.Active {
				state = "active"
			}
			fmt.Fprintf(stdout, "%s here after setup: %s (%s).%s\n", surface, state, local.Reason, action)
		}
		if slices.Contains(plan.Hosts, "claude") || slices.Contains(plan.Hosts, "antigravity") {
			fmt.Fprintln(stdout, "Use flinch enable to grant Claude Code and Antigravity access to a reviewed clone or flinch disable to revoke that grant.")
		}
		fmt.Fprintln(stdout, "When you correct the agent, Flinch can run the repository's own tests and linters with your user permissions to turn the correction into a check. These runs bypass the agent's per-command approval. Flinch's hooks make no network calls. Repository tools retain their own network access.")
		fmt.Fprintln(stdout, "Repository-defined command and script checks require flinch allow-commands after you review their contents.")
		fmt.Fprintln(stdout, "Turn Flinch off in a repository with flinch pause, for a session with FLINCH=off, or everywhere with flinch setup --remove.")
	}
	if *preview {
		return 0
	}
	if !*yes {
		info, err := os.Stdin.Stat()
		if err != nil || info.Mode()&os.ModeCharDevice == 0 {
			fmt.Fprintln(stderr, "flinch setup: noninteractive setup requires --yes after reviewing --plan")
			return 2
		}
		fmt.Fprint(stdout, "Apply these changes? [y/N] ")
		response, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil || (strings.ToLower(strings.TrimSpace(response)) != "y" && strings.ToLower(strings.TrimSpace(response)) != "yes") {
			fmt.Fprintln(stdout, "Setup canceled.")
			return 1
		}
	}
	if err := ctx.Err(); err != nil {
		fmt.Fprintln(stderr, "flinch setup:", err)
		return 1
	}
	if err := plan.Apply(); err != nil {
		fmt.Fprintln(stderr, "flinch setup:", err)
		return 1
	}
	fmt.Fprintln(stdout, "User hook configuration applied.")
	return 0
}
