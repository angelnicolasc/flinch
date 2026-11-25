package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/angelnicolasc/flinch/internal/prove"
	"github.com/angelnicolasc/flinch/internal/runner"
	"gopkg.in/yaml.v3"
)

func runAllowCommands(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("allow-commands", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", "", "repository directory")
	specPath := fs.String("spec", "", "allow a reviewed YAML or JSON proof candidate")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "flinch allow-commands: unexpected positional arguments")
		return 2
	}
	root, err := resolveRepoRoot(ctx, *repo)
	if err != nil {
		fmt.Fprintf(stderr, "flinch allow-commands: %v\n", err)
		return 2
	}
	var changed bool
	if *specPath == "" {
		changed, err = runner.EnableCommands(ctx, root)
	} else {
		var spec prove.CandidateSpec
		var data []byte
		data, err = os.ReadFile(*specPath)
		if err == nil {
			err = yaml.Unmarshal(data, &spec)
		}
		if err == nil {
			changed, err = runner.AllowCandidateCommand(ctx, root, spec.Artifact, spec.OverlayFiles)
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "flinch allow-commands: %v\n", err)
		return 2
	}
	if changed {
		fmt.Fprintln(stdout, "Approved the current command definitions and script contents for this clone.")
	} else {
		fmt.Fprintln(stdout, "No new command approvals for this clone.")
	}
	return 0
}
