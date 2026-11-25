package hook

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/angelnicolasc/flinch/internal/usersetup"
)

// Each recorded checkout has its own file, so processes in different checkouts
// never need to rewrite a shared index.
func rememberActivation(root string) {
	dir, err := usersetup.StateDir()
	if err != nil {
		return
	}
	base, err := os.OpenRoot(dir)
	if err != nil {
		return
	}
	defer base.Close()
	if err = base.Mkdir("repositories", 0700); err != nil && !errors.Is(err, fs.ErrExist) {
		return
	}
	key := sha256.Sum256([]byte(root))
	name := filepath.Join("repositories", hex.EncodeToString(key[:]))
	f, err := base.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	_, err = f.WriteString(root)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		_ = base.Remove(name)
	}
}

func ActivationHistory() ([]string, error) {
	dir, err := usersetup.StateDir()
	if err != nil {
		return nil, err
	}
	base, err := os.OpenRoot(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer base.Close()
	entries, err := fs.ReadDir(base.FS(), "repositories")
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		b, err := base.ReadFile(filepath.Join("repositories", entry.Name()))
		if err != nil {
			return nil, err
		}
		key := sha256.Sum256(b)
		if hex.EncodeToString(key[:]) == entry.Name() && filepath.IsAbs(string(b)) {
			paths = append(paths, string(b))
		}
	}
	sort.Strings(paths)
	return paths, nil
}
