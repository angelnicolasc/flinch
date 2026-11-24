package check

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/angelnicolasc/flinch/internal/gitutil"
)

// CleanupReport summarizes verifiable privacy/retention pruning under <stateDir> (D11, P10).
type CleanupReport struct {
	RetentionDays      int       `json:"retention_days"`
	Cutoff             time.Time `json:"cutoff"`
	RemovedCheckpoints int       `json:"removed_checkpoints"`
	RemovedProofTrees  int       `json:"removed_proof_trees"`
	PrunedLogEvents    int       `json:"pruned_log_events"`
}

// CleanupExpired enforces policy.retention_days across local checkpoints, proof trees, and JSONL logs (D11, P10).
func CleanupExpired(stateDir string, retentionDays int, now time.Time) (CleanupReport, error) {
	if retentionDays < 1 {
		retentionDays = 30
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	cutoff := now.Add(-time.Duration(retentionDays) * 24 * time.Hour)
	rep := CleanupReport{
		RetentionDays: retentionDays,
		Cutoff:        cutoff,
	}

	// 1. Prune expired turn checkpoints under <stateDir>/turns/.
	turnsDir := filepath.Join(stateDir, "turns")
	if entries, err := os.ReadDir(turnsDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			p := filepath.Join(turnsDir, e.Name())
			expired := false
			if data, readErr := os.ReadFile(p); readErr == nil {
				var cp TurnCheckpoint
				if json.Unmarshal(data, &cp) == nil && !cp.StartedAt.IsZero() {
					expired = cp.StartedAt.Before(cutoff)
				} else if info, stErr := e.Info(); stErr == nil {
					expired = info.ModTime().Before(cutoff)
				}
			}
			if expired {
				if os.Remove(p) == nil {
					rep.RemovedCheckpoints++
				}
			}
		}
	}

	// 2. Reclaim expired proof trees after checking their leases.
	if entries, err := os.ReadDir(filepath.Join(stateDir, "proof-trees")); err == nil {
		for _, e := range entries {
			if info, err := e.Info(); err == nil && e.IsDir() && info.ModTime().Before(cutoff) {
				if removed, _ := gitutil.PruneProofTree(stateDir, e.Name()); removed {
					rep.RemovedProofTrees++
				}
			}
		}
	}

	// 3. Prune expired lines from <stateDir>/log/events.jsonl atomically.
	logPath := filepath.Join(stateDir, "log", "events.jsonl")
	pruned, err := pruneJSONLLog(logPath, cutoff)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return rep, err
	}
	rep.PrunedLogEvents = pruned
	return rep, nil
}

func pruneJSONLLog(path string, cutoff time.Time) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	var kept [][]byte
	pruned := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		raw := append([]byte(nil), scanner.Bytes()...)
		if len(raw) == 0 {
			continue
		}
		var tsProbe struct {
			Timestamp time.Time `json:"timestamp"`
		}
		if json.Unmarshal(raw, &tsProbe) == nil && !tsProbe.Timestamp.IsZero() && tsProbe.Timestamp.Before(cutoff) {
			pruned++
			continue
		}
		kept = append(kept, raw)
	}
	scanErr := scanner.Err()
	_ = f.Close()
	if scanErr != nil {
		return 0, scanErr
	}
	if pruned == 0 {
		return 0, nil
	}

	var out []byte
	for _, line := range kept {
		out = append(out, line...)
		out = append(out, '\n')
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	return pruned, nil
}
