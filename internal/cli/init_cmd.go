package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/angelnicolasc/flinch/internal/config"
	"github.com/angelnicolasc/flinch/internal/doctor"
	"github.com/angelnicolasc/flinch/internal/hook"
	"github.com/angelnicolasc/flinch/internal/runner"
)

func runInit(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repoDir := fs.String("repo", "", "repository directory (defaults to current working directory)")
	force := fs.Bool("force", false, "replace a readable .flinch/flinch.toml with explicit overrides, or remove it when defaults apply")
	owner := fs.String("owner", "", "default owner written to policy.owner")
	mode := fs.String("mode", string(config.ModeLocalAuto), "preparation mode: local_auto | observe_only")
	hostsCSV := fs.String("hooks", "", "comma-separated host hooks to install idempotently: claude,codex,antigravity")
	flinchBin := fs.String("bin", "flinch", "flinch command path written into installed hooks")
	asJSON := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	selectedMode := config.PreparationMode(*mode)
	if selectedMode != config.ModeLocalAuto && selectedMode != config.ModeObserveOnly {
		fmt.Fprintf(stderr, "flinch init: invalid --mode %q (supported: local_auto, observe_only)\n", *mode)
		return 2
	}

	root, err := resolveRepoRoot(ctx, *repoDir)
	if err != nil {
		fmt.Fprintf(stderr, "flinch init: %v\n", err)
		return 2
	}

	cfgPath := filepath.Join(root, filepath.FromSlash(config.ConfigRelPath))
	unchanged := false
	var notes []string

	if _, statErr := os.Stat(cfgPath); statErr == nil {
		// D10: Never overwrite an existing configuration that cannot be read or parsed.
		if _, loadErr := config.Load(root); loadErr != nil {
			fmt.Fprintf(stderr, "flinch init: existing %s could not be read or validated and will not be overwritten: %v\n", config.ConfigRelPath, loadErr)
			return 2
		}
		if !*force {
			unchanged = true
			notes = append(notes, fmt.Sprintf("Kept existing %s unchanged.", config.ConfigRelPath))
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		fmt.Fprintf(stderr, "flinch init: cannot access %s: %v\n", config.ConfigRelPath, statErr)
		return 2
	}

	if !unchanged {
		changed, err := config.SaveSetup(root, *owner, selectedMode, *force)
		unchanged = !changed
		if err != nil {
			fmt.Fprintf(stderr, "flinch init: %v\n", err)
			return 2
		}
	}

	var hookResults []hook.InstallResult
	if strings.TrimSpace(*hostsCSV) != "" {
		for _, part := range strings.Split(*hostsCSV, ",") {
			h := hook.Host(strings.ToLower(strings.TrimSpace(part)))
			if h == "" {
				continue
			}
			hr, hErr := hook.InstallHooks(root, h, *flinchBin)
			if hErr != nil {
				fmt.Fprintf(stderr, "flinch init: install %s hooks: %v\n", h, hErr)
				return 2
			}
			hookResults = append(hookResults, hr)
			if hr.Changed {
				notes = append(notes, fmt.Sprintf("Installed %s hooks in %s.", h, hr.Path))
			} else {
				notes = append(notes, fmt.Sprintf("Verified %s hooks in %s (unchanged).", h, hr.Path))
			}
		}
	}

	enabled, err := runner.EnableCommands(ctx, root)
	if err != nil {
		fmt.Fprintf(stderr, "flinch init: %v\n", err)
		return 2
	}
	if enabled {
		notes = append(notes, "Enabled repository command and script checks for this clone.")
	}
	rep, err := doctor.Run(ctx, root, false)
	if err != nil {
		fmt.Fprintf(stderr, "flinch init: doctor check failed: %v\n", err)
		return 2
	}

	if *asJSON {
		return writeJSON(stdout, map[string]any{
			"config_path": config.ConfigRelPath,
			"unchanged":   unchanged,
			"hooks":       hookResults,
			"notes":       notes,
			"report":      rep,
		})
	}

	if !rep.ConfigPresent {
		fmt.Fprintf(stdout, "Using discovered repository defaults (mode=%s)\n", rep.PolicyMode)
	} else if unchanged {
		fmt.Fprintf(stdout, "Using repository settings (mode=%s)\n", rep.PolicyMode)
	} else {
		fmt.Fprintf(stdout, "Initialized %s (mode=%s)\n", config.ConfigRelPath, rep.PolicyMode)
	}
	for _, n := range notes {
		fmt.Fprintf(stdout, "  - %s\n", n)
	}
	printCapabilityMatrix(stdout, rep.Capabilities)
	return 0
}
