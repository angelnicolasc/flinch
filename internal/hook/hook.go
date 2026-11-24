package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/angelnicolasc/flinch/internal/author"
	"github.com/angelnicolasc/flinch/internal/check"
	"github.com/angelnicolasc/flinch/internal/config"
	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/pause"
	"github.com/angelnicolasc/flinch/internal/stats"
)

// Host identifies a supported coding-agent host adapter (D8, P9).
type Host string

const (
	HostClaude      Host = "claude"
	HostCodex       Host = "codex"
	HostAntigravity Host = "antigravity"
)

// Payload decodes hook stdin JSON across Claude Code / Codex (snake_case)
// and Antigravity (camelCase, protojson) into one normalized structure.
type Payload struct {
	// Claude Code & Codex snake_case fields
	SessionID            string `json:"session_id"`
	TurnID               string `json:"turn_id"`
	PromptID             string `json:"prompt_id"`
	EventID              string `json:"event_id"`
	HookEventNameSnake   string `json:"hook_event_name"`
	Prompt               string `json:"prompt"`
	StopHookActive       bool   `json:"stop_hook_active"`
	LastAssistantMessage string `json:"last_assistant_message"`
	Origin               string `json:"origin"`
	Trigger              string `json:"trigger"`
	Source               string `json:"source"`

	// Shared CWD field
	CWD string `json:"cwd"`

	// Antigravity camelCase fields (.agents/hooks.json contract)
	ConversationID     string   `json:"conversationId"`
	WorkspacePaths     []string `json:"workspacePaths"`
	LastUserInput      string   `json:"lastUserInput"`
	HookEventNameCamel string   `json:"hookEventName"`
	InvocationNum      int      `json:"invocationNum"`
	ExecutionNum       int      `json:"executionNum"`
	TerminationReason  string   `json:"terminationReason"`
	FullyIdle          bool     `json:"fullyIdle"`
	ContinuationActive bool     `json:"continuationActive"`
	ErrorText          string   `json:"error"`
}

func (p Payload) sessionID() string {
	if p.ConversationID != "" {
		return p.ConversationID
	}
	if p.SessionID != "" {
		return p.SessionID
	}
	return ""
}

func (p Payload) promptText() string {
	if p.LastUserInput != "" {
		return p.LastUserInput
	}
	return p.Prompt
}

func (p Payload) workDir(fallback string) string {
	if fallback != "" {
		return fallback
	}
	if p.CWD != "" {
		return p.CWD
	}
	if len(p.WorkspacePaths) > 0 && p.WorkspacePaths[0] != "" {
		return p.WorkspacePaths[0]
	}
	cwd, _ := os.Getwd()
	return cwd
}

func (p Payload) eventName(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if p.HookEventNameCamel != "" {
		return p.HookEventNameCamel
	}
	return p.HookEventNameSnake
}

func (p Payload) isNonHumanOrigin() bool {
	for _, field := range []string{p.Origin, p.Trigger, p.Source} {
		low := strings.ToLower(strings.TrimSpace(field))
		switch low {
		case "scheduled", "cron", "loop", "subagent", "synthetic", "hook", "automation":
			return true
		}
	}
	return false
}

// Response captures the exact stdout JSON (if any) and fail-open exit status returned to the host.
type Response struct {
	Stdout   []byte
	ExitCode int
}

type hookStartedKey struct{}
type hookResponseDeadlineKey struct{}

const (
	startTimeoutSeconds = 10
	stopTimeoutSeconds  = 30
	responseReserve     = 250 * time.Millisecond
)

// WithBudget starts the host deadline before stdin is read. Handle also uses
// this constructor when called directly. A nested call keeps the earlier start.
func WithBudget(ctx context.Context, event string, started time.Time) (context.Context, context.CancelFunc) {
	if prior, ok := ctx.Value(hookStartedKey{}).(time.Time); ok {
		started = prior
	} else {
		ctx = context.WithValue(ctx, hookStartedKey{}, started)
	}
	timeout := time.Duration(startTimeoutSeconds) * time.Second
	if event == "Stop" {
		timeout = time.Duration(stopTimeoutSeconds) * time.Second
	}
	if _, set := ctx.Value(hookResponseDeadlineKey{}).(time.Time); !set {
		responseBy := started.Add(timeout)
		if callerDeadline, ok := ctx.Deadline(); ok && callerDeadline.Before(responseBy) {
			responseBy = callerDeadline
		}
		ctx = context.WithValue(ctx, hookResponseDeadlineKey{}, responseBy)
	}
	return context.WithDeadline(ctx, started.Add(timeout-responseReserve))
}

// ResponseDeadline includes the margin reserved for writing the host reply.
func ResponseDeadline(ctx context.Context) time.Time {
	deadline, _ := ctx.Value(hookResponseDeadlineKey{}).(time.Time)
	return deadline
}

// Handle executes one host hook event against the shared Flinch engine (D1-D3, D5-D8, P7, P9).
// Every error path fails open (ExitCode 0) so Flinch never wedges a host session.
func Handle(ctx context.Context, host Host, explicitEvent string, stdinBytes []byte, explicitRepo string) Response {
	allowResp := AllowResponse(host)
	started := time.Now()

	// Fast exit when FLINCH=off is set for this session (D7).
	if pause.DisabledByEnv() {
		return allowResp
	}

	var payload Payload
	if len(strings.TrimSpace(string(stdinBytes))) > 0 {
		if err := json.Unmarshal(stdinBytes, &payload); err != nil {
			// Malformed payload -> fail open (P7).
			return allowResp
		}
	}
	event := payload.eventName(explicitEvent)
	ctx, cancel := WithBudget(ctx, event, started)
	defer cancel()
	sessionID := payload.sessionID()
	if ctx.Err() != nil || sessionID == "" || (event != "UserPromptSubmit" && event != "PreInvocation" && event != "Stop" && event != "StopFailure") {
		return allowResp
	}

	workDir := payload.workDir(explicitRepo)
	repoRoot, err := gitutil.RepoRoot(ctx, workDir)
	if err != nil {
		// Outside a Git repository -> fail open.
		return allowResp
	}
	stateDir, err := gitutil.FlinchStateDir(ctx, repoRoot)
	if err != nil {
		return allowResp
	}

	// Respect local clone/worktree pause (D7).
	if _, paused, pErr := pause.Check(stateDir, time.Now().UTC()); pErr == nil && paused {
		return allowResp
	}

	cfg, _, cfgErr := config.LoadOrDefault(repoRoot)
	if cfgErr != nil {
		return allowResp
	}

	release, err := check.LockTurn(ctx, stateDir, sessionID, repoRoot)
	if err != nil {
		return allowResp
	}
	defer release()

	switch event {
	case "UserPromptSubmit", "PreInvocation":
		return handleTurnStart(ctx, host, repoRoot, stateDir, cfg, sessionID, payload)
	case "Stop":
		return handleStop(ctx, host, repoRoot, stateDir, cfg, sessionID, payload)
	case "StopFailure":
		closeInterruptedTurn(stateDir, sessionID, repoRoot, payload.TurnID)
		return allowResp
	default:
		return allowResp
	}
}

func closeInterruptedTurn(stateDir, sessionID, repoRoot, hostTurnID string) {
	cp, ok, err := check.LoadCheckpoint(stateDir, sessionID, repoRoot)
	if err != nil || !ok || (hostTurnID != "" && cp.HostTurnID != "" && hostTurnID != cp.HostTurnID) {
		return
	}
	cp.Closed = true
	cp.ContinuationHash = ""
	cp.PreviousStartSnap, cp.PreviousEndSnap = "", ""
	_ = check.SaveCheckpoint(stateDir, cp)
}

func handleTurnStart(
	ctx context.Context,
	host Host,
	repoRoot, stateDir string,
	cfg config.Config,
	sessionID string,
	payload Payload,
) Response {
	allowResp := AllowResponse(host)

	cp, exists, err := check.LoadCheckpoint(stateDir, sessionID, repoRoot)
	if err != nil {
		return allowResp
	}
	promptHash := check.HashPrompt(payload.promptText())
	promptID := payload.PromptID
	if promptID == "" {
		promptID = payload.EventID
	}
	// A Stop continuation belongs to its existing human turn. Some hosts
	// resubmit the exact reason as prompt input. A text prefix is insufficient.
	if exists && !cp.Closed && cp.ContinuationHash != "" &&
		(promptHash == cp.ContinuationHash || payload.StopHookActive || payload.ContinuationActive) {
		if payload.TurnID != "" && payload.TurnID != cp.HostTurnID {
			cp.ContinuationTurnID = payload.TurnID
			_ = check.SaveCheckpoint(stateDir, cp)
		}
		return allowResp
	}
	if payload.isNonHumanOrigin() || payload.StopHookActive || payload.ContinuationActive || promptHash == "" {
		closeInterruptedTurn(stateDir, sessionID, repoRoot, "")
		return allowResp
	}
	if exists && ((payload.TurnID != "" && payload.TurnID == cp.HostTurnID) ||
		(promptID != "" && promptID == cp.PromptID)) {
		return allowResp
	}

	// Antigravity repeats PreInvocation for every model call. Without a host
	// event ID, matching input deduplicates only while this turn is still open.
	if exists && !cp.Closed && cp.PromptHash == promptHash &&
		(host == HostAntigravity || (promptID == "" && payload.TurnID == "")) {
		return allowResp
	}
	// Do not leave an interrupted previous turn active if a new capture fails.
	if exists && !cp.Closed {
		closeInterruptedTurn(stateDir, sessionID, repoRoot, "")
	}
	_, _ = check.CaptureCheckpointWithPolicy(
		ctx, repoRoot, sessionID, repoRoot,
		payload.promptText(), promptID, true,
		cfg.Policy.StorePromptText, cfg.Policy.RetentionDays, payload.TurnID,
	)
	return allowResp
}

func handleStop(
	ctx context.Context,
	host Host,
	repoRoot, stateDir string,
	cfg config.Config,
	sessionID string,
	payload Payload,
) Response {
	allowResp := AllowResponse(host)

	if host == HostAntigravity {
		if payload.TerminationReason != "model_stop" || payload.ErrorText != "" {
			closeInterruptedTurn(stateDir, sessionID, repoRoot, payload.TurnID)
			return allowResp
		}
		if !payload.FullyIdle {
			return allowResp
		}
	}

	cp, hasCP, err := check.LoadCheckpoint(stateDir, sessionID, repoRoot)
	if err != nil || !hasCP || cp.Closed || cp.SnapshotSHA == "" || ctx.Err() != nil {
		return allowResp
	}
	if payload.TurnID != "" && cp.HostTurnID != "" && payload.TurnID != cp.HostTurnID && payload.TurnID != cp.ContinuationTurnID {
		return allowResp
	}
	receipt := payload.EventID
	if receipt == "" {
		// Codex and Claude may omit a delivery ID. The next real Stop after a
		// continuation changes the active flag or the assistant's response.
		receipt = fmt.Sprintf("%q|%q|%t|%t|%d|%q", payload.TurnID, payload.LastAssistantMessage, payload.StopHookActive, payload.ContinuationActive, payload.ExecutionNum, payload.TerminationReason)
	}
	receipt = check.HashPrompt(receipt)
	if cp.StopReceipts[receipt] {
		return allowResp
	}
	if cp.StopReceipts == nil {
		cp.StopReceipts = make(map[string]bool)
	}
	cp.StopReceipts[receipt] = true
	if err := check.SaveCheckpoint(stateDir, cp); err != nil {
		return allowResp
	}

	// Capture post-turn state cleanly without creating Git refs (D11).
	currEndSnap, err := gitutil.CaptureWorkingTreeSnapshot(ctx, repoRoot, "flinch post-turn snapshot")
	if err != nil || ctx.Err() != nil {
		return allowResp
	}

	// 1. Enforcement of trusted protections ALWAYS takes priority over authoring (D1, D5, D6, P6).
	started := ctx.Value(hookStartedKey{}).(time.Time)
	enforcement, cancel := context.WithDeadline(ctx, started.Add(cfg.LoopDeadline()))
	turnRes, err := check.RunTurn(enforcement, repoRoot, cfg, check.TurnOptions{
		SessionID:  sessionID,
		WorktreeID: repoRoot,
		CheckIntg:  true,
	})
	timedOut := enforcement.Err() != nil
	cancel()
	if err != nil || timedOut || ctx.Err() != nil || turnRes.Decision == check.DecisionAllowTimeout {
		return allowResp
	}

	st := stats.NewStore(stateDir)
	_ = st.Record(stats.Event{Kind: stats.EventTurnChecked, Surface: string(host), DurationMS: turnRes.DurationMS})
	for _, sk := range turnRes.SkippedControls {
		_ = st.Record(stats.Event{Kind: stats.EventCheckSkipped, ReflexID: sk.ReflexID, Surface: string(host), Reason: sk.Reason})
	}
	// Reload checkpoint (RunTurn may have updated TestBudgetUsedMS / TotalContinuations).
	cp, hasCP, err = check.LoadCheckpoint(stateDir, sessionID, repoRoot)
	if err != nil || !hasCP {
		return allowResp
	}

	// Helper to finalize turn snapshots so the NEXT turn can nominate overlapping rework (D2).
	requestContinuation := func(reason string) Response {
		cp.ContinuationHash = check.HashPrompt(reason)
		if ctx.Err() != nil || check.SaveCheckpoint(stateDir, cp) != nil {
			return allowResp
		}
		return continueResponse(host, reason)
	}
	if turnRes.Blocking {
		_ = st.Record(stats.Event{Kind: stats.EventTurnIntercepted, Surface: string(host)})
		return requestContinuation(turnRes.DiagnosticMessage)
	}

	finalizeTurn := func() bool {
		cp.Closed = true
		cp.ContinuationHash = ""
		cp.PreviousStartSnap, cp.PreviousEndSnap = "", ""
		if currEndSnap != "" && cp.SnapshotSHA != "" && currEndSnap != cp.SnapshotSHA {
			cp.PreviousStartSnap = cp.SnapshotSHA
			cp.PreviousEndSnap = currEndSnap
		}
		return check.SaveCheckpoint(stateDir, cp) == nil
	}

	if cfg.Policy.Mode == config.ModeObserveOnly {
		finalizeTurn()
		return allowResp
	}

	// If the agent explicitly closed a nomination via `flinch skip` CLI or final message (D2).
	if cp.SkipReason != "" {
		_ = author.RecordAuthoringEvent(stateDir, author.AuthoringEvent{
			SessionID: sessionID, TurnID: cp.TurnID, Host: string(host),
			Kind: "skipped", Reason: cp.SkipReason,
		})
		finalizeTurn()
		return allowResp
	}
	if skipReason, ok := author.ParseSkipFromText(payload.LastAssistantMessage); ok {
		_ = author.RecordAuthoringEvent(stateDir, author.AuthoringEvent{
			SessionID: sessionID, TurnID: cp.TurnID, Host: string(host),
			Kind: "skipped", Reason: skipReason,
		})
		finalizeTurn()
		return allowResp
	}

	// 2. Follow-up Stop AFTER an authoring continuation was requested (D1, D3).
	if cp.AuthoringAttempted {
		remaining := author.AuthoringBudgetCap
		if cp.AuthoringStartedAt != nil {
			elapsed := time.Since(*cp.AuthoringStartedAt)
			if elapsed > author.AuthoringBudgetCap {
				_ = author.RecordAuthoringEvent(stateDir, author.AuthoringEvent{
					SessionID: sessionID, TurnID: cp.TurnID, Host: string(host),
					Kind: "timeout_exceeded", ElapsedMS: elapsed.Milliseconds(),
					Reason: "authoring exceeded 90s experimental cap; starting no further work",
				})
				finalizeTurn()
				return allowResp
			}
			remaining = author.AuthoringBudgetCap - elapsed
		}

		proofCtx, cancelProof := context.WithTimeout(ctx, remaining)
		outcome, _ := author.ReuseFirst(proofCtx, repoRoot, cfg, cp.SnapshotSHA, currEndSnap, remaining, string(host))
		cancelProof()
		switch outcome.Status {
		case author.ReuseProven:
			_ = author.RecordAuthoringEvent(stateDir, author.AuthoringEvent{
				SessionID: sessionID, TurnID: cp.TurnID, Host: string(host),
				Kind: "written", ReflexID: outcome.Reflex.ID, Selector: outcome.Reflex.Artifact.Selector,
				UnrecognizedAssertions: outcome.UnrecognizedAssertions,
			})
		case author.ReuseLineValid:
			_ = author.RecordAuthoringEvent(stateDir, author.AuthoringEvent{
				SessionID: sessionID, TurnID: cp.TurnID, Host: string(host),
				Kind: "line_recorded", Reason: strings.Join(outcome.AddedLines, "; "),
			})
		case author.ReuseLineFormatError:
			// D3: At most one formatting repair continuation, only if budget remains.
			if !cp.LineRepairUsed && cp.TotalContinuations < cfg.Loop.MaxContinuationsPerTurn {
				cp.LineRepairUsed = true
				cp.TotalContinuations++
				return requestContinuation(fmt.Sprintf(
					"flinch: managed guidance line check failed (%s). Fix the formatting inside %s..%s without editing outside that section, or close with `flinch skip: <reason>`.",
					outcome.Reason, author.ManagedStartMarker, author.ManagedEndMarker,
				))
			}
			_ = author.RecordAuthoringEvent(stateDir, author.AuthoringEvent{
				SessionID: sessionID, TurnID: cp.TurnID, Host: string(host),
				Kind: "inconclusive_closed", Reason: outcome.Reason,
			})
		default:
			// D3: Never auto-fallback to AGENTS.md when proof fails or is inconclusive; close silently.
			_ = author.RecordAuthoringEvent(stateDir, author.AuthoringEvent{
				SessionID: sessionID, TurnID: cp.TurnID, Host: string(host),
				Kind: "inconclusive_closed", Reason: outcome.Reason,
				UnrecognizedAssertions: outcome.UnrecognizedAssertions,
			})
		}
		finalizeTurn()
		return allowResp
	}

	// 3. Normal end of turn: check deterministic D2 nomination (hunk/symbol overlap with prior turn).
	nom, nomErr := author.NominateCorrection(ctx, repoRoot, cp.PreviousStartSnap, cp.PreviousEndSnap, cp.SnapshotSHA, currEndSnap)
	if nomErr != nil || !nom.Nominated {
		finalizeTurn()
		return allowResp
	}

	_ = author.RecordAuthoringEvent(stateDir, author.AuthoringEvent{
		SessionID: sessionID, TurnID: cp.TurnID, Host: string(host),
		Kind: "nominated", Reason: strings.Join(nom.OverlappingFiles, ", "),
	})

	// 4. Reuse-first (D1): inspect the fix diff for a protection the agent already wrote!
	authorStart := time.Now().UTC()
	outcome, _ := author.ReuseFirst(ctx, repoRoot, cfg, nom.BrokenRef, nom.FixedRef, author.AuthoringBudgetCap, string(host))
	switch outcome.Status {
	case author.ReuseProven:
		_ = author.RecordAuthoringEvent(stateDir, author.AuthoringEvent{
			SessionID: sessionID, TurnID: cp.TurnID, Host: string(host),
			Kind: "reused", ReflexID: outcome.Reflex.ID, Selector: outcome.Reflex.Artifact.Selector,
			UnrecognizedAssertions: outcome.UnrecognizedAssertions,
			ElapsedMS:              time.Since(authorStart).Milliseconds(),
		})
		if !finalizeTurn() || ctx.Err() != nil {
			return allowResp
		}
		if host == HostClaude || host == HostCodex {
			message, err := json.Marshal(map[string]string{"systemMessage": outcome.SummaryLine})
			if err == nil {
				return Response{Stdout: append(message, '\n')}
			}
		}
		return allowResp
	case author.ReuseLineValid:
		_ = author.RecordAuthoringEvent(stateDir, author.AuthoringEvent{
			SessionID: sessionID, TurnID: cp.TurnID, Host: string(host),
			Kind: "line_recorded", Reason: strings.Join(outcome.AddedLines, "; "),
			ElapsedMS: time.Since(authorStart).Milliseconds(),
		})
		finalizeTurn()
		return allowResp
	case author.ReuseInconclusive:
		// D4: Keep passing test as ordinary test out of protection count, close silently.
		_ = author.RecordAuthoringEvent(stateDir, author.AuthoringEvent{
			SessionID: sessionID, TurnID: cp.TurnID, Host: string(host),
			Kind: "inconclusive_closed", Reason: outcome.Reason,
			UnrecognizedAssertions: outcome.UnrecognizedAssertions,
			ElapsedMS:              time.Since(authorStart).Milliseconds(),
		})
		finalizeTurn()
		return allowResp
	case author.ReuseLineSectionFull:
		_ = author.RecordAuthoringEvent(stateDir, author.AuthoringEvent{
			SessionID: sessionID, TurnID: cp.TurnID, Host: string(host),
			Kind: "skipped", Reason: outcome.Reason,
		})
		finalizeTurn()
		return allowResp
	case author.ReuseLineFormatError:
		if !cp.LineRepairUsed && cp.TotalContinuations < cfg.Loop.MaxContinuationsPerTurn {
			cp.LineRepairUsed = true
			cp.AuthoringAttempted = true
			cp.AuthoringStartedAt = &authorStart
			cp.TotalContinuations++
			return requestContinuation(fmt.Sprintf(
				"flinch: managed guidance line check failed (%s). Fix the formatting inside %s..%s without editing outside that section, or reply `flinch skip: <reason>`.",
				outcome.Reason, author.ManagedStartMarker, author.ManagedEndMarker,
			))
		}
		finalizeTurn()
		return allowResp
	}

	// 5. Ask only when needed (D1): one continuation if budget and 90s cap permit.
	if time.Since(authorStart) > author.AuthoringBudgetCap {
		_ = author.RecordAuthoringEvent(stateDir, author.AuthoringEvent{
			SessionID: sessionID, TurnID: cp.TurnID, Host: string(host),
			Kind: "timeout_exceeded", ElapsedMS: time.Since(authorStart).Milliseconds(),
		})
		finalizeTurn()
		return allowResp
	}
	if cp.TotalContinuations >= cfg.Loop.MaxContinuationsPerTurn {
		_ = author.RecordAuthoringEvent(stateDir, author.AuthoringEvent{
			SessionID: sessionID, TurnID: cp.TurnID, Host: string(host),
			Kind: "skipped", Reason: "turn continuation budget exhausted",
		})
		finalizeTurn()
		return allowResp
	}

	cp.AuthoringAttempted = true
	cp.AuthoringStartedAt = &authorStart
	cp.TotalContinuations++

	_ = author.RecordAuthoringEvent(stateDir, author.AuthoringEvent{
		SessionID: sessionID, TurnID: cp.TurnID, Host: string(host),
		Kind: "authoring_requested", Reason: strings.Join(nom.OverlappingFiles, ", "),
	})

	guide := author.ManagedGuide(ctx, repoRoot, nom.BrokenRef, cfg, string(host))
	if guide != "" && guide != author.ManagedGuide(ctx, repoRoot, nom.FixedRef, cfg, string(host)) {
		guide = ""
	}
	prompt := buildAuthoringContinuationPrompt(guide, nom)
	return requestContinuation(prompt)
}

func buildAuthoringContinuationPrompt(guideFile string, nom author.Nomination) string {
	guidance := ""
	if guideFile != "" {
		guidance = fmt.Sprintf(" For a durable convention with no suitable executable check, add one concise line inside %s .. %s in %s.", author.ManagedStartMarker, author.ManagedEndMarker, guideFile)
	}
	return fmt.Sprintf(
		"flinch: this turn reworked code from the previous turn (%s). "+
			"If that rework came from a reusable correction, add ONE protection using the repository's existing tools "+
			"(preferring structure, lint, a focused test, or a guard). "+
			"For a test, add a new case that fails at a standard assertion in its own body and identify a distinct neighboring case that still passes.%s "+
			"Include one concise line in your final message explaining it. "+
			"If this was a one-off preference or not a correction, do not edit any files and end with `flinch skip: <reason>`.",
		strings.Join(nom.OverlappingFiles, ", "),
		guidance,
	)
}

// AllowResponse allows normal host completion without a continuation.
func AllowResponse(host Host) Response {
	if host == HostAntigravity {
		return Response{Stdout: []byte("{}\n"), ExitCode: 0}
	}
	return Response{Stdout: nil, ExitCode: 0}
}

func continueResponse(host Host, reason string) Response {
	decision := "block"
	if host == HostAntigravity {
		decision = "continue"
	}
	payload := map[string]string{
		"decision": decision,
		"reason":   reason,
	}
	b, _ := json.Marshal(payload)
	return Response{
		Stdout:   append(b, '\n'),
		ExitCode: 0,
	}
}
