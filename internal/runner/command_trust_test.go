package runner

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/angelnicolasc/flinch/internal/config"
	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/reflex"
)

func TestCommandApprovalMatchesTheCommandAndScriptBytes(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if _, _, err := gitutil.Run(ctx, root, nil, "init"); err != nil {
		t.Fatal(err)
	}
	path := "guard with space.sh"
	if err := os.WriteFile(filepath.Join(root, path), []byte("approved bytes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command := reflex.Artifact{Tool: "command", Command: "echo approved"}
	script := reflex.Artifact{Tool: "script", Path: path, Command: "echo checked"}
	check := reflex.Reflex{ID: "command-guard", Version: 1, Obligation: "Run the reviewed guard", Artifact: command}
	if _, err := reflex.Save(root, check); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Lint = []config.LintConfig{{Kind: "script", Config: path, Command: script.Command}}
	if err := config.Save(root, cfg); err != nil {
		t.Fatal(err)
	}
	requireApproval := func(ctx context.Context, dir string, art reflex.Artifact) {
		t.Helper()
		if _, err := RunArtifact(ctx, dir, art, nil, time.Second); err == nil || !strings.Contains(err.Error(), "flinch allow-commands") {
			t.Fatalf("unapproved %q in %q: got %v", art.Command, dir, err)
		}
	}
	run := func(ctx context.Context, dir string, art reflex.Artifact) {
		t.Helper()
		if result, err := RunArtifact(ctx, dir, art, nil, time.Second); err != nil || result.ExitCode != 0 {
			t.Fatalf("approved %q: %v, exit %d", art.Command, err, result.ExitCode)
		}
	}
	state, err := gitutil.FlinchStateDir(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "commands-enabled"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	requireApproval(ctx, root, command)
	requireApproval(ctx, root, script)
	if changed, err := EnableCommands(ctx, root); err != nil || !changed {
		t.Fatalf("first content approval: changed=%t err=%v", changed, err)
	}
	run(ctx, root, command)
	run(ctx, root, script)
	if changed, err := EnableCommands(ctx, root); err != nil || changed {
		t.Fatalf("identical content: changed=%t err=%v", changed, err)
	}
	modified := command
	modified.Command += " again"
	requireApproval(ctx, root, modified)
	otherTree := t.TempDir()
	if err := os.WriteFile(filepath.Join(otherTree, path), []byte("unreviewed overlay\n"), 0600); err != nil {
		t.Fatal(err)
	}
	requireApproval(WithCommandRepository(ctx, root), otherTree, script)
	if err := os.WriteFile(filepath.Join(root, path), []byte("edited\n"), 0600); err != nil {
		t.Fatal(err)
	}
	requireApproval(ctx, root, script)
	run(ctx, root, command)
	if changed, err := EnableCommands(ctx, root); err != nil || !changed {
		t.Fatalf("approve edited script: changed=%t err=%v", changed, err)
	}
	run(ctx, root, script)
}

func TestCommandApprovalBindsCustomAnalyzerAndScriptArguments(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if _, _, err := gitutil.Run(ctx, root, nil, "init"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Lint = []config.LintConfig{
		{Kind: "go-vet", Command: "go vet -vettool=./local-analyzer"},
		{Kind: "script", Command: "sh \"./scan sources.sh\""},
	}
	if err := config.Save(root, cfg); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"local-analyzer", "scan sources.sh"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("initial\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := EnableCommands(ctx, root); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"local-analyzer", "scan sources.sh"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("changed\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, lint := range cfg.Lint {
		if _, err := RunArtifact(ctx, root, reflex.Artifact{Tool: lint.Kind, Command: lint.Command}, nil, time.Second); err == nil || !strings.Contains(err.Error(), "flinch allow-commands") {
			t.Fatalf("changed %s executed or returned the wrong diagnostic: %v", lint.Command, err)
		}
	}
}

func TestCommandApprovalRejectsIndirectScriptSelection(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	if _, _, err := gitutil.Run(ctx, root, nil, "init"); err != nil {
		t.Fatal(err)
	}
	// A literal script path is reviewable even when it has no file extension.
	path := filepath.Join(root, "check inputs")
	if err := os.WriteFile(path, []byte("echo approved\n"), 0600); err != nil {
		t.Fatal(err)
	}
	art := reflex.Artifact{Tool: "script", Command: `sh "check inputs"`}
	if _, err := AllowCandidateCommand(ctx, root, art, nil); err != nil {
		t.Fatal(err)
	}
	if err := checkCommandTrust(ctx, root, art); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("echo changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkCommandTrust(ctx, root, art); err == nil {
		t.Fatal("approval survived an edit to the extensionless script")
	}
	for _, tool := range []string{"command", "script"} {
		for _, command := range []string{`sh "$SCRIPT_PATH"`, `sh %SCRIPT_PATH%`, `sh check" inputs"`, `sh -c "echo sample"`, `cmd /c call %SCRIPT_PATH%`, `powershell "check inputs"`, `python -m repo_script`, `npm run check`} {
			candidate := reflex.Artifact{Tool: tool, Command: command}
			if _, err := AllowCandidateCommand(ctx, root, candidate, nil); err == nil {
				t.Errorf("approved an indirect %s: %q", tool, command)
			}
		}
	}
}

func TestCommandProcessPreservesLiteralInterpreterArguments(t *testing.T) {
	cmd, err := commandProcess(context.Background(), `python "check inputs" sample`)
	if err != nil || !slices.Equal(cmd.Args, []string{"python", "check inputs", "sample"}) {
		t.Fatalf("literal interpreter args: cmd=%v err=%v", cmd, err)
	}
	if runtime.GOOS == "windows" {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "check"), []byte("sample"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := commandDigest(context.Background(), root, reflex.Artifact{Tool: "command", Command: "./check"}, nil); err == nil {
			t.Fatal("approved a Windows executable with an implicit extension")
		}
	}
}
