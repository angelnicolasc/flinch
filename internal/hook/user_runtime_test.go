package hook

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/angelnicolasc/flinch/internal/testutil"
)

func setupHookProfile(t *testing.T, repo string) string {
	t.Helper()
	return testutil.HookProfile(t, repo)
}

func hookFootprint(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			result[rel] = "/"
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[rel] = fmt.Sprintf("%x", sha256.Sum256(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// footprintChanges lists the paths that were added, removed or rewritten, so a
// failure names what changed instead of only reporting that something did.
func footprintChanges(before, after map[string]string) []string {
	var changed []string
	for path, sum := range before {
		if other, ok := after[path]; !ok {
			changed = append(changed, "removed "+path)
		} else if other != sum {
			changed = append(changed, "rewritten "+path)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			changed = append(changed, "added "+path)
		}
	}
	sort.Strings(changed)
	return changed
}

func TestInactiveHookLeavesCheckoutAndGitObjectsUntouched(t *testing.T) {
	for _, mode := range []string{"outside", "revoked", "unknown", "ambiguous", "paused", "session-off", "missing-consent", "spoofed-repo"} {
		t.Run(mode, func(t *testing.T) {
			repo := initHookTestRepo(t)
			home := setupHookProfile(t, repo)
			cwd := repo
			var paths []string
			explicit := ""
			switch mode {
			case "outside":
				cwd = t.TempDir()
			case "revoked":
				if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unknown":
				if err := os.Remove(filepath.Join(home, ".claude.json")); err != nil {
					t.Fatal(err)
				}
			case "ambiguous":
				paths = []string{repo, t.TempDir()}
			case "paused":
				dir := filepath.Join(repo, ".git", "flinch")
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "pause.json"), []byte(`{"paused":true}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "session-off":
				t.Setenv("FLINCH", "off")
			case "missing-consent":
				err := filepath.WalkDir(home, func(p string, e fs.DirEntry, err error) error {
					if err == nil && e.Name() == "setup.json" {
						return os.Remove(p)
					}
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
			case "spoofed-repo":
				cwd = t.TempDir()
				explicit = repo
			}
			before := hookFootprint(t, repo)
			outside := hookFootprint(t, cwd)
			t.Setenv("FLINCH_RECORD", "1")
			data, _ := json.Marshal(map[string]any{"cwd": cwd, "workspacePaths": paths, "session_id": "trial", "prompt": "ordinary edit"})
			got := Handle(context.Background(), HostClaude, "UserPromptSubmit", data, explicit)
			if len(got.Stdout) != 0 {
				t.Fatalf("inactive hook wrote host output: %s", got.Stdout)
			}
			if changed := footprintChanges(before, hookFootprint(t, repo)); len(changed) > 0 {
				t.Fatalf("inactive hook changed the checkout or its Git directory: %v", changed)
			}
			if changed := footprintChanges(outside, hookFootprint(t, cwd)); len(changed) > 0 {
				t.Fatalf("inactive hook changed the event directory: %v", changed)
			}
		})
	}
}

func TestActivationNoticeAndPrivateRecording(t *testing.T) {
	repo := initHookTestRepo(t)
	setupHookProfile(t, repo)
	t.Setenv("FLINCH_RECORD", "1")
	sentinel := "PRIVATE-DATA-937452"
	for i := 0; i < 2; i++ {
		data, _ := json.Marshal(map[string]any{"session_id": sentinel + "session", "cwd": repo, "prompt": sentinel + "prompt", "prompt_id": sentinel + "promptID", "turn_id": sentinel + "turn", "event_id": sentinel + "event", "origin": sentinel + "origin", "error": sentinel + "error", "transcript_path": sentinel + "path", "last_assistant_message": sentinel + "response"})
		got := Handle(context.Background(), HostClaude, "UserPromptSubmit", data, "")
		if i == 0 {
			var out map[string]string
			if json.Unmarshal(got.Stdout, &out) != nil || len(out) != 1 || out["systemMessage"] != "flinch: active in this repo · flinch pause to turn it off" {
				t.Fatalf("first passive notice: %s", got.Stdout)
			}
		} else if len(got.Stdout) != 0 {
			t.Fatalf("duplicate notice: %s", got.Stdout)
		}
	}
	files, _ := filepath.Glob(filepath.Join(repo, ".git", "flinch", "recordings", "*.json"))
	if len(files) != 2 {
		t.Fatalf("recorded %d deliveries", len(files))
	}
	for _, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !json.Valid(b) || strings.Contains(string(b), sentinel) || strings.Contains(string(b), filepath.Base(repo)) && len(filepath.Base(repo)) > 5 || strings.Contains(string(b), repo) {
			t.Fatalf("private data or invalid record in %s", path)
		}
	}
	t.Setenv("FLINCH_RECORD", "")
	regressionHook(t, repo, HostCodex, "UserPromptSubmit", map[string]any{"prompt": "again"})
	after, _ := filepath.Glob(filepath.Join(repo, ".git", "flinch", "recordings", "*.json"))
	if len(after) != len(files) {
		t.Fatal("recorder wrote with opt-in disabled")
	}
}
