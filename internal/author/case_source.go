package author

import (
	"go/ast"
	"go/format"
	"go/token"
	"strings"

	"github.com/angelnicolasc/flinch/internal/testcase"
)

func changedTestCases(tool, path, scope string, before, after []testcase.Region) []DiscoveredTestCandidate {
	previous := make(map[string]testcase.Region, len(before))
	present := make(map[string]bool, len(after))
	var beforeNames, afterNames []string
	for _, r := range after {
		present[r.Name] = true
		afterNames = append(afterNames, r.Name)
	}
	for _, r := range before {
		if _, seen := previous[r.Name]; !seen {
			previous[r.Name] = r
			if present[r.Name] {
				beforeNames = append(beforeNames, r.Name)
			}
		}
	}

	var out []DiscoveredTestCandidate
	seen := make(map[string]bool)
	for _, r := range after {
		if seen[r.Name] {
			continue
		}
		seen[r.Name] = true
		old, existed := previous[r.Name]
		if existed && old.Source == r.Source && old.Shared == r.Shared {
			continue
		}
		out = append(out, DiscoveredTestCandidate{
			Tool: tool, Path: path, Scope: scope, Selector: r.Name,
			NeighborSelector: pickNeighbor(r.Name, beforeNames, afterNames),
			beforeLines:      old.Lines,
			afterLines:       r.Lines,
		})
	}
	return out
}

func goNodeText(node ast.Node) string {
	// Parsing omits comments. An empty position set prints the same syntax
	// consistently across single-line and multiline layouts.
	var b strings.Builder
	_ = format.Node(&b, token.NewFileSet(), node)
	return b.String()
}
