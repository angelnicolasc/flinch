package cli

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/angelnicolasc/flinch/internal/config"
	"github.com/angelnicolasc/flinch/internal/doctor"
)

func runInit(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repoDir := fs.String("repo", "", "repository directory (defaults to current working directory)")
	force := fs.Bool("force", false, "overwrite existing .flinch/flinch.toml")
	owner := fs.String("owner", "", "default owner written to policy.owner")
	mode := fs.String("mode", string(config.ModeLocalAuto), "preparation mode: local_auto | team_connected | observe_only")
	asJSON := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	root, err := resolveRepoRoot(ctx, *repoDir)
	if err != nil {
		fmt.Fprintf(stderr, "flinch init: %v\n", err)
		return 2
	}

	_, exists, loadErr := config.LoadOrDefault(root)
	if loadErr == nil && exists && !*force {
		fmt.Fprintf(stderr, "flinch init: %s already exists (pass --force to overwrite)\n", config.ConfigRelPath)
		return 2
	}

	cfg, notes := doctor.DiscoverConfig(ctx, root)
	cfg.Policy.Mode = config.PreparationMode(*mode)
	if *owner != "" {
		cfg.Policy.Owner = *owner
	}
	if err := config.Save(root, cfg); err != nil {
		fmt.Fprintf(stderr, "flinch init: %v\n", err)
		return 2
	}

	rep, err := doctor.Run(ctx, root, false)
	if err != nil {
		fmt.Fprintf(stderr, "flinch init: doctor check failed: %v\n", err)
		return 2
	}

	if *asJSON {
		return writeJSON(stdout, map[string]any{
			"config_path": config.ConfigRelPath,
			"notes":       notes,
			"report":      rep,
		})
	}

	fmt.Fprintf(stdout, "Initialized %s (mode=%s)\n", config.ConfigRelPath, cfg.Policy.Mode)
	for _, n := range notes {
		fmt.Fprintf(stdout, "  - %s\n", n)
	}
	printCapabilityMatrix(stdout, rep.Capabilities)
	return 0
}
