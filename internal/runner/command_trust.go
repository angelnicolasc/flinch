package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/angelnicolasc/flinch/internal/config"
	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/localstate"
	"github.com/angelnicolasc/flinch/internal/reflex"
)

// CommandPermissionNotice is also displayed by hooks that skip an unapproved check.
const CommandPermissionNotice = "Repository command or script needs approval. Review it and run flinch allow-commands."

var ErrCommandApproval = errors.New(CommandPermissionNotice)

type commandApprovalError struct{ digest string }

func (e *commandApprovalError) Error() string { return CommandPermissionNotice }
func (e *commandApprovalError) Unwrap() error { return ErrCommandApproval }

// CommandApprovalDigest identifies the rejected executable inputs for a passive hook notice.
func CommandApprovalDigest(err error) string {
	var rejected *commandApprovalError
	if errors.As(err, &rejected) {
		return rejected.digest
	}
	return ""
}

type repositoryKey struct{}

// WithCommandRepository carries the user's clone into disposable proof directories.
func WithCommandRepository(ctx context.Context, repoRoot string) context.Context {
	return context.WithValue(ctx, repositoryKey{}, repoRoot)
}

// EnableCommands approves the present definitions in the clone's check records and lint configuration.
func EnableCommands(ctx context.Context, repoRoot string) (bool, error) {
	checks, err := reflex.LoadAll(repoRoot)
	if err != nil {
		return false, err
	}
	cfg, _, err := config.LoadOrDefault(repoRoot)
	if err != nil {
		return false, err
	}
	return approveArtifacts(ctx, repoRoot, configuredCommandArtifacts(cfg, checks), nil)
}

func configuredCommandArtifacts(cfg config.Config, checks []reflex.Reflex) []reflex.Artifact {
	var artifacts []reflex.Artifact
	for _, check := range checks {
		if needsCommandApproval(check.Artifact) {
			artifacts = append(artifacts, check.Artifact)
		}
	}
	for _, lint := range cfg.Lint {
		art := reflex.Artifact{Tool: lint.Kind, Path: lint.Config, Command: lint.Command}
		if needsCommandApproval(art) {
			artifacts = append(artifacts, art)
		}
	}
	return artifacts
}

// CommandsNeedApproval inspects the same executable inputs as the runner without granting permission or running them.
func CommandsNeedApproval(ctx context.Context, repoRoot string, cfg config.Config, checks []reflex.Reflex) bool {
	artifacts := configuredCommandArtifacts(cfg, checks)
	if len(artifacts) == 0 {
		return false
	}
	approved, err := repositoryApprovals(ctx, repoRoot)
	if err != nil || len(approved) == 0 {
		return true
	}
	for _, art := range artifacts {
		if !matchesApproval(ctx, repoRoot, art, approved) {
			return true
		}
	}
	return false
}

// AllowCandidateCommand grants an unregistered proof candidate's exact executable inputs.
func AllowCandidateCommand(ctx context.Context, root string, art reflex.Artifact, overlay map[string]string) (bool, error) {
	if !needsCommandApproval(art) {
		return false, fmt.Errorf("candidate does not use a command, script, or custom vet analyzer")
	}
	return approveArtifacts(ctx, root, []reflex.Artifact{art}, overlay)
}

func needsCommandApproval(art reflex.Artifact) bool {
	return art.Tool == "command" || art.Tool == "script" || (art.Tool == "go-vet" && strings.Contains(art.Command, "-vettool="))
}

func approvalPath(ctx context.Context, repoRoot string) (string, error) {
	common, err := gitutil.CommonDir(ctx, repoRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(common, "flinch", "command-approvals.json"), nil
}

func readApprovals(path string) (map[string]string, error) {
	stat, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !stat.Mode().IsRegular() {
		return nil, fmt.Errorf("command approval store must be a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var approved map[string]string
	if err := json.Unmarshal(data, &approved); err != nil || approved == nil {
		return nil, fmt.Errorf("invalid command approvals in %s", path)
	}
	return approved, nil
}

func approveArtifacts(ctx context.Context, repoRoot string, artifacts []reflex.Artifact, overlay map[string]string) (bool, error) {
	pending := map[string]string{}
	for _, art := range artifacts {
		key, digest, err := commandDigest(ctx, repoRoot, art, overlay)
		if err != nil {
			return false, fmt.Errorf("approve %s %q: %w", art.Tool, art.Command, err)
		}
		pending[key] = digest
	}
	if len(pending) == 0 {
		return false, nil
	}
	path, err := approvalPath(ctx, repoRoot)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return false, err
	}
	release, locked, err := localstate.TryLock(path + ".lock")
	if err != nil {
		return false, err
	}
	if !locked {
		return false, fmt.Errorf("another process is approving commands, retry after it finishes")
	}
	defer release()
	approved, err := readApprovals(path)
	if err != nil {
		return false, err
	}
	changed := false
	for key, digest := range pending {
		if approved[key] != digest {
			approved[key], changed = digest, true
		}
	}
	if !changed {
		return false, nil
	}
	data, err := json.Marshal(approved)
	if err != nil {
		return false, err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".command-approvals-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(append(data, '\n'))
	if err := errors.Join(writeErr, f.Close(), ctx.Err()); err != nil {
		return false, err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return false, err
	}
	return true, nil
}

func checkCommandTrust(ctx context.Context, executionRoot string, art reflex.Artifact) error {
	source, ok := ctx.Value(repositoryKey{}).(string)
	if !ok {
		source = executionRoot
	}
	approved, err := repositoryApprovals(ctx, source)
	key, digest, digestErr := commandDigest(ctx, executionRoot, art, nil)
	if digestErr != nil {
		// An unsupported definition has no approvable file set. Its exact
		// definition still identifies the notice until the command is repaired.
		definition := commandKeyBytes(art)
		return &commandApprovalError{digest: hex.EncodeToString(definition[:])}
	}
	if err != nil || approved[key] != digest {
		return &commandApprovalError{digest: digest}
	}
	return nil
}

func repositoryApprovals(ctx context.Context, repoRoot string) (map[string]string, error) {
	path, err := approvalPath(ctx, repoRoot)
	if err != nil {
		return nil, err
	}
	return readApprovals(path)
}

func matchesApproval(ctx context.Context, executionRoot string, art reflex.Artifact, approved map[string]string) bool {
	key, digest, err := commandDigest(ctx, executionRoot, art, nil)
	return err == nil && approved[key] == digest
}

// Only accept literal arguments whose executable inputs can be identified before the shell runs.
// Complex shell logic belongs in a named, reviewable repository script.
func literalCommandWords(command string) ([]string, error) {
	if strings.ContainsAny(command, "$%`^!;&|<>(){}[]*?~\r\n\x00") ||
		(runtime.GOOS != "windows" && strings.Contains(command, `\`)) {
		return nil, errIndirectCommand
	}
	var words []string
	for rest := strings.Trim(command, " \t"); rest != ""; rest = strings.TrimLeft(rest, " \t") {
		switch rest[0] {
		case '"', '\'':
			quote := rest[:1]
			if quote == "'" && runtime.GOOS == "windows" {
				return nil, errIndirectCommand
			}
			word, tail, closed := strings.Cut(rest[1:], quote)
			if !closed || (tail != "" && tail[0] != ' ' && tail[0] != '\t') {
				return nil, errIndirectCommand
			}
			words = append(words, word)
			rest = tail
		default:
			end := strings.IndexAny(rest, " \t")
			if end < 0 {
				end = len(rest)
			}
			if strings.ContainsAny(rest[:end], "\"'") {
				return nil, errIndirectCommand
			}
			words = append(words, rest[:end])
			rest = rest[end:]
		}
	}
	if len(words) == 0 || words[0] == "" {
		return nil, errIndirectCommand
	}
	return words, nil
}

var errIndirectCommand = errors.New("use a literal command or invoke a reviewed repository script by its path")

func commandDigest(ctx context.Context, dir string, art reflex.Artifact, overlay map[string]string) (string, string, error) {
	if strings.TrimSpace(art.Command) == "" {
		return "", "", errors.New("command artifact requires artifact.command")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", "", err
	}
	defer root.Close()
	files := make(map[string]bool)
	if art.Path != "" {
		files[art.Path] = true
	}
	words, err := literalCommandWords(art.Command)
	if err != nil {
		return "", "", err
	}
	if err := executableInputs(root, art, words, overlay, files); err != nil {
		return "", "", err
	}
	key := commandKeyBytes(art)
	digest := sha256.New()
	digest.Write(key[:])
	for _, name := range slices.Sorted(maps.Keys(files)) {
		local, err := gitutil.LocalPath(".", name)
		if err != nil {
			return "", "", err
		}
		if err := ctx.Err(); err != nil {
			return "", "", err
		}
		content, err := approvalFileHash(root, local, overlay)
		if err != nil {
			return "", "", fmt.Errorf("read command input %q: %w", name, err)
		}
		fmt.Fprintf(digest, "\x00%s\x00%x", name, content)
	}
	return hex.EncodeToString(key[:]), hex.EncodeToString(digest.Sum(nil)), nil
}

func commandKeyBytes(art reflex.Artifact) [sha256.Size]byte {
	return sha256.Sum256([]byte(art.Tool + "\x00" + art.Path + "\x00" + art.Command))
}

func executableInputs(root *os.Root, art reflex.Artifact, words []string, overlay map[string]string, files map[string]bool) error {
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(words[0])), ".exe")
	if art.Tool == "go-vet" {
		for _, arg := range words {
			if path, found := strings.CutPrefix(arg, "-vettool="); found {
				files[path] = true
			}
		}
		return nil
	}
	executableFile := strings.ContainsAny(words[0], `/\`)
	if executableFile {
		if runtime.GOOS == "windows" && filepath.Ext(words[0]) == "" {
			return errIndirectCommand
		}
		files[words[0]] = true
	}
	switch name {
	case "echo", "printf", "true", "false", "pwd", "exit":
		return nil
	case "sh", "bash", "dash", "zsh", "node", "ruby", "python", "python3", "perl", "pwsh", "powershell", "cmd":
		args := words[1:]
		switch name {
		case "cmd":
			if len(args) == 0 || !strings.EqualFold(args[0], "/c") {
				return errIndirectCommand
			}
			args = args[1:]
		case "pwsh", "powershell":
			if len(args) == 0 || !strings.EqualFold(args[0], "-file") {
				return errIndirectCommand
			}
			args = args[1:]
		}
		if len(args) == 0 || strings.HasPrefix(args[0], "-") || strings.HasPrefix(args[0], "/") {
			return errIndirectCommand
		}
		if name == "cmd" {
			switch strings.ToLower(filepath.Ext(args[0])) {
			case ".cmd", ".bat":
			default:
				return errIndirectCommand
			}
		}
		files[args[0]] = true
		return nil
	case "go":
		if len(words) < 3 || words[1] != "run" {
			return errIndirectCommand
		}
		if strings.HasSuffix(words[2], ".go") {
			for _, arg := range words[2:] {
				if !strings.HasSuffix(arg, ".go") {
					break
				}
				files[arg] = true
			}
			return nil
		}
		if words[2] == "." || strings.HasPrefix(words[2], "./") {
			return addGoCommandFiles(root, words[2], overlay, files)
		}
	default:
		if executableFile {
			return nil
		}
	}
	return errIndirectCommand
}

// Include the named package's Go and native source inputs, including proof overlays.
func addGoCommandFiles(root *os.Root, dir string, overlay map[string]string, files map[string]bool) error {
	dir = filepath.Clean(filepath.FromSlash(dir))
	entries, err := fs.ReadDir(root.FS(), filepath.ToSlash(dir))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() && goCommandSource(entry.Name()) {
			files[filepath.ToSlash(filepath.Join(dir, entry.Name()))] = true
		}
	}
	for name := range overlay {
		if filepath.Dir(filepath.FromSlash(name)) == dir && goCommandSource(name) {
			files[filepath.ToSlash(filepath.Clean(name))] = true
		}
	}
	return nil
}

func goCommandSource(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".go":
		return !strings.HasSuffix(name, "_test.go")
	case ".s", ".syso", ".c", ".cc", ".cpp", ".cxx", ".h", ".hh", ".hpp", ".hxx", ".m", ".f", ".for", ".f90", ".swig", ".swigcxx":
		return true
	}
	return false
}

func approvalFileHash(root *os.Root, local string, overlay map[string]string) ([]byte, error) {
	if data, ok := overlay[filepath.ToSlash(local)]; ok {
		sum := sha256.Sum256([]byte(data))
		return sum[:], nil
	}
	stat, err := root.Stat(local)
	if err != nil {
		return nil, err
	}
	if !stat.Mode().IsRegular() {
		return nil, errors.New("expected a regular file inside the repository")
	}
	f, err := root.Open(local)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, f); err != nil {
		return nil, err
	}
	return digest.Sum(nil), nil
}
