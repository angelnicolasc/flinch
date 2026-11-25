package check

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/angelnicolasc/flinch/internal/config"
	"github.com/angelnicolasc/flinch/internal/evidence"
	"github.com/angelnicolasc/flinch/internal/finding"
	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/prove"
	"github.com/angelnicolasc/flinch/internal/reflex"
	"github.com/angelnicolasc/flinch/internal/runner"
)

// Decision represents the enforcement outcome of a turn or CI check.
type Decision string

const (
	DecisionAllow                Decision = "allow"
	DecisionBlock                Decision = "block"
	DecisionAdvise               Decision = "advise"
	DecisionAllowTimeout         Decision = "allow_timeout"
	DecisionAllowBudgetExhausted Decision = "allow_budget_exhausted"
)

// FindingWithReflex pairs a normalized diagnostic with the reflex that triggered it.
type FindingWithReflex struct {
	ReflexID   string          `json:"check_id"`
	Rung       reflex.Rung     `json:"rung"`
	Obligation string          `json:"obligation"`
	FixHint    string          `json:"fix_hint,omitempty"`
	Mode       string          `json:"mode"` // block | warn | repair_if_supported | advise
	Finding    finding.Finding `json:"finding"`
}

// SkippedControl explains why a reflex was not evaluated in this run.
type SkippedControl struct {
	ReflexID              string `json:"check_id"`
	Reason                string `json:"reason"`
	CommandApprovalDigest string `json:"-"`
}

// Result summarizes the outcome of `flinch check --turn` or `flinch check --ci`.
type Result struct {
	Schema              int                  `json:"schema"`
	Surface             string               `json:"surface"` // turn | ci
	Decision            Decision             `json:"decision"`
	Blocking            bool                 `json:"blocking"`
	TurnID              string               `json:"turn_id,omitempty"`
	HasCheckpoint       bool                 `json:"has_checkpoint,omitempty"`
	TouchedFiles        []string             `json:"touched_files,omitempty"`
	Findings            []FindingWithReflex  `json:"findings,omitempty"`
	IntegrityViolations []IntegrityViolation `json:"integrity_violations,omitempty"`
	SkippedControls     []SkippedControl     `json:"skipped_controls,omitempty"`
	ContinuationsUsed   int                  `json:"continuations_used,omitempty"`
	ContinuationsLimit  int                  `json:"continuations_limit,omitempty"`
	DurationMS          int64                `json:"duration_ms"`
	DiagnosticMessage   string               `json:"diagnostic_message,omitempty"`
}

// TurnOptions configures interactive turn enforcement.
type TurnOptions struct {
	SessionID  string
	WorktreeID string
	CheckIntg  bool
	Deadline   time.Duration
}

// CIOptions configures shared CI enforcement.
type CIOptions struct {
	BaseRef   string
	CheckIntg bool
	Timeout   time.Duration
}

// AccreditOptions configures activation accreditation on an integrated SHA.
type AccreditOptions struct {
	IntegratedRef    string
	ReflexID         string
	Surface          evidence.SurfaceKind
	HostVersion      string
	RequiredForMerge bool
	TriggerWitness   *evidence.TriggerWitness
	Timeout          time.Duration
}

// RunTurn evaluates trusted active reflexes over changes made during the current turn,
// applying baseline occurrence diffing, integrity checks, a single unified deadline
// covering startup through completion, and shared continuation/test budgets.
func RunTurn(ctx context.Context, repoRoot string, cfg config.Config, opts TurnOptions) (Result, error) {
	ctx = runner.WithCommandRepository(ctx, repoRoot)
	start := time.Now()
	if opts.SessionID == "" {
		opts.SessionID = "default"
	}
	if opts.WorktreeID == "" {
		opts.WorktreeID = filepath.ToSlash(repoRoot)
	}
	deadline := opts.Deadline
	if deadline <= 0 {
		deadline = cfg.LoopDeadline()
	}

	res := Result{
		Schema:             1,
		Surface:            "turn",
		Decision:           DecisionAllow,
		ContinuationsLimit: cfg.Loop.MaxContinuationsPerTurn,
	}

	// Single unified deadline starts immediately to cover startup, loading, integrity, static scans, and fast tests.
	tctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	failOpenIfExpired := func() (Result, bool) {
		if errors.Is(tctx.Err(), context.DeadlineExceeded) {
			res.Decision = DecisionAllowTimeout
			res.Blocking = false
			res.DurationMS = time.Since(start).Milliseconds()
			return res, true
		}
		return res, false
	}

	if expiredRes, expired := failOpenIfExpired(); expired {
		return expiredRes, nil
	}

	stateDir, err := gitutil.FlinchStateDir(tctx, repoRoot)
	if err != nil {
		if expiredRes, expired := failOpenIfExpired(); expired {
			return expiredRes, nil
		}
		return res, err
	}

	cp, hasCP, err := LoadCheckpoint(stateDir, opts.SessionID, opts.WorktreeID)
	if err != nil {
		return res, err
	}
	res.HasCheckpoint = hasCP
	if hasCP {
		res.TurnID = cp.TurnID
		res.ContinuationsUsed = cp.TotalContinuations
	}

	if hasCP && cp.SnapshotSHA == "" {
		res.Decision = DecisionAdvise
		res.DurationMS = time.Since(start).Milliseconds()
		return res, nil
	}
	currentStatus, err := gitutil.WorkingTreeStatus(tctx, repoRoot)
	if err != nil {
		if expiredRes, expired := failOpenIfExpired(); expired {
			return expiredRes, nil
		}
		return res, err
	}
	var touched []string
	if hasCP {
		touched = ChangedFilesSinceCheckpoint(cp, currentStatus)
	} else {
		for p := range currentStatus {
			touched = append(touched, p)
		}
	}
	res.TouchedFiles = touched

	// Load ONLY reflexes present in the trusted revision so proposed rules stay isolated and deleted records stay visible.
	reflexes, err := loadTrustedReflexes(tctx, repoRoot, cfg.Executor.TrustedRef)
	if err != nil {
		if expiredRes, expired := failOpenIfExpired(); expired {
			return expiredRes, nil
		}
		return res, err
	}
	if len(reflexes) == 0 {
		res.DurationMS = time.Since(start).Milliseconds()
		return res, nil
	}

	if expiredRes, expired := failOpenIfExpired(); expired {
		return expiredRes, nil
	}

	if opts.CheckIntg {
		viols, intgErr := CheckIntegrity(tctx, repoRoot, cfg.Executor.TrustedRef, reflexes, touched)
		if intgErr == nil && len(viols) > 0 {
			res.IntegrityViolations = viols
		}
	}

	// Compare against the checkout as it was when the human turn opened.
	baselineRef := ""
	if hasCP {
		baselineRef = cp.SnapshotSHA
	}
	var baselineWT *gitutil.Worktree
	defer func() {
		if baselineWT != nil {
			_ = baselineWT.Cleanup(context.Background())
		}
	}()

	ensureBaselineWT := func() (*gitutil.Worktree, error) {
		if baselineWT != nil {
			return baselineWT, nil
		}
		if baselineRef == "" {
			return nil, fmt.Errorf("no baseline ref available")
		}
		wt, wtErr := gitutil.CreateWorktree(tctx, repoRoot, baselineRef, "turn-base-")
		if wtErr != nil {
			return nil, wtErr
		}
		baselineWT = wt
		return baselineWT, nil
	}

	maxTestDur, _ := config.ParseDuration(cfg.Loop.MaxTestRuntime)
	if maxTestDur <= 0 {
		maxTestDur = 5 * time.Second
	}
	testBudgetDur, _ := config.ParseDuration(cfg.Loop.TestBudget)
	if testBudgetDur <= 0 {
		testBudgetDur = 20 * time.Second
	}
	testBudgetDirty := false

	for _, r := range reflexes {
		if errors.Is(tctx.Err(), context.DeadlineExceeded) {
			res.SkippedControls = append(res.SkippedControls, SkippedControl{
				ReflexID: r.ID,
				Reason:   "unified turn deadline reached; deferred to CI",
			})
			continue
		}
		if !r.IsDeterministic() {
			continue
		}
		turnMode := "repair_if_supported"
		if r.Surfaces.AgentTurn != nil && r.Surfaces.AgentTurn.Mode != "" {
			turnMode = r.Surfaces.AgentTurn.Mode
		}
		if turnMode == "disabled" {
			res.SkippedControls = append(res.SkippedControls, SkippedControl{ReflexID: r.ID, Reason: "disabled on agent_turn surface"})
			continue
		}

		matchedFiles := filterTouchedFiles(r, touched)
		if len(matchedFiles) == 0 {
			continue
		}

		remaining := time.Until(deadlineTime(tctx, deadline))
		if remaining <= 0 {
			res.SkippedControls = append(res.SkippedControls, SkippedControl{ReflexID: r.ID, Reason: "turn deadline exhausted"})
			continue
		}

		switch r.EffectiveRung() {
		case reflex.RungTest:
			localProof, err := reflex.LocalProof(tctx, repoRoot, r)
			if err != nil {
				res.SkippedControls = append(res.SkippedControls, SkippedControl{ReflexID: r.ID, Reason: "could not read local test measurement"})
				continue
			}
			if localProof != nil && time.Duration(localProof.RuntimeMS)*time.Millisecond > maxTestDur {
				res.SkippedControls = append(res.SkippedControls, SkippedControl{
					ReflexID: r.ID,
					Reason:   fmt.Sprintf("test runtime %dms exceeds max_test_runtime (%s); deferred to CI", localProof.RuntimeMS, maxTestDur),
				})
				continue
			}
			remTestBudget := testBudgetDur - time.Duration(cp.TestBudgetUsedMS)*time.Millisecond
			if remTestBudget <= 0 {
				res.SkippedControls = append(res.SkippedControls, SkippedControl{
					ReflexID: r.ID,
					Reason:   fmt.Sprintf("logical turn test budget exhausted (%dms/%s); deferred to CI", cp.TestBudgetUsedMS, testBudgetDur),
				})
				continue
			}
			runCap := min(remaining, remTestBudget, maxTestDur)
			// A fresh clone has the reviewed record but no local timing. Charge a
			// bounded run at the checkpoint and reuse its result for comparison.
			var measuredBaseline *runner.TestRunResult
			if localProof == nil || localProof.RuntimeMS <= 0 {
				wt, err := ensureBaselineWT()
				if err != nil {
					continue
				}
				measuredAt := time.Now()
				calibration, skip := runTestReflexRaw(tctx, repoRoot, wt.Path, r, runCap)
				elapsed := max(time.Since(measuredAt).Milliseconds(), int64(1))
				cp.TestBudgetUsedMS += elapsed
				testBudgetDirty = true
				cases := prove.SelectedCases(calibration.Cases, r.Artifact.Selector)
				if skip != "" || calibration.ExitCode != 0 || len(cases) != 1 || cases[0].Outcome != runner.OutcomePass || time.Duration(elapsed)*time.Millisecond > maxTestDur {
					res.SkippedControls = append(res.SkippedControls, SkippedControl{ReflexID: r.ID, Reason: "no qualifying fast pass at the turn checkpoint"})
					continue
				}
				if err := reflex.SaveLocalProof(tctx, repoRoot, r, reflex.ProofSummary{RuntimeMS: elapsed}); err != nil {
					continue
				}
				measuredBaseline = &calibration
				runCap = min(maxTestDur, time.Until(deadlineTime(tctx, deadline)), testBudgetDur-time.Duration(cp.TestBudgetUsedMS)*time.Millisecond)
				if runCap <= 0 {
					continue
				}
			}
			testStart := time.Now()
			currResult, skipReason := runTestReflexRaw(tctx, repoRoot, repoRoot, r, runCap)
			elapsedTest := time.Since(testStart).Milliseconds()
			if hasCP {
				cp.TestBudgetUsedMS += elapsedTest
				testBudgetDirty = true
			}
			if skipReason != "" {
				res.SkippedControls = append(res.SkippedControls, SkippedControl{ReflexID: r.ID, Reason: skipReason})
				continue
			}
			currFailing := failingTestCases(currResult, r.Artifact.Selector)
			if len(currFailing) == 0 {
				continue
			}

			// Baseline comparison for test reflexes: a test already failing at checkpoint never blocks.
			wt, wtErr := ensureBaselineWT()
			if wtErr != nil {
				res.SkippedControls = append(res.SkippedControls, SkippedControl{
					ReflexID: r.ID,
					Reason:   fmt.Sprintf("baseline unavailable for test check (%v); failing open", wtErr),
				})
				continue
			}
			remAfterCurr := time.Until(deadlineTime(tctx, deadline))
			remTestAfterCurr := testBudgetDur - time.Duration(cp.TestBudgetUsedMS)*time.Millisecond
			if remTestAfterCurr < remAfterCurr {
				remAfterCurr = remTestAfterCurr
			}
			if remAfterCurr <= 0 {
				res.SkippedControls = append(res.SkippedControls, SkippedControl{
					ReflexID: r.ID,
					Reason:   "test budget exhausted before baseline comparison; failing open",
				})
				continue
			}
			baseStart := time.Now()
			var baseResult runner.TestRunResult
			var baseSkip string
			if measuredBaseline != nil {
				baseResult = *measuredBaseline
			} else {
				baseResult, baseSkip = runTestReflexRaw(tctx, repoRoot, wt.Path, r, min(remAfterCurr, maxTestDur))
			}
			if hasCP && measuredBaseline == nil {
				cp.TestBudgetUsedMS += time.Since(baseStart).Milliseconds()
				testBudgetDirty = true
			}
			if baseSkip != "" {
				res.SkippedControls = append(res.SkippedControls, SkippedControl{
					ReflexID: r.ID,
					Reason:   fmt.Sprintf("baseline test run inconclusive (%s); failing open", baseSkip),
				})
				continue
			}

			baseOutcomeByName := make(map[string]runner.TestOutcome, len(baseResult.Cases))
			for _, bc := range baseResult.Cases {
				baseOutcomeByName[bc.Name] = bc.Outcome
			}
			for _, fc := range currFailing {
				// Block ONLY when the case demonstrably passed at the baseline checkpoint.
				if baseOutcomeByName[fc.Name] != runner.OutcomePass {
					continue
				}
				res.Findings = append(res.Findings, FindingWithReflex{
					ReflexID:   r.ID,
					Rung:       r.EffectiveRung(),
					Obligation: r.Obligation,
					FixHint:    r.Artifact.FixHint,
					Mode:       turnMode,
					Finding: finding.Finding{
						Tool:      r.Artifact.Tool,
						Rule:      r.ID,
						Selector:  fc.Name,
						Path:      r.Artifact.Path,
						StartLine: 1,
						EndLine:   1,
						Symbol:    fc.Name,
						Message:   fmt.Sprintf("regression test %s failed: %s", fc.Name, fc.Output),
						Severity:  "error",
					},
				})
			}
		case reflex.RungLint, reflex.RungStructure, reflex.RungGuard:
			currRun, runErr := runner.RunArtifact(tctx, repoRoot, r.Artifact, targetArgForTool(r.Artifact.Tool, matchedFiles), remaining)
			if runErr != nil {
				reason := fmt.Sprintf("tool unavailable or failed to launch: %v", runErr)
				switch {
				case errors.Is(runErr, runner.ErrCommandApproval):
					reason = runner.CommandPermissionNotice
				case errors.Is(runErr, runner.ErrUnreadableReport):
					reason = fmt.Sprintf("%v; failing open because an unreadable report can't show what the turn changed", runErr)
				}
				res.SkippedControls = append(res.SkippedControls, SkippedControl{ReflexID: r.ID, Reason: reason, CommandApprovalDigest: runner.CommandApprovalDigest(runErr)})
				continue
			}
			if currRun.TimedOut {
				res.SkippedControls = append(res.SkippedControls, SkippedControl{ReflexID: r.ID, Reason: "static scan exceeded turn deadline; deferred to CI"})
				continue
			}
			currFiltered := filterFindings(r, currRun.Findings)
			if len(currFiltered) == 0 {
				continue
			}

			// Diff against turn baseline so pre-existing debt never blocks.
			// If novelty cannot be established because the baseline tree or run fails, fail open.
			wt, wtErr := ensureBaselineWT()
			if wtErr != nil {
				res.SkippedControls = append(res.SkippedControls, SkippedControl{
					ReflexID: r.ID,
					Reason:   fmt.Sprintf("baseline tree unavailable (%v); failing open so pre-existing debt cannot block", wtErr),
				})
				continue
			}
			baseTargets := existingFilesIn(wt.Path, matchedFiles)
			var baseFiltered []finding.Finding
			if len(baseTargets) > 0 || isWholeTreeTool(r.Artifact.Tool) {
				baseRem := time.Until(deadlineTime(tctx, remaining))
				if baseRem <= 0 {
					res.SkippedControls = append(res.SkippedControls, SkippedControl{
						ReflexID: r.ID,
						Reason:   "turn deadline reached before baseline comparison; failing open",
					})
					continue
				}
				baseRun, baseErr := runner.RunArtifact(tctx, wt.Path, r.Artifact, targetArgForTool(r.Artifact.Tool, baseTargets), baseRem)
				if baseErr != nil || baseRun.TimedOut || (baseRun.ExitCode != 0 && len(baseRun.Findings) == 0) {
					reason := "baseline comparison failed or timed out; failing open so pre-existing debt cannot block"
					if errors.Is(baseErr, runner.ErrCommandApproval) {
						reason = runner.CommandPermissionNotice
					}
					res.SkippedControls = append(res.SkippedControls, SkippedControl{
						ReflexID:              r.ID,
						Reason:                reason,
						CommandApprovalDigest: runner.CommandApprovalDigest(baseErr),
					})
					continue
				}
				baseFiltered = filterFindings(r, baseRun.Findings)
			}
			newOnes := finding.NewFindings(baseFiltered, currFiltered, nil)

			for _, f := range newOnes {
				res.Findings = append(res.Findings, FindingWithReflex{
					ReflexID:   r.ID,
					Rung:       r.EffectiveRung(),
					Obligation: r.Obligation,
					FixHint:    r.Artifact.FixHint,
					Mode:       turnMode,
					Finding:    f,
				})
			}
		}
	}

	if hasCP && testBudgetDirty {
		if err := SaveCheckpoint(stateDir, cp); err != nil {
			return res, err
		}
	}

	// Determine final turn decision, honoring fail-open timeout, missing checkpoint advise-only, and loop guard.
	if tctx.Err() != nil {
		res.Decision = DecisionAllowTimeout
		res.DurationMS = time.Since(start).Milliseconds()
		return res, nil
	}

	if len(res.Findings) == 0 && len(res.IntegrityViolations) == 0 {
		res.Decision = DecisionAllow
		res.DurationMS = time.Since(start).Milliseconds()
		return res, nil
	}

	if !hasCP {
		// Without a turn checkpoint, report advisory diagnostics without blocking.
		res.Decision = DecisionAdvise
		res.Blocking = false
		res.DiagnosticMessage = FormatAgentDiagnostic(res)
		res.DurationMS = time.Since(start).Milliseconds()
		return res, nil
	}

	// Check loop guard limits: max total continuations per turn AND max per finding.
	if cp.TotalContinuations >= cfg.Loop.MaxContinuationsPerTurn {
		res.Decision = DecisionAllowBudgetExhausted
		res.Blocking = false
		res.SkippedControls = append(res.SkippedControls, SkippedControl{
			ReflexID: "*",
			Reason:   fmt.Sprintf("turn continuation budget reached (%d/%d); remaining checks deferred to CI", cp.TotalContinuations, cfg.Loop.MaxContinuationsPerTurn),
		})
		res.DurationMS = time.Since(start).Milliseconds()
		return res, nil
	}

	allExhausted := len(res.IntegrityViolations) == 0
	for _, fwr := range res.Findings {
		if fwr.Mode == "advise" {
			continue
		}
		key := fwr.Finding.OccurrenceKey()
		if cp.FindingContinuations[key] < cfg.Loop.MaxContinuationsPerFinding {
			allExhausted = false
			break
		}
	}
	if allExhausted && len(res.Findings) > 0 {
		hasBlockingMode := false
		for _, fwr := range res.Findings {
			if fwr.Mode != "advise" {
				hasBlockingMode = true
			}
		}
		if hasBlockingMode {
			res.Decision = DecisionAllowBudgetExhausted
			res.Blocking = false
			res.DurationMS = time.Since(start).Milliseconds()
			return res, nil
		}
		res.Decision = DecisionAdvise
		res.Blocking = false
		res.DiagnosticMessage = FormatAgentDiagnostic(res)
		res.DurationMS = time.Since(start).Milliseconds()
		return res, nil
	}

	// Block and record continuation budget consumption.
	res.Decision = DecisionBlock
	res.Blocking = true
	cp.TotalContinuations++
	for _, fwr := range res.Findings {
		key := fwr.Finding.OccurrenceKey()
		cp.FindingContinuations[key]++
	}
	res.ContinuationsUsed = cp.TotalContinuations
	if err := SaveCheckpoint(stateDir, cp); err != nil {
		res.Blocking = false
		res.Decision = DecisionAllow
		return res, err
	}

	res.DiagnosticMessage = FormatAgentDiagnostic(res)
	res.DurationMS = time.Since(start).Milliseconds()
	return res, nil
}

// RunCI evaluates all active deterministic reflexes on the shared CI surface.
func RunCI(ctx context.Context, repoRoot string, cfg config.Config, opts CIOptions) (Result, error) {
	ctx = runner.WithCommandRepository(ctx, repoRoot)
	start := time.Now()
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = cfg.TestTimeout()
	}

	res := Result{
		Schema:   1,
		Surface:  "ci",
		Decision: DecisionAllow,
	}

	reflexes, err := reflex.LoadAll(repoRoot)
	if err != nil {
		return res, err
	}

	var changedFiles []string
	var renameMap map[string]string
	if opts.BaseRef != "" {
		changes, diffErr := gitutil.DiffFileChanges(ctx, repoRoot, opts.BaseRef, "HEAD")
		if diffErr == nil {
			renameMap = make(map[string]string)
			for _, ch := range changes {
				changedFiles = append(changedFiles, ch.Path)
				if ch.Status == "R" && ch.OldPath != "" {
					renameMap[ch.Path] = ch.OldPath
				}
			}
		}
	}
	res.TouchedFiles = changedFiles

	if opts.CheckIntg {
		ref := opts.BaseRef
		if ref == "" {
			ref = cfg.Executor.TrustedRef
		}
		reviewed, err := loadTrustedReflexes(ctx, repoRoot, ref)
		if err != nil {
			return res, err
		}
		viols, intgErr := CheckIntegrity(ctx, repoRoot, ref, reviewed, changedFiles)
		if intgErr == nil && len(viols) > 0 {
			res.IntegrityViolations = viols
		}
	}

	var baseWT *gitutil.Worktree
	defer func() {
		if baseWT != nil {
			_ = baseWT.Cleanup(context.Background())
		}
	}()

	for _, r := range reflexes {
		if !r.IsDeterministic() {
			continue
		}
		prMode := "block"
		if r.Surfaces.PullRequest != nil && r.Surfaces.PullRequest.Mode != "" {
			prMode = r.Surfaces.PullRequest.Mode
		}
		if prMode == "disabled" {
			res.SkippedControls = append(res.SkippedControls, SkippedControl{ReflexID: r.ID, Reason: "disabled on pull_request surface"})
			continue
		}

		switch r.EffectiveRung() {
		case reflex.RungTest:
			tFindings, skipReason := runTurnTestReflex(ctx, repoRoot, r, timeout)
			if skipReason != "" {
				res.SkippedControls = append(res.SkippedControls, SkippedControl{ReflexID: r.ID, Reason: skipReason})
				continue
			}
			for _, f := range tFindings {
				res.Findings = append(res.Findings, FindingWithReflex{
					ReflexID:   r.ID,
					Rung:       r.EffectiveRung(),
					Obligation: r.Obligation,
					FixHint:    r.Artifact.FixHint,
					Mode:       prMode,
					Finding:    f,
				})
			}
		case reflex.RungLint, reflex.RungStructure, reflex.RungGuard:
			currRun, runErr := runner.RunArtifact(ctx, repoRoot, r.Artifact, nil, timeout)
			if runErr != nil {
				res.SkippedControls = append(res.SkippedControls, SkippedControl{ReflexID: r.ID, Reason: fmt.Sprintf("tool failed: %v", runErr)})
				continue
			}
			currFiltered := filterFindings(r, currRun.Findings)

			// If --base is specified, block only findings introduced relative to base (new-only debt baseline).
			effective := currFiltered
			if opts.BaseRef != "" && len(currFiltered) > 0 {
				if baseWT == nil {
					if wt, wtErr := gitutil.CreateWorktree(ctx, repoRoot, opts.BaseRef, "ci-base-"); wtErr == nil {
						baseWT = wt
					}
				}
				if baseWT != nil {
					if baseRun, baseErr := runner.RunArtifact(ctx, baseWT.Path, r.Artifact, nil, timeout); baseErr == nil {
						baseFiltered := filterFindings(r, baseRun.Findings)
						effective = finding.NewFindings(baseFiltered, currFiltered, renameMap)
					} else if errors.Is(baseErr, runner.ErrCommandApproval) {
						res.SkippedControls = append(res.SkippedControls, SkippedControl{ReflexID: r.ID, Reason: runner.CommandPermissionNotice})
						continue
					}
				}
			}

			for _, f := range effective {
				res.Findings = append(res.Findings, FindingWithReflex{
					ReflexID:   r.ID,
					Rung:       r.EffectiveRung(),
					Obligation: r.Obligation,
					FixHint:    r.Artifact.FixHint,
					Mode:       prMode,
					Finding:    f,
				})
			}
		}
	}

	hasBlocking := len(res.IntegrityViolations) > 0
	for _, fwr := range res.Findings {
		if fwr.Mode == "block" {
			hasBlocking = true
			break
		}
	}
	if hasBlocking {
		res.Decision = DecisionBlock
		res.Blocking = true
	} else if len(res.Findings) > 0 {
		res.Decision = DecisionAdvise
		res.Blocking = false
	}
	res.DiagnosticMessage = FormatAgentDiagnostic(res)
	res.DurationMS = time.Since(start).Milliseconds()
	return res, nil
}

// Accredit verifies on an isolated checkout of an integrated SHA that each selected deterministic
// reflex is present, passes on clean code, fails on its materialized in-scope negative control
// with attributed selector ID, and passes on its allowed near-miss cases.
func Accredit(ctx context.Context, repoRoot string, cfg config.Config, opts AccreditOptions) ([]evidence.ActivationRecord, error) {
	ctx = runner.WithCommandRepository(ctx, repoRoot)
	ref := opts.IntegratedRef
	if strings.TrimSpace(ref) == "" {
		ref = "HEAD"
	}
	sha, err := gitutil.ResolveRef(ctx, repoRoot, ref)
	if err != nil {
		return nil, fmt.Errorf("resolve integrated ref %q: %w", ref, err)
	}
	surface := opts.Surface
	if surface == "" {
		surface = evidence.SurfaceCI
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = cfg.TestTimeout()
	}

	wt, err := gitutil.CreateWorktree(ctx, repoRoot, sha, "accredit-")
	if err != nil {
		return nil, err
	}
	defer wt.Cleanup(ctx)

	reflexes, err := reflex.LoadAll(wt.Path)
	if err != nil {
		return nil, err
	}
	stateDir, err := gitutil.FlinchStateDir(ctx, repoRoot)
	if err != nil {
		return nil, err
	}
	evStore := evidence.NewStore(stateDir)

	var records []evidence.ActivationRecord
	for _, r := range reflexes {
		if !r.IsDeterministic() {
			continue
		}
		if opts.ReflexID != "" && r.ID != opts.ReflexID {
			continue
		}

		fp, _ := r.ComputeFingerprint(wt.Path)
		mode := "block"
		job := cfg.Surfaces.CIJob
		if surface == evidence.SurfaceCI && r.Surfaces.PullRequest != nil {
			if r.Surfaces.PullRequest.Mode != "" {
				mode = r.Surfaces.PullRequest.Mode
			}
			if r.Surfaces.PullRequest.Job != "" {
				job = r.Surfaces.PullRequest.Job
			}
		} else if surface == evidence.SurfaceAgentTurn && r.Surfaces.AgentTurn != nil {
			if r.Surfaces.AgentTurn.Mode != "" {
				mode = r.Surfaces.AgentTurn.Mode
			}
		}

		rec := evidence.ActivationRecord{
			Schema:            1,
			ReflexID:          r.ID,
			ReflexVersion:     r.Version,
			ReflexFingerprint: fp,
			IntegratedSHA:     sha,
			Surface:           surface,
			HostVersion:       opts.HostVersion,
			Runner:            r.Artifact.Tool,
			Command:           r.Artifact.Command,
			Job:               job,
			Mode:              mode,
			RequiredForMerge:  opts.RequiredForMerge,
			ArtifactPresent:   artifactExistsIn(wt.Path, r),
			ControlDiscovered: true,
		}

		// Preserve existing trigger witness if still valid, or attach newly supplied witness.
		if opts.TriggerWitness != nil {
			rec.TriggerWitness = *opts.TriggerWitness
		} else if existing, loadErr := evStore.Load(r.ID, surface); loadErr == nil && existing.TriggerWitness.Witnessed {
			rec.TriggerWitness = existing.TriggerWitness
		}

		// 1. Clean integrated tree must pass.
		cleanRun, cleanErr := runner.RunArtifact(ctx, wt.Path, r.Artifact, nil, timeout)
		if cleanErr != nil {
			rec.ControlDiscovered = false
			rec.Summary = fmt.Sprintf("failed to execute control: %v", cleanErr)
			saved, _ := evStore.Save(rec)
			records = append(records, saved)
			continue
		}
		rec.Command = cleanRun.Command
		cleanFiltered := filterFindings(r, cleanRun.Findings)
		rec.CleanTreePassed = (cleanRun.ExitCode == 0 && len(cleanFiltered) == 0)

		// 2. Materialize negative control under real in-scope product path and verify attributed failure.
		rec.NegativeControl.ExpectedID = r.ExpectedID()
		if r.Cases.Broken != "" {
			targetPath := r.Cases.TargetPath
			if targetPath == "" {
				targetPath = r.Cases.Broken
			}
			brokenBytes, readErr := os.ReadFile(filepath.Join(wt.Path, filepath.FromSlash(r.Cases.Broken)))
			if readErr == nil && r.MatchesPath(targetPath) {
				rec.NegativeControl.Executed = true
				origBytes, origErr := os.ReadFile(filepath.Join(wt.Path, filepath.FromSlash(targetPath)))
				hadOrig := origErr == nil

				_ = wt.WriteFile(targetPath, brokenBytes)
				negRun, negErr := runner.RunArtifact(ctx, wt.Path, r.Artifact, nil, timeout)
				if hadOrig {
					_ = wt.WriteFile(targetPath, origBytes)
				} else {
					_ = wt.RemoveFile(targetPath)
				}
				if negErr == nil {
					negFiltered := filterFindings(r, negRun.Findings)
					if len(negFiltered) > 0 {
						rec.NegativeControl.Failed = true
						rec.NegativeControl.AttributedID = negFiltered[0].EffectiveID()
						rec.NegativeControl.Detail = fmt.Sprintf("%s:%d %s", negFiltered[0].Path, negFiltered[0].StartLine, negFiltered[0].Message)
					} else if negRun.ExitCode != 0 {
						rec.NegativeControl.Failed = true
						// Failed, but check whether any finding attributed the failure to another rule.
						if len(negRun.Findings) > 0 {
							rec.NegativeControl.AttributedID = negRun.Findings[0].EffectiveID()
						} else if r.Artifact.ExpectedSignal != "" && strings.Contains(negRun.Stdout+negRun.Stderr, r.Artifact.ExpectedSignal) {
							rec.NegativeControl.AttributedID = r.ExpectedID()
						}
						rec.NegativeControl.Detail = "non-zero exit without attributed finding matching expected selector"
					}
				}
			} else if !r.MatchesPath(targetPath) {
				rec.NegativeControl.Detail = fmt.Sprintf("target_path %q is outside scope %q", targetPath, r.Scope)
			}
		}

		// 3. Materialize allowed near-miss cases in-scope and verify clean pass.
		if len(r.Cases.Allowed) > 0 {
			rec.AllowedControl.Executed = true
			rec.AllowedControl.Passed = true
			for _, allowedRel := range r.Cases.Allowed {
				targetPath := r.Cases.TargetPath
				if targetPath == "" {
					targetPath = allowedRel
				}
				if !r.MatchesPath(targetPath) {
					rec.AllowedControl.Passed = false
					rec.AllowedControl.Detail = fmt.Sprintf("allowed target_path %q is outside scope %q", targetPath, r.Scope)
					break
				}
				allowedBytes, readErr := os.ReadFile(filepath.Join(wt.Path, filepath.FromSlash(allowedRel)))
				if readErr != nil {
					rec.AllowedControl.Passed = false
					rec.AllowedControl.Detail = fmt.Sprintf("read allowed fixture %s: %v", allowedRel, readErr)
					break
				}
				origBytes, origErr := os.ReadFile(filepath.Join(wt.Path, filepath.FromSlash(targetPath)))
				hadOrig := origErr == nil
				_ = wt.WriteFile(targetPath, allowedBytes)
				alRun, alErr := runner.RunArtifact(ctx, wt.Path, r.Artifact, nil, timeout)
				if hadOrig {
					_ = wt.WriteFile(targetPath, origBytes)
				} else {
					_ = wt.RemoveFile(targetPath)
				}
				if alErr != nil || alRun.ExitCode != 0 || len(filterFindings(r, alRun.Findings)) > 0 {
					rec.AllowedControl.Passed = false
					rec.AllowedControl.Detail = fmt.Sprintf("allowed case %s was flagged", allowedRel)
					break
				}
			}
		}

		saved, saveErr := evStore.Save(rec)
		if saveErr != nil {
			return nil, saveErr
		}
		records = append(records, saved)
	}
	return records, nil
}

// FormatAgentDiagnostic builds a single concise message listing rule/case, location,
// expected behavior, recommended API/fix, and the `flinch report` channel.
func FormatAgentDiagnostic(res Result) string {
	if len(res.Findings) == 0 && len(res.IntegrityViolations) == 0 {
		return ""
	}
	var b strings.Builder
	if len(res.IntegrityViolations) > 0 {
		b.WriteString(fmt.Sprintf("flinch: %d protection integrity violation(s) detected:\n", len(res.IntegrityViolations)))
		for i, v := range res.IntegrityViolations {
			b.WriteString(fmt.Sprintf("%d. [%s] %s: %s\n", i+1, v.Kind, v.Path, v.Detail))
		}
	}
	if len(res.Findings) > 0 {
		b.WriteString(fmt.Sprintf("flinch: %d active protection finding(s) in changed code:\n", len(res.Findings)))
		for i, fwr := range res.Findings {
			f := fwr.Finding
			b.WriteString(fmt.Sprintf("%d. [%s] %s  %s:%d (%s)\n", i+1, fwr.Rung, fwr.ReflexID, f.Path, f.StartLine, f.Symbol))
			b.WriteString(fmt.Sprintf("   obligation: %s\n", fwr.Obligation))
			if f.Message != "" {
				b.WriteString(fmt.Sprintf("   finding: %s\n", f.Message))
			}
			if fwr.FixHint != "" {
				b.WriteString(fmt.Sprintf("   recommended: %s\n", fwr.FixHint))
			}
		}
		b.WriteString("Fix the root cause without suppressing or weakening the protection.\n")
		b.WriteString("If a finding is a false positive, report it with: flinch report <id> --at <path:line> --reason \"<why>\"\n")
	}
	return strings.TrimSpace(b.String())
}

// FormatGitHubAnnotations formats findings as GitHub Actions workflow commands.
func FormatGitHubAnnotations(res Result) string {
	var b strings.Builder
	for _, fwr := range res.Findings {
		level := "warning"
		if fwr.Mode == "block" {
			level = "error"
		}
		f := fwr.Finding
		b.WriteString(fmt.Sprintf("::%s file=%s,line=%d,title=%s::%s\n", level, f.Path, f.StartLine, fwr.ReflexID, f.Message))
	}
	for _, v := range res.IntegrityViolations {
		b.WriteString(fmt.Sprintf("::error file=%s,title=flinch-integrity::%s\n", v.Path, v.Detail))
	}
	return b.String()
}

// FormatSARIF encodes Result as a SARIF 2.1.0 JSON document.
func FormatSARIF(res Result) ([]byte, error) {
	type sarifLoc struct {
		PhysicalLocation struct {
			ArtifactLocation struct {
				URI string `json:"uri"`
			} `json:"artifactLocation"`
			Region struct {
				StartLine int `json:"startLine"`
			} `json:"region"`
		} `json:"physicalLocation"`
	}
	type sarifResult struct {
		RuleID  string `json:"ruleId"`
		Level   string `json:"level"`
		Message struct {
			Text string `json:"text"`
		} `json:"message"`
		Locations []sarifLoc `json:"locations"`
	}
	var results []sarifResult
	for _, fwr := range res.Findings {
		lvl := "warning"
		if fwr.Mode == "block" {
			lvl = "error"
		}
		sr := sarifResult{RuleID: fwr.ReflexID, Level: lvl}
		sr.Message.Text = fmt.Sprintf("%s (%s)", fwr.Finding.Message, fwr.Obligation)
		var loc sarifLoc
		loc.PhysicalLocation.ArtifactLocation.URI = fwr.Finding.Path
		loc.PhysicalLocation.Region.StartLine = max(1, fwr.Finding.StartLine)
		sr.Locations = []sarifLoc{loc}
		results = append(results, sr)
	}
	doc := map[string]any{
		"$schema": "https://json.schemastore.org/sarif-2.1.0.json",
		"version": "2.1.0",
		"runs": []map[string]any{
			{
				"tool": map[string]any{
					"driver": map[string]any{
						"name": "flinch",
					},
				},
				"results": results,
			},
		},
	}
	return json.MarshalIndent(doc, "", "  ")
}

func loadTrustedReflexes(ctx context.Context, repoRoot, trustedRef string) ([]reflex.Reflex, error) {
	if strings.TrimSpace(trustedRef) == "" {
		trustedRef = "HEAD"
	}
	if _, err := gitutil.ResolveRef(ctx, repoRoot, trustedRef); err != nil {
		// A missing revision provides no reviewed records.
		return nil, nil
	}
	// Enumerate and parse reflexes directly from trustedRef so deleted records remain visible
	// to integrity checks and unreviewed working-tree reflexes stay isolated.
	files, err := gitutil.ListTreeFiles(ctx, repoRoot, trustedRef, reflex.ReflexesRelDir)
	if err != nil {
		return nil, err
	}
	legacy, err := gitutil.ListTreeFiles(ctx, repoRoot, trustedRef, reflex.LegacyRelDir)
	if err != nil {
		return nil, err
	}
	var trusted []reflex.Reflex
	seen := map[string]bool{}
	for _, rel := range append(files, legacy...) {
		if !strings.HasSuffix(rel, ".yml") && !strings.HasSuffix(rel, ".yaml") {
			continue
		}
		data, readErr := gitutil.FileAtRef(ctx, repoRoot, trustedRef, rel)
		if readErr != nil {
			continue
		}
		r, parseErr := reflex.ParseBytes(data, rel)
		if parseErr != nil || strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel)) != r.ID {
			continue
		}
		if !seen[r.ID] {
			trusted = append(trusted, r)
			seen[r.ID] = true
		}
	}
	return trusted, nil
}

func runTestReflexRaw(ctx context.Context, repoRoot, rootDir string, r reflex.Reflex, timeout time.Duration) (runner.TestRunResult, string) {
	var pkgs []string
	if r.Artifact.Path != "" {
		dir := filepath.ToSlash(filepath.Dir(r.Artifact.Path))
		if dir == "." || dir == "" {
			pkgs = []string{"."}
		} else {
			pkgs = []string{"./" + dir}
		}
	}
	var tr runner.TestRunResult
	var err error
	switch r.Artifact.Tool {
	case "go-test", "go":
		tr, err = runner.RunGoTest(ctx, rootDir, pkgs, r.Artifact.Selector, timeout)
	case "pytest":
		tr, err = runner.RunPytest(ctx, rootDir, []string{r.Artifact.Path}, r.Artifact.Selector, timeout)
	case "vitest":
		tr, err = runner.RunVitest(ctx, repoRoot, rootDir, []string{r.Artifact.Path}, r.Artifact.Selector, timeout)
	case "jest":
		tr, err = runner.RunJest(ctx, repoRoot, rootDir, []string{r.Artifact.Path}, r.Artifact.Selector, timeout)
	default:
		return runner.TestRunResult{}, fmt.Sprintf("unsupported test tool %q", r.Artifact.Tool)
	}
	if err != nil {
		return runner.TestRunResult{}, fmt.Sprintf("test runner failed: %v", err)
	}
	if tr.TimedOut {
		return runner.TestRunResult{}, "test timed out; deferred to CI"
	}
	if tr.CompileOnly {
		return runner.TestRunResult{}, "package failed to compile; deferred to build/CI"
	}
	return tr, ""
}

func failingTestCases(tr runner.TestRunResult, selector string) []runner.TestCaseResult {
	cases := prove.SelectedCases(tr.Cases, selector)
	if len(cases) == 1 && cases[0].Outcome == runner.OutcomeFail {
		return cases
	}
	return nil
}

func isWholeTreeTool(tool string) bool {
	return tool == "go-build" || tool == "command" || tool == "script"
}

func runTurnTestReflex(ctx context.Context, repoRoot string, r reflex.Reflex, timeout time.Duration) ([]finding.Finding, string) {
	tr, skip := runTestReflexRaw(ctx, repoRoot, repoRoot, r, timeout)
	if skip != "" {
		return nil, skip
	}
	var out []finding.Finding
	for _, c := range failingTestCases(tr, r.Artifact.Selector) {
		out = append(out, finding.Finding{
			Tool:      r.Artifact.Tool,
			Rule:      r.ID,
			Selector:  c.Name,
			Path:      r.Artifact.Path,
			StartLine: 1,
			EndLine:   1,
			Symbol:    c.Name,
			Message:   fmt.Sprintf("regression test %s failed: %s", c.Name, c.Output),
			Severity:  "error",
		})
	}
	return out, ""
}

func filterTouchedFiles(r reflex.Reflex, touched []string) []string {
	var out []string
	for _, p := range touched {
		if r.MatchesPath(p) {
			out = append(out, p)
		}
	}
	return out
}

func filterFindings(r reflex.Reflex, findings []finding.Finding) []finding.Finding {
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

func targetArgForTool(tool string, files []string) []string {
	switch tool {
	case "go-build", "command", "script":
		return nil
	default:
		return files
	}
}

func existingFilesIn(root string, files []string) []string {
	var out []string
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(f))); err == nil {
			out = append(out, f)
		}
	}
	return out
}

func artifactExistsIn(root string, r reflex.Reflex) bool {
	if r.Artifact.Path == "" {
		return true
	}
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(r.Artifact.Path)))
	return err == nil
}

func deadlineTime(ctx context.Context, fallback time.Duration) time.Time {
	if dl, ok := ctx.Deadline(); ok {
		return dl
	}
	return time.Now().Add(fallback)
}
