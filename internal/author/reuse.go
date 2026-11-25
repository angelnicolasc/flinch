package author

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/angelnicolasc/flinch/internal/config"
	"github.com/angelnicolasc/flinch/internal/finding"
	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/prove"
	"github.com/angelnicolasc/flinch/internal/reflex"
	"github.com/angelnicolasc/flinch/internal/runner"
)

// AuthoringBudgetCap is the experimental threshold for in-turn authoring including proof.
const AuthoringBudgetCap = 90 * time.Second

// ReuseStatus describes the outcome of inspecting a fix diff for a reusable protection.
type ReuseStatus string

const (
	ReuseNone            ReuseStatus = "none"
	ReuseProven          ReuseStatus = "proven"
	ReuseInconclusive    ReuseStatus = "inconclusive_kept"
	ReuseLineValid       ReuseStatus = "line_valid"
	ReuseLineFormatError ReuseStatus = "line_format_error"
	ReuseLineSectionFull ReuseStatus = "line_section_full"
)

// ReuseOutcome summarizes the deterministic reuse-first check over a turn's fix diff.
type ReuseOutcome struct {
	CommandApprovalDigest  string                    `json:"-"`
	Status                 ReuseStatus               `json:"status"`
	Reflex                 *reflex.Reflex            `json:"reflex,omitempty"`
	Report                 *prove.VerificationReport `json:"report,omitempty"`
	Neighbor               string                    `json:"neighbor,omitempty"`
	AddedLines             []string                  `json:"added_lines,omitempty"`
	Reason                 string                    `json:"reason,omitempty"`
	SummaryLine            string                    `json:"summary_line,omitempty"`
	UnrecognizedAssertions int                       `json:"no_recognized_assertion,omitempty"`
}

// DiscoveredTestCandidate identifies one newly added test case and an adjacent sibling neighbor.
type DiscoveredTestCandidate struct {
	Tool             string
	Path             string
	Scope            string
	Selector         string
	NeighborSelector string
}

// AuthoringEvent logs authoring metrics (nominations, skips by reason, reused, written, timeouts) locally.
type AuthoringEvent struct {
	Timestamp              time.Time `json:"timestamp"`
	SessionID              string    `json:"session_id,omitempty"`
	TurnID                 string    `json:"turn_id,omitempty"`
	Host                   string    `json:"host,omitempty"`
	Kind                   string    `json:"kind"` // nominated | skipped | reused | written | line_recorded | inconclusive_closed | authoring_requested | timeout_exceeded
	ReflexID               string    `json:"reflex_id,omitempty"`
	Selector               string    `json:"selector,omitempty"`
	Reason                 string    `json:"reason,omitempty"`
	ElapsedMS              int64     `json:"elapsed_ms,omitempty"`
	UnrecognizedAssertions int       `json:"no_recognized_assertion,omitempty"`
}

// RecordAuthoringEvent appends an authoring/nomination metric event to <stateDir>/log/authoring.jsonl.
func RecordAuthoringEvent(stateDir string, ev AuthoringEvent) error {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	p := filepath.Join(stateDir, "log", "authoring.jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s\n", b)
	return err
}

// CountUnrecognizedAssertions counts candidates in this clone's authoring log.
func CountUnrecognizedAssertions(stateDir string) (int, error) {
	f, err := os.Open(filepath.Join(stateDir, "log", "authoring.jsonl"))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	count := 0
	lines := bufio.NewScanner(f)
	lines.Buffer(make([]byte, 4096), 4<<20)
	for lines.Scan() {
		var event AuthoringEvent
		if json.Unmarshal(lines.Bytes(), &event) == nil {
			count += event.UnrecognizedAssertions
		}
	}
	return count, lines.Err()
}

var skipLineRe = regexp.MustCompile(`(?im)^\s*flinch\s+skip\s*:\s*(.+?)\s*$`)

// ParseSkipFromText detects an agent's `flinch skip: <reason>` closure.
func ParseSkipFromText(text string) (string, bool) {
	m := skipLineRe.FindStringSubmatch(text)
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

// ReuseFirst inspects the diff between brokenRef and fixedRef for an existing protection
// (such as a newly added test case/row or a valid managed guidance line) and proves it
// without requiring a continuation.
func ReuseFirst(ctx context.Context, repoRoot string, cfg config.Config, brokenRef, fixedRef string, timeout time.Duration, hosts ...string) (ReuseOutcome, error) {
	ctx = runner.WithCommandRepository(ctx, repoRoot)
	if timeout <= 0 {
		timeout = AuthoringBudgetCap
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	candidates, err := ExtractTestCandidates(ctx, repoRoot, cfg, brokenRef, fixedRef)
	if err != nil {
		return ReuseOutcome{Status: ReuseInconclusive, Reason: err.Error()}, nil
	}

	hadInconclusive := false
	lastInconclusiveReason := ""
	unrecognized := 0

	for _, cand := range candidates {
		if cand.Selector == "" || cand.NeighborSelector == "" {
			hadInconclusive = true
			lastInconclusiveReason = "no distinct neighboring case"
			continue
		}
		id := makeReflexID(cand.Path, cand.Selector)
		rep, err := prove.Verify(ctx, repoRoot, prove.CandidateSpec{
			ID:         id,
			Version:    1,
			Rung:       reflex.RungTest,
			Scope:      cand.Scope,
			Obligation: fmt.Sprintf("Preserve behavior checked by %s.", cand.Selector),
			Artifact: reflex.Artifact{
				Tool:     cand.Tool,
				Path:     cand.Path,
				Selector: cand.Selector,
			},
			NeighborSelector: cand.NeighborSelector,
			BrokenRef:        brokenRef,
			FixedRef:         fixedRef,
			TestSupportGlobs: cfg.Tests.Support,
		}, prove.Options{Timeout: timeout})
		if err != nil {
			hadInconclusive = true
			lastInconclusiveReason = err.Error()
			continue
		}
		if rep.Verdict == prove.VerdictVerified {
			r := reflex.Reflex{
				ID:         id,
				Version:    1,
				Owner:      cfg.Policy.Owner,
				Scope:      cand.Scope,
				Rung:       reflex.RungTest,
				Obligation: fmt.Sprintf("Preserve behavior checked by %s.", cand.Selector),
				Artifact: reflex.Artifact{
					Tool:     cand.Tool,
					Path:     cand.Path,
					Selector: cand.Selector,
				},
				Cases: reflex.Cases{
					Allowed: reflex.StringList{cand.NeighborSelector},
				},
				Surfaces: reflex.Surfaces{
					PullRequest: &reflex.PRSurface{Mode: "block"},
					AgentTurn:   &reflex.TurnSurface{Mode: "repair_if_supported"},
				},
				Proof: &reflex.ProofSummary{
					Verdict:       string(rep.Verdict),
					At:            time.Now().UTC().Format("2006-01-02"),
					RuntimeMS:     rep.FixedRuntimeMS,
					RunnerVersion: rep.RunnerVersion,
					ReportHash:    rep.CandidateHash,
				},
			}
			if _, saveErr := reflex.SaveContext(ctx, repoRoot, r, rep.FixedRef); saveErr != nil {
				return ReuseOutcome{Status: ReuseInconclusive, Reason: fmt.Sprintf("could not register proven check: %v", saveErr), UnrecognizedAssertions: unrecognized}, nil
			}
			return ReuseOutcome{
				Status:                 ReuseProven,
				Reflex:                 &r,
				Report:                 &rep,
				Neighbor:               cand.NeighborSelector,
				UnrecognizedAssertions: unrecognized,
				SummaryLine:            fmt.Sprintf("flinch: proved test %s (%s; neighbor %s passed)", cand.Selector, id, cand.NeighborSelector),
			}, nil
		}
		hadInconclusive = true
		if rep.Reason == prove.ReasonNoRecognizedAssertion {
			unrecognized++
		}
		lastInconclusiveReason = fmt.Sprintf("%s (%s)", rep.Reason, rep.ActionableError)
	}

	// Check lint reuse: if a configured repository linter already distinguishes
	// brokenRef from fixedRef and an in-scope legitimate neighbor exists, prove and register it.
	if lintOut, ok := reuseConfiguredLint(ctx, repoRoot, cfg, brokenRef, fixedRef, timeout); ok {
		lintOut.UnrecognizedAssertions = unrecognized
		return lintOut, nil
	}

	if hadInconclusive {
		return ReuseOutcome{
			Status:                 ReuseInconclusive,
			UnrecognizedAssertions: unrecognized,
			Reason:                 "candidate test kept as ordinary test; proof inconclusive: " + lastInconclusiveReason,
		}, nil
	}

	// Check managed context-guide file if changed in the diff.
	guideFile := cfg.ContextGuide.File
	if strings.TrimSpace(guideFile) == "" {
		guideFile = "AGENTS.md"
	}
	if !guideLoaded(ctx, repoRoot, brokenRef, guideFile, hosts) || !guideLoaded(ctx, repoRoot, fixedRef, guideFile, hosts) {
		return ReuseOutcome{Status: ReuseNone, Reason: "no existing instruction file loaded by this host"}, nil
	}
	beforeGuide, beforeErr := gitutil.FileAtRef(ctx, repoRoot, brokenRef, guideFile)
	if beforeErr != nil {
		return ReuseOutcome{Status: ReuseNone, Reason: "the instruction file did not exist before the correction"}, nil
	}
	afterGuide, afterErr := gitutil.FileAtRef(ctx, repoRoot, fixedRef, guideFile)
	if afterErr == nil && string(beforeGuide) != string(afterGuide) {
		val := ValidateManagedLine(beforeGuide, afterGuide, cfg.ContextGuide.MaxLines)
		if val.Changed {
			if val.Valid {
				return ReuseOutcome{
					Status:      ReuseLineValid,
					AddedLines:  val.AddedLines,
					SummaryLine: fmt.Sprintf("flinch: recorded managed guidance line in %s", guideFile),
				}, nil
			}
			if val.SectionFull {
				return ReuseOutcome{
					Status: ReuseLineSectionFull,
					Reason: val.Reason,
				}, nil
			}
			if val.FormattingErr {
				return ReuseOutcome{
					Status: ReuseLineFormatError,
					Reason: val.Reason,
				}, nil
			}
		}
	}

	return ReuseOutcome{Status: ReuseNone}, nil
}

// ExtractTestCandidates discovers newly added test cases and sibling legitimate neighbors
// from the diff between brokenRef and fixedRef across Go, Python, and JS/TS test files.
func ExtractTestCandidates(ctx context.Context, repoRoot string, cfg config.Config, brokenRef, fixedRef string) ([]DiscoveredTestCandidate, error) {
	changes, err := gitutil.DiffFileChanges(ctx, repoRoot, brokenRef, fixedRef)
	if err != nil {
		return nil, err
	}

	classifier := prove.NewClassifier(cfg.Tests.Support, nil, nil, nil)
	var out []DiscoveredTestCandidate
	for _, ch := range changes {
		if ch.Status == "D" || classifier.Classify(ch.Path) != prove.ClassTest {
			continue
		}
		p := filepath.ToSlash(ch.Path)
		base := filepath.Base(p)
		var beforeBytes []byte
		if ch.Status != "A" {
			priorPath := ch.Path
			if ch.OldPath != "" {
				priorPath = ch.OldPath
			}
			beforeBytes, err = gitutil.FileAtRef(ctx, repoRoot, brokenRef, priorPath)
			if err != nil {
				return nil, err
			}
		}
		afterBytes, err := gitutil.FileAtRef(ctx, repoRoot, fixedRef, p)
		if err != nil {
			return nil, err
		}

		scope := productScope(p, changes, cfg.Tests.Support)
		switch {
		case strings.HasSuffix(base, "_test.go"):
			cands := extractGoTestCandidates(p, scope, beforeBytes, afterBytes)
			out = append(out, cands...)
		case strings.HasSuffix(base, ".py"):
			cands := extractPythonTestCandidates(p, scope, beforeBytes, afterBytes)
			out = append(out, cands...)
		default:
			tool := detectJSTestTool(repoRoot, cfg)
			cands := extractJSTestCandidates(tool, p, scope, beforeBytes, afterBytes)
			out = append(out, cands...)
		}
	}
	return out, nil
}

func extractGoTestCandidates(relPath, scope string, beforeBytes, afterBytes []byte) []DiscoveredTestCandidate {
	beforeCases := listGoTestCases(beforeBytes)
	afterCases := listGoTestCases(afterBytes)
	beforeSet := make(map[string]bool, len(beforeCases))
	for _, c := range beforeCases {
		beforeSet[c] = true
	}

	var added []string
	for _, c := range afterCases {
		if !beforeSet[c] {
			added = append(added, c)
		}
	}
	if len(added) == 0 {
		return nil
	}

	// Prefer specific subtest/table rows over parent wrapper functions when both are added.
	hasSubtest := false
	for _, a := range added {
		if strings.Contains(a, "/") {
			hasSubtest = true
			break
		}
	}

	var out []DiscoveredTestCandidate
	for _, sel := range added {
		if hasSubtest && !strings.Contains(sel, "/") {
			continue
		}
		neighbor := pickNeighbor(sel, beforeCases, afterCases)
		out = append(out, DiscoveredTestCandidate{
			Tool:             "go-test",
			Path:             relPath,
			Scope:            scope,
			Selector:         sel,
			NeighborSelector: neighbor,
		})
	}
	return out
}

func listGoTestCases(src []byte) []string {
	if len(src) == 0 {
		return nil
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, 0)
	if err != nil {
		return nil
	}
	var out []string
	seen := make(map[string]bool)
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || !strings.HasPrefix(fn.Name.Name, "Test") || fn.Body == nil {
			continue
		}
		fnName := fn.Name.Name
		foundChild := false

		// 1. Direct t.Run("name", ...) subtests
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Run" {
				return true
			}
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if unq, err := strconv.Unquote(lit.Value); err == nil && unq != "" {
					foundChild = true
					add(fnName + "/" + strings.ReplaceAll(strings.TrimSpace(unq), " ", "_"))
				}
			}
			return true
		})

		// 2. Table-driven slice/map literals inside Test*
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			comp, ok := n.(*ast.CompositeLit)
			if !ok || len(comp.Elts) == 0 {
				return true
			}
			switch comp.Type.(type) {
			case *ast.ArrayType, *ast.MapType:
				for _, elt := range comp.Elts {
					if name, ok := extractGoTableCaseName(elt); ok && name != "" {
						foundChild = true
						add(fnName + "/" + strings.ReplaceAll(strings.TrimSpace(name), " ", "_"))
					}
				}
			}
			return true
		})

		if !foundChild {
			add(fnName)
		}
	}
	return out
}

func extractGoTableCaseName(elt ast.Expr) (string, bool) {
	if kv, ok := elt.(*ast.KeyValueExpr); ok {
		if lit, ok := kv.Key.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if s, err := strconv.Unquote(lit.Value); err == nil {
				return s, true
			}
		}
	}
	comp, ok := elt.(*ast.CompositeLit)
	if !ok {
		if un, ok := elt.(*ast.UnaryExpr); ok {
			comp, _ = un.X.(*ast.CompositeLit)
		}
	}
	if comp == nil {
		return "", false
	}
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
	if len(comp.Elts) > 0 {
		if lit, ok := comp.Elts[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if s, err := strconv.Unquote(lit.Value); err == nil {
				return s, true
			}
		}
	}
	return "", false
}

var pyTestFnRe = regexp.MustCompile(`(?m)^def\s+(test_[A-Za-z0-9_]+)\s*\(`)

func extractPythonTestCandidates(relPath, scope string, beforeBytes, afterBytes []byte) []DiscoveredTestCandidate {
	listPy := func(b []byte) []string {
		var res []string
		for _, m := range pyTestFnRe.FindAllStringSubmatch(string(b), -1) {
			res = append(res, relPath+"::"+m[1])
		}
		return res
	}
	before := listPy(beforeBytes)
	after := listPy(afterBytes)
	bSet := make(map[string]bool, len(before))
	for _, c := range before {
		bSet[c] = true
	}
	var out []DiscoveredTestCandidate
	for _, sel := range after {
		if bSet[sel] {
			continue
		}
		out = append(out, DiscoveredTestCandidate{
			Tool:             "pytest",
			Path:             relPath,
			Scope:            scope,
			Selector:         sel,
			NeighborSelector: pickNeighbor(sel, before, after),
		})
	}
	return out
}

var (
	jsDescribeRe  = regexp.MustCompile(`\bdescribe\s*\(\s*['"\x60]([^'"\x60]+)['"\x60]`)
	jsItRe        = regexp.MustCompile(`\b(?:it|test)\s*\(\s*['"\x60]([^'"\x60]+)['"\x60]`)
	jsEachStartRe = regexp.MustCompile(`\b(?:it|test)\.each\s*(?:<[^>]+>)?\s*\(\s*\[`)
	jsRowNameObj  = regexp.MustCompile(`\b(?:name|title|desc|case)\s*:\s*['"\x60]([^'"\x60]+)['"\x60]`)
	jsRowTuple    = regexp.MustCompile(`\[\s*['"\x60]([^'"\x60]+)['"\x60]`)
	jsEachTitleRe = regexp.MustCompile(`\]\s*\)\s*\(\s*['"\x60]([^'"\x60]+)['"\x60]`)
)

func extractJSTestCandidates(tool, relPath, scope string, beforeBytes, afterBytes []byte) []DiscoveredTestCandidate {
	before := listJSTestCases(string(beforeBytes))
	after := listJSTestCases(string(afterBytes))
	bSet := make(map[string]bool, len(before))
	for _, c := range before {
		bSet[c] = true
	}
	var out []DiscoveredTestCandidate
	for _, sel := range after {
		if bSet[sel] {
			continue
		}
		out = append(out, DiscoveredTestCandidate{
			Tool:             tool,
			Path:             relPath,
			Scope:            scope,
			Selector:         sel,
			NeighborSelector: pickNeighbor(sel, before, after),
		})
	}
	return out
}

func listJSTestCases(src string) []string {
	if strings.TrimSpace(src) == "" {
		return nil
	}
	var out []string
	seen := make(map[string]bool)
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}

	describeName := ""
	if m := jsDescribeRe.FindStringSubmatch(src); m != nil {
		describeName = strings.TrimSpace(m[1])
	}
	qualify := func(title string) string {
		if describeName != "" {
			return describeName + " › " + strings.TrimSpace(title)
		}
		return strings.TrimSpace(title)
	}

	// 1. Direct it(...) / test(...) calls
	for _, m := range jsItRe.FindAllStringSubmatch(src, -1) {
		add(qualify(m[1]))
	}

	// 2. test.each([...])('title', ...) rows
	eachLocs := jsEachStartRe.FindAllStringIndex(src, -1)
	for _, loc := range eachLocs {
		rest := src[loc[0]:]
		titleMatch := jsEachTitleRe.FindStringSubmatchIndex(rest)
		if titleMatch == nil {
			continue
		}
		tableBody := rest[:titleMatch[0]+1]
		titleTemplate := rest[titleMatch[2]:titleMatch[3]]

		var rowNames []string
		for _, m := range jsRowNameObj.FindAllStringSubmatch(tableBody, -1) {
			rowNames = append(rowNames, m[1])
		}
		if len(rowNames) == 0 {
			for _, m := range jsRowTuple.FindAllStringSubmatch(tableBody, -1) {
				rowNames = append(rowNames, m[1])
			}
		}
		for _, rn := range rowNames {
			expanded := titleTemplate
			for _, ph := range []string{"$name", "$title", "$desc", "$case", "%s"} {
				expanded = strings.Replace(expanded, ph, rn, 1)
			}
			add(qualify(expanded))
		}
	}
	return out
}

func pickNeighbor(sel string, beforeCases, afterCases []string) string {
	parentOf := func(s string) string {
		if idx := strings.LastIndex(s, "/"); idx > 0 {
			return s[:idx]
		}
		if idx := strings.LastIndex(s, " › "); idx > 0 {
			return s[:idx]
		}
		return ""
	}
	selParent := parentOf(sel)

	// 1. Prefer a pre-existing sibling case in the same parent test/describe block.
	for _, b := range beforeCases {
		if b != sel && (selParent == "" || parentOf(b) == selParent) {
			return b
		}
	}
	// 2. Next prefer any other sibling in afterCases with the same parent.
	for _, a := range afterCases {
		if a != sel && (selParent == "" || parentOf(a) == selParent) {
			return a
		}
	}
	// 3. Fall back to any distinct pre-existing or new test in the same file.
	for _, b := range beforeCases {
		if b != sel {
			return b
		}
	}
	for _, a := range afterCases {
		if a != sel {
			return a
		}
	}
	return ""
}

func detectJSTestTool(repoRoot string, cfg config.Config) string {
	for _, r := range cfg.Tests.Runners {
		if r.Kind == "vitest" || r.Kind == "jest" {
			return r.Kind
		}
	}
	if data, err := os.ReadFile(filepath.Join(repoRoot, "package.json")); err == nil {
		if strings.Contains(string(data), `"jest"`) && !strings.Contains(string(data), `"vitest"`) {
			return "jest"
		}
	}
	return "vitest"
}

func scopeForTestFile(relPath string) string {
	dir := filepath.ToSlash(filepath.Dir(relPath))
	if dir == "." || dir == "" {
		return "**"
	}
	return dir + "/**"
}

var nonSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

func makeReflexID(relPath, selector string) string {
	identity := filepath.ToSlash(relPath) + "\x00" + selector
	digest := sha256.Sum256([]byte(identity))
	slug := strings.Trim(nonSlugRe.ReplaceAllString(strings.ToLower(selector), "-"), "-")
	if len(slug) > 48 {
		slug = strings.TrimRight(slug[:48], "-")
	}
	if slug == "" {
		slug = "check"
	}
	return fmt.Sprintf("%s-%x", slug, digest[:8])
}

// Use the actual product files from the fix. Go tests also cover changes in their package.
func productScope(testPath string, changes []gitutil.FileChange, support []string) string {
	classifier := prove.NewClassifier(support, nil, nil, nil)
	literal := strings.NewReplacer("\\", "\\\\", "*", "\\*", "?", "\\?", "[", "\\[", "]", "\\]", "{", "\\{", "}", "\\}", ",", "\\,")
	paths := map[string]bool{literal.Replace(testPath): true}
	for _, ch := range changes {
		p := filepath.ToSlash(ch.Path)
		if !isIgnoredForNomination(p) && classifier.Classify(p) == prove.ClassProduct {
			paths[literal.Replace(p)] = true
		}
	}
	if strings.HasSuffix(testPath, "_test.go") {
		dir := filepath.ToSlash(filepath.Dir(testPath))
		pattern := "*.go"
		if dir != "." {
			pattern = literal.Replace(dir) + "/*.go"
		}
		paths[pattern] = true
	}
	var sorted []string
	for p := range paths {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)
	if len(sorted) == 1 {
		return sorted[0]
	}
	return "{" + strings.Join(sorted, ",") + "}"
}

// reuseConfiguredLint checks whether any linter configured in .flinch/flinch.toml catches
// a rule violation on brokenRef that disappeared on fixedRef and passes an in-scope neighbor.
func reuseConfiguredLint(ctx context.Context, repoRoot string, cfg config.Config, brokenRef, fixedRef string, timeout time.Duration) (ReuseOutcome, bool) {
	if len(cfg.Lint) == 0 {
		return ReuseOutcome{}, false
	}
	changes, err := gitutil.DiffFileChanges(ctx, repoRoot, brokenRef, fixedRef)
	if err != nil {
		return ReuseOutcome{Status: ReuseInconclusive, Reason: err.Error()}, true
	}
	type lintTargets struct {
		rule  config.LintConfig
		files []string
	}
	var targets []lintTargets
	for _, lint := range cfg.Lint {
		var files []string
		for _, ch := range changes {
			if ch.Status == "D" || isIgnoredForNomination(ch.Path) {
				continue
			}
			path := filepath.ToSlash(ch.Path)
			if len(lint.Paths) == 0 {
				files = append(files, path)
				continue
			}
			for _, pattern := range lint.Paths {
				if (reflex.Reflex{Scope: pattern}).MatchesPath(path) {
					files = append(files, path)
					break
				}
			}
		}
		if len(files) > 0 {
			targets = append(targets, lintTargets{rule: lint, files: files})
		}
	}
	if len(targets) == 0 {
		return ReuseOutcome{}, false
	}

	brokenWT, err := gitutil.CreateWorktree(ctx, repoRoot, brokenRef, "reuse-lint-broken-")
	if err != nil {
		return ReuseOutcome{Status: ReuseInconclusive, Reason: err.Error()}, true
	}
	defer brokenWT.Cleanup(ctx)

	fixedWT, err := gitutil.CreateWorktree(ctx, repoRoot, fixedRef, "reuse-lint-fixed-")
	if err != nil {
		return ReuseOutcome{Status: ReuseInconclusive, Reason: err.Error()}, true
	}
	defer fixedWT.Cleanup(ctx)

	for _, target := range targets {
		lc := target.rule
		art := reflex.Artifact{
			Tool:    lc.Kind,
			Path:    lc.Config,
			Command: lc.Command,
		}
		bRun, bErr := runner.RunArtifact(ctx, brokenWT.Path, art, target.files, timeout/2)
		if errors.Is(bErr, runner.ErrCommandApproval) {
			return ReuseOutcome{Status: ReuseInconclusive, Reason: runner.CommandPermissionNotice, CommandApprovalDigest: runner.CommandApprovalDigest(bErr)}, true
		}
		if bErr != nil || bRun.TimedOut || (bRun.ExitCode != 0 && len(bRun.Findings) == 0) {
			return ReuseOutcome{Status: ReuseInconclusive, Reason: "configured lint could not complete on the broken tree"}, true
		}
		if len(bRun.Findings) == 0 {
			continue
		}
		fRun, fErr := runner.RunArtifact(ctx, fixedWT.Path, art, target.files, timeout/2)
		if errors.Is(fErr, runner.ErrCommandApproval) {
			return ReuseOutcome{Status: ReuseInconclusive, Reason: runner.CommandPermissionNotice, CommandApprovalDigest: runner.CommandApprovalDigest(fErr)}, true
		}
		if fErr != nil || fRun.TimedOut || (fRun.ExitCode != 0 && len(fRun.Findings) == 0) {
			return ReuseOutcome{Status: ReuseInconclusive, Reason: "configured lint could not complete on the fixed tree"}, true
		}

		// Find any rule that flagged the broken incident site and disappeared on fixed.
		seenRule := make(map[string]bool)
		for _, bf := range bRun.Findings {
			ruleID := bf.Rule
			if ruleID == "" {
				ruleID = bf.Selector
			}
			if ruleID == "" || seenRule[ruleID] {
				continue
			}
			seenRule[ruleID] = true

			match := finding.MatchIncidentSites(changes, filterByRule(bRun.Findings, ruleID), filterByRule(fRun.Findings, ruleID))
			if len(match.MatchedBroken) == 0 || len(match.RemainingFixed) > 0 || match.Ambiguous {
				continue
			}

			scope := scopeForTestFile(bf.Path)
			neighborPath, neighborContent, hasNeighbor := findInScopeAllowedNeighbor(fixedWT.Path, scope, bf.Path, filepath.Ext(bf.Path))
			if !hasNeighbor {
				return ReuseOutcome{Status: ReuseInconclusive, Reason: "no in-scope permitted example for the lint rule"}, true
			}

			id := makeReflexID(bf.Path, lc.Kind+"-"+ruleID)
			candArt := reflex.Artifact{
				Tool:     lc.Kind,
				Path:     lc.Config,
				Rule:     ruleID,
				Selector: ruleID,
				Command:  lc.Command,
			}
			rep, pErr := prove.Verify(ctx, repoRoot, prove.CandidateSpec{
				ID:         id,
				Version:    1,
				Rung:       reflex.RungLint,
				Scope:      scope,
				Obligation: fmt.Sprintf("Prevent %s (%s) violations in %s.", ruleID, lc.Kind, scope),
				Artifact:   candArt,
				AllowedCases: []prove.AllowedCase{
					{
						Name:       filepath.Base(neighborPath),
						TargetPath: neighborPath,
						Content:    neighborContent,
					},
				},
				BrokenRef: brokenRef,
				FixedRef:  fixedRef,
			}, prove.Options{Timeout: timeout})
			if pErr != nil || rep.Verdict != prove.VerdictVerified {
				return ReuseOutcome{Status: ReuseInconclusive, Reason: "configured lint did not pass verification"}, true
			}

			r := reflex.Reflex{
				ID:         id,
				Version:    1,
				Owner:      cfg.Policy.Owner,
				Scope:      scope,
				Rung:       reflex.RungLint,
				Obligation: fmt.Sprintf("Prevent %s (%s) violations in %s.", ruleID, lc.Kind, scope),
				Artifact:   candArt,
				Cases:      reflex.Cases{Allowed: reflex.StringList{neighborPath}},
				Surfaces: reflex.Surfaces{
					PullRequest: &reflex.PRSurface{Mode: "block"},
					AgentTurn:   &reflex.TurnSurface{Mode: "repair_if_supported"},
				},
				Proof: &reflex.ProofSummary{
					Verdict:       string(rep.Verdict),
					At:            time.Now().UTC().Format("2006-01-02"),
					Census:        rep.Census,
					RuntimeMS:     rep.TotalRuntimeMS,
					RunnerVersion: rep.RunnerVersion,
					ReportHash:    rep.CandidateHash,
				},
			}
			if _, saveErr := reflex.SaveContext(ctx, repoRoot, r, rep.FixedRef); saveErr != nil {
				return ReuseOutcome{Status: ReuseInconclusive, Reason: saveErr.Error()}, true
			}
			return ReuseOutcome{
				Status:      ReuseProven,
				Reflex:      &r,
				Report:      &rep,
				Neighbor:    neighborPath,
				SummaryLine: fmt.Sprintf("flinch: proved lint rule %s/%s (%s; neighbor %s passed)", lc.Kind, ruleID, id, neighborPath),
			}, true
		}
	}
	return ReuseOutcome{}, false
}

func filterByRule(all []finding.Finding, ruleID string) []finding.Finding {
	var out []finding.Finding
	for _, f := range all {
		if f.Rule == ruleID || f.Selector == ruleID {
			out = append(out, f)
		}
	}
	return out
}

func findInScopeAllowedNeighbor(fixedRoot, scope, changedPath, ext string) (string, string, bool) {
	r := reflex.Reflex{Scope: scope}
	dir := filepath.ToSlash(filepath.Dir(changedPath))
	absDir := filepath.Join(fixedRoot, filepath.FromSlash(dir))
	entries, err := os.ReadDir(absDir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ext {
				continue
			}
			rel := filepath.ToSlash(filepath.Join(dir, e.Name()))
			if rel == changedPath || !r.MatchesPath(rel) {
				continue
			}
			if data, readErr := os.ReadFile(filepath.Join(absDir, e.Name())); readErr == nil && len(data) > 0 {
				return rel, string(data), true
			}
		}
	}
	return "", "", false
}
