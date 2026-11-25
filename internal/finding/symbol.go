package finding

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// maxEnrichedFile bounds how much of one source file Flinch reads to describe a finding.
const maxEnrichedFile = 8 << 20

type sourceFile struct {
	lines []string
	data  []byte
}

// Enrich populates Symbol (enclosing function/method/declaration) and Context (normalized source line)
// for each finding by inspecting the source file under rootDir.
//
// The reported path comes from a tool's output, so Flinch reads it only through
// an os.Root opened on rootDir: a path that leaves the checkout, directly or
// through a link, is never opened. Such a finding keeps its location but gets
// no context. Each file is opened once, and the Go symbol parser reuses the
// same bytes instead of opening the name again.
func Enrich(rootDir string, findings []Finding) []Finding {
	var root *os.Root
	if rootDir != "" {
		if r, err := os.OpenRoot(rootDir); err == nil {
			root = r
			defer root.Close()
		}
	}
	files := make(map[string]*sourceFile)
	goFileCache := make(map[string]*parsedGoFile)

	out := make([]Finding, len(findings))
	for i, f := range findings {
		f.Path = NormalizePath(rootDir, f.Path)
		if f.EndLine == 0 {
			f.EndLine = f.StartLine
		}
		src, ok := files[f.Path]
		if !ok {
			src = readConfined(root, f.Path)
			files[f.Path] = src
		}
		var lines []string
		if src != nil {
			lines = src.lines
		}
		if f.Context == "" && f.StartLine >= 1 && f.StartLine <= len(lines) {
			f.Context = NormalizeContext(lines[f.StartLine-1])
		}
		if f.Symbol == "" {
			if strings.HasSuffix(f.Path, ".go") && src != nil {
				pf, parsed := goFileCache[f.Path]
				if !parsed {
					pf = parseGoSymbols(f.Path, src.data)
					goFileCache[f.Path] = pf
				}
				if pf != nil {
					f.Symbol = pf.symbolAt(f.StartLine)
				}
			} else if len(lines) > 0 {
				f.Symbol = heuristicSymbolAt(lines, f.StartLine)
			}
			if f.Symbol == "" {
				f.Symbol = "<file>"
			}
		}
		out[i] = f
	}
	return out
}

// readConfined reads a repo-relative regular file below root, or returns nil.
// An absolute path, a path with "..", a link that resolves outside root, a
// directory, a device or a file larger than maxEnrichedFile is never read.
func readConfined(root *os.Root, rel string) *sourceFile {
	name := filepath.FromSlash(rel)
	if root == nil || rel == "" || !filepath.IsLocal(name) {
		return nil
	}
	f, err := root.Open(name)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxEnrichedFile {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(f, maxEnrichedFile+1))
	if err != nil || len(data) > maxEnrichedFile {
		return nil
	}
	return &sourceFile{
		lines: strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n"),
		data:  data,
	}
}

type goSpan struct {
	name      string
	startLine int
	endLine   int
}

type parsedGoFile struct {
	spans []goSpan
}

func parseGoSymbols(name string, src []byte) *parsedGoFile {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return nil
	}
	pf := &parsedGoFile{}
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			if d.Recv != nil && len(d.Recv.List) > 0 {
				recvType := formatRecvExpr(d.Recv.List[0].Type)
				name = fmt.Sprintf("(%s).%s", recvType, name)
			}
			pf.spans = append(pf.spans, goSpan{
				name:      name,
				startLine: fset.Position(d.Pos()).Line,
				endLine:   fset.Position(d.End()).Line,
			})
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				var name string
				switch s := spec.(type) {
				case *ast.TypeSpec:
					name = "type " + s.Name.Name
				case *ast.ValueSpec:
					if len(s.Names) > 0 {
						name = s.Names[0].Name
					}
				}
				if name != "" {
					pf.spans = append(pf.spans, goSpan{
						name:      name,
						startLine: fset.Position(spec.Pos()).Line,
						endLine:   fset.Position(spec.End()).Line,
					})
				}
			}
		}
	}
	return pf
}

func formatRecvExpr(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return "*" + formatRecvExpr(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return formatRecvExpr(t.X)
	case *ast.IndexListExpr:
		return formatRecvExpr(t.X)
	default:
		return "recv"
	}
}

func (p *parsedGoFile) symbolAt(line int) string {
	for _, s := range p.spans {
		if line >= s.startLine && line <= s.endLine {
			return s.name
		}
	}
	return ""
}

var (
	pyDefRe = regexp.MustCompile(`^\s*(?:async\s+)?(?:def|class)\s+([A-Za-z_][A-Za-z0-9_]*)`)
	jsFunRe = regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:async\s+)?(?:function\s+([A-Za-z_$][A-Za-z0-9_$]*)|class\s+([A-Za-z_$][A-Za-z0-9_$]*)|(?:const|let|var)\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*=\s*(?:async\s*)?\()`)
)

func heuristicSymbolAt(lines []string, line int) string {
	if line < 1 {
		return ""
	}
	idx := line - 1
	if idx >= len(lines) {
		idx = len(lines) - 1
	}
	for i := idx; i >= 0; i-- {
		l := lines[i]
		if m := pyDefRe.FindStringSubmatch(l); m != nil {
			return m[1]
		}
		if m := jsFunRe.FindStringSubmatch(l); m != nil {
			for _, g := range m[1:] {
				if g != "" {
					return g
				}
			}
		}
	}
	return ""
}
