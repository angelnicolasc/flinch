package prove

import (
	"bytes"
	"context"
	"errors"
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

	pkgs := inferTestTargets(spec, changes)
	selector := spec.Artifact.Selector

	// 1. Run target test on fixed tree (must execute and pass cleanly without skips).
	fixedRun, err := runTestRunner(ctx, wt.Path, spec.Artifact.Tool, pkgs, selector, timeout)
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
		report.ActionableError = fmt.Sprintf("selector %q did not execute any test case on fixed tree", selector)
		return
	}
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

	// 2. Revert ONLY product files to broken state, keeping test and support files intact (§5.1).
	classifier := NewClassifier(
		spec.TestSupportGlobs,
		spec.ExplicitProductFiles,
		spec.ExplicitTestFiles,
		spec.ExplicitSupportFiles,
	)
	revertedProduct := 0
	for _, ch := range changes {
		if classifier.Classify(ch.Path) != ClassProduct {
			continue
		}
		revertedProduct++
		switch ch.Status {
		case "A":
			// Delete product files introduced by the fix (prevents false "does not protect" or leftover symbols).
			if err := wt.RemoveFile(ch.Path); err != nil {
				report.Verdict = VerdictInconclusive
				report.Reason = ReasonSetupError
				report.ActionableError = fmt.Sprintf("remove added product file %s: %v", ch.Path, err)
				return
			}
		case "R":
			if err := wt.RemoveFile(ch.Path); err != nil {
				report.Verdict = VerdictInconclusive
				report.Reason = ReasonSetupError
				report.ActionableError = fmt.Sprintf("remove renamed product file %s: %v", ch.Path, err)
				return
			}
			oldBytes, err := gitutil.FileAtRef(ctx, repoRoot, report.BrokenRef, ch.OldPath)
			if err != nil {
				report.Verdict = VerdictInconclusive
				report.Reason = ReasonSetupError
				report.ActionableError = fmt.Sprintf("restore renamed product file %s: %v", ch.OldPath, err)
				return
			}
			if err := wt.WriteFile(ch.OldPath, oldBytes); err != nil {
				report.Verdict = VerdictInconclusive
				report.Reason = ReasonSetupError
				report.ActionableError = fmt.Sprintf("write restored product file %s: %v", ch.OldPath, err)
				return
			}
		default: // M, D
			oldBytes, err := gitutil.FileAtRef(ctx, repoRoot, report.BrokenRef, ch.Path)
			if err != nil {
				report.Verdict = VerdictInconclusive
				report.Reason = ReasonSetupError
				report.ActionableError = fmt.Sprintf("restore product file %s from broken ref: %v", ch.Path, err)
				return
			}
			if err := wt.WriteFile(ch.Path, oldBytes); err != nil {
				report.Verdict = VerdictInconclusive
				report.Reason = ReasonSetupError
				report.ActionableError = fmt.Sprintf("write restored product file %s: %v", ch.Path, err)
				return
			}
		}
	}

	if revertedProduct == 0 {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonSetupError
		report.ActionableError = "no product files changed between broken_ref and fixed_ref; cannot separate test from fix"
		return
	}

	// 3. Run the EXACT SAME selector TWICE on the broken tree (§5.2).
	var brokenRuns [2]runner.TestRunResult
	for i := 0; i < 2; i++ {
		r, runErr := runTestRunner(ctx, wt.Path, spec.Artifact.Tool, pkgs, selector, timeout)
		if runErr != nil {
			report.Verdict = VerdictInconclusive
			report.Reason = ReasonSetupError
			report.ActionableError = fmt.Sprintf("run test attempt %d on broken tree: %v", i+1, runErr)
			return
		}
		brokenRuns[i] = r
		report.BrokenRuntimeMS += r.DurationMS
		if r.TimedOut {
			report.Verdict = VerdictInconclusive
			report.Reason = ReasonTimeout
			report.ActionableError = fmt.Sprintf("test timed out on broken tree (attempt %d)", i+1)
			return
		}
		if r.CompileOnly {
			report.Verdict = VerdictInconclusive
			report.Reason = ReasonCompileOnly
			report.ObservedSignal = trimOneLine(r.BuildOutput)
			report.ActionableError = "test only fails because the broken tree does not compile/import (uses API introduced by the fix); reach the fix through an API that already existed"
			return
		}
	}

	outcomes := [2]runner.TestOutcome{}
	var failOutput string
	for i := 0; i < 2; i++ {
		matched := matchTestCases(brokenRuns[i].Cases, selector)
		if len(matched) == 0 {
			report.Verdict = VerdictInconclusive
			report.Reason = ReasonSelectorNotExecuted
			report.ActionableError = fmt.Sprintf("selector %q did not execute on broken tree (attempt %d)", selector, i+1)
			return
		}
		state := aggregateCaseOutcome(matched)
		if state == runner.OutcomeSkip {
			report.Verdict = VerdictInconclusive
			report.Reason = ReasonSkippedOnBroken
			report.ActionableError = "candidate test was skipped on the broken tree; skip->pass never proves protection"
			return
		}
		if state == runner.OutcomeError {
			report.Verdict = VerdictInconclusive
			report.Reason = ReasonSetupError
			report.ActionableError = "candidate test encountered a setup error on the broken tree"
			return
		}
		outcomes[i] = state
		if state == runner.OutcomeFail && failOutput == "" {
			for _, tc := range matched {
				if tc.Outcome == runner.OutcomeFail {
					failOutput = tc.Output
					break
				}
			}
		}
	}

	if outcomes[0] != outcomes[1] {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonFlaky
		report.ActionableError = fmt.Sprintf("contradictory runs on broken tree: run 1=%s, run 2=%s", outcomes[0], outcomes[1])
		return
	}
	if outcomes[0] == runner.OutcomePass {
		report.Verdict = VerdictRefuted
		report.Reason = ReasonPassedOnBroken
		report.ActionableError = "candidate test passed on the broken tree and therefore does not protect the fix"
		return
	}

	report.ObservedSignal = trimOneLine(failOutput)
	if spec.Artifact.ExpectedSignal != "" && !matchesExpectedSignal(failOutput+"\n"+brokenRuns[0].RawOutput, spec.Artifact.ExpectedSignal) {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonUnexpectedFailureSignal
		report.ActionableError = fmt.Sprintf("broken tree failed, but output did not match expected_signal %q", spec.Artifact.ExpectedSignal)
		return
	}

	report.Verdict = VerdictVerified
}

func verifyLintOrStructure(ctx context.Context, repoRoot string, r reflex.Reflex, spec CandidateSpec, report *VerificationReport, timeout time.Duration) {
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
	fixedTargets := collectFixedTargetFiles(r, changes)

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
	_ = fixedTargets

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
		if !matchedSignal && !matchesExpectedSignal(brokenRun.Stdout+"\n"+brokenRun.Stderr, spec.Artifact.ExpectedSignal) {
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

	// Verify every allowed near-miss case materialized at its in-scope product path (§4, §5.2).
	if !verifyAllowedCases(ctx, repoRoot, fixedWT, r, spec, fixedFiltered, report, timeout) {
		return
	}

	report.Verdict = VerdictVerified
}

func verifyGuard(ctx context.Context, repoRoot string, r reflex.Reflex, spec CandidateSpec, report *VerificationReport, timeout time.Duration) {
	brokenWT, err := gitutil.CreateWorktree(ctx, repoRoot, report.BrokenRef, "prove-guard-broken-")
	if err != nil {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonSetupError
		report.ActionableError = fmt.Sprintf("create broken worktree: %v", err)
		return
	}
	defer brokenWT.Cleanup(ctx)

	fixedWT, err := gitutil.CreateWorktree(ctx, repoRoot, report.FixedRef, "prove-guard-fixed-")
	if err != nil {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonSetupError
		report.ActionableError = fmt.Sprintf("create fixed worktree: %v", err)
		return
	}
	defer fixedWT.Cleanup(ctx)

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

	brokenRun, err := runner.RunArtifact(ctx, brokenWT.Path, spec.Artifact, nil, timeout)
	if err != nil {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonSetupError
		report.ActionableError = fmt.Sprintf("run guard on broken tree: %v", err)
		return
	}
	report.BrokenRuntimeMS = brokenRun.DurationMS
	if brokenRun.TimedOut {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonTimeout
		report.ActionableError = "guard timed out on broken tree"
		return
	}
	if brokenRun.ExitCode == 0 && len(filterFindingsForReflex(r, brokenRun.Findings)) == 0 {
		report.Verdict = VerdictRefuted
		report.Reason = ReasonPassedOnBroken
		report.ActionableError = "guard command succeeded on broken tree without flagging the error"
		return
	}
	combinedBroken := brokenRun.Stdout + "\n" + brokenRun.Stderr
	report.ObservedSignal = trimOneLine(combinedBroken)
	if spec.Artifact.ExpectedSignal != "" && !matchesExpectedSignal(combinedBroken, spec.Artifact.ExpectedSignal) {
		report.Verdict = VerdictInconclusive
		report.Reason = ReasonUnexpectedFailureSignal
		report.ActionableError = fmt.Sprintf("guard failed on broken tree, but output did not match expected_signal %q", spec.Artifact.ExpectedSignal)
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
	if fixedRun.ExitCode != 0 || len(fixedFiltered) > 0 {
		report.Verdict = VerdictRefuted
		report.Reason = ReasonFailedOnFixed
		report.ActionableError = "guard command failed on fixed tree: " + trimOneLine(fixedRun.Stdout+"\n"+fixedRun.Stderr)
		return
	}

	if !verifyAllowedCases(ctx, repoRoot, fixedWT, r, spec, fixedFiltered, report, timeout) {
		return
	}

	report.Verdict = VerdictVerified
}

func verifyAllowedCases(
	ctx context.Context,
	repoRoot string,
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
			acr.Detail = fmt.Sprintf("allowed case target %q is outside reflex scope %q", target, r.Scope)
			report.AllowedResults = append(report.AllowedResults, acr)
			report.Verdict = VerdictInconclusive
			report.Reason = ReasonAllowedCaseOutOfScope
			report.ActionableError = acr.Detail
			return false
		}

		content := []byte(ac.Content)
		if len(content) == 0 && ac.SourceFile != "" {
			src := ac.SourceFile
			if !filepath.IsAbs(src) {
				src = filepath.Join(repoRoot, filepath.FromSlash(src))
			}
			var err error
			content, err = os.ReadFile(src)
			if err != nil {
				acr.Detail = fmt.Sprintf("read allowed case fixture %s: %v", ac.SourceFile, err)
				report.AllowedResults = append(report.AllowedResults, acr)
				report.Verdict = VerdictInconclusive
				report.Reason = ReasonSetupError
				report.ActionableError = acr.Detail
				return false
			}
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
		abs := filepath.Join(wt.Path, filepath.FromSlash(rel))
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
		} else if spec.Artifact.Tool == "pytest" && (strings.HasSuffix(ch.Path, ".py") && isTestFile(ch.Path)) {
			if !seen[ch.Path] {
				seen[ch.Path] = true
				targets = append(targets, ch.Path)
			}
		}
	}
	if len(targets) == 0 && spec.Artifact.Path != "" {
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

func runTestRunner(ctx context.Context, dir, tool string, targets []string, selector string, timeout time.Duration) (runner.TestRunResult, error) {
	switch tool {
	case "go-test", "go":
		return runner.RunGoTest(ctx, dir, targets, selector, timeout)
	case "pytest":
		return runner.RunPytest(ctx, dir, targets, selector, timeout)
	default:
		return runner.TestRunResult{}, fmt.Errorf("unsupported test runner %q", tool)
	}
}

func matchTestCases(cases []runner.TestCaseResult, selector string) []runner.TestCaseResult {
	sel := strings.TrimSpace(selector)
	if sel == "" {
		return cases
	}
	normSel := normalizePytestSelector(sel)
	var exact []runner.TestCaseResult
	var prefix []runner.TestCaseResult
	for _, c := range cases {
		normCase := normalizePytestSelector(c.Name)
		if c.Name == sel || normCase == normSel || strings.HasSuffix(c.Name, "::"+sel) {
			exact = append(exact, c)
		} else if strings.HasPrefix(c.Name, sel+"/") || strings.HasPrefix(normCase, normSel+"[") || strings.Contains(normCase, normSel) {
			prefix = append(prefix, c)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return prefix
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

func aggregateCaseOutcome(cases []runner.TestCaseResult) runner.TestOutcome {
	hasFail := false
	hasSkip := false
	hasError := false
	for _, c := range cases {
		switch c.Outcome {
		case runner.OutcomeFail:
			hasFail = true
		case runner.OutcomeSkip:
			hasSkip = true
		case runner.OutcomeError:
			hasError = true
		}
	}
	if hasError {
		return runner.OutcomeError
	}
	if hasSkip {
		return runner.OutcomeSkip
	}
	if hasFail {
		return runner.OutcomeFail
	}
	return runner.OutcomePass
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

func collectFixedTargetFiles(r reflex.Reflex, changes []gitutil.FileChange) []string {
	var out []string
	for _, ch := range changes {
		if ch.Status == "D" {
			continue
		}
		if r.MatchesPath(ch.Path) {
			out = append(out, ch.Path)
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

// Unused import guard for errors.
var _ = errors.New
