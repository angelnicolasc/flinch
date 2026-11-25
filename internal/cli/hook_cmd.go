package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/angelnicolasc/flinch/internal/author"
	"github.com/angelnicolasc/flinch/internal/check"
	"github.com/angelnicolasc/flinch/internal/config"
	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/hook"
	"github.com/angelnicolasc/flinch/internal/pause"
	"github.com/angelnicolasc/flinch/internal/usersetup"
)

// maxHookPayload bounds how much a host event can make the hook buffer. A
// larger event is not handled, so the turn continues without Flinch.
const maxHookPayload = 16 << 20

// hookDelivery holds what a hook invocation reads and calls. Tests replace it
// per invocation instead of through package state, because a worker can
// outlive a delivery that timed out.
type hookDelivery struct {
	stdin      io.Reader
	handle     func(ctx context.Context, host hook.Host, event string, stdin []byte, repo string) hook.Response
	registered func(host, event string) bool
}

func runHook(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return runHookDelivery(ctx, args, stdout, hookDelivery{stdin: os.Stdin, handle: hook.Handle, registered: usersetup.UserHookRegistered})
}

// runHookDelivery answers one host event. Whatever happens inside Flinch, the
// host gets either Flinch's decision or that host's allow response, exactly
// once, with exit code 0 and nothing on stderr. In Claude Code and Codex a
// nonzero exit with text on stderr would block the user's prompt or keep the
// agent working.
func runHookDelivery(ctx context.Context, args []string, stdout io.Writer, in hookDelivery) int {
	started := time.Now()
	ignoreBrokenPipe()
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // Hooks fail open silently.
	hostStr := fs.String("host", "", "host adapter: claude | codex | antigravity")
	eventStr := fs.String("event", "", "hook event name")
	userHook := fs.Bool("user", false, "delivery from the user hook installed by setup")
	repoDir := fs.String("repo", "", "optional repository directory override")
	if err := fs.Parse(args); err != nil {
		return 0
	}

	ctx, cancel := hook.WithBudget(ctx, *eventStr, started)
	defer cancel()
	host := hook.Host(strings.ToLower(strings.TrimSpace(*hostStr)))
	allow := hook.AllowResponse(host)
	result := make(chan hook.Response, 1)
	go func() {
		resp, stage := allow, "read"
		// The worker publishes exactly one response, and a recovered failure
		// publishes the allow response.
		defer func() {
			if recover() != nil {
				resp = allow
				hook.RecordIncident(ctx, string(host), *eventStr, stage, Version)
			}
			result <- resp
		}()
		if file, ok := in.stdin.(*os.File); ok {
			if deadline, ok := ctx.Deadline(); ok {
				if err := file.SetReadDeadline(deadline); err == nil {
					defer file.SetReadDeadline(time.Time{})
				}
			}
		}
		data, err := io.ReadAll(io.LimitReader(in.stdin, maxHookPayload+1))
		if err != nil || len(data) > maxHookPayload {
			return
		}
		stage = "registration"
		if !*userHook && in.registered(string(host), *eventStr) {
			return
		}
		stage = "handle"
		resp = in.handle(ctx, host, *eventStr, data, *repoDir)
	}()
	var resp hook.Response
	select {
	case resp = <-result:
		// A decision that arrives after the budget ran out is dropped, so the
		// turn finishes as the budget promises.
		if ctx.Err() != nil {
			resp = allow
		}
	case <-ctx.Done():
		// Filesystems and some host pipes do not support cancellation of an
		// in-flight operation. The CLI must still release the host on time.
		resp = allow
	}
	return writeHookResponse(ctx, host, *eventStr, stdout, resp)
}

// writeHookResponse sends the prepared response once. A partial or uncertain
// delivery is never followed by a second message, because the host would read
// the two concatenated.
func writeHookResponse(ctx context.Context, host hook.Host, event string, stdout io.Writer, resp hook.Response) (code int) {
	defer func() {
		if recover() != nil {
			hook.RecordIncident(ctx, string(host), event, "write", Version)
			code = 0
		}
	}()
	if len(resp.Stdout) == 0 {
		return resp.ExitCode
	}
	if file, ok := stdout.(*os.File); ok {
		deadline := hook.ResponseDeadline(ctx)
		if err := file.SetWriteDeadline(deadline); err != nil {
			// Some inherited Windows handles do not support I/O deadlines.
			// Let the CLI exit on time even if the host stops reading its pipe.
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer func() { _ = recover() }()
				_, _ = file.Write(resp.Stdout)
			}()
			timer := time.NewTimer(time.Until(deadline))
			defer timer.Stop()
			select {
			case <-done:
			case <-timer.C:
			}
			return resp.ExitCode
		}
		defer file.SetWriteDeadline(time.Time{})
	}
	_, _ = stdout.Write(resp.Stdout)
	return resp.ExitCode
}

func runPause(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pause", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repoDir := fs.String("repo", "", "repository directory")
	durationStr := fs.String("duration", "", "optional auto-resume duration (e.g. 2h, 1d)")
	reason := fs.String("reason", "", "optional pause reason")
	statusOnly := fs.Bool("status", false, "print current pause status without changing it")
	asJSON := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	root, err := resolveRepoRoot(ctx, *repoDir)
	if err != nil {
		fmt.Fprintf(stderr, "flinch pause: %v\n", err)
		return 2
	}
	stateDir, err := gitutil.FlinchStateDir(ctx, root)
	if err != nil {
		fmt.Fprintf(stderr, "flinch pause: %v\n", err)
		return 2
	}

	if *statusOnly {
		st, active, err := pause.Check(stateDir, time.Now().UTC())
		if err != nil {
			fmt.Fprintf(stderr, "flinch pause --status: %v\n", err)
			return 2
		}
		if *asJSON {
			return writeJSON(stdout, map[string]any{"paused": active, "state": st})
		}
		if !active {
			fmt.Fprintln(stdout, "flinch is active (not paused)")
			return 0
		}
		fmt.Fprintf(stdout, "flinch is paused (reason=%q)\n", st.Reason)
		return 0
	}

	var dur time.Duration
	if strings.TrimSpace(*durationStr) != "" {
		var dErr error
		dur, dErr = config.ParseDuration(*durationStr)
		if dErr != nil {
			fmt.Fprintf(stderr, "flinch pause: invalid --duration: %v\n", dErr)
			return 2
		}
	}
	st, err := pause.Pause(stateDir, dur, *reason, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(stderr, "flinch pause: %v\n", err)
		return 2
	}
	if *asJSON {
		return writeJSON(stdout, st)
	}
	if st.ExpiresAt != nil {
		fmt.Fprintf(stdout, "Paused Flinch for local clone and worktrees until %s (native repo checks keep running).\n", st.ExpiresAt.Format(time.RFC3339))
	} else {
		fmt.Fprintln(stdout, "Paused Flinch for local clone and worktrees until `flinch resume` (native repo checks keep running).")
	}
	return 0
}

func runResume(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repoDir := fs.String("repo", "", "repository directory")
	asJSON := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	root, err := resolveRepoRoot(ctx, *repoDir)
	if err != nil {
		fmt.Fprintf(stderr, "flinch resume: %v\n", err)
		return 2
	}
	stateDir, err := gitutil.FlinchStateDir(ctx, root)
	if err != nil {
		fmt.Fprintf(stderr, "flinch resume: %v\n", err)
		return 2
	}
	wasPaused, err := pause.Resume(stateDir, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(stderr, "flinch resume: %v\n", err)
		return 2
	}
	if *asJSON {
		return writeJSON(stdout, map[string]any{"resumed": true, "was_paused": wasPaused})
	}
	if wasPaused {
		fmt.Fprintln(stdout, "Resumed Flinch for local clone and worktrees.")
	} else {
		fmt.Fprintln(stdout, "Flinch was not paused.")
	}
	return 0
}

func runSkip(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("skip", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repoDir := fs.String("repo", "", "repository directory")
	sessionID := fs.String("session", "default", "agent session identifier")
	reason := fs.String("reason", "", "skip reason")
	asJSON := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	r := strings.TrimSpace(*reason)
	if r == "" && fs.NArg() > 0 {
		r = strings.TrimSpace(strings.Join(fs.Args(), " "))
	}
	if r == "" {
		r = "no reusable protection needed"
	}

	root, err := resolveRepoRoot(ctx, *repoDir)
	if err != nil {
		fmt.Fprintf(stderr, "flinch skip: %v\n", err)
		return 2
	}
	stateDir, err := gitutil.FlinchStateDir(ctx, root)
	if err != nil {
		fmt.Fprintf(stderr, "flinch skip: %v\n", err)
		return 2
	}
	if cp, ok, err := check.LoadCheckpoint(stateDir, *sessionID, root); err == nil && ok {
		cp.SkipReason = r
		_ = check.SaveCheckpoint(stateDir, cp)
	}
	_ = author.RecordAuthoringEvent(stateDir, author.AuthoringEvent{
		SessionID: *sessionID,
		Kind:      "skipped",
		Reason:    r,
	})
	if *asJSON {
		return writeJSON(stdout, map[string]any{"skipped": true, "reason": r})
	}
	return 0
}

func runReport(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repoDir := fs.String("repo", "", "repository directory")
	at := fs.String("at", "", "location (path:line)")
	reason := fs.String("reason", "", "why the protection appears to be a false positive")
	asJSON := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(stderr, "flinch report: usage: flinch report <check-id> --at <path:line> --reason \"<why>\"")
		return 2
	}
	reflexID := fs.Arg(0)
	if err := fs.Parse(fs.Args()[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "flinch report: expected one check ID")
		return 2
	}

	root, err := resolveRepoRoot(ctx, *repoDir)
	if err != nil {
		fmt.Fprintf(stderr, "flinch report: %v\n", err)
		return 2
	}
	stateDir, err := gitutil.FlinchStateDir(ctx, root)
	if err != nil {
		fmt.Fprintf(stderr, "flinch report: %v\n", err)
		return 2
	}

	rec := map[string]any{
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"check_id":  reflexID,
		"at":        *at,
		"reason":    *reason,
	}
	repPath := filepath.Join(stateDir, "reports", "false-positives.jsonl")
	if err := os.MkdirAll(filepath.Dir(repPath), 0o755); err != nil {
		fmt.Fprintf(stderr, "flinch report: %v\n", err)
		return 2
	}
	b, _ := json.Marshal(rec)
	f, err := os.OpenFile(repPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintf(stderr, "flinch report: %v\n", err)
		return 2
	}
	_, _ = fmt.Fprintf(f, "%s\n", b)
	_ = f.Close()

	if *asJSON {
		return writeJSON(stdout, rec)
	}
	fmt.Fprintf(stdout, "Recorded false-positive report for %s at %s\n", reflexID, *at)
	return 0
}
