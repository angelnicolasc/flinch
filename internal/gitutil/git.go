package gitutil

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/angelnicolasc/flinch/internal/redact"
)

// LineRange represents a 1-based inclusive range of lines [Start, End].
type LineRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// Overlaps reports whether two nonempty ranges share a line.
func (r LineRange) Overlaps(other LineRange) bool {
	return r.Start > 0 && other.Start > 0 && r.Start <= other.End && other.Start <= r.End
}

// DiffHunk pairs the changed lines of one zero-context hunk. A missing side has a zero range.
type DiffHunk struct {
	Before LineRange
	After  LineRange
}

// FileChange captures a diff entry between two Git revisions, including renames and insertion points.
type FileChange struct {
	Path         string      `json:"path"`
	OldPath      string      `json:"old_path,omitempty"`
	Status       string      `json:"status"` // A, M, D, R
	RemovedLines []LineRange `json:"removed_lines,omitempty"`
	AddedLines   []LineRange `json:"added_lines,omitempty"`
	InsertionsAt []int       `json:"insertions_at,omitempty"`
	Hunk         string      `json:"hunk,omitempty"`
	Hunks        []DiffHunk  `json:"-"`
}

// Run executes git with args in dir and returns stdout, stderr, and any error.
func Run(ctx context.Context, dir string, env []string, args ...string) (string, string, error) {
	return runWithInput(ctx, dir, env, nil, args...)
}

func runWithInput(ctx context.Context, dir string, env []string, input io.Reader, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Stdin = input
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return stdout.String(), stderr.String(), fmt.Errorf("git %s: %w (stderr: %s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), stderr.String(), nil
}

// RepoRoot returns the absolute path to the top-level working tree containing dir.
func RepoRoot(ctx context.Context, dir string) (string, error) {
	out, _, err := Run(ctx, dir, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return filepath.Clean(strings.TrimSpace(out)), nil
}

// CommonDir returns the absolute path to the shared .git common directory for dir.
func CommonDir(ctx context.Context, dir string) (string, error) {
	out, _, err := Run(ctx, dir, nil, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	raw := strings.TrimSpace(out)
	if !filepath.IsAbs(raw) {
		raw = filepath.Join(dir, raw)
	}
	return filepath.Clean(raw), nil
}

// FlinchStateDir returns the local per-clone state directory under <git-common-dir>/flinch.
func FlinchStateDir(ctx context.Context, repoRoot string) (string, error) {
	common, err := CommonDir(ctx, repoRoot)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(common, "flinch")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create flinch state dir: %w", err)
	}
	return dir, nil
}

// ResolveRef resolves a Git reference (commit, tag, branch, or pure tree OID) to a full 40-char OID.
func ResolveRef(ctx context.Context, repoRoot, ref string) (string, error) {
	if out, _, err := Run(ctx, repoRoot, nil, "rev-parse", "--verify", ref+"^{commit}"); err == nil {
		return strings.TrimSpace(out), nil
	}
	out, _, err := Run(ctx, repoRoot, nil, "rev-parse", "--verify", ref+"^{tree}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// HeadSHA returns the current HEAD commit SHA, or empty string if the repository has no commits yet.
func HeadSHA(ctx context.Context, repoRoot string) (string, bool, error) {
	out, _, err := Run(ctx, repoRoot, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		// Check whether repository simply has no commits yet.
		if _, _, statusErr := Run(ctx, repoRoot, nil, "rev-parse", "--is-inside-work-tree"); statusErr == nil {
			return "", false, nil
		}
		return "", false, err
	}
	return strings.TrimSpace(out), true, nil
}

// FileAtRef reads the bytes of relPath at ref. Returns os.ErrNotExist if absent at that ref.
func FileAtRef(ctx context.Context, repoRoot, ref, relPath string) ([]byte, error) {
	spec := fmt.Sprintf("%s:%s", ref, filepath.ToSlash(relPath))
	cmd := exec.CommandContext(ctx, "git", "show", spec)
	cmd.Dir = repoRoot
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := stderr.String()
		if strings.Contains(msg, "does not exist") || strings.Contains(msg, "exists on disk, but not in") || strings.Contains(msg, "Path") {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("git show %s: %w (%s)", spec, err, strings.TrimSpace(msg))
	}
	return stdout.Bytes(), nil
}

// ListTreeFiles returns the files directly inside dirPath at ref, with repo-relative paths.
func ListTreeFiles(ctx context.Context, repoRoot, ref, dirPath string) ([]string, error) {
	dir := strings.TrimSuffix(filepath.ToSlash(dirPath), "/") + "/"
	out, _, err := Run(ctx, repoRoot, nil, "ls-tree", "-z", ref, "--", dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for entry := range strings.SplitSeq(out, "\x00") {
		header, path, ok := strings.Cut(entry, "\t")
		if ok && strings.Contains(header, " blob ") {
			files = append(files, path)
		}
	}
	return files, nil
}

// WorkingTreeStatus returns a map of repo-relative slash paths to content SHA-256 hashes
// for all modified, staged, deleted ("DELETED"), or untracked non-ignored files.
func WorkingTreeStatus(ctx context.Context, repoRoot string) (map[string]string, error) {
	out, _, err := Run(ctx, repoRoot, nil, "status", "--porcelain=v1", "-z", "-uall")
	if err != nil {
		return nil, err
	}
	result := make(map[string]string)
	entries := strings.Split(out, "\x00")
	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		if len(entry) < 4 {
			continue
		}
		xy := entry[:2]
		path := filepath.ToSlash(entry[3:])
		// Rename/copy entries are followed by the original path NUL record.
		if xy[0] == 'R' || xy[0] == 'C' || xy[1] == 'R' || xy[1] == 'C' {
			if i+1 < len(entries) {
				oldPath := filepath.ToSlash(entries[i+1])
				result[oldPath] = "DELETED"
				i++
			}
		}
		abs := filepath.Join(repoRoot, filepath.FromSlash(path))
		data, readErr := os.ReadFile(abs)
		if readErr != nil {
			if errors.Is(readErr, os.ErrNotExist) {
				result[path] = "DELETED"
				continue
			}
			return nil, fmt.Errorf("hash working file %s: %w", path, readErr)
		}
		sum := sha256.Sum256(data)
		result[path] = hex.EncodeToString(sum[:])
	}
	return result, nil
}

var hunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// DiffFileChanges returns detailed file changes (including renames, line ranges, and insertion points)
// between baseRef and headRef.
func DiffFileChanges(ctx context.Context, repoRoot, baseRef, headRef string) ([]FileChange, error) {
	nameStatusOut, _, err := Run(ctx, repoRoot, nil, "diff", "--find-renames", "--name-status", "-z", baseRef, headRef)
	if err != nil {
		return nil, err
	}
	var changes []FileChange
	byPath := make(map[string]int)

	tokens := strings.Split(nameStatusOut, "\x00")
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if tok == "" {
			continue
		}
		statusCode := tok[:1]
		switch statusCode {
		case "R", "C":
			if i+2 >= len(tokens) {
				break
			}
			oldPath := filepath.ToSlash(tokens[i+1])
			newPath := filepath.ToSlash(tokens[i+2])
			i += 2
			idx := len(changes)
			changes = append(changes, FileChange{
				Path:    newPath,
				OldPath: oldPath,
				Status:  "R",
			})
			byPath[newPath] = idx
		default:
			if i+1 >= len(tokens) {
				break
			}
			p := filepath.ToSlash(tokens[i+1])
			i++
			idx := len(changes)
			changes = append(changes, FileChange{
				Path:   p,
				Status: statusCode,
			})
			byPath[p] = idx
		}
	}

	diffOut, _, err := Run(ctx, repoRoot, nil, "diff", "--find-renames", "--unified=0", "--no-color", baseRef, headRef)
	if err != nil {
		return nil, err
	}

	var currentIdx = -1
	var currentHunk strings.Builder
	flushHunk := func() {
		if currentIdx >= 0 && currentHunk.Len() > 0 {
			changes[currentIdx].Hunk = redact.TruncateLines(redact.Text(strings.TrimRight(currentHunk.String(), "\n")), 60)
			currentHunk.Reset()
		}
	}

	for _, line := range strings.Split(diffOut, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			flushHunk()
			currentIdx = -1
			continue
		}
		if strings.HasPrefix(line, "+++ b/") {
			p := filepath.ToSlash(strings.TrimPrefix(line, "+++ b/"))
			if idx, ok := byPath[p]; ok {
				currentIdx = idx
			}
			continue
		}
		if strings.HasPrefix(line, "--- a/") && currentIdx == -1 {
			p := filepath.ToSlash(strings.TrimPrefix(line, "--- a/"))
			if idx, ok := byPath[p]; ok && changes[idx].Status == "D" {
				currentIdx = idx
			}
			continue
		}
		if currentIdx < 0 {
			continue
		}
		if m := hunkHeaderRe.FindStringSubmatch(line); m != nil {
			oldStart, _ := strconv.Atoi(m[1])
			oldCount := 1
			if m[2] != "" {
				oldCount, _ = strconv.Atoi(m[2])
			}
			newStart, _ := strconv.Atoi(m[3])
			newCount := 1
			if m[4] != "" {
				newCount, _ = strconv.Atoi(m[4])
			}
			var hunk DiffHunk
			if oldCount > 0 {
				hunk.Before = LineRange{Start: oldStart, End: oldStart + oldCount - 1}
				changes[currentIdx].RemovedLines = append(changes[currentIdx].RemovedLines, hunk.Before)
			} else if newCount > 0 {
				// Pure insertion in head: anchor at oldStart (broken-tree coordinate).
				changes[currentIdx].InsertionsAt = append(changes[currentIdx].InsertionsAt, oldStart)
			}
			if newCount > 0 {
				hunk.After = LineRange{Start: newStart, End: newStart + newCount - 1}
				changes[currentIdx].AddedLines = append(changes[currentIdx].AddedLines, hunk.After)
			}
			changes[currentIdx].Hunks = append(changes[currentIdx].Hunks, hunk)
			currentHunk.WriteString(line)
			currentHunk.WriteByte('\n')
			continue
		}
		if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
			if !strings.HasPrefix(line, "+++") && !strings.HasPrefix(line, "---") {
				currentHunk.WriteString(line)
				currentHunk.WriteByte('\n')
			}
		}
	}
	flushHunk()

	return changes, nil
}
