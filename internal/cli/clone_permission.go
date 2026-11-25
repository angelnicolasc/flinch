package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/angelnicolasc/flinch/internal/workspacetrust"
)

func runClonePermission(ctx context.Context, action string, args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintf(stderr, "usage: flinch %s\n", action)
		return 2
	}
	cwd, err := os.Getwd()
	if err == nil {
		err = workspacetrust.SetCloneEnabled(ctx, cwd, action == "enable")
	}
	if err != nil {
		fmt.Fprintf(stderr, "flinch %s: %v\n", action, err)
		return 2
	}
	if action == "enable" {
		fmt.Fprintln(stdout, "Clone permission granted for Claude Code and Antigravity. A clone pause still takes precedence.")
	} else {
		fmt.Fprintln(stdout, "Clone permission revoked for Claude Code and Antigravity. Saved host trust still applies.")
	}
	fmt.Fprintln(stdout, "Codex uses its own workspace trust. Run flinch doctor to see the current decision.")
	return 0
}
