package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/angelnicolasc/flinch/internal/gitutil"
)

type repositoryKey struct{}

// WithCommandRepository carries the user's clone into disposable proof directories.
func WithCommandRepository(ctx context.Context, repoRoot string) context.Context {
	return context.WithValue(ctx, repositoryKey{}, repoRoot)
}

func commandMarker(ctx context.Context, repoRoot string) (string, error) {
	common, err := gitutil.CommonDir(ctx, repoRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(common, "flinch", "commands-enabled"), nil
}

// EnableCommands records the maintainer's one-time enrollment outside the working tree.
func EnableCommands(ctx context.Context, repoRoot string) (bool, error) {
	p, err := commandMarker(ctx, repoRoot)
	if err != nil {
		return false, err
	}
	if err = os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return false, err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		entry, err := os.Lstat(p)
		if err != nil {
			return false, err
		}
		if !entry.Mode().IsRegular() {
			return false, fmt.Errorf("clone enrollment marker is not a regular file")
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, f.Close()
}

func checkCommandTrust(ctx context.Context, root string) error {
	if source, ok := ctx.Value(repositoryKey{}).(string); ok {
		root = source
	}
	p, err := commandMarker(ctx, root)
	if err == nil {
		entry, e := os.Lstat(p)
		if e == nil && entry.Mode().IsRegular() {
			return nil
		}
	}
	return fmt.Errorf("repository commands require one-time clone enrollment with flinch init")
}
