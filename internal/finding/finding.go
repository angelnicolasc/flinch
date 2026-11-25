package finding

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Finding is a normalized diagnostic produced by a linter, compiler, or structural check.
type Finding struct {
	Tool      string `json:"tool"`
	Rule      string `json:"rule"`
	Selector  string `json:"selector,omitempty"`
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	StartCol  int    `json:"start_col,omitempty"`
	EndCol    int    `json:"end_col,omitempty"`
	Symbol    string `json:"symbol,omitempty"`
	Context   string `json:"context,omitempty"`
	Message   string `json:"message"`
	Severity  string `json:"severity,omitempty"` // error | warning | info
}

// EffectiveID returns Selector when set, otherwise Rule.
func (f Finding) EffectiveID() string {
	if f.Selector != "" {
		return f.Selector
	}
	return f.Rule
}

// NormalizePath converts Path to a clean repo-relative forward-slash path.
func NormalizePath(repoRoot, rawPath string) string {
	p := rawPath
	if repoRoot != "" {
		if filepath.IsAbs(p) {
			if rel, err := filepath.Rel(repoRoot, p); err == nil {
				p = rel
			}
		} else {
			rootSlash := strings.TrimSuffix(filepath.ToSlash(repoRoot), "/") + "/"
			pSlash := filepath.ToSlash(p)
			if strings.HasPrefix(pSlash, rootSlash) {
				p = strings.TrimPrefix(pSlash, rootSlash)
			}
		}
	}
	p = filepath.ToSlash(filepath.Clean(p))
	p = strings.TrimPrefix(p, "./")
	return p
}

// NormalizeContext collapses whitespace so formatting shifts do not alter occurrence identity.
func NormalizeContext(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

// OccurrenceKey identifies a finding independent of line number shifts, combining rule/selector,
// normalized path, enclosing symbol, and normalized expression context.
func (f Finding) OccurrenceKey() string {
	return fmt.Sprintf("%s|%s|%s|%s",
		f.EffectiveID(),
		filepath.ToSlash(strings.TrimPrefix(f.Path, "./")),
		f.Symbol,
		NormalizeContext(f.Context),
	)
}
