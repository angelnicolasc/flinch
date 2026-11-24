package check

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/angelnicolasc/flinch/internal/reflex"
)

// artifactCaseIntact compares only the protected case/rule in currBytes against trustedBytes (D6, P8).
// Adding an unrelated sibling test or an unrelated row to a table-driven test returns intact=true.
// Modifying or deleting the protected case, weakening the enclosing test assertion logic, or
// deleting the artifact returns intact=false with a concrete explanation.
func artifactCaseIntact(r reflex.Reflex, trustedBytes, currBytes []byte) (bool, string) {
	normTrusted := normalizeLineEndings(trustedBytes)
	normCurr := normalizeLineEndings(currBytes)
	if bytes.Equal(normTrusted, normCurr) {
		return true, ""
	}

	// Test reflexes: compare the specific protected selector (function, subtest, or table row)
	// plus the test loop/assertion skeleton rather than the entire test file.
	if r.EffectiveRung() == reflex.RungTest && strings.TrimSpace(r.Artifact.Selector) != "" {
		ext := strings.ToLower(filepath.Ext(r.Artifact.Path))
		switch ext {
		case ".go":
			return compareGoTestCase(r.Artifact.Selector, normTrusted, normCurr)
		case ".py":
			return comparePythonTestCase(r.Artifact.Selector, normTrusted, normCurr)
		case ".ts", ".tsx", ".js", ".jsx", ".mts", ".cts":
			return compareJSTestCase(r.Artifact.Selector, normTrusted, normCurr)
		}
	}

	// Shared linter configuration files (e.g., eslint.config.*, pyproject.toml, .golangci.yml):
	// verify the protected rule/selector block itself is intact rather than blocking unrelated rules.
	if isSharedConfigFile(r.Artifact.Path) && r.ExpectedID() != "" {
		return compareSharedConfigEntry(r.ExpectedID(), normTrusted, normCurr)
	}

	return false, fmt.Sprintf("artifact %s backing check %s was modified", r.Artifact.Path, r.ID)
}

func compareGoTestCase(selector string, trustedBytes, currBytes []byte) (bool, string) {
	parts := strings.SplitN(strings.TrimSpace(selector), "/", 2)
	funcName := parts[0]
	subName := ""
	if len(parts) == 2 {
		subName = parts[1]
	}

	tFunc, errT := findGoFunc(trustedBytes, funcName)
	if errT != nil {
		// Fallback to whole-file mismatch if trusted AST cannot be parsed.
		return false, fmt.Sprintf("cannot parse trusted test function %s: %v", funcName, errT)
	}
	cFunc, errC := findGoFunc(currBytes, funcName)
	if errC != nil {
		return false, fmt.Sprintf("protected test function %s is missing or unparseable: %v", funcName, errC)
	}

	// Whole function protection (no subtest selector): compare normalized function AST only,
	// ignoring new unrelated test functions added elsewhere in the same file.
	if subName == "" {
		if formatASTNode(tFunc.fset, tFunc.decl) == formatASTNode(cFunc.fset, cFunc.decl) {
			return true, ""
		}
		return false, fmt.Sprintf("protected test function %s was modified", funcName)
	}

	// Subtest / table-row protection:
	// Strategy 1: direct `t.Run("subName", ...)` call inside funcName.
	tSub := findGoDirectSubtest(tFunc.decl.Body, subName)
	if tSub != nil {
		cSub := findGoDirectSubtest(cFunc.decl.Body, subName)
		if cSub == nil {
			return false, fmt.Sprintf("protected subtest %s/%s was removed or renamed", funcName, subName)
		}
		if formatASTNode(tFunc.fset, tSub) != formatASTNode(cFunc.fset, cSub) {
			return false, fmt.Sprintf("protected subtest %s/%s was modified", funcName, subName)
		}
		return true, ""
	}

	// Strategy 2: table-driven test slice/map literal inside funcName.
	// Compare BOTH the matching row element AND the test skeleton outside the table elements
	// (so adding an unrelated row passes, while editing the row OR weakening the loop assertion fails).
	tRow, tSkeleton := extractGoTableRowAndSkeleton(tFunc.fset, tFunc.decl, subName)
	if tRow != "" {
		cRow, cSkeleton := extractGoTableRowAndSkeleton(cFunc.fset, cFunc.decl, subName)
		if cRow == "" {
			return false, fmt.Sprintf("protected table test case %s/%s was removed or renamed", funcName, subName)
		}
		if tRow != cRow {
			return false, fmt.Sprintf("protected table test case %s/%s was modified", funcName, subName)
		}
		if tSkeleton != cSkeleton {
			return false, fmt.Sprintf("assertion or loop body in %s guarding %s/%s was modified", funcName, funcName, subName)
		}
		return true, ""
	}

	// Fallback: compare the enclosing test function only.
	if formatASTNode(tFunc.fset, tFunc.decl) == formatASTNode(cFunc.fset, cFunc.decl) {
		return true, ""
	}
	return false, fmt.Sprintf("protected test %s was modified", selector)
}

type parsedFunc struct {
	fset *token.FileSet
	decl *ast.FuncDecl
}

func findGoFunc(src []byte, name string) (parsedFunc, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, 0)
	if err != nil {
		return parsedFunc{}, err
	}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == name {
			return parsedFunc{fset: fset, decl: fn}, nil
		}
	}
	return parsedFunc{}, fmt.Errorf("function %s not found", name)
}

func normalizeGoSubtestName(raw string) string {
	return strings.ReplaceAll(strings.TrimSpace(raw), " ", "_")
}

func findGoDirectSubtest(body *ast.BlockStmt, target string) *ast.CallExpr {
	if body == nil {
		return nil
	}
	normTarget := normalizeGoSubtestName(target)
	var matched *ast.CallExpr
	ast.Inspect(body, func(n ast.Node) bool {
		if matched != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Run" {
			return true
		}
		if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if unq, err := strconv.Unquote(lit.Value); err == nil {
				if unq == target || normalizeGoSubtestName(unq) == normTarget {
					matched = call
					return false
				}
			}
		}
		return true
	})
	return matched
}

func extractGoTableRowAndSkeleton(fset *token.FileSet, fn *ast.FuncDecl, target string) (rowFormatted string, skeletonFormatted string) {
	if fn == nil || fn.Body == nil {
		return "", ""
	}
	normTarget := normalizeGoSubtestName(target)

	// Locate the composite literal slice or map containing an element matching target.
	var tableLit *ast.CompositeLit
	var matchedElt ast.Expr

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if tableLit != nil {
			return false
		}
		comp, ok := n.(*ast.CompositeLit)
		if !ok || len(comp.Elts) == 0 {
			return true
		}
		switch comp.Type.(type) {
		case *ast.ArrayType, *ast.MapType:
			for _, elt := range comp.Elts {
				if name, ok := extractTableElementName(elt); ok {
					if name == target || normalizeGoSubtestName(name) == normTarget {
						tableLit = comp
						matchedElt = elt
						return false
					}
				}
			}
		}
		return true
	})

	if tableLit == nil || matchedElt == nil {
		return "", ""
	}

	rowFormatted = formatASTNode(fset, matchedElt)

	// Temporarily replace tableLit.Elts with nil to format the surrounding test loop & assertions.
	origElts := tableLit.Elts
	tableLit.Elts = nil
	skeletonFormatted = formatASTNode(fset, fn)
	tableLit.Elts = origElts
	return rowFormatted, skeletonFormatted
}

func extractTableElementName(elt ast.Expr) (string, bool) {
	// Map entry: "case_name": { ... }
	if kv, ok := elt.(*ast.KeyValueExpr); ok {
		if lit, ok := kv.Key.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if s, err := strconv.Unquote(lit.Value); err == nil {
				return s, true
			}
		}
	}
	// Struct literal: {name: "case_name", ...} or {"case_name", ...}
	comp, ok := elt.(*ast.CompositeLit)
	if !ok {
		if un, ok := elt.(*ast.UnaryExpr); ok {
			comp, _ = un.X.(*ast.CompositeLit)
		}
	}
	if comp == nil {
		return "", false
	}
	// Prefer explicit name/Name/desc/title key first.
	for _, field := range comp.Elts {
		if kv, ok := field.(*ast.KeyValueExpr); ok {
			if ident, ok := kv.Key.(*ast.Ident); ok {
				low := strings.ToLower(ident.Name)
				if low == "name" || low == "desc" || low == "description" || low == "title" || low == "case" {
					if lit, ok := kv.Value.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if s, err := strconv.Unquote(lit.Value); err == nil {
							return s, true
						}
					}
				}
			}
		}
	}
	// Fall back to first positional string literal.
	if len(comp.Elts) > 0 {
		if lit, ok := comp.Elts[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if s, err := strconv.Unquote(lit.Value); err == nil {
				return s, true
			}
		}
	}
	return "", false
}

func formatASTNode(fset *token.FileSet, node any) string {
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, node); err != nil {
		return ""
	}
	return strings.TrimSpace(buf.String())
}

func comparePythonTestCase(selector string, trustedBytes, currBytes []byte) (bool, string) {
	// Selector format: "test_file.py::test_fn" or "test_fn" or "test_file.py::TestCls::test_fn[param]"
	fnName := selector
	if idx := strings.LastIndex(fnName, "::"); idx >= 0 {
		fnName = fnName[idx+2:]
	}
	if idx := strings.Index(fnName, "["); idx >= 0 {
		fnName = fnName[:idx]
	}
	tBlock, okT := extractPythonDefBlock(string(trustedBytes), fnName)
	if !okT {
		return false, fmt.Sprintf("could not locate Python test %s in trusted version", fnName)
	}
	cBlock, okC := extractPythonDefBlock(string(currBytes), fnName)
	if !okC {
		return false, fmt.Sprintf("protected Python test %s was removed or renamed", fnName)
	}
	if tBlock == cBlock {
		return true, ""
	}
	return false, fmt.Sprintf("protected Python test %s was modified", fnName)
}

func extractPythonDefBlock(src, fnName string) (string, bool) {
	lines := strings.Split(src, "\n")
	defRe := regexp.MustCompile(`^(\s*)(?:async\s+)?def\s+` + regexp.QuoteMeta(fnName) + `\s*\(`)
	start := -1
	indent := 0
	for i, l := range lines {
		if m := defRe.FindStringSubmatch(l); m != nil {
			start = i
			indent = len(m[1])
			// Include immediately preceding decorators (@pytest.mark...).
			for start > 0 && strings.HasPrefix(strings.TrimSpace(lines[start-1]), "@") {
				start--
			}
			break
		}
	}
	if start < 0 {
		return "", false
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if len(lines[i])-len(strings.TrimLeft(lines[i], " \t")) <= indent && !strings.HasPrefix(trimmed, "@") && i > start {
			if strings.HasPrefix(trimmed, "def ") || strings.HasPrefix(trimmed, "async def ") || strings.HasPrefix(trimmed, "class ") || len(lines[i])-len(strings.TrimLeft(lines[i], " \t")) < indent {
				end = i
				break
			}
		}
	}
	return strings.TrimSpace(strings.Join(lines[start:end], "\n")), true
}

func compareJSTestCase(selector string, trustedBytes, currBytes []byte) (bool, string) {
	// Extract the leaf test name from "describe > it" or "describe › it" or plain name.
	leaf := selector
	for _, sep := range []string{" › ", " > "} {
		if idx := strings.LastIndex(leaf, sep); idx >= 0 {
			leaf = strings.TrimSpace(leaf[idx+len(sep):])
		}
	}
	tBlock, okT := extractJSCaseSnippet(string(trustedBytes), leaf)
	if !okT {
		return false, fmt.Sprintf("could not locate JS/TS test case %q in trusted version", selector)
	}
	cBlock, okC := extractJSCaseSnippet(string(currBytes), leaf)
	if !okC {
		return false, fmt.Sprintf("protected JS/TS test case %q was removed or renamed", selector)
	}
	if tBlock == cBlock {
		return true, ""
	}
	return false, fmt.Sprintf("protected JS/TS test case %q was modified", selector)
}

func extractJSCaseSnippet(src, leaf string) (string, bool) {
	lines := strings.Split(src, "\n")
	// 1. Direct it("leaf", ...) or test("leaf", ...) block
	callRe := regexp.MustCompile(`\b(?:it|test)\s*\(\s*['"\x60]` + regexp.QuoteMeta(leaf) + `['"\x60]`)
	for i, l := range lines {
		if callRe.MatchString(l) {
			return extractBalancedJSBlock(lines, i), true
		}
	}
	// 2. Row inside test.each / it.each containing the exact case label (plus enclosing test.each assertion body).
	quotedSingle := "'" + leaf + "'"
	quotedDouble := `"` + leaf + `"`
	for i, l := range lines {
		if strings.Contains(l, quotedSingle) || strings.Contains(l, quotedDouble) {
			row := strings.TrimSpace(l)
			// Find the following `(`...`)` assertion callback of the enclosing test.each block.
			callback := ""
			for j := i + 1; j < len(lines); j++ {
				if strings.Contains(lines[j], "=>") {
					callback = extractBalancedJSBlock(lines, j)
					break
				}
			}
			return row + "\n" + callback, true
		}
	}
	return "", false
}

func extractBalancedJSBlock(lines []string, startLine int) string {
	var b strings.Builder
	depth := 0
	seenOpen := false
	for i := startLine; i < len(lines); i++ {
		l := lines[i]
		b.WriteString(strings.TrimSpace(l))
		b.WriteByte('\n')
		for _, ch := range l {
			switch ch {
			case '(', '{':
				depth++
				seenOpen = true
			case ')', '}':
				depth--
			}
		}
		if seenOpen && depth <= 0 {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

func isSharedConfigFile(relPath string) bool {
	base := strings.ToLower(filepath.Base(relPath))
	switch {
	case strings.HasPrefix(base, "eslint.config."),
		strings.HasPrefix(base, ".eslintrc"),
		base == "pyproject.toml",
		base == "ruff.toml",
		base == ".ruff.toml",
		base == ".golangci.yml",
		base == ".golangci.yaml":
		return true
	}
	return false
}

func compareSharedConfigEntry(id string, trustedBytes, currBytes []byte) (bool, string) {
	tLines := extractMatchingLines(string(trustedBytes), id)
	if len(tLines) == 0 {
		return false, fmt.Sprintf("shared config entry for %q was modified", id)
	}
	cLines := extractMatchingLines(string(currBytes), id)
	if strings.Join(tLines, "\n") == strings.Join(cLines, "\n") {
		return true, ""
	}
	return false, fmt.Sprintf("shared config entry for %q was removed or modified", id)
}

func extractMatchingLines(src, token string) []string {
	var out []string
	for _, line := range strings.Split(src, "\n") {
		if strings.Contains(line, token) {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
}
