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
	"github.com/angelnicolasc/flinch/internal/redact"
)

// TurnCheckpoint captures the minimal working-tree baseline at turn start and tracks
// continuation counts across logical turn retries (§8.1, §9).
type TurnCheckpoint struct {
	Schema               int               `json:"schema"`
	SessionID            string            `json:"session_id"`
	WorktreeID           string            `json:"worktree_id"`
	TurnID               string            `json:"turn_id"`
	HeadSHA              string            `json:"head_sha"`
	SnapshotSHA          string            `json:"snapshot_sha,omitempty"`
	FileHashes           map[string]string `json:"file_hashes"`
	Prompt               string            `json:"prompt,omitempty"`
	PromptID             string            `json:"prompt_id,omitempty"`
	StartedAt            time.Time         `json:"started_at"`
	TotalContinuations   int               `json:"total_continuations"`
	FindingContinuations map[string]int    `json:"finding_continuations,omitempty"`
}

func checkpointPath(stateDir, sessionID, worktreeID string) string {
	h := sha256.Sum256([]byte(sessionID + "|" + worktreeID))
	return filepath.Join(stateDir, "turns", hex.EncodeToString(h[:12])+".json")
}

// CaptureCheckpoint records the current working-tree file hashes, HEAD SHA, and optional
// redacted prompt for a session + worktree without mutating Git state.
func CaptureCheckpoint(ctx context.Context, repoRoot, sessionID, worktreeID, prompt, promptID string, captureSnapshot bool) (TurnCheckpoint, error) {
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
	cp := TurnCheckpoint{
		Schema:               1,
		SessionID:            sessionID,
		WorktreeID:           worktreeID,
		TurnID:               "turn-" + hex.EncodeToString(th[:6]),
		HeadSHA:              headSHA,
		SnapshotSHA:          snapSHA,
		FileHashes:           hashes,
		Prompt:               redact.Prompt(prompt, 500),
		PromptID:             promptID,
		StartedAt:            now,
		TotalContinuations:   0,
		FindingContinuations: make(map[string]int),
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
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
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
