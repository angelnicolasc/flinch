package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// RepositoryDefaults recognizes tools from files that teams already maintain.
func RepositoryDefaults(root string) Config {
	cfg := Default()
	has := func(names ...string) bool {
		for _, name := range names {
			if st, err := os.Stat(filepath.Join(root, name)); err == nil && st.Mode().IsRegular() {
				return true
			}
		}
		return false
	}
	if has("go.mod") {
		cfg.Tests.Runners = append(cfg.Tests.Runners, TestRunner{Kind: "go", Paths: []string{"**/*.go"}})
		kind := "go-vet"
		if has(".golangci.yml", ".golangci.yaml") {
			kind = "golangci-lint"
		}
		cfg.Lint = append(cfg.Lint, LintConfig{Kind: kind, Paths: []string{"**/*.go"}})
	}
	if has("pyproject.toml", "pytest.ini", "setup.cfg") {
		cfg.Tests.Runners = append(cfg.Tests.Runners, TestRunner{Kind: "pytest", Paths: []string{"**/*.py"}})
	}
	py, _ := os.ReadFile(filepath.Join(root, "pyproject.toml"))
	if has(".ruff.toml", "ruff.toml") || bytes.Contains(py, []byte("[tool.ruff")) {
		cfg.Lint = append(cfg.Lint, LintConfig{Kind: "ruff", Paths: []string{"**/*.py"}})
	}
	var pkg struct {
		Dependencies    map[string]string
		DevDependencies map[string]string
	}
	pkgData, _ := os.ReadFile(filepath.Join(root, "package.json"))
	_ = json.Unmarshal(pkgData, &pkg)
	jsPaths := []string{"**/*.ts", "**/*.js", "**/*.tsx", "**/*.jsx"}
	for _, kind := range []string{"vitest", "jest"} {
		if pkg.Dependencies[kind] != "" || pkg.DevDependencies[kind] != "" || has(kind+".config.ts", kind+".config.js", kind+".config.mjs") {
			cfg.Tests.Runners = append(cfg.Tests.Runners, TestRunner{Kind: kind, Paths: jsPaths})
		}
	}
	if has("eslint.config.js", "eslint.config.mjs", "eslint.config.ts", ".eslintrc.json") {
		cfg.Lint = append(cfg.Lint, LintConfig{Kind: "eslint", Paths: jsPaths})
	}
	return cfg
}

// SaveSetup writes only explicit init overrides. A default install needs no tracked file.
func SaveSetup(root, owner string, mode PreparationMode, replace bool) (bool, error) {
	policy := map[string]string{}
	if owner != "" {
		policy["owner"] = owner
	}
	if mode != ModeLocalAuto {
		policy["mode"] = string(mode)
	}
	path := filepath.Join(root, ConfigRelPath)
	if len(policy) == 0 {
		if replace {
			err := os.Remove(path)
			if err == nil {
				return true, nil
			}
			if !errors.Is(err, os.ErrNotExist) {
				return false, err
			}
		}
		return false, nil
	}
	var data bytes.Buffer
	if err := toml.NewEncoder(&data).Encode(map[string]any{"policy": policy}); err != nil {
		return false, err
	}
	if err := writeConfig(path, data.Bytes()); err != nil {
		return false, err
	}
	return true, nil
}

func writeConfig(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".flinch-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
