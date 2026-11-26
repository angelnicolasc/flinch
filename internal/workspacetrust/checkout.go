package workspacetrust

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/angelnicolasc/flinch/internal/gitutil"
)

type checkout struct {
	cwd, root, common string
	worktreeGitDir    string // Original gitdir spelling used by Codex for the linked main checkout.
}

// Resolve Git metadata without invoking Git, loading a repository's configuration,
// or creating an object. A linked checkout must be registered by its main repository.
func findCheckout(ctx context.Context, path string) (checkout, error) {
	var c checkout
	for _, key := range []string{"GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES"} {
		if os.Getenv(key) != "" {
			return c, errors.New("Git environment overrides make the repository identity ambiguous")
		}
	}
	if !filepath.IsAbs(path) {
		return c, errors.New("the host did not identify an absolute working directory")
	}
	cwd, err := filepath.EvalSymlinks(path)
	if err != nil || !isDir(cwd) {
		return c, errors.New("the host working directory is unavailable")
	}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if err := ctx.Err(); err != nil {
			return c, err
		}
		marker := filepath.Join(dir, ".git")
		info, err := os.Lstat(marker)
		if err == nil {
			var common, worktreeDir string
			switch {
			case info.IsDir():
				if !validCommon(marker) {
					return c, errors.New("the Git directory is incomplete or ambiguous")
				}
				common = marker
			case info.Mode().IsRegular():
				worktreeDir, err = readWorktreeGitDir(marker)
				if err == nil {
					common, err = linkedCommon(marker, worktreeDir)
				}
				if err != nil {
					return c, err
				}
			default:
				return c, errors.New("the Git directory is indirect")
			}
			if within(common, cwd) || insideGitMetadata(cwd, dir) {
				return c, errors.New("the host working directory is inside Git metadata")
			}
			return checkout{cwd: cwd, root: dir, common: common, worktreeGitDir: worktreeDir}, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return c, err
		}
		if validCommon(dir) {
			return c, errors.New("the host working directory is inside a bare repository")
		}
		if next := filepath.Dir(dir); next == dir {
			break
		}
	}
	return c, errors.New("the host working directory is outside a Git checkout")
}

// After the host trust and pause checks, confirm the Git implementation used by
// subsequent commands agrees with the physical checkout. A core.worktree config
// or a bare repository can otherwise redirect commands despite the .git layout.
func (c checkout) verifyGit(ctx context.Context) error {
	out, _, err := gitutil.Run(ctx, c.cwd, []string{"GIT_OPTIONAL_LOCKS=0"}, "rev-parse", "--show-toplevel", "--git-common-dir")
	if err != nil {
		return errors.New("Git could not verify the working tree")
	}
	paths := strings.Split(strings.TrimSuffix(strings.ReplaceAll(out, "\r\n", "\n"), "\n"), "\n")
	if len(paths) != 2 {
		return errors.New("Git reported an ambiguous working tree")
	}
	for i, expected := range []string{c.root, c.common} {
		path := paths[i]
		if !filepath.IsAbs(path) {
			path = filepath.Join(c.cwd, path)
		}
		physical, err := filepath.EvalSymlinks(path)
		if err != nil || !samePath(physical, expected) {
			return errors.New("Git resolves a different working tree or common directory")
		}
	}
	return nil
}

func insideGitMetadata(cwd, root string) bool {
	rel, err := filepath.Rel(root, cwd)
	if err != nil {
		return true
	}
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		if component == ".git" || runtime.GOOS == "windows" && strings.EqualFold(component, ".git") {
			return true
		}
	}
	return false
}

func validCommon(path string) bool {
	// A normal checkout has an actual Git directory. A commondir redirect in it
	// would make Git and this resolver disagree about the owner of local state.
	if _, err := os.Lstat(filepath.Join(path, "commondir")); !errors.Is(err, os.ErrNotExist) {
		return false
	}
	head, err := os.Lstat(filepath.Join(path, "HEAD"))
	return err == nil && head.Mode().IsRegular() && isDir(filepath.Join(path, "objects")) && isDir(filepath.Join(path, "refs"))
}

func isDir(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir()
}

// Codex uses Rust's byte-slice trim_ascii on Git metadata. Other whitespace
// can be part of a directory name and must retain its filesystem identity.
const gitPointerWhitespace = " \t\n\f\r"

func readWorktreeGitDir(marker string) (string, error) {
	data, err := readRegular(marker, 65536)
	if err != nil {
		return "", err
	}
	value, ok := strings.CutPrefix(strings.Trim(string(data), gitPointerWhitespace), "gitdir: ")
	if !ok || value == "" || strings.ContainsAny(value, "\n\r\x00") {
		return "", errors.New("invalid worktree Git directory")
	}
	return value, nil
}

func linkedCommon(marker, worktreeDir string) (string, error) {
	invalid := errors.New("Git did not identify a registered linked worktree")
	admin, err := resolveMetadataPath(filepath.Dir(marker), worktreeDir)
	if err != nil || filepath.Base(filepath.Dir(admin)) != "worktrees" {
		return "", invalid
	}
	expected := filepath.Dir(filepath.Dir(admin))
	if filepath.Base(expected) != ".git" || !validCommon(expected) {
		return "", invalid
	}
	commonBytes, err := readRegular(filepath.Join(admin, "commondir"), 65536)
	if err != nil {
		return "", invalid
	}
	common, err := resolveMetadataPath(admin, strings.Trim(string(commonBytes), gitPointerWhitespace))
	if err != nil || !samePath(common, expected) || !sameDirectory(common, expected) {
		return "", invalid
	}
	back, err := readRegular(filepath.Join(admin, "gitdir"), 65536)
	if err != nil {
		return "", invalid
	}
	target := strings.Trim(string(back), gitPointerWhitespace)
	if filepath.Base(target) != ".git" || strings.ContainsAny(target, "\n\r\x00") {
		return "", invalid
	}
	registered, err := resolveMetadataPath(admin, filepath.Dir(target))
	if err != nil || !samePath(registered, filepath.Dir(marker)) || !sameDirectory(registered, filepath.Dir(marker)) {
		return "", invalid
	}
	return common, nil
}

func resolveMetadataPath(dir, path string) (string, error) {
	if path == "" || strings.ContainsAny(path, "\n\r\x00") {
		return "", errors.New("invalid Git metadata path")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	return filepath.EvalSymlinks(path)
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func within(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// Check the type before opening. In particular, a named pipe is not a profile or a Git pointer.
func readRegular(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("invalid regular file %s", filepath.Base(path))
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	openInfo, err := f.Stat()
	if err != nil || !os.SameFile(info, openInfo) {
		return nil, errors.New("file changed during trust inspection")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(b)) > limit {
		return nil, errors.New("trust configuration exceeds the read limit")
	}
	return b, err
}
