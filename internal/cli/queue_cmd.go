package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/angelnicolasc/flinch/internal/check"
	"github.com/angelnicolasc/flinch/internal/config"
	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/queue"
)

func runCheckpoint(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("checkpoint", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repoDir := fs.String("repo", "", "repository directory")
	sessionID := fs.String("session", "default", "agent session identifier")
	prompt := fs.String("prompt", "", "human prompt text (hashed only by default; text stored only if policy.store_prompt_text = true)")
	promptID := fs.String("prompt-id", "", "prompt identifier")
	snapshot := fs.Bool("snapshot", true, "capture isolated working-tree tree snapshot")
	asJSON := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	root, err := resolveRepoRoot(ctx, *repoDir)
	if err != nil {
		fmt.Fprintf(stderr, "flinch checkpoint: %v\n", err)
		return 2
	}
	cfg, _, err := config.LoadOrDefault(root)
	if err != nil {
		fmt.Fprintf(stderr, "flinch checkpoint: %v\n", err)
		return 2
	}
	cp, err := check.CaptureCheckpointWithPolicy(
		ctx, root, *sessionID, root, *prompt, *promptID, *snapshot,
		cfg.Policy.StorePromptText, cfg.Policy.RetentionDays,
	)
	if err != nil {
		fmt.Fprintf(stderr, "flinch checkpoint: %v\n", err)
		return 2
	}
	if *asJSON {
		return writeJSON(stdout, cp)
	}
	fmt.Fprintf(stdout, "checkpoint %s captured (head=%s, dirty_files=%d)\n", cp.TurnID, shortSHA(cp.HeadSHA), len(cp.FileHashes))
	return 0
}

func runSignal(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("signal", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repoDir := fs.String("repo", "", "repository directory")
	repoName := fs.String("repo-name", "", "repository identifier (defaults to repo directory name)")
	source := fs.String("source", "", "signal source: correction | fix | ci | review | note")
	eventID := fs.String("event-id", "", "stable per-source event identifier for deduplication")
	provenance := fs.String("provenance", string(queue.ProvenanceHuman), "provenance: human | synthetic")
	surface := fs.String("surface", "", "capture surface: claude-code | codex | opencode | git | ci")
	sessionID := fs.String("session", "", "session identifier")
	branch := fs.String("branch", "", "branch name")
	prNum := fs.Int("pr", 0, "pull request number")
	brokenRef := fs.String("broken", "", "broken Git ref")
	fixedRef := fs.String("fixed", "", "fixed Git ref")
	pathsCSV := fs.String("paths", "", "comma-separated affected repo paths")
	summary := fs.String("summary", "", "redacted signal summary")
	escapeOf := fs.String("escape-of", "", "check ID escaped by this incident (boosts priority)")
	cursor := fs.String("cursor", "", "source cursor watermark saved strictly after signal persistence")
	asJSON := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	root, err := resolveRepoRoot(ctx, *repoDir)
	if err != nil {
		fmt.Fprintf(stderr, "flinch signal: %v\n", err)
		return 2
	}
	stateDir, err := gitutil.FlinchStateDir(ctx, root)
	if err != nil {
		fmt.Fprintf(stderr, "flinch signal: %v\n", err)
		return 2
	}
	name := *repoName
	if name == "" {
		name = filepath.Base(root)
	}
	var paths []string
	if strings.TrimSpace(*pathsCSV) != "" {
		for _, p := range strings.Split(*pathsCSV, ",") {
			if trimmed := strings.TrimSpace(p); trimmed != "" {
				paths = append(paths, trimmed)
			}
		}
	}

	store := queue.NewStore(stateDir)
	res, err := store.IngestSignal(queue.Signal{
		Repo:             name,
		Source:           *source,
		EventID:          *eventID,
		Provenance:       queue.Provenance(*provenance),
		Surface:          *surface,
		SessionID:        *sessionID,
		WorktreeID:       root,
		Branch:           *branch,
		PRNumber:         *prNum,
		BrokenRef:        *brokenRef,
		FixedRef:         *fixedRef,
		Paths:            paths,
		Summary:          *summary,
		EscapeOfReflexID: *escapeOf,
	}, *cursor)
	if err != nil {
		fmt.Fprintf(stderr, "flinch signal: %v\n", err)
		return 2
	}
	if *asJSON {
		return writeJSON(stdout, res)
	}
	if res.IgnoredSynthetic {
		fmt.Fprintf(stdout, "ignored synthetic correction signal %s\n", res.Signal.ID)
		return 0
	}
	if res.Duplicate {
		fmt.Fprintf(stdout, "duplicate signal %s (episode=%s)\n", res.Signal.ID, res.Episode.ID)
		return 0
	}
	fmt.Fprintf(stdout, "queued signal %s -> episode %s (priority=%d, evidence=%s)\n",
		res.Signal.ID, res.Episode.ID, res.Episode.Priority, res.Episode.EvidenceState)
	return 0
}

func runQueue(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "flinch queue: subcommand required: list | claim | release | suspend | resume | deliver")
		return 2
	}
	sub := args[0]
	rest := args[1:]

	fs := flag.NewFlagSet("queue "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	repoDir := fs.String("repo", "", "repository directory")
	workerID := fs.String("worker", "local-worker", "worker identifier")
	ttlStr := fs.String("ttl", "5m", "lease TTL")
	episodeID := fs.String("episode", "", "episode identifier")
	cause := fs.String("cause", "", "suspension cause")
	nextTrigger := fs.String("next-trigger", "", "trigger that resumes a suspended episode")
	target := fs.String("target", "", "delivery target (for example pr:27 or draft-pr:main)")
	observedSHA := fs.String("observed-sha", "", "base SHA observed before writing")
	resultSHA := fs.String("result-sha", "", "resulting commit SHA")
	prURL := fs.String("pr-url", "", "pull request URL")
	asJSON := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(rest); err != nil {
		return 2
	}

	root, err := resolveRepoRoot(ctx, *repoDir)
	if err != nil {
		fmt.Fprintf(stderr, "flinch queue: %v\n", err)
		return 2
	}
	stateDir, err := gitutil.FlinchStateDir(ctx, root)
	if err != nil {
		fmt.Fprintf(stderr, "flinch queue: %v\n", err)
		return 2
	}
	store := queue.NewStore(stateDir)

	switch sub {
	case "list":
		eps, err := store.ListEpisodes()
		if err != nil {
			fmt.Fprintf(stderr, "flinch queue list: %v\n", err)
			return 2
		}
		if *asJSON {
			return writeJSON(stdout, eps)
		}
		if len(eps) == 0 {
			fmt.Fprintln(stdout, "queue is empty")
			return 0
		}
		for _, ep := range eps {
			fmt.Fprintf(stdout, "%s  priority=%d  state=%s  evidence=%s  delivery=%s  pr=%d  %s\n",
				ep.ID, ep.Priority, ep.LifecycleState, ep.EvidenceState, ep.DeliveryState, ep.PRNumber, ep.Summary)
		}
		return 0
	case "claim":
		ttl, err := time.ParseDuration(*ttlStr)
		if err != nil {
			fmt.Fprintf(stderr, "flinch queue claim: invalid --ttl: %v\n", err)
			return 2
		}
		ep, ok, err := store.ClaimNext(*workerID, ttl, time.Now().UTC())
		if err != nil {
			fmt.Fprintf(stderr, "flinch queue claim: %v\n", err)
			return 2
		}
		if *asJSON {
			return writeJSON(stdout, map[string]any{"claimed": ok, "episode": ep})
		}
		if !ok {
			fmt.Fprintln(stdout, "no claimable episodes")
			return 0
		}
		fmt.Fprintf(stdout, "claimed %s for worker %s until %s\n", ep.ID, *workerID, ep.Lease.ExpiresAt.Format(time.RFC3339))
		return 0
	case "release":
		if err := store.ReleaseLease(*episodeID, *workerID); err != nil {
			fmt.Fprintf(stderr, "flinch queue release: %v\n", err)
			return 2
		}
		fmt.Fprintf(stdout, "released lease on %s\n", *episodeID)
		return 0
	case "suspend":
		if err := store.Suspend(*episodeID, *cause, *nextTrigger); err != nil {
			fmt.Fprintf(stderr, "flinch queue suspend: %v\n", err)
			return 2
		}
		fmt.Fprintf(stdout, "suspended %s (cause=%s)\n", *episodeID, *cause)
		return 0
	case "resume":
		if err := store.Resume(*episodeID); err != nil {
			fmt.Fprintf(stderr, "flinch queue resume: %v\n", err)
			return 2
		}
		fmt.Fprintf(stdout, "resumed %s\n", *episodeID)
		return 0
	case "deliver":
		eps, err := store.ListEpisodes()
		if err != nil {
			fmt.Fprintf(stderr, "flinch queue deliver: %v\n", err)
			return 2
		}
		var found *queue.Episode
		for i := range eps {
			if eps[i].ID == *episodeID {
				found = &eps[i]
				break
			}
		}
		if found == nil {
			fmt.Fprintf(stderr, "flinch queue deliver: episode %s not found\n", *episodeID)
			return 2
		}
		key := queue.DeliveryKey(found.Repo, found.ID, found.CandidateVersion, *target)
		dup, err := store.RecordDelivery(queue.DeliveryRecord{
			Key:         key,
			EpisodeID:   found.ID,
			Target:      *target,
			ObservedSHA: *observedSHA,
			ResultSHA:   *resultSHA,
			PRURL:       *prURL,
		})
		if err != nil {
			fmt.Fprintf(stderr, "flinch queue deliver: %v\n", err)
			return 2
		}
		if *asJSON {
			return writeJSON(stdout, map[string]any{"key": key, "already_delivered": dup})
		}
		if dup {
			fmt.Fprintf(stdout, "already delivered (%s)\n", key)
		} else {
			fmt.Fprintf(stdout, "recorded delivery (%s)\n", key)
		}
		return 0
	default:
		fmt.Fprintf(stderr, "flinch queue: unknown subcommand %q\n", sub)
		return 2
	}
}
