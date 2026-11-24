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
)

func runHook(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	started := time.Now()
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // Hooks fail open silently (P7).
	hostStr := fs.String("host", "", "host adapter: claude | codex | antigravity")
	eventStr := fs.String("event", "", "hook event name")
	repoDir := fs.String("repo", "", "optional repository directory override")
	if err := fs.Parse(args); err != nil {
		return 0
	}

	ctx, cancel := hook.WithBudget(ctx, *eventStr, started)
	defer cancel()
	host := hook.Host(strings.ToLower(strings.TrimSpace(*hostStr)))
	result := make(chan hook.Response, 1)
	go func(r *os.File) {
		if deadline, ok := ctx.Deadline(); ok {
			if err := r.SetReadDeadline(deadline); err == nil {
				defer r.SetReadDeadline(time.Time{})
			}
		}
		data, err := io.ReadAll(r)
		if err != nil {
			data = nil
		}
		result <- hook.Handle(ctx, host, *eventStr, data, *repoDir)
	}(os.Stdin)
	var resp hook.Response
	select {
	case resp = <-result:
	case <-ctx.Done():
		// Filesystems and some host pipes do not support cancellation of an
		// in-flight operation. The CLI must still release the host on time.
		resp = hook.AllowResponse(host)
	}
	if len(resp.Stdout) > 0 {
		if file, ok := stdout.(*os.File); ok {
			deadline := hook.ResponseDeadline(ctx)
			if err := file.SetWriteDeadline(deadline); err != nil {
				// Some inherited Windows handles do not support I/O deadlines.
				// Let the CLI exit on time even if the host stops reading its pipe.
				done := make(chan struct{})
				go func() {
					_, _ = file.Write(resp.Stdout)
					close(done)
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
	}
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
