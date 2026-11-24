package prove

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/angelnicolasc/flinch/internal/finding"
	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/reflex"
	"github.com/angelnicolasc/flinch/internal/runner"
)

// Options controls execution settings for Verify.
type Options struct {
	Timeout time.Duration
}

// Verify runs the isolated §5 broken/fixed/candidate verification contract and returns
// a reproducible VerificationReport without modifying the user's working tree or Git index.
func Verify(ctx context.Context, repoRoot string, spec CandidateSpec, opts Options) (VerificationReport, error) {
	ctx = runner.WithCommandRepository(ctx, repoRoot)
	start := time.Now()
	if spec.Version == 0 {
		spec.Version = 1
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 2 * time.Minute
	}

	r := reflex.Reflex{
		ID:         spec.ID,
		Version:    spec.Version,
		Rung:       spec.Rung,
		Scope:      spec.Scope,
		Exclude:    spec.Exclude,
		Obligation: spec.Obligation,
		Artifact:   spec.Artifact,
	}
	rung := r.EffectiveRung()
	selector := r.ExpectedID()

	report := VerificationReport{
		Schema:           1,
		CandidateID:      spec.ID,
		CandidateVersion: spec.Version,
		CandidateHash:    spec.CandidateHash(),
		Rung:             rung,
		Obligation:       spec.Obligation,
		Selector:         selector,
		BrokenRef:        spec.BrokenRef,
		FixedRef:         spec.FixedRef,
		Runner:           spec.Artifact.Tool,
		ExpectedSignal:   spec.Artifact.ExpectedSignal,
		VerifiedAt:       time.Now().UTC(),
	}

	if err := prepareProofInputs(repoRoot, &spec); err != nil {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonSetupError
		report.ActionableError = err.Error()
		return report, nil
	}
	report.CandidateHash = spec.CandidateHash()

	if !r.IsDeterministic() {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonSetupError
		report.ActionableError = "probabilistic context lines cannot be deterministically verified by flinch prove"
		report.TotalRuntimeMS = time.Since(start).Milliseconds()
		return report, nil
	}

	if strings.TrimSpace(spec.BrokenRef) == "" {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonSetupError
		report.ActionableError = "broken_ref is required"
		report.TotalRuntimeMS = time.Since(start).Milliseconds()
		return report, nil
	}

	brokenSHA, err := gitutil.ResolveRef(ctx, repoRoot, spec.BrokenRef)
	if err != nil {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonSetupError
		report.ActionableError = fmt.Sprintf("resolve broken_ref %q: %v", spec.BrokenRef, err)
		report.TotalRuntimeMS = time.Since(start).Milliseconds()
		return report, nil
	}
	report.BrokenRef = brokenSHA

	var fixedSHA string
	if strings.TrimSpace(spec.FixedRef) == "" || strings.EqualFold(strings.TrimSpace(spec.FixedRef), "WORKTREE") {
		snap, snapErr := gitutil.CaptureWorkingTreeSnapshot(ctx, repoRoot, "flinch prove fixed snapshot")
		if snapErr != nil {
			report.Verdict = VerdictInconclusive
			report.Reason = ReasonSetupError
			report.ActionableError = fmt.Sprintf("capture working tree snapshot: %v", snapErr)
			report.TotalRuntimeMS = time.Since(start).Milliseconds()
			return report, nil
		}
		fixedSHA = snap
	} else {
		sha, resErr := gitutil.ResolveRef(ctx, repoRoot, spec.FixedRef)
		if resErr != nil {
			report.Verdict = VerdictInconclusive
			report.Reason = ReasonSetupError
			report.ActionableError = fmt.Sprintf("resolve fixed_ref %q: %v", spec.FixedRef, err)
			report.TotalRuntimeMS = time.Since(start).Milliseconds()
			return report, nil
		}
		fixedSHA = sha
	}
	report.FixedRef = fixedSHA

	switch rung {
	case reflex.RungTest:
		verifyTest(ctx, repoRoot, spec, &report, opts.Timeout)
	case reflex.RungLint, reflex.RungStructure:
		verifyLintOrStructure(ctx, repoRoot, r, spec, &report, opts.Timeout)
	case reflex.RungGuard:
		verifyGuard(ctx, repoRoot, r, spec, &report, opts.Timeout)
	default:
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonSetupError
		report.ActionableError = fmt.Sprintf("unsupported rung %q", rung)
	}

	report.TotalRuntimeMS = time.Since(start).Milliseconds()
	return report, nil
}

func verifyTest(ctx context.Context, repoRoot string, spec CandidateSpec, report *VerificationReport, timeout time.Duration) {
	selector := strings.TrimSpace(spec.Artifact.Selector)
	if selector == "" {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonMissingSelector
		report.ActionableError = "a non-empty exact selector is required for test proofs"
		return
	}

	neighbors := collectTestNeighbors(spec)
	if len(neighbors) == 0 {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonMissingAllowedCase
		report.ActionableError = "test proof requires at least one legitimate neighbor case (neighbor_selector or allowed_cases[].selector)"
		return
	}
	for _, nb := range neighbors {
		if normalizeSelectorForCompare(nb.Selector) == normalizeSelectorForCompare(selector) {
			report.Verdict = VerdictInconclusive
			report.Reason = ReasonMissingAllowedCase
			report.ActionableError = fmt.Sprintf("legitimate neighbor selector %q must differ from the broken-case selector %q", nb.Selector, selector)
			return
		}
	}

	changes, err := gitutil.DiffFileChanges(ctx, repoRoot, report.BrokenRef, report.FixedRef)
	if err != nil {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonSetupError
		report.ActionableError = fmt.Sprintf("diff broken..fixed: %v", err)
		return
	}

	wt, err := gitutil.CreateWorktree(ctx, repoRoot, report.FixedRef, "prove-test-")
	if err != nil {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonSetupError
		report.ActionableError = fmt.Sprintf("create fixed worktree: %v", err)
		return
	}
	defer wt.Cleanup(ctx)

	if err := applyCandidateToWorktree(ctx, wt, spec); err != nil {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonSetupError
		report.ActionableError = fmt.Sprintf("apply candidate to fixed tree: %v", err)
		return
	}

	expected := strings.TrimSpace(spec.Artifact.ExpectedSignal)
	sites := findAssertionSites(wt.Path, spec)
	if expected == "" {
		report.ExpectedSignal = sites.description()
	}
	pkgs := inferTestTargets(spec, changes)

	// 1. Run target test on fixed tree (must execute and pass cleanly without skips).
	fixedRun, err := runTestRunner(ctx, repoRoot, wt.Path, spec.Artifact.Tool, pkgs, selector, timeout)
	if err != nil {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonSetupError
		report.ActionableError = fmt.Sprintf("run test on fixed tree: %v", err)
		return
	}
	report.RunnerVersion = fixedRun.RunnerVersion
	report.FixedRuntimeMS = fixedRun.DurationMS

	if fixedRun.TimedOut {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonTimeout
		report.ActionableError = "test timed out on fixed tree"
		return
	}
	if fixedRun.CompileOnly {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonFixedCompileError
		report.ActionableError = "fixed tree failed to compile/collect tests: " + trimOneLine(fixedRun.BuildOutput)
		return
	}

	fixedMatched := matchTestCases(fixedRun.Cases, selector)
	if len(fixedMatched) == 0 {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonSelectorNotExecuted
		report.ActionableError = fmt.Sprintf("selector %q did not execute any matching test case on fixed tree", selector)
		return
	}
	if len(fixedMatched) != 1 {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonSelectorNotExecuted
		report.ActionableError = "test selector resolved to more than one case"
		return
	}
	fixedID := caseIdentity(fixedMatched[0], wt.Path)
	for _, tc := range fixedMatched {
		switch tc.Outcome {
		case runner.OutcomeSkip:
			report.Verdict = VerdictInconclusive
			report.Reason = ReasonSkippedOnFixed
			report.ActionableError = fmt.Sprintf("test %q was skipped on fixed tree; skipped tests never prove protection", tc.Name)
			return
		case runner.OutcomeFail:
			report.Verdict = VerdictRefuted
			report.Reason = ReasonFailedOnFixed
			report.ObservedSignal = tc.Output
			report.ActionableError = fmt.Sprintf("candidate test %q fails on the fixed tree", tc.Name)
			return
		case runner.OutcomeError:
			report.Verdict = VerdictInconclusive
			report.Reason = ReasonSetupError
			report.ObservedSignal = tc.Output
			report.ActionableError = fmt.Sprintf("test %q errored during setup on fixed tree", tc.Name)
			return
		}
	}

	if fixedRun.ExitCode != 0 {
		report.Verdict, report.Reason = VerdictInconclusive, ReasonSetupError
		report.ActionableError = "fixed runner exited unsuccessfully despite the selected case passing"
		return
	}
	missingSites := expected == "" && len(sites.lines) == 0
	if !missingSites && !verifyTestNeighbors(ctx, repoRoot, wt, spec, pkgs, neighbors, fixedID, report, timeout) {
		return
	}

	classifier := NewClassifier(spec.TestSupportGlobs, spec.ExplicitProductFiles, spec.ExplicitTestFiles, spec.ExplicitSupportFiles)
	var previousOutcome runner.TestOutcome
	var firstFailure assertionFailure
	for attempt := 0; attempt < 2; attempt++ {
		run, treePath, err := runBrokenTest(ctx, repoRoot, spec, *report, changes, classifier, pkgs, selector, timeout)
		if err != nil {
			report.Verdict, report.Reason = VerdictInconclusive, ReasonSetupError
			report.ActionableError = err.Error()
			return
		}
		report.BrokenRuntimeMS += run.DurationMS
		if run.TimedOut {
			report.Verdict, report.Reason = VerdictInconclusive, ReasonTimeout
			report.ActionableError = "test timed out on the broken tree"
			return
		}
		if run.CompileOnly {
			report.Verdict, report.Reason = VerdictInconclusive, ReasonCompileOnly
			report.ActionableError = "test could not compile or load on the broken tree"
			return
		}
		matched := matchTestCases(run.Cases, selector)
		if len(matched) != 1 || caseIdentity(matched[0], treePath) != fixedID {
			report.Verdict, report.Reason = VerdictInconclusive, ReasonSelectorNotExecuted
			report.ActionableError = "the same unique test case must execute on both trees"
			return
		}
		tc := matched[0]
		switch tc.Outcome {
		case runner.OutcomeSkip:
			report.Verdict, report.Reason = VerdictInconclusive, ReasonSkippedOnBroken
			report.ActionableError = "selected case was skipped on the broken tree"
			return
		case runner.OutcomeFail:
			if run.ExitCode != 1 {
				report.Verdict, report.Reason = VerdictInconclusive, ReasonSetupError
				report.ActionableError = "test runner did not report an ordinary test failure"
				return
			}
			if missingSites {
				report.Verdict, report.Reason = VerdictInconclusive, ReasonNoRecognizedAssertion
				report.ActionableError = unsupportedAssertions(selector)
				return
			}
			failure, ok := sites.matchFailure(caseDiagnostic(tc.Output, treePath), expected)
			if !ok || (attempt > 0 && previousOutcome == runner.OutcomeFail && failure != firstFailure) {
				report.Verdict, report.Reason = VerdictInconclusive, ReasonUnexpectedFailureSignal
				report.ActionableError = fmt.Sprintf("selected case did not reproduce the expected assertion and diagnostic on broken run %d", attempt+1)
				return
			}
			if attempt == 0 {
				firstFailure = failure
			}
		case runner.OutcomePass:
			if run.ExitCode != 0 {
				report.Verdict, report.Reason = VerdictInconclusive, ReasonSetupError
				report.ActionableError = "broken runner failed outside the selected case"
				return
			}
		default:
			report.Verdict, report.Reason = VerdictInconclusive, ReasonSetupError
			report.ActionableError = "selected test errored on the broken tree"
			return
		}
		if missingSites {
			report.Verdict, report.Reason = VerdictInconclusive, ReasonNoRecognizedAssertion
			report.ActionableError = unsupportedAssertions(selector)
			return
		}
		if attempt > 0 && tc.Outcome != previousOutcome {
			report.Verdict, report.Reason = VerdictInconclusive, ReasonFlaky
			report.ActionableError = "selected case produced inconsistent broken outcomes"
			return
		}
		previousOutcome = tc.Outcome
	}
	if previousOutcome == runner.OutcomePass {
		report.Verdict, report.Reason = VerdictRefuted, ReasonPassedOnBroken
		report.ActionableError = "selected test passed on the broken tree"
		return
	}
	report.ObservedSignal = trimOneLine(firstFailure.diagnostic)
	report.Verdict = VerdictVerified
}

func runBrokenTest(ctx context.Context, repoRoot string, spec CandidateSpec, report VerificationReport, changes []gitutil.FileChange, classifier Classifier, pkgs []string, selector string, timeout time.Duration) (runner.TestRunResult, string, error) {
	var empty runner.TestRunResult
	wt, err := gitutil.CreateWorktree(ctx, repoRoot, report.FixedRef, "prove-test-broken-")
	if err != nil {
		return empty, "", err
	}
	defer wt.Cleanup(ctx)
	if err := applyCandidateToWorktree(ctx, wt, spec); err != nil {
		return empty, "", err
	}
	reverted := false
	for _, ch := range changes {
		if classifier.Classify(ch.Path) != ClassProduct {
			continue
		}
		reverted = true
		path := ch.Path
		if ch.Status == "A" || ch.Status == "R" {
			if err := wt.RemoveFile(path); err != nil {
				return empty, "", err
			}
			if ch.Status == "A" {
				continue
			}
			path = ch.OldPath
		}
		content, err := gitutil.FileAtRef(ctx, repoRoot, report.BrokenRef, path)
		if err != nil {
			return empty, "", err
		}
		if err := wt.WriteFile(path, content); err != nil {
			return empty, "", err
		}
	}
	if !reverted {
		return empty, "", fmt.Errorf("no product change separates the broken and fixed trees")
	}
	res, err := runTestRunner(ctx, repoRoot, wt.Path, spec.Artifact.Tool, pkgs, selector, timeout)
	return res, wt.Path, err
}

func caseIdentity(tc runner.TestCaseResult, treePath string) string {
	pkg := tc.Package
	if filepath.IsAbs(pkg) {
		if rel, err := filepath.Rel(treePath, pkg); err == nil {
			pkg = filepath.ToSlash(rel)
		}
	}
	return pkg + "::" + normalizeSelectorForCompare(tc.Name)
}

func caseDiagnostic(output, treePath string) string {
	output = terminalEscape.ReplaceAllString(output, "")
	output = strings.ReplaceAll(output, treePath, "<tree>")
	output = strings.ReplaceAll(output, filepath.ToSlash(treePath), "<tree>")
	var evidence []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		// go-cmp varies the spacing after a diff prefix between processes.
		if strings.HasPrefix(line, "+\u00a0") || strings.HasPrefix(line, "-\u00a0") {
			line = line[:1] + " " + strings.TrimPrefix(line[1:], "\u00a0")
		}
		if line != "" && !strings.HasPrefix(line, "=== ") && !strings.HasPrefix(line, "--- ") {
			evidence = append(evidence, line)
		}
	}
	return strings.Join(evidence, "\n")
}

func verifyLintOrStructure(ctx context.Context, repoRoot string, r reflex.Reflex, spec CandidateSpec, report *VerificationReport, timeout time.Duration) {
	if r.Artifact.Rule == "" && r.Artifact.Selector == "" {
		if r.Artifact.Tool != "go-build" {
			report.Verdict, report.Reason = VerdictInconclusive, ReasonMissingSelector
			report.ActionableError = "static proof requires an identifiable diagnostic rule or selector"
			return
		}
		r.Artifact.Selector = "go-build"
	}
	changes, err := gitutil.DiffFileChanges(ctx, repoRoot, report.BrokenRef, report.FixedRef)
	if err != nil {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonSetupError
		report.ActionableError = fmt.Sprintf("diff broken..fixed: %v", err)
		return
	}

	brokenWT, err := gitutil.CreateWorktree(ctx, repoRoot, report.BrokenRef, "prove-broken-")
	if err != nil {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonSetupError
		report.ActionableError = fmt.Sprintf("create broken worktree: %v", err)
		return
	}
	defer brokenWT.Cleanup(ctx)

	fixedWT, err := gitutil.CreateWorktree(ctx, repoRoot, report.FixedRef, "prove-fixed-")
	if err != nil {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonSetupError
		report.ActionableError = fmt.Sprintf("create fixed worktree: %v", err)
		return
	}
	defer fixedWT.Cleanup(ctx)

	// Apply the exact same candidate artifact to both isolated trees (§5.1).
	if err := applyCandidateToWorktree(ctx, brokenWT, spec); err != nil {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonSetupError
		report.ActionableError = fmt.Sprintf("apply candidate to broken tree: %v", err)
		return
	}
	if err := applyCandidateToWorktree(ctx, fixedWT, spec); err != nil {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonSetupError
		report.ActionableError = fmt.Sprintf("apply candidate to fixed tree: %v", err)
		return
	}

	brokenTargets := collectBrokenTargetFiles(r, changes)

	brokenRun, err := runner.RunArtifact(ctx, brokenWT.Path, spec.Artifact, brokenTargets, timeout)
	if err != nil {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonSetupError
		report.ActionableError = fmt.Sprintf("run %s on broken tree: %v", spec.Artifact.Tool, err)
		return
	}
	report.RunnerVersion = brokenRun.ToolVersion
	report.BrokenRuntimeMS = brokenRun.DurationMS
	if brokenRun.TimedOut {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonTimeout
		report.ActionableError = "tool timed out on broken tree"
		return
	}

	// Run on fixed tree (full repo for census + targeted check).
	fixedRun, err := runner.RunArtifact(ctx, fixedWT.Path, spec.Artifact, nil, timeout)
	if err != nil {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonSetupError
		report.ActionableError = fmt.Sprintf("run %s on fixed tree: %v", spec.Artifact.Tool, err)
		return
	}
	report.FixedRuntimeMS = fixedRun.DurationMS
	if fixedRun.TimedOut {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonTimeout
		report.ActionableError = "tool timed out on fixed tree"
		return
	}
	if !staticRunComplete(spec.Artifact.Tool, brokenRun) || !staticRunComplete(spec.Artifact.Tool, fixedRun) {
		report.Verdict, report.Reason = VerdictInconclusive, ReasonSetupError
		report.ActionableError = "static analyzer did not produce a completed analysis on both trees"
		return
	}

	brokenFiltered := filterFindingsForReflex(r, brokenRun.Findings)
	fixedFiltered := filterFindingsForReflex(r, fixedRun.Findings)
	report.Census = len(fixedFiltered)

	match := finding.MatchIncidentSites(changes, brokenFiltered, fixedFiltered)
	report.MatchedBroken = match.MatchedBroken
	report.RemainingFixed = match.RemainingFixed

	if match.Ambiguous {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonAmbiguousSiteMatch
		report.ActionableError = match.AmbiguousReason
		return
	}
	if len(match.MatchedBroken) == 0 {
		report.Verdict = VerdictRefuted
		report.Reason = ReasonMissedBrokenIncident
		report.ActionableError = fmt.Sprintf("candidate %s did not flag the broken incident site", r.ExpectedID())
		return
	}
	report.ObservedSignal = match.MatchedBroken[0].Message

	if spec.Artifact.ExpectedSignal != "" {
		matchedSignal := false
		for _, bf := range match.MatchedBroken {
			if matchesExpectedSignal(bf.Message+"\n"+bf.Context, spec.Artifact.ExpectedSignal) {
				matchedSignal = true
				break
			}
		}
		if !matchedSignal {
			report.Verdict = VerdictInconclusive
			report.Reason = ReasonUnexpectedFailureSignal
			report.ActionableError = fmt.Sprintf("broken finding did not match expected_signal %q", spec.Artifact.ExpectedSignal)
			return
		}
	}

	if len(match.RemainingFixed) > 0 {
		report.Verdict = VerdictRefuted
		report.Reason = ReasonFlaggedFixedCode
		report.ActionableError = fmt.Sprintf("candidate still flags fixed code at %s:%d (%s)",
			match.RemainingFixed[0].Path, match.RemainingFixed[0].StartLine, match.RemainingFixed[0].Message)
		return
	}

	// Verify every allowed near-miss case materialized at its in-scope product path (D4, P4).
	if len(spec.AllowedCases) == 0 {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonMissingAllowedCase
		report.ActionableError = "lint/structure proof requires at least one in-scope allowed neighbor case"
		return
	}
	if !verifyAllowedCases(ctx, fixedWT, r, spec, fixedFiltered, report, timeout) {
		return
	}

	report.Verdict = VerdictVerified
}

func verifyGuard(ctx context.Context, repoRoot string, r reflex.Reflex, spec CandidateSpec, report *VerificationReport, timeout time.Duration) {
	if len(spec.AllowedCases) == 0 {
		report.Verdict, report.Reason = VerdictInconclusive, ReasonMissingAllowedCase
		report.ActionableError = "guard proof requires an executed allowed case"
		return
	}
	if strings.TrimSpace(spec.Artifact.ExpectedSignal) == "" {
		report.Verdict, report.Reason = VerdictInconclusive, ReasonUnexpectedFailureSignal
		report.ActionableError = "guard proof requires its recognizable failure signal"
		return
	}
	for attempt := 0; attempt < 2; attempt++ {
		brokenRun, err := runBrokenGuard(ctx, repoRoot, spec, report.BrokenRef, timeout)
		if err != nil {
			report.Verdict, report.Reason = VerdictInconclusive, ReasonSetupError
			report.ActionableError = err.Error()
			return
		}
		report.BrokenRuntimeMS += brokenRun.DurationMS
		if brokenRun.TimedOut {
			report.Verdict, report.Reason = VerdictInconclusive, ReasonTimeout
			report.ActionableError = "guard timed out on the broken tree"
			return
		}
		if brokenRun.ExitCode == 0 {
			report.Verdict, report.Reason = VerdictRefuted, ReasonPassedOnBroken
			if attempt > 0 {
				report.Verdict, report.Reason = VerdictInconclusive, ReasonFlaky
			}
			report.ActionableError = "guard succeeded on the broken tree"
			return
		}
		output := brokenRun.Stdout + "\n" + brokenRun.Stderr
		if !matchesExpectedSignal(output, spec.Artifact.ExpectedSignal) {
			report.Verdict, report.Reason = VerdictInconclusive, ReasonUnexpectedFailureSignal
			report.ActionableError = fmt.Sprintf("broken guard run %d did not emit its expected signal", attempt+1)
			return
		}
		report.ObservedSignal = trimOneLine(output)
	}
	fixedWT, err := gitutil.CreateWorktree(ctx, repoRoot, report.FixedRef, "prove-guard-fixed-")
	if err != nil {
		report.Verdict, report.Reason = VerdictInconclusive, ReasonSetupError
		report.ActionableError = err.Error()
		return
	}
	defer fixedWT.Cleanup(ctx)
	if err := applyCandidateToWorktree(ctx, fixedWT, spec); err != nil {
		report.Verdict, report.Reason = VerdictInconclusive, ReasonSetupError
		report.ActionableError = err.Error()
		return
	}

	fixedRun, err := runner.RunArtifact(ctx, fixedWT.Path, spec.Artifact, nil, timeout)
	if err != nil {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonSetupError
		report.ActionableError = fmt.Sprintf("run guard on fixed tree: %v", err)
		return
	}
	report.FixedRuntimeMS = fixedRun.DurationMS
	if fixedRun.TimedOut {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonTimeout
		report.ActionableError = "guard timed out on fixed tree"
		return
	}
	fixedFiltered := filterFindingsForReflex(r, fixedRun.Findings)
	if fixedRun.ExitCode != 0 && !matchesExpectedSignal(fixedRun.Stdout+"\n"+fixedRun.Stderr, spec.Artifact.ExpectedSignal) {
		report.Verdict, report.Reason = VerdictInconclusive, ReasonSetupError
		report.ActionableError = "guard exited unsuccessfully on the fixed tree for an unrelated reason"
		return
	}
	if fixedRun.ExitCode != 0 || len(fixedFiltered) > 0 {
		report.Verdict = VerdictRefuted
		report.Reason = ReasonFailedOnFixed
		report.ActionableError = "guard command failed on fixed tree: " + trimOneLine(fixedRun.Stdout+"\n"+fixedRun.Stderr)
		return
	}

	if !verifyAllowedCases(ctx, fixedWT, r, spec, fixedFiltered, report, timeout) {
		return
	}

	report.Verdict = VerdictVerified
}

func runBrokenGuard(ctx context.Context, repoRoot string, spec CandidateSpec, brokenRef string, timeout time.Duration) (runner.LintRunResult, error) {
	wt, err := gitutil.CreateWorktree(ctx, repoRoot, brokenRef, "prove-guard-broken-")
	if err != nil {
		return runner.LintRunResult{}, err
	}
	defer wt.Cleanup(ctx)
	if err := applyCandidateToWorktree(ctx, wt, spec); err != nil {
		return runner.LintRunResult{}, err
	}
	return runner.RunArtifact(ctx, wt.Path, spec.Artifact, nil, timeout)
}

// A diagnostic exit with findings is valid analysis. A usage/configuration failure is not.
func staticRunComplete(tool string, result runner.LintRunResult) bool {
	if result.TimedOut || result.ExitCode < 0 {
		return false
	}
	switch tool {
	case "ruff", "eslint", "golangci-lint":
		if !json.Valid([]byte(result.Stdout)) {
			return false
		}
	}
	return result.ExitCode == 0 || (result.ExitCode == 1 && len(result.Findings) > 0)
}

func verifyAllowedCases(
	ctx context.Context,
	fixedWT *gitutil.Worktree,
	r reflex.Reflex,
	spec CandidateSpec,
	fixedBaseline []finding.Finding,
	report *VerificationReport,
	timeout time.Duration,
) bool {
	for _, ac := range spec.AllowedCases {
		target := filepath.ToSlash(strings.TrimPrefix(filepath.Clean(ac.TargetPath), "./"))
		inScope := r.MatchesPath(target)
		acr := AllowedCaseResult{
			Name:       ac.Name,
			TargetPath: target,
			InScope:    inScope,
		}
		if !inScope {
			acr.Passed = false
			acr.Detail = fmt.Sprintf("allowed case target %q is outside check scope %q", target, r.Scope)
			report.AllowedResults = append(report.AllowedResults, acr)
			report.Verdict = VerdictInconclusive
			report.Reason = ReasonAllowedCaseOutOfScope
			report.ActionableError = acr.Detail
			return false
		}

		content := []byte(ac.Content)
		if ac.TargetPath == "" || len(bytes.TrimSpace(content)) == 0 {
			report.Verdict, report.Reason = VerdictInconclusive, ReasonMissingAllowedCase
			report.ActionableError = "an allowed case needs an in-scope target and fixture contents"
			return false
		}

		absTarget := filepath.Join(fixedWT.Path, filepath.FromSlash(target))
		origBytes, origErr := os.ReadFile(absTarget)
		hadOrig := origErr == nil

		if err := fixedWT.WriteFile(target, content); err != nil {
			acr.Detail = fmt.Sprintf("materialize allowed case at %s: %v", target, err)
			report.AllowedResults = append(report.AllowedResults, acr)
			report.Verdict = VerdictInconclusive
			report.Reason = ReasonSetupError
			report.ActionableError = acr.Detail
			return false
		}

		var targets []string
		if spec.Artifact.Tool != "go-build" && spec.Artifact.Tool != "command" && spec.Artifact.Tool != "script" {
			targets = []string{target}
		}
		runRes, runErr := runner.RunArtifact(ctx, fixedWT.Path, spec.Artifact, targets, timeout)

		// Restore worktree state after running this allowed case.
		if hadOrig {
			_ = fixedWT.WriteFile(target, origBytes)
		} else {
			_ = fixedWT.RemoveFile(target)
		}

		if runErr != nil {
			acr.Detail = fmt.Sprintf("run tool on allowed case %s: %v", target, runErr)
			report.AllowedResults = append(report.AllowedResults, acr)
			report.Verdict = VerdictInconclusive
			report.Reason = ReasonSetupError
			report.ActionableError = acr.Detail
			return false
		}

		if runRes.TimedOut {
			report.Verdict, report.Reason = VerdictInconclusive, ReasonTimeout
			report.ActionableError = "the allowed case timed out"
			return false
		}
		if r.EffectiveRung() == reflex.RungGuard && runRes.ExitCode != 0 && !matchesExpectedSignal(runRes.Stdout+"\n"+runRes.Stderr, spec.Artifact.ExpectedSignal) {
			report.Verdict, report.Reason = VerdictInconclusive, ReasonSetupError
			report.ActionableError = "the guard failed on the allowed case for an unrelated reason"
			return false
		}
		if r.EffectiveRung() != reflex.RungGuard && !staticRunComplete(spec.Artifact.Tool, runRes) {
			report.Verdict, report.Reason = VerdictInconclusive, ReasonSetupError
			report.ActionableError = "the analyzer did not complete on the allowed case"
			return false
		}
		filtered := filterFindingsForReflex(r, runRes.Findings)
		var flagged []finding.Finding
		if len(targets) > 0 {
			for _, f := range filtered {
				if f.Path == target {
					flagged = append(flagged, f)
				}
			}
		} else {
			flagged = finding.NewFindings(fixedBaseline, filtered, nil)
		}

		if len(flagged) > 0 || ((spec.Artifact.Tool == "go-build" || spec.Artifact.Tool == "command" || spec.Artifact.Tool == "script") && runRes.ExitCode != 0) {
			acr.Passed = false
			acr.Findings = flagged
			if len(flagged) > 0 {
				acr.Detail = fmt.Sprintf("allowed case %s flagged at line %d: %s", target, flagged[0].StartLine, flagged[0].Message)
			} else {
				acr.Detail = fmt.Sprintf("allowed case %s failed check with exit code %d: %s", target, runRes.ExitCode, trimOneLine(runRes.Stdout+"\n"+runRes.Stderr))
			}
			report.AllowedResults = append(report.AllowedResults, acr)
			report.Verdict = VerdictRefuted
			report.Reason = ReasonFlaggedAllowedCase
			report.ActionableError = acr.Detail
			return false
		}
		acr.Passed = true
		report.AllowedResults = append(report.AllowedResults, acr)
	}
	return true
}

func applyCandidateToWorktree(ctx context.Context, wt *gitutil.Worktree, spec CandidateSpec) error {
	if strings.TrimSpace(spec.Patch) != "" {
		if err := wt.ApplyPatch(ctx, []byte(spec.Patch)); err != nil {
			return err
		}
	}
	for rel, content := range spec.OverlayFiles {
		abs, err := gitutil.LocalPath(wt.Path, rel)
		if err != nil {
			return err
		}
		existing, err := os.ReadFile(abs)
		if err == nil && bytes.Equal(existing, []byte(content)) {
			continue
		}
		if err := wt.WriteFile(rel, []byte(content)); err != nil {
			return fmt.Errorf("overlay %s: %w", rel, err)
		}
	}
	return nil
}

func inferTestTargets(spec CandidateSpec, changes []gitutil.FileChange) []string {
	if len(spec.TestPackages) > 0 {
		return spec.TestPackages
	}
	seen := make(map[string]bool)
	var targets []string
	for _, ch := range changes {
		if spec.Artifact.Tool == "go-test" && strings.HasSuffix(ch.Path, "_test.go") {
			dir := filepath.ToSlash(filepath.Dir(ch.Path))
			pkg := "./" + dir
			if dir == "." || dir == "" {
				pkg = "."
			}
			if !seen[pkg] {
				seen[pkg] = true
				targets = append(targets, pkg)
			}
		} else if (spec.Artifact.Tool == "vitest" || spec.Artifact.Tool == "jest") && isTestFile(ch.Path) {
			if !seen[ch.Path] {
				seen[ch.Path] = true
				targets = append(targets, ch.Path)
			}
		} else if spec.Artifact.Tool == "pytest" && (strings.HasSuffix(ch.Path, ".py") && isTestFile(ch.Path)) {
			if !seen[ch.Path] {
				seen[ch.Path] = true
				targets = append(targets, ch.Path)
			}
		}
	}
	if spec.Artifact.Path != "" {
		if spec.Artifact.Tool == "go-test" {
			dir := filepath.ToSlash(filepath.Dir(spec.Artifact.Path))
			if dir == "." || dir == "" {
				return []string{"."}
			}
			return []string{"./" + dir}
		}
		return []string{spec.Artifact.Path}
	}
	if len(targets) == 0 && spec.Artifact.Tool == "go-test" {
		return []string{"./..."}
	}
	return targets
}

func collectTestNeighbors(spec CandidateSpec) []AllowedCase {
	var out []AllowedCase
	if sel := strings.TrimSpace(spec.NeighborSelector); sel != "" {
		out = append(out, AllowedCase{Name: sel, Selector: sel})
	}
	for _, ac := range spec.AllowedCases {
		if strings.TrimSpace(ac.Selector) != "" {
			if ac.Name == "" {
				ac.Name = ac.Selector
			}
			out = append(out, ac)
		}
	}
	return out
}

func verifyTestNeighbors(
	ctx context.Context,
	repoRoot string,
	fixedWT *gitutil.Worktree,
	spec CandidateSpec,
	pkgs []string,
	neighbors []AllowedCase,
	fixedID string,
	report *VerificationReport,
	timeout time.Duration,
) bool {
	for _, nb := range neighbors {
		nbSel := strings.TrimSpace(nb.Selector)
		acr := AllowedCaseResult{
			Name:       nb.Name,
			TargetPath: nb.TargetPath,
			InScope:    true,
		}

		// Optional materialized test file overlay for the neighbor.
		var restoreTarget string
		var origBytes []byte
		hadOrig := false
		if nb.TargetPath != "" && nb.Content != "" {
			target := filepath.ToSlash(strings.TrimPrefix(filepath.Clean(nb.TargetPath), "./"))
			acr.TargetPath = target
			content := []byte(nb.Content)
			absTarget := filepath.Join(fixedWT.Path, filepath.FromSlash(target))
			if b, err := os.ReadFile(absTarget); err == nil {
				origBytes = b
				hadOrig = true
			}
			restoreTarget = target
			if err := fixedWT.WriteFile(target, content); err != nil {
				acr.Detail = fmt.Sprintf("write neighbor fixture %s: %v", target, err)
				report.AllowedResults = append(report.AllowedResults, acr)
				report.Verdict = VerdictInconclusive
				report.Reason = ReasonSetupError
				report.ActionableError = acr.Detail
				return false
			}
		}

		nRun, nErr := runTestRunner(ctx, repoRoot, fixedWT.Path, spec.Artifact.Tool, pkgs, nbSel, timeout)
		if restoreTarget != "" {
			if hadOrig {
				_ = fixedWT.WriteFile(restoreTarget, origBytes)
			} else {
				_ = fixedWT.RemoveFile(restoreTarget)
			}
		}
		if nErr != nil {
			acr.Detail = fmt.Sprintf("run neighbor test %q: %v", nbSel, nErr)
			report.AllowedResults = append(report.AllowedResults, acr)
			report.Verdict = VerdictInconclusive
			report.Reason = ReasonSetupError
			report.ActionableError = acr.Detail
			return false
		}
		if nRun.TimedOut {
			acr.Detail = fmt.Sprintf("neighbor test %q timed out", nbSel)
			report.AllowedResults = append(report.AllowedResults, acr)
			report.Verdict = VerdictInconclusive
			report.Reason = ReasonTimeout
			report.ActionableError = acr.Detail
			return false
		}
		if nRun.CompileOnly {
			acr.Detail = fmt.Sprintf("neighbor test %q failed to compile/collect: %s", nbSel, trimOneLine(nRun.BuildOutput))
			report.AllowedResults = append(report.AllowedResults, acr)
			report.Verdict = VerdictInconclusive
			report.Reason = ReasonFixedCompileError
			report.ActionableError = acr.Detail
			return false
		}
		matched := matchTestCases(nRun.Cases, nbSel)
		if len(matched) == 0 {
			acr.Detail = fmt.Sprintf("neighbor selector %q did not execute any matching test case on fixed tree", nbSel)
			report.AllowedResults = append(report.AllowedResults, acr)
			report.Verdict = VerdictInconclusive
			report.Reason = ReasonNeighborNotExecuted
			report.ActionableError = acr.Detail
			return false
		}
		if len(matched) != 1 || caseIdentity(matched[0], fixedWT.Path) == fixedID {
			report.Verdict, report.Reason = VerdictInconclusive, ReasonNeighborNotExecuted
			report.ActionableError = "neighbor selector must identify a unique test case"
			return false
		}
		for _, tc := range matched {
			switch tc.Outcome {
			case runner.OutcomeSkip:
				acr.Detail = fmt.Sprintf("neighbor test %q was skipped; skipped neighbors never satisfy proof", tc.Name)
				report.AllowedResults = append(report.AllowedResults, acr)
				report.Verdict = VerdictInconclusive
				report.Reason = ReasonNeighborSkipped
				report.ActionableError = acr.Detail
				return false
			case runner.OutcomeFail:
				acr.Detail = fmt.Sprintf("legitimate neighbor test %q failed on fixed tree: %s", tc.Name, trimOneLine(tc.Output))
				report.AllowedResults = append(report.AllowedResults, acr)
				report.Verdict = VerdictRefuted
				report.Reason = ReasonNeighborFailed
				report.ActionableError = acr.Detail
				return false
			case runner.OutcomeError:
				acr.Detail = fmt.Sprintf("neighbor test %q errored on fixed tree: %s", tc.Name, trimOneLine(tc.Output))
				report.AllowedResults = append(report.AllowedResults, acr)
				report.Verdict = VerdictInconclusive
				report.Reason = ReasonSetupError
				report.ActionableError = acr.Detail
				return false
			}
		}
		if nRun.ExitCode != 0 {
			report.Verdict, report.Reason = VerdictInconclusive, ReasonSetupError
			report.ActionableError = "neighbor runner did not complete successfully"
			return false
		}
		acr.Passed = true
		report.AllowedResults = append(report.AllowedResults, acr)
	}
	return true
}

func runTestRunner(ctx context.Context, repoRoot, dir, tool string, targets []string, selector string, timeout time.Duration) (runner.TestRunResult, error) {
	switch tool {
	case "go-test", "go":
		return runner.RunGoTest(ctx, dir, targets, selector, timeout)
	case "pytest":
		return runner.RunPytest(ctx, dir, targets, selector, timeout)
	case "vitest":
		return runner.RunVitest(ctx, repoRoot, dir, targets, selector, timeout)
	case "jest":
		return runner.RunJest(ctx, repoRoot, dir, targets, selector, timeout)
	default:
		return runner.TestRunResult{}, fmt.Errorf("unsupported test runner %q", tool)
	}
}

// SelectedCases selects the exact test identity used by proof and later enforcement.
// Callers expecting a single case must reject ambiguous matches.
func SelectedCases(cases []runner.TestCaseResult, selector string) []runner.TestCaseResult {
	return matchTestCases(cases, selector)
}

// matchTestCases requires a non-empty selector and performs strict normalized exact matching (D4, D9).
// An empty selector never matches any test case.
func matchTestCases(cases []runner.TestCaseResult, selector string) []runner.TestCaseResult {
	sel := strings.TrimSpace(selector)
	if sel == "" {
		return nil
	}
	normSel := normalizeSelectorForCompare(sel)
	spaceSel := strings.ReplaceAll(normSel, " › ", " ")
	var exact []runner.TestCaseResult
	for _, c := range cases {
		normCase := normalizeSelectorForCompare(c.Name)
		spaceCase := strings.ReplaceAll(normCase, " › ", " ")
		if c.Name == sel || normCase == normSel || spaceCase == spaceSel {
			exact = append(exact, c)
			continue
		}
		// Allow plain pytest function name without module prefix only when selector has no "::".
		if !strings.Contains(sel, "::") && strings.HasSuffix(c.Name, "::"+sel) {
			exact = append(exact, c)
		}
	}
	return exact
}

// normalizeSelectorForCompare aligns Go, pytest, and Vitest/Jest full test names for exact comparison.
func normalizeSelectorForCompare(s string) string {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "::") {
		return normalizePytestSelector(s)
	}
	// Normalize Vitest/Jest ancestor separators (" › ", " > ") and Vitest 3.x quoted $var titles.
	if strings.Contains(s, " › ") || strings.Contains(s, " > ") {
		s = strings.ReplaceAll(s, " > ", " › ")
		parts := strings.Split(s, " › ")
		for i := range parts {
			parts[i] = strings.Trim(strings.Join(strings.Fields(parts[i]), " "), "'")
		}
		return strings.Join(parts, " › ")
	}
	return strings.Trim(s, "'")
}

// normalizePytestSelector aligns pytest CLI node IDs (e.g. "pkg/test_calc.py::TestA::test_fn")
// with pytest JUnit XML names (e.g. "pkg.test_calc.TestA::test_fn" or "test_calc::test_fn").
func normalizePytestSelector(s string) string {
	if !strings.Contains(s, "::") {
		return s
	}
	parts := strings.Split(s, "::")
	filePart := filepath.ToSlash(strings.TrimPrefix(parts[0], "./"))
	filePart = strings.TrimSuffix(filePart, ".py")
	filePart = strings.ReplaceAll(filePart, "/", ".")
	if len(parts) == 2 {
		return filePart + "::" + parts[1]
	}
	// Flatten class segments into the dotted prefix to match JUnit classname::name.
	prefix := strings.Join(append([]string{filePart}, parts[1:len(parts)-1]...), ".")
	return prefix + "::" + parts[len(parts)-1]
}

func collectBrokenTargetFiles(r reflex.Reflex, changes []gitutil.FileChange) []string {
	var out []string
	for _, ch := range changes {
		p := ch.Path
		if ch.Status == "R" && ch.OldPath != "" {
			p = ch.OldPath
		}
		if ch.Status == "A" {
			continue
		}
		if r.MatchesPath(p) {
			out = append(out, p)
		}
	}
	return out
}

func filterFindingsForReflex(r reflex.Reflex, findings []finding.Finding) []finding.Finding {
	var out []finding.Finding
	for _, f := range findings {
		if !r.MatchesPath(f.Path) {
			continue
		}
		if r.Artifact.Rule != "" && f.Rule != r.Artifact.Rule && f.Selector != r.Artifact.Rule && f.Selector != r.Artifact.Selector {
			continue
		}
		if r.Artifact.Selector != "" && f.Selector != r.Artifact.Selector && f.Rule != r.Artifact.Selector && r.Artifact.Rule == "" {
			continue
		}
		out = append(out, f)
	}
	return out
}

func matchesExpectedSignal(text, expected string) bool {
	if strings.Contains(text, expected) {
		return true
	}
	if re, err := regexp.Compile(expected); err == nil {
		return re.MatchString(text)
	}
	return false
}

func trimOneLine(s string) string {
	s = strings.TrimSpace(s)
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			if len(l) > 220 {
				return l[:220] + "..."
			}
			return l
		}
	}
	return ""
}
