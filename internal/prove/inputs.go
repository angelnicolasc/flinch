package prove

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/angelnicolasc/flinch/internal/gitutil"
)

// Resolve file inputs before starting any runner. Freeze fixture bytes for every replay.
func prepareProofInputs(root string, spec *CandidateSpec) error {
	validate := func(name string) error {
		if name == "" {
			return nil
		}
		_, err := gitutil.LocalPath(root, name)
		return err
	}
	if err := validate(spec.Artifact.Path); err != nil {
		return err
	}
	if spec.ID != "" && (strings.ContainsAny(spec.ID, "/\\") || validate(spec.ID) != nil) {
		return fmt.Errorf("check ID must be a repository-relative file name")
	}
	for path := range spec.OverlayFiles {
		if _, err := gitutil.LocalPath(root, path); err != nil {
			return err
		}
	}
	for _, target := range spec.TestPackages {
		if target == "." || target == "./" {
			continue
		}
		name, _, _ := strings.Cut(target, "::")
		if strings.HasPrefix(name, "-") {
			return fmt.Errorf("expected a repository-relative test target, got %q", target)
		}
		if _, err := gitutil.LocalPath(root, name); err != nil {
			return err
		}
	}
	validateNodeID := func(selector string) error {
		if spec.Artifact.Tool == "pytest" {
			if name, _, ok := strings.Cut(selector, "::"); ok {
				return validate(name)
			}
		}
		return nil
	}
	if err := validateNodeID(spec.Artifact.Selector); err != nil {
		return err
	}
	if err := validateNodeID(spec.NeighborSelector); err != nil {
		return err
	}
	spec.AllowedCases = append([]AllowedCase(nil), spec.AllowedCases...)
	for i := range spec.AllowedCases {
		ac := &spec.AllowedCases[i]
		if err := validateNodeID(ac.Selector); err != nil {
			return err
		}
		if ac.TargetPath != "" {
			dst, err := gitutil.LocalPath(root, ac.TargetPath)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, dst)
			if err != nil {
				return err
			}
			ac.TargetPath = filepath.ToSlash(rel)
		}
		if ac.SourceFile != "" {
			src, err := gitutil.LocalPath(root, ac.SourceFile)
			if err != nil {
				return err
			}
			if ac.Content == "" {
				data, err := os.ReadFile(src)
				if err != nil {
					return fmt.Errorf("read allowed fixture: %w", err)
				}
				ac.Content = string(data)
			}
			ac.SourceFile = ""
		}
	}
	return nil
}
