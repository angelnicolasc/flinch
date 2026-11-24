package check

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/localstate"
	"github.com/angelnicolasc/flinch/internal/redact"
)

// TurnCheckpoint captures the minimal working-tree baseline at turn start and tracks
// continuation and test budgets across logical turn retries (D5, D11, P6, P10).
// By default no prompt text is persisted; only identifiers and PromptHash are stored.
type TurnCheckpoint struct {
	Schema               int               `json:"schema"`
	SessionID            string            `json:"session_id"`
	WorktreeID           string            `json:"worktree_id"`
	TurnID               string            `json:"turn_id"`
	HeadSHA              string            `json:"head_sha"`
	SnapshotSHA          string            `json:"snapshot_sha,omitempty"`
	FileHashes           map[string]string `json:"file_hashes"`
	Prompt               string            `json:"prompt,omitempty"`
	PromptHash           string            `json:"prompt_hash,omitempty"`
	PromptID             string            `json:"prompt_id,omitempty"`
	StartedAt            time.Time         `json:"started_at"`
	TotalContinuations   int               `json:"total_continuations"`
	FindingContinuations map[string]int    `json:"finding_continuations,omitempty"`
	TestBudgetUsedMS     int64             `json:"test_budget_used_ms,omitempty"`
	PreviousStartSnap    string            `json:"previous_start_snap,omitempty"`
	PreviousEndSnap      string            `json:"previous_end_snap,omitempty"`
	AuthoringStartedAt   *time.Time        `json:"authoring_started_at,omitempty"`
	AuthoringAttempted   bool              `json:"authoring_attempted,omitempty"`
	LineRepairUsed       bool              `json:"line_repair_used,omitempty"`
	SkipReason           string            `json:"skip_reason,omitempty"`
	// These receipts belong to this human turn. They contain digests only.
	HostTurnID         string          `json:"host_turn_id,omitempty"`
	Closed             bool            `json:"closed,omitempty"`
	StopReceipts       map[string]bool `json:"stop_receipts,omitempty"`
	ContinuationHash   string          `json:"continuation_hash,omitempty"`
	ContinuationTurnID string          `json:"continuation_turn_id,omitempty"`
}

func checkpointPath(stateDir, sessionID, worktreeID string) string {
	h := sha256.Sum256([]byte(sessionID + "|" + worktreeID))
	return filepath.Join(stateDir, "turns", hex.EncodeToString(h[:12])+".json")
}

// LockTurn serializes hook deliveries that update the same checkpoint in
// separate CLI processes. A small fixed set of stripes bounds lock-file growth.
func LockTurn(ctx context.Context, stateDir, sessionID, worktreeID string) (func(), error) {
	hash := sha256.Sum256([]byte(sessionID + "|" + worktreeID))
	dir := filepath.Join(stateDir, "locks", "turns")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, fmt.Sprintf("%02x.lock", hash[0]))
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		release, acquired, err := localstate.TryLock(path)
		if err != nil || acquired {
			return release, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// HashPrompt returns a deterministic SHA-256 digest of trimmed prompt text without storing text (D11).
func HashPrompt(prompt string) string {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(trimmed))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// CaptureCheckpoint records the current working-tree file hashes, HEAD SHA, and prompt digest
// for a session + worktree without mutating Git state or persisting prompt text by default (D11, P10).
func CaptureCheckpoint(ctx context.Context, repoRoot, sessionID, worktreeID, prompt, promptID string, captureSnapshot bool) (TurnCheckpoint, error) {
	return CaptureCheckpointWithPolicy(ctx, repoRoot, sessionID, worktreeID, prompt, promptID, captureSnapshot, false, 0)
}

// CaptureCheckpointWithPolicy captures a turn checkpoint honoring storePromptText and retentionDays (D11).
func CaptureCheckpointWithPolicy(
	ctx context.Context,
	repoRoot, sessionID, worktreeID, prompt, promptID string,
	captureSnapshot, storePromptText bool,
	retentionDays int,
	hostTurnID ...string,
) (TurnCheckpoint, error) {
	if sessionID == "" {
		sessionID = "default"
	}
	if worktreeID == "" {
		worktreeID = filepath.ToSlash(repoRoot)
	}
	stateDir, err := gitutil.FlinchStateDir(ctx, repoRoot)
	if err != nil {
		return TurnCheckpoint{}, err
	}
	if retentionDays > 0 {
		_, _ = CleanupExpired(stateDir, retentionDays, time.Now().UTC())
	}

	// Preserve previous turn's edit snapshots for correction nomination (D2).
	var prevStart, prevEnd string
	if prev, ok, _ := LoadCheckpoint(stateDir, sessionID, worktreeID); ok {
		prevStart = prev.PreviousStartSnap
		prevEnd = prev.PreviousEndSnap
	}

	headSHA, _, err := gitutil.HeadSHA(ctx, repoRoot)
	if err != nil {
		return TurnCheckpoint{}, err
	}
	hashes, err := gitutil.WorkingTreeStatus(ctx, repoRoot)
	if err != nil {
		return TurnCheckpoint{}, err
	}
	var snapSHA string
	if captureSnapshot {
		snapSHA, _ = gitutil.CaptureWorkingTreeSnapshot(ctx, repoRoot, "flinch turn checkpoint")
	}

	now := time.Now().UTC()
	th := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d", sessionID, worktreeID, now.UnixNano())))
	storedPrompt := ""
	if storePromptText {
		storedPrompt = redact.Prompt(prompt, 500)
	}
	cp := TurnCheckpoint{
		Schema:               1,
		SessionID:            sessionID,
		WorktreeID:           worktreeID,
		TurnID:               "turn-" + hex.EncodeToString(th[:6]),
		HeadSHA:              headSHA,
		SnapshotSHA:          snapSHA,
		FileHashes:           hashes,
		Prompt:               storedPrompt,
		PromptHash:           HashPrompt(prompt),
		PromptID:             promptID,
		StartedAt:            now,
		TotalContinuations:   0,
		FindingContinuations: make(map[string]int),
		PreviousStartSnap:    prevStart,
		PreviousEndSnap:      prevEnd,
	}
	if len(hostTurnID) > 0 {
		cp.HostTurnID = hostTurnID[0]
	}
	if err := SaveCheckpoint(stateDir, cp); err != nil {
		return TurnCheckpoint{}, err
	}
	return cp, nil
}

// SaveCheckpoint writes cp atomically under <stateDir>/turns/.
func SaveCheckpoint(stateDir string, cp TurnCheckpoint) error {
	path := checkpointPath(stateDir, cp.SessionID, cp.WorktreeID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".checkpoint-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// LoadCheckpoint reads the checkpoint for (sessionID, worktreeID). Returns (zero, false, nil) if absent.
func LoadCheckpoint(stateDir, sessionID, worktreeID string) (TurnCheckpoint, bool, error) {
	if sessionID == "" {
		sessionID = "default"
	}
	path := checkpointPath(stateDir, sessionID, worktreeID)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return TurnCheckpoint{}, false, nil
		}
		return TurnCheckpoint{}, false, err
	}
	var cp TurnCheckpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return TurnCheckpoint{}, false, err
	}
	if cp.FindingContinuations == nil {
		cp.FindingContinuations = make(map[string]int)
	}
	return cp, true, nil
}

// ChangedFilesSinceCheckpoint returns repo-relative paths whose content hash changed since cp.
func ChangedFilesSinceCheckpoint(cp TurnCheckpoint, current map[string]string) []string {
	seen := make(map[string]bool)
	var changed []string
	for p, h := range current {
		if prev, ok := cp.FileHashes[p]; !ok || prev != h {
			if !seen[p] {
				seen[p] = true
				changed = append(changed, p)
			}
		}
	}
	for p := range cp.FileHashes {
		if _, ok := current[p]; !ok && !seen[p] {
			seen[p] = true
			changed = append(changed, p)
		}
	}
	return changed
}

// HasSuppressionDirective checks whether a newly added source line introduces a suppression pragma.
func HasSuppressionDirective(line string) (string, bool) {
	markers := []string{
		"//nolint",
		"// nolint",
		"eslint-disable",
		"# noqa",
		"#noqa",
		"ast-grep-ignore",
		"flinch:ignore",
		"flinch-disable",
	}
	low := strings.ToLower(line)
	for _, m := range markers {
		if strings.Contains(low, m) {
			return m, true
		}
	}
	return "", false
}
