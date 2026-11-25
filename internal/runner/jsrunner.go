package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// ErrLockfileMismatch indicates the proof tree lockfile differs from the main checkout,
// so reusing node_modules would be unsound (Flinch never installs dependencies during proofs).
var ErrLockfileMismatch = errors.New("package lockfile differs from checkout; cannot safely reuse node_modules")

type jsJSONReport struct {
	Success       bool            `json:"success"`
	NumTotalTests int             `json:"numTotalTests"`
	TestResults   []jsSuiteResult `json:"testResults"`
}

type jsSuiteResult struct {
	Name             string              `json:"name"`
	Status           string              `json:"status"`
	Message          string              `json:"message"`
	AssertionResults []jsAssertionResult `json:"assertionResults"`
}

type jsAssertionResult struct {
	AncestorTitles  []string `json:"ancestorTitles"`
	Title           string   `json:"title"`
	FullName        string   `json:"fullName"`
	Status          string   `json:"status"` // passed | failed | pending | skipped | todo | disabled
	Duration        float64  `json:"duration"`
	FailureMessages []string `json:"failureMessages"`
}

// BuildJSTestNameRegex converts a full test name such as "retryDelay › uses explicit zero"
// or "retryDelay > uses explicit zero" into an anchored regex matching Vitest/Jest fullName,
// accounting for Vitest 3.x single-quoted $var interpolations in test.each titles.
func BuildJSTestNameRegex(selector string) string {
	sel := strings.TrimSpace(selector)
	if sel == "" {
		return ""
	}
	norm := strings.ReplaceAll(sel, " > ", " › ")
	parts := strings.Split(norm, " › ")
	escaped := make([]string, 0, len(parts))
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		unquoted := strings.Trim(p, "'")
		q := regexp.QuoteMeta(unquoted)
		if i == len(parts)-1 && len(parts) > 0 {
			// Vitest 3.x wraps string values interpolated via $name in test.each titles with single quotes.
			escaped = append(escaped, "'?"+q+"'?")
		} else {
			escaped = append(escaped, q)
		}
	}
	if len(escaped) == 0 {
		return ""
	}
	return "^" + strings.Join(escaped, `\s+`) + "$"
}

// RunVitest runs Vitest from the owning workspace package directory, writing structured JSON
// to a temporary file via `--reporter=json --outputFile` so test stdout cannot corrupt results.
func RunVitest(ctx context.Context, repoRoot, proofRoot string, targets []string, selector string, timeout time.Duration) (TestRunResult, error) {
	return runJSTestFramework(ctx, "vitest", repoRoot, proofRoot, targets, selector, timeout)
}

// RunJest runs Jest from the owning workspace package directory, writing structured JSON
// to a temporary file via `--json --outputFile` so test stdout cannot corrupt results.
func RunJest(ctx context.Context, repoRoot, proofRoot string, targets []string, selector string, timeout time.Duration) (TestRunResult, error) {
	return runJSTestFramework(ctx, "jest", repoRoot, proofRoot, targets, selector, timeout)
}

func runJSTestFramework(ctx context.Context, kind, repoRoot, proofRoot string, targets []string, selector string, timeout time.Duration) (TestRunResult, error) {
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	if repoRoot == "" {
		repoRoot = proofRoot
	}

	firstTarget := ""
	if len(targets) > 0 {
		firstTarget = targets[0]
	}
	pkgDir, pkgRelDir := findOwningPackageDir(proofRoot, firstTarget)

	// Link node_modules safely from the main checkout when running inside an isolated proof tree.
	if cleanAbs(repoRoot) != cleanAbs(proofRoot) {
		cleanupLinks, linkErr := linkNodeModulesIfLockfileMatches(repoRoot, proofRoot, pkgRelDir)
		if linkErr != nil {
			return TestRunResult{Runner: kind}, linkErr
		}
		defer cleanupLinks()
	}

	tmpDir, err := os.MkdirTemp("", "flinch-"+kind+"-*")
	if err != nil {
		return TestRunResult{}, err
	}
	defer os.RemoveAll(tmpDir)
	reportFile := filepath.Join(tmpDir, kind+"-report.json")

	binPath, baseArgs, err := resolveJSRunnerBinary(kind, pkgDir, proofRoot, repoRoot)
	if err != nil {
		return TestRunResult{}, err
	}

	args := append([]string(nil), baseArgs...)
	if kind == "vitest" {
		args = append(args, "run", "--no-cache", "--reporter=json", "--outputFile="+reportFile)
	} else {
		args = append(args, "--json", "--no-cache", "--outputFile="+reportFile, "--runInBand")
	}
	if pattern := BuildJSTestNameRegex(selector); pattern != "" {
		args = append(args, "-t", pattern)
	}
	for _, tgt := range targets {
		relToPkg := targetRelativeToPkg(proofRoot, pkgDir, tgt)
		if relToPkg != "" {
			args = append(args, filepath.ToSlash(relToPkg))
		}
	}

	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(tctx, binPath, args...)
	cmd.Dir = pkgDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := runCandidate(cmd, proofRoot)
	elapsedMS := time.Since(start).Milliseconds()

	nodeInfo := ProbeTool(ctx, []string{"node"}, "--version")
	res := TestRunResult{
		Runner:        kind,
		RunnerVersion: strings.TrimSpace(kind + " (" + nodeInfo.Version + ")"),
		Command:       filepath.Base(binPath) + " " + strings.Join(args, " "),
		DurationMS:    elapsedMS,
		RawOutput:     stdout.String() + stderr.String(),
	}

	if errors.Is(tctx.Err(), context.DeadlineExceeded) {
		res.TimedOut = true
		res.ExitCode = -1
		return res, nil
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else {
			return res, runErr
		}
	}

	data, readErr := os.ReadFile(reportFile)
	if readErr != nil || len(bytes.TrimSpace(data)) == 0 {
		// Runner failed before writing JSON output (for example syntax/config/module load failure).
		if looksLikeJSCompileOrLoadError(res.RawOutput) {
			res.CompileOnly = true
			res.BuildOutput = strings.TrimSpace(res.RawOutput)
		}
		return res, nil
	}

	if err := ParseJSReportJSON(data, kind, &res); err != nil {
		return res, fmt.Errorf("parse %s json report: %w", kind, err)
	}
	return res, nil
}

// ParseJSReportJSON decodes Vitest (`--reporter=json --outputFile`) and Jest (`--json --outputFile`)
// JSON output into normalized TestCaseResults.
func ParseJSReportJSON(data []byte, runnerName string, res *TestRunResult) error {
	var rep jsJSONReport
	if err := json.Unmarshal(bytes.TrimSpace(data), &rep); err != nil {
		return err
	}
	for _, suite := range rep.TestResults {
		if suite.Status == "failed" && len(suite.AssertionResults) == 0 {
			res.CompileOnly = true
			if suite.Message != "" {
				res.BuildOutput = strings.TrimSpace(res.BuildOutput + "\n" + suite.Message)
			}
		}
		for _, ar := range suite.AssertionResults {
			name := formatJSFullName(ar)
			msg := strings.TrimSpace(strings.Join(ar.FailureMessages, "\n"))
			outcome := OutcomeError
			switch strings.ToLower(ar.Status) {
			case "passed":
				outcome = OutcomePass
			case "failed":
				if looksLikeJSCompileOrLoadError(msg) {
					res.CompileOnly = true
					res.BuildOutput = strings.TrimSpace(res.BuildOutput + "\n" + msg)
					outcome = OutcomeError
				} else {
					outcome = OutcomeFail
				}
			case "skipped", "pending", "todo", "disabled":
				// Skipped, todo, or filtered-out cases never count as pass.
				outcome = OutcomeSkip
			default:
				outcome = OutcomeError
			}
			res.Cases = append(res.Cases, TestCaseResult{
				Package:    filepath.ToSlash(suite.Name),
				Name:       name,
				Outcome:    outcome,
				ElapsedSec: ar.Duration / 1000.0,
				Output:     msg,
			})
		}
	}
	return nil
}

func formatJSFullName(ar jsAssertionResult) string {
	if len(ar.AncestorTitles) > 0 {
		parts := make([]string, 0, len(ar.AncestorTitles)+1)
		for _, a := range ar.AncestorTitles {
			if t := strings.TrimSpace(a); t != "" {
				parts = append(parts, t)
			}
		}
		if t := strings.TrimSpace(ar.Title); t != "" {
			parts = append(parts, t)
		}
		if len(parts) > 0 {
			return strings.Join(parts, " › ")
		}
	}
	if strings.TrimSpace(ar.FullName) != "" {
		return strings.TrimSpace(ar.FullName)
	}
	return strings.TrimSpace(ar.Title)
}

var tsErrorCodeRe = regexp.MustCompile(`\bTS\d{4,5}:`)

func looksLikeJSCompileOrLoadError(output string) bool {
	if tsErrorCodeRe.MatchString(output) {
		return true
	}
	markers := []string{
		"SyntaxError:",
		"Transform failed with",
		"Failed to load url",
		"Failed to resolve import",
		"Cannot find module",
		"ERR_MODULE_NOT_FOUND",
		"ReferenceError: Cannot access",
		"does not provide an export named",
		"is not a function", // only when paired with missing export? No, leave runtime TypeErrors as real test failures!
	}
	// Exclude generic runtime TypeErrors/ReferenceErrors that can be real bug assertions.
	for _, m := range markers[:len(markers)-1] {
		if strings.Contains(output, m) {
			return true
		}
	}
	return false
}

// findOwningPackageDir walks upward from targetRel inside proofRoot to find the nearest package.json (monorepo support).
func findOwningPackageDir(proofRoot, targetRel string) (absPkgDir string, relPkgDir string) {
	if targetRel == "" {
		return proofRoot, ""
	}
	curr := filepath.Join(proofRoot, filepath.FromSlash(targetRel))
	if info, err := os.Stat(curr); err != nil || !info.IsDir() {
		curr = filepath.Dir(curr)
	}
	cleanRoot := cleanAbs(proofRoot)
	for {
		if _, err := os.Stat(filepath.Join(curr, "package.json")); err == nil {
			rel, _ := filepath.Rel(proofRoot, curr)
			if rel == "." {
				rel = ""
			}
			return curr, filepath.ToSlash(rel)
		}
		if cleanAbs(curr) == cleanRoot {
			break
		}
		parent := filepath.Dir(curr)
		if parent == curr {
			break
		}
		curr = parent
	}
	return proofRoot, ""
}

func targetRelativeToPkg(proofRoot, pkgDir, target string) string {
	absTarget := target
	if !filepath.IsAbs(absTarget) {
		absTarget = filepath.Join(proofRoot, filepath.FromSlash(target))
	}
	rel, err := filepath.Rel(pkgDir, absTarget)
	if err != nil {
		return target
	}
	return rel
}

func resolveJSRunnerBinary(kind, pkgDir, proofRoot, repoRoot string) (string, []string, error) {
	// Prefer invoking the JS CLI entrypoint directly via `node` to avoid Windows .cmd batch
	// argument mangling of regex characters like '^' and spaces.
	if nodeBin, err := exec.LookPath("node"); err == nil {
		var entryRel string
		switch kind {
		case "vitest":
			entryRel = filepath.Join("node_modules", "vitest", "vitest.mjs")
		case "jest":
			entryRel = filepath.Join("node_modules", "jest", "bin", "jest.js")
		}
		if entryRel != "" {
			for _, base := range []string{pkgDir, proofRoot, repoRoot} {
				cand := filepath.Join(base, entryRel)
				if _, statErr := os.Stat(cand); statErr == nil {
					return nodeBin, []string{cand}, nil
				}
			}
		}
	}

	binNames := []string{kind}
	if runtime.GOOS == "windows" {
		binNames = []string{kind + ".cmd", kind + ".exe", kind}
	}
	searchDirs := []string{
		filepath.Join(pkgDir, "node_modules", ".bin"),
		filepath.Join(proofRoot, "node_modules", ".bin"),
		filepath.Join(repoRoot, "node_modules", ".bin"),
	}
	for _, d := range searchDirs {
		for _, name := range binNames {
			cand := filepath.Join(d, name)
			if _, err := os.Stat(cand); err == nil {
				return cand, nil, nil
			}
		}
	}
	if p, err := exec.LookPath(kind); err == nil {
		return p, nil, nil
	}
	if npx, err := exec.LookPath("npx"); err == nil {
		return npx, []string{"--no-install", kind}, nil
	}
	return "", nil, fmt.Errorf("%s binary not found in node_modules/.bin or PATH", kind)
}

func linkNodeModulesIfLockfileMatches(repoRoot, proofRoot, pkgRelDir string) (func(), error) {
	lockfiles := []string{"package-lock.json", "pnpm-lock.yaml", "yarn.lock", "bun.lockb", "bun.lock"}
	for _, lf := range lockfiles {
		srcLF := filepath.Join(repoRoot, lf)
		dstLF := filepath.Join(proofRoot, lf)
		srcBytes, srcErr := os.ReadFile(srcLF)
		dstBytes, dstErr := os.ReadFile(dstLF)
		if srcErr == nil && dstErr == nil {
			normSrc := bytes.ReplaceAll(srcBytes, []byte("\r\n"), []byte("\n"))
			normDst := bytes.ReplaceAll(dstBytes, []byte("\r\n"), []byte("\n"))
			if sha256.Sum256(normSrc) != sha256.Sum256(normDst) {
				return nil, ErrLockfileMismatch
			}
		} else if (srcErr == nil) != (dstErr == nil) {
			return nil, ErrLockfileMismatch
		}
	}

	var createdLinks []string
	linkOne := func(src, dst string) error {
		if _, err := os.Stat(src); err != nil {
			return nil
		}
		if _, err := os.Lstat(dst); err == nil {
			return nil
		}
		if err := createDirLink(src, dst); err != nil {
			return err
		}
		createdLinks = append(createdLinks, dst)
		return nil
	}

	if err := linkOne(filepath.Join(repoRoot, "node_modules"), filepath.Join(proofRoot, "node_modules")); err != nil {
		return nil, err
	}
	if pkgRelDir != "" {
		srcPkgNM := filepath.Join(repoRoot, filepath.FromSlash(pkgRelDir), "node_modules")
		dstPkgNM := filepath.Join(proofRoot, filepath.FromSlash(pkgRelDir), "node_modules")
		if err := linkOne(srcPkgNM, dstPkgNM); err != nil {
			for _, l := range createdLinks {
				_ = os.Remove(l)
			}
			return nil, err
		}
	}

	return func() {
		// Unlink junctions/symlinks explicitly before proof directory removal so target node_modules is never touched.
		for _, l := range createdLinks {
			_ = os.Remove(l)
		}
	}, nil
}

func createDirLink(targetDir, linkPath string) error {
	if runtime.GOOS == "windows" {
		cmd := exec.Command("cmd", "/C", "mklink", "/J", linkPath, targetDir)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("create directory junction %s -> %s: %w (%s)", linkPath, targetDir, err, strings.TrimSpace(out.String()))
		}
		return nil
	}
	return os.Symlink(targetDir, linkPath)
}

func cleanAbs(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(p)
}
