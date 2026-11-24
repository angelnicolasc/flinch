package prove

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"strconv"
	"strings"
)

type goSourceSites struct {
	lines, testify, logs []int
}

func goAssertionLines(source []byte, selector string) goSourceSites {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", source, 0)
	if err != nil {
		return goSourceSites{}
	}
	imports := make(map[string]string)
	for _, imp := range file.Imports {
		pkg, err := strconv.Unquote(imp.Path.Value)
		if err == nil {
			alias := path.Base(pkg)
			if imp.Name != nil {
				alias = imp.Name.Name
			}
			imports[alias] = pkg
		}
	}
	parts := strings.Split(selector, "/")
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != parts[0] || fn.Body == nil {
			continue
		}
		body, params := fn.Body, fn.Type.Params
		for _, part := range parts[1:] {
			child := subtestCallback(body, part)
			if child == nil {
				return goSourceSites{}
			}
			body, params = child.Body, child.Type.Params
		}
		if params == nil || len(params.List) != 1 || len(params.List[0].Names) != 1 {
			return goSourceSites{}
		}
		receiver := params.List[0].Names[0].Name
		var sites goSourceSites
		ast.Inspect(body, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.FuncLit:
				return false
			case *ast.CallExpr:
				pkg, method := goCallName(v)
				if pkg == receiver && (method == "Log" || method == "Logf") {
					sites.logs = append(sites.logs, fset.Position(v.Pos()).Line)
				}
				if isTestifyAssertion(imports[pkg], method) && len(v.Args) >= 2 {
					if t, ok := v.Args[0].(*ast.Ident); ok && t.Name == receiver {
						line := fset.Position(v.Pos()).Line
						sites.lines = append(sites.lines, line)
						sites.testify = append(sites.testify, line)
					}
				}
			case *ast.IfStmt:
				if !isBehaviorComparison(v.Cond, imports) {
					return true
				}
				for _, stmt := range v.Body.List {
					expr, ok := stmt.(*ast.ExprStmt)
					if !ok {
						continue
					}
					call, ok := expr.X.(*ast.CallExpr)
					if !ok {
						continue
					}
					object, method := goCallName(call)
					if object == receiver && (method == "Fatal" || method == "Fatalf" || method == "Error" || method == "Errorf") {
						line := fset.Position(call.Pos()).Line
						// A T.Helper call on this line could report its caller before the assertion runs.
						if !passesTestingTOnLine(v, receiver, line, fset) {
							sites.lines = append(sites.lines, line)
						}
					}
				}
			}
			return true
		})
		return sites
	}
	return goSourceSites{}
}

func passesTestingTOnLine(stmt *ast.IfStmt, receiver string, line int, fset *token.FileSet) bool {
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		if found || n == nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || fset.Position(call.Pos()).Line != line {
			return true
		}
		for _, arg := range call.Args {
			if id, ok := arg.(*ast.Ident); ok && id.Name == receiver {
				found = true
				break
			}
		}
		return !found
	})
	return found
}

func goCallName(call *ast.CallExpr) (string, string) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if ok {
		if receiver, ok := sel.X.(*ast.Ident); ok {
			return receiver.Name, sel.Sel.Name
		}
	}
	return "", ""
}

func isTestifyAssertion(pkg, method string) bool {
	if pkg != "github.com/stretchr/testify/assert" && pkg != "github.com/stretchr/testify/require" {
		return false
	}
	switch strings.TrimSuffix(method, "f") {
	case "Equal", "NotEqual", "EqualValues", "Exactly", "True", "False", "Nil", "NotNil",
		"Contains", "NotContains", "ElementsMatch", "Len", "Empty", "NotEmpty", "Greater", "GreaterOrEqual",
		"Less", "LessOrEqual", "ErrorIs", "NotErrorIs", "EqualError", "ErrorContains", "Panics", "NotPanics":
		return true
	}
	return false
}

func isBehaviorComparison(expr ast.Expr, imports map[string]string) bool {
	switch v := expr.(type) {
	case *ast.ParenExpr:
		return isBehaviorComparison(v.X, imports)
	case *ast.UnaryExpr:
		if call, ok := v.X.(*ast.CallExpr); ok && v.Op == token.NOT {
			pkg, method := goCallName(call)
			return (imports[pkg] == "reflect" && method == "DeepEqual") || (imports[pkg] == "errors" && method == "Is")
		}
	case *ast.BinaryExpr:
		for _, operand := range []ast.Expr{v.X, v.Y} {
			if id, ok := operand.(*ast.Ident); ok && id.Name == "nil" {
				return false
			}
		}
		switch v.Op {
		case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
			return true
		}
	}
	return false
}

// One dynamic t.Run callback can serve all rows of a table. Exact executed names
// still have to match on each replay.
func subtestCallback(body *ast.BlockStmt, name string) *ast.FuncLit {
	var named, dynamic []*ast.FuncLit
	ast.Inspect(body, func(n ast.Node) bool {
		if _, nested := n.(*ast.FuncLit); nested {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 2 {
			return true
		}
		method, ok := call.Fun.(*ast.SelectorExpr)
		callback, hasBody := call.Args[1].(*ast.FuncLit)
		if !ok || method.Sel.Name != "Run" || !hasBody {
			return true
		}
		if lit, ok := call.Args[0].(*ast.BasicLit); ok {
			value, err := strconv.Unquote(lit.Value)
			if err == nil && strings.ReplaceAll(value, " ", "_") == name {
				named = append(named, callback)
			}
		} else {
			dynamic = append(dynamic, callback)
		}
		return false
	})
	if len(named) == 1 {
		return named[0]
	}
	if len(named) == 0 && len(dynamic) == 1 {
		return dynamic[0]
	}
	return nil
}
