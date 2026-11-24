package runner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/angelnicolasc/flinch/internal/finding"
	"github.com/angelnicolasc/flinch/internal/reflex"
)

// RunArtifact executes a lint, structure, or command artifact from rootDir and returns
// normalized, symbol-enriched findings alongside raw output and exit code.
func RunArtifact(ctx context.Context, rootDir string, art reflex.Artifact, targetFiles []string, timeout time.Duration) (LintRunResult, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var (
		res LintRunResult
		err error
	)
	switch art.Tool {
	case "go-vet":
		res, err = runGoVet(tctx, rootDir, art, targetFiles)
	case "go-build":
		res, err = runGoBuild(tctx, rootDir, art)
	case "ruff":
		res, err = runRuff(tctx, rootDir, art, targetFiles)
	case "eslint":
		res, err = runESLint(tctx, rootDir, art, targetFiles)
	case "golangci-lint":
		res, err = runGolangCILint(tctx, rootDir, art, targetFiles)
	case "ast-grep":
		res, err = runAstGrep(tctx, rootDir, art, targetFiles)
	case "command", "script":
		res, err = runCommandArtifact(tctx, rootDir, art, targetFiles)
	default:
		return LintRunResult{}, fmt.Errorf("unsupported lint/structure tool %q", art.Tool)
	}
	if err != nil {
		return res, err
	}
	if errors.Is(tctx.Err(), context.DeadlineExceeded) {
		res.TimedOut = true
		return res, nil
	}
	res.Findings = finding.Enrich(rootDir, res.Findings)
	return res, nil
}

func runGoVet(ctx context.Context, rootDir string, art reflex.Artifact, targetFiles []string) (LintRunResult, error) {
	args := []string{"vet", "-json"}
	if art.Path != "" && strings.HasSuffix(art.Path, ".exe") || strings.Contains(art.Command, "-vettool=") {
		if strings.Contains(art.Command, "-vettool=") {
			for _, tok := range strings.Fields(art.Command) {
				if strings.HasPrefix(tok, "-vettool=") {
					args = append(args, tok)
				}
			}
		}
	}
	pkgs := goPackagesFromFiles(rootDir, targetFiles)
	args = append(args, pkgs...)

	start := time.Now()
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = rootDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	res := LintRunResult{
		Tool:        "go-vet",
		ToolVersion: ProbeTool(ctx, []string{"go"}, "version").Version,
		Command:     "go " + strings.Join(args, " "),
		DurationMS:  time.Since(start).Milliseconds(),
		Stdout:      stdout.String(),
		Stderr:      stderr.String(),
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return res, err
		}
	}
	// go vet -json writes structured JSON objects to stderr (and occasionally stdout).
	combined := append(append([]byte(nil), stderr.Bytes()...), '\n')
	combined = append(combined, stdout.Bytes()...)
	res.Findings = ParseGoVetJSON(rootDir, combined)
	return res, nil
}

// ParseGoVetJSON parses the stream of JSON objects emitted by `go vet -json`.
func ParseGoVetJSON(rootDir string, data []byte) []finding.Finding {
	type vetDiag struct {
		Posn    string `json:"posn"`
		Message string `json:"message"`
	}
	var out []finding.Finding
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		var pkgMap map[string]map[string][]vetDiag
		if err := dec.Decode(&pkgMap); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			// Skip non-JSON lines if any precede JSON output.
			break
		}
		for _, analyzers := range pkgMap {
			for analyzer, diags := range analyzers {
				for _, d := range diags {
					p, line, col := parsePosn(d.Posn)
					out = append(out, finding.Finding{
						Tool:      "go-vet",
						Rule:      analyzer,
						Selector:  analyzer,
						Path:      finding.NormalizePath(rootDir, p),
						StartLine: line,
						EndLine:   line,
						StartCol:  col,
						Message:   d.Message,
						Severity:  "error",
					})
				}
			}
		}
	}
	return out
}

func runGoBuild(ctx context.Context, rootDir string, art reflex.Artifact) (LintRunResult, error) {
	args := []string{"build", "./..."}
	start := time.Now()
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = rootDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	res := LintRunResult{
		Tool:        "go-build",
		ToolVersion: ProbeTool(ctx, []string{"go"}, "version").Version,
		Command:     "go build ./...",
		DurationMS:  time.Since(start).Milliseconds(),
		Stdout:      stdout.String(),
		Stderr:      stderr.String(),
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return res, err
		}
	}
	selector := art.Selector
	if selector == "" {
		selector = "go-build"
	}
	res.Findings = ParseCompilerDiagnostics(rootDir, "go-build", selector, stderr.String()+"\n"+stdout.String())
	return res, nil
}

func runRuff(ctx context.Context, rootDir string, art reflex.Artifact, targetFiles []string) (LintRunResult, error) {
	args := []string{"check", "--output-format", "json"}
	if art.Path != "" {
		args = append(args, "--config", art.Path)
	}
	if art.Rule != "" {
		args = append(args, "--select", art.Rule)
	}
	targets := relTargetsOrAll(rootDir, targetFiles)
	if len(targets) == 0 {
		args = append(args, ".")
	} else {
		args = append(args, targets...)
	}

	start := time.Now()
	cmd := exec.CommandContext(ctx, "ruff", args...)
	cmd.Dir = rootDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	res := LintRunResult{
		Tool:        "ruff",
		ToolVersion: ProbeTool(ctx, []string{"ruff"}, "--version").Version,
		Command:     "ruff " + strings.Join(args, " "),
		DurationMS:  time.Since(start).Milliseconds(),
		Stdout:      stdout.String(),
		Stderr:      stderr.String(),
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return res, err
		}
	}
	res.Findings = ParseRuffJSON(rootDir, stdout.Bytes())
	return res, nil
}

// ParseRuffJSON decodes `ruff check --output-format json` output into normalized findings.
func ParseRuffJSON(rootDir string, data []byte) []finding.Finding {
	type ruffLoc struct {
		Row    int `json:"row"`
		Column int `json:"column"`
	}
	type ruffDiag struct {
		Code        string  `json:"code"`
		Filename    string  `json:"filename"`
		Message     string  `json:"message"`
		Location    ruffLoc `json:"location"`
		EndLocation ruffLoc `json:"end_location"`
	}
	var items []ruffDiag
	if err := json.Unmarshal(bytes.TrimSpace(data), &items); err != nil {
		return nil
	}
	out := make([]finding.Finding, 0, len(items))
	for _, it := range items {
		endLine := it.EndLocation.Row
		if endLine == 0 {
			endLine = it.Location.Row
		}
		out = append(out, finding.Finding{
			Tool:      "ruff",
			Rule:      it.Code,
			Selector:  it.Code,
			Path:      finding.NormalizePath(rootDir, it.Filename),
			StartLine: it.Location.Row,
			EndLine:   endLine,
			StartCol:  it.Location.Column,
			EndCol:    it.EndLocation.Column,
			Message:   it.Message,
			Severity:  "error",
		})
	}
	return out
}

func runESLint(ctx context.Context, rootDir string, art reflex.Artifact, targetFiles []string) (LintRunResult, error) {
	args := []string{"--format", "json"}
	if art.Path != "" {
		args = append(args, "--config", art.Path)
	}
	targets := relTargetsOrAll(rootDir, targetFiles)
	if len(targets) == 0 {
		args = append(args, ".")
	} else {
		args = append(args, targets...)
	}

	start := time.Now()
	cmd := exec.CommandContext(ctx, "eslint", args...)
	cmd.Dir = rootDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	res := LintRunResult{
		Tool:        "eslint",
		ToolVersion: ProbeTool(ctx, []string{"eslint"}, "--version").Version,
		Command:     "eslint " + strings.Join(args, " "),
		DurationMS:  time.Since(start).Milliseconds(),
		Stdout:      stdout.String(),
		Stderr:      stderr.String(),
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return res, err
		}
	}
	res.Findings = ParseESLintJSON(rootDir, stdout.Bytes())
	return res, nil
}

// ParseESLintJSON decodes `eslint --format json` output into normalized findings.
func ParseESLintJSON(rootDir string, data []byte) []finding.Finding {
	type esMsg struct {
		RuleID   string `json:"ruleId"`
		Severity int    `json:"severity"`
		Message  string `json:"message"`
		Line     int    `json:"line"`
		EndLine  int    `json:"endLine"`
		Column   int    `json:"column"`
	}
	type esFile struct {
		FilePath string  `json:"filePath"`
		Messages []esMsg `json:"messages"`
	}
	var files []esFile
	if err := json.Unmarshal(bytes.TrimSpace(data), &files); err != nil {
		return nil
	}
	var out []finding.Finding
	for _, f := range files {
		for _, m := range f.Messages {
			sev := "warning"
			if m.Severity >= 2 {
				sev = "error"
			}
			end := m.EndLine
			if end == 0 {
				end = m.Line
			}
			out = append(out, finding.Finding{
				Tool:      "eslint",
				Rule:      m.RuleID,
				Selector:  m.RuleID,
				Path:      finding.NormalizePath(rootDir, f.FilePath),
				StartLine: m.Line,
				EndLine:   end,
				StartCol:  m.Column,
				Message:   m.Message,
				Severity:  sev,
			})
		}
	}
	return out
}

func runGolangCILint(ctx context.Context, rootDir string, art reflex.Artifact, targetFiles []string) (LintRunResult, error) {
	args := []string{"run", "--out-format", "json"}
	if art.Path != "" {
		args = append(args, "--config", art.Path)
	}
	args = append(args, goPackagesFromFiles(rootDir, targetFiles)...)

	start := time.Now()
	cmd := exec.CommandContext(ctx, "golangci-lint", args...)
	cmd.Dir = rootDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	res := LintRunResult{
		Tool:        "golangci-lint",
		ToolVersion: ProbeTool(ctx, []string{"golangci-lint"}, "--version").Version,
		Command:     "golangci-lint " + strings.Join(args, " "),
		DurationMS:  time.Since(start).Milliseconds(),
		Stdout:      stdout.String(),
		Stderr:      stderr.String(),
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return res, err
		}
	}
	res.Findings = ParseGolangCILintJSON(rootDir, stdout.Bytes())
	return res, nil
}

// ParseGolangCILintJSON decodes `golangci-lint run --out-format json` output.
func ParseGolangCILintJSON(rootDir string, data []byte) []finding.Finding {
	type glPos struct {
		Filename string `json:"Filename"`
		Line     int    `json:"Line"`
		Column   int    `json:"Column"`
	}
	type glIssue struct {
		FromLinter string `json:"FromLinter"`
		Text       string `json:"Text"`
		Pos        glPos  `json:"Pos"`
		Severity   string `json:"Severity"`
	}
	var report struct {
		Issues []glIssue `json:"Issues"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(data), &report); err != nil {
		return nil
	}
	out := make([]finding.Finding, 0, len(report.Issues))
	for _, iss := range report.Issues {
		sev := iss.Severity
		if sev == "" {
			sev = "error"
		}
		out = append(out, finding.Finding{
			Tool:      "golangci-lint",
			Rule:      iss.FromLinter,
			Selector:  iss.FromLinter,
			Path:      finding.NormalizePath(rootDir, iss.Pos.Filename),
			StartLine: iss.Pos.Line,
			EndLine:   iss.Pos.Line,
			StartCol:  iss.Pos.Column,
			Message:   iss.Text,
			Severity:  sev,
		})
	}
	return out
}

func runAstGrep(ctx context.Context, rootDir string, art reflex.Artifact, targetFiles []string) (LintRunResult, error) {
	info := ProbeTool(ctx, []string{"ast-grep", "sg"}, "--version")
	if !info.Available {
		return LintRunResult{}, fmt.Errorf("ast-grep (or sg) binary not found in PATH")
	}
	args := []string{"scan", "--json=stream"}
	if art.Path != "" {
		if strings.HasSuffix(art.Path, "sgconfig.yml") || strings.HasSuffix(art.Path, "sgconfig.yaml") {
			args = append(args, "--config", art.Path)
		} else {
			args = append(args, "--rule", art.Path)
		}
	}
	args = append(args, relTargetsOrAll(rootDir, targetFiles)...)

	start := time.Now()
	cmd := exec.CommandContext(ctx, info.Path, args...)
	cmd.Dir = rootDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	res := LintRunResult{
		Tool:        "ast-grep",
		ToolVersion: info.Version,
		Command:     filepath.Base(info.Path) + " " + strings.Join(args, " "),
		DurationMS:  time.Since(start).Milliseconds(),
		Stdout:      stdout.String(),
		Stderr:      stderr.String(),
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return res, err
		}
	}
	res.Findings = ParseAstGrepJSON(rootDir, stdout.Bytes())
	return res, nil
}

// ParseAstGrepJSON parses both NDJSON (`--json=stream`) and JSON array output from ast-grep.
func ParseAstGrepJSON(rootDir string, data []byte) []finding.Finding {
	type sgPos struct {
		Line   int `json:"line"`   // 0-based in ast-grep
		Column int `json:"column"` // 0-based in ast-grep
	}
	type sgRange struct {
		Start sgPos `json:"start"`
		End   sgPos `json:"end"`
	}
	type sgMatch struct {
		RuleID   string  `json:"ruleId"`
		File     string  `json:"file"`
		Message  string  `json:"message"`
		Severity string  `json:"severity"`
		Text     string  `json:"text"`
		Range    sgRange `json:"range"`
	}
	toFinding := func(m sgMatch) finding.Finding {
		return finding.Finding{
			Tool:      "ast-grep",
			Rule:      m.RuleID,
			Selector:  m.RuleID,
			Path:      finding.NormalizePath(rootDir, m.File),
			StartLine: m.Range.Start.Line + 1,
			EndLine:   m.Range.End.Line + 1,
			StartCol:  m.Range.Start.Column + 1,
			EndCol:    m.Range.End.Column + 1,
			Context:   finding.NormalizeContext(m.Text),
			Message:   m.Message,
			Severity:  strings.ToLower(m.Severity),
		}
	}

	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil
	}
	if trimmed[0] == '[' {
		var arr []sgMatch
		if err := json.Unmarshal(trimmed, &arr); err == nil {
			out := make([]finding.Finding, 0, len(arr))
			for _, m := range arr {
				out = append(out, toFinding(m))
			}
			return out
		}
	}
	var out []finding.Finding
	scanner := bufio.NewScanner(bytes.NewReader(trimmed))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var m sgMatch
		if err := json.Unmarshal(line, &m); err == nil && m.File != "" {
			out = append(out, toFinding(m))
		}
	}
	return out
}

func runCommandArtifact(ctx context.Context, rootDir string, art reflex.Artifact, targetFiles []string) (LintRunResult, error) {
	if strings.TrimSpace(art.Command) == "" {
		return LintRunResult{}, fmt.Errorf("command artifact requires artifact.command")
	}
	start := time.Now()
	cmd := shellCommand(ctx, art.Command)
	cmd.Dir = rootDir
	env := append(os.Environ(), "FLINCH_ROOT="+rootDir)
	if len(targetFiles) > 0 {
		env = append(env, "FLINCH_FILES="+strings.Join(targetFiles, " "))
	}
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	res := LintRunResult{
		Tool:       art.Tool,
		Command:    art.Command,
		DurationMS: time.Since(start).Milliseconds(),
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return res, err
		}
	}

	// Attempt structured Flinch JSON findings first; fall back to path:line:col diagnostics.
	if parsed, ok := ParseCommandJSONFindings(rootDir, art, stdout.Bytes()); ok {
		res.Findings = parsed
	} else {
		rule := art.Selector
		if rule == "" {
			rule = art.Rule
		}
		if rule == "" {
			rule = "command"
		}
		res.Findings = ParseCompilerDiagnostics(rootDir, art.Tool, rule, stdout.String()+"\n"+stderr.String())
	}
	return res, nil
}

// ParseCommandJSONFindings decodes either a JSON array of Findings or {"findings": [...]}.
func ParseCommandJSONFindings(rootDir string, art reflex.Artifact, data []byte) ([]finding.Finding, bool) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, false
	}
	var items []finding.Finding
	if trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return nil, false
		}
	} else if trimmed[0] == '{' {
		var wrap struct {
			Findings []finding.Finding `json:"findings"`
		}
		if err := json.Unmarshal(trimmed, &wrap); err != nil || wrap.Findings == nil {
			return nil, false
		}
		items = wrap.Findings
	} else {
		return nil, false
	}
	for i := range items {
		if items[i].Tool == "" {
			items[i].Tool = art.Tool
		}
		if items[i].Rule == "" {
			items[i].Rule = art.Rule
		}
		if items[i].Selector == "" {
			items[i].Selector = items[i].Rule
		}
		items[i].Path = finding.NormalizePath(rootDir, items[i].Path)
	}
	return items, true
}

var diagLineRe = regexp.MustCompile(`^([^:\r\n]+?\.[A-Za-z0-9_]+):(\d+)(?::(\d+))?:\s*(?:\[([^\]]+)\]\s*)?(.*)$`)

// ParseCompilerDiagnostics parses standard `path:line[:col]: [optional-rule] message` lines.
func ParseCompilerDiagnostics(rootDir, tool, defaultRule, output string) []finding.Finding {
	var out []finding.Finding
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		m := diagLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		lineNum, _ := strconv.Atoi(m[2])
		colNum, _ := strconv.Atoi(m[3])
		rule := defaultRule
		if m[4] != "" {
			rule = m[4]
		}
		out = append(out, finding.Finding{
			Tool:      tool,
			Rule:      rule,
			Selector:  rule,
			Path:      finding.NormalizePath(rootDir, m[1]),
			StartLine: lineNum,
			EndLine:   lineNum,
			StartCol:  colNum,
			Message:   strings.TrimSpace(m[5]),
			Severity:  "error",
		})
	}
	return out
}

func parsePosn(posn string) (string, int, int) {
	// Handles both POSIX (/a/b.go:10:2) and Windows (C:\a\b.go:10:2) positions.
	parts := strings.Split(posn, ":")
	if len(parts) < 2 {
		return posn, 0, 0
	}
	if len(parts) >= 3 {
		col, errCol := strconv.Atoi(parts[len(parts)-1])
		line, errLine := strconv.Atoi(parts[len(parts)-2])
		if errCol == nil && errLine == nil {
			return strings.Join(parts[:len(parts)-2], ":"), line, col
		}
	}
	if line, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
		return strings.Join(parts[:len(parts)-1], ":"), line, 0
	}
	return posn, 0, 0
}

func goPackagesFromFiles(rootDir string, files []string) []string {
	if len(files) == 0 {
		return []string{"./..."}
	}
	seen := make(map[string]bool)
	var pkgs []string
	for _, f := range files {
		if !strings.HasSuffix(f, ".go") {
			continue
		}
		dir := filepath.ToSlash(filepath.Dir(filepath.ToSlash(strings.TrimPrefix(f, "./"))))
		pkg := "./" + dir
		if dir == "." || dir == "" {
			pkg = "."
		}
		if !seen[pkg] {
			seen[pkg] = true
			pkgs = append(pkgs, pkg)
		}
	}
	if len(pkgs) == 0 {
		return []string{"./..."}
	}
	return pkgs
}
