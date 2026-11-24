package check

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/reflex"
)

// IntegrityViolation flags unauthorized edits to active reflexes or newly added suppression pragmas (§8.1).
type IntegrityViolation struct {
	ReflexID string `json:"check_id,omitempty"`
	Path     string `json:"path"`
	Kind     string `json:"kind"` // reflex_modified | reflex_deleted | artifact_modified | suppression_added
	Detail   string `json:"detail"`
}

// CheckIntegrity compares active reflexes, their referenced artifact files, and newly added
// suppression comments in touchedFiles against trustedRef (§8.1).
func CheckIntegrity(ctx context.Context, repoRoot, trustedRef string, reflexes []reflex.Reflex, touchedFiles []string) ([]IntegrityViolation, error) {
	if strings.TrimSpace(trustedRef) == "" {
		trustedRef = "HEAD"
	}
	if _, err := gitutil.ResolveRef(ctx, repoRoot, trustedRef); err != nil {
		// No trusted commit exists yet (for example an unborn repo).
		return nil, nil
	}

	var violations []IntegrityViolation

	for _, r := range reflexes {
		if !r.IsDeterministic() {
			continue
		}
		// Use the stored path so older records remain covered.
		relReflex := r.FilePath
		if filepath.IsAbs(relReflex) {
			relReflex, _ = filepath.Rel(repoRoot, relReflex)
		}
		if !filepath.IsLocal(relReflex) {
			relReflex = filepath.Join(reflex.ReflexesRelDir, r.ID+".yml")
		}
		relReflex = filepath.ToSlash(relReflex)
		trustedReflexBytes, err := gitutil.FileAtRef(ctx, repoRoot, trustedRef, relReflex)
		if err == nil {
			currPath := filepath.Join(repoRoot, filepath.FromSlash(relReflex))
			currBytes, readErr := os.ReadFile(currPath)
			if errors.Is(readErr, os.ErrNotExist) {
				violations = append(violations, IntegrityViolation{
					ReflexID: r.ID,
					Path:     relReflex,
					Kind:     "check_deleted",
					Detail:   fmt.Sprintf("reviewed check record %s was deleted relative to %s", relReflex, trustedRef),
				})
			} else if readErr == nil && !bytes.Equal(normalizeLineEndings(trustedReflexBytes), normalizeLineEndings(currBytes)) {
				violations = append(violations, IntegrityViolation{
					ReflexID: r.ID,
					Path:     relReflex,
					Kind:     "check_modified",
					Detail:   fmt.Sprintf("reviewed check record %s was modified relative to %s; checks change only through review", relReflex, trustedRef),
				})
			}
		}

		// 2. Check referenced artifact (protected case/rule, not unrelated edits in the same file; D6).
		if r.Artifact.Path != "" {
			relArt := filepath.ToSlash(strings.TrimPrefix(filepath.Clean(r.Artifact.Path), "./"))
			trustedArtBytes, artErr := gitutil.FileAtRef(ctx, repoRoot, trustedRef, relArt)
			if artErr == nil {
				currArt, readErr := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(relArt)))
				if errors.Is(readErr, os.ErrNotExist) {
					violations = append(violations, IntegrityViolation{
						ReflexID: r.ID,
						Path:     relArt,
						Kind:     "artifact_modified",
						Detail:   fmt.Sprintf("artifact %s backing check %s was deleted relative to %s", relArt, r.ID, trustedRef),
					})
				} else if readErr == nil {
					if intact, reason := artifactCaseIntact(r, trustedArtBytes, currArt); !intact {
						violations = append(violations, IntegrityViolation{
							ReflexID: r.ID,
							Path:     relArt,
							Kind:     "artifact_modified",
							Detail:   reason,
						})
					}
				}
			}
		}
	}

	// 3. Detect newly added suppression directives in touched product files.
	if len(touchedFiles) > 0 {
		diffOut, _, err := gitutil.Run(ctx, repoRoot, nil, append([]string{"diff", "--unified=0", "--no-color", trustedRef, "--"}, touchedFiles...)...)
		if err == nil && diffOut != "" {
			var curFile string
			for _, line := range strings.Split(diffOut, "\n") {
				if strings.HasPrefix(line, "+++ b/") {
					curFile = filepath.ToSlash(strings.TrimPrefix(line, "+++ b/"))
					continue
				}
				if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
					added := strings.TrimPrefix(line, "+")
					if marker, ok := HasSuppressionDirective(added); ok {
						violations = append(violations, IntegrityViolation{
							Path:   curFile,
							Kind:   "suppression_added",
							Detail: fmt.Sprintf("new suppression directive %q added in %s; use `flinch report` instead of suppressing protections", marker, curFile),
						})
					}
				}
			}
		}
	}

	return violations, nil
}

func normalizeLineEndings(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
}
