package author_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/hook"
)

// Exercise the public adapter entry point so a configured filename alone never
// establishes that the host actually imports the shared instructions.
func TestClaudeCreditsOnlyAnExistingImportedGuide(t *testing.T) {
	for _, tc := range []struct {
		name, imports string
		want          bool
	}{
		{"imported", "@AGENTS.md\n", true},
		{"not-imported", "Use the standard project tools.\n", false},
		{"commented import", "<!--\n@AGENTS.md\n-->\n", false},
		{"indented example", "Example:\n\n    @AGENTS.md\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo := t.TempDir()
			if _, _, err := gitutil.Run(ctx, repo, nil, "init"); err != nil {
				t.Fatal(err)
			}
			write := func(name, contents string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(repo, name), []byte(contents), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			guide := "<!-- flinch:start -->\n<!-- flinch:end -->\n"
			write("AGENTS.md", guide)
			write("CLAUDE.md", tc.imports)
			write("label.txt", "one\n")
			if _, _, err := gitutil.Run(ctx, repo, nil, "add", "-A"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := gitutil.Run(ctx, repo, nil, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "seed"); err != nil {
				t.Fatal(err)
			}
			call := func(event, prompt string) {
				t.Helper()
				payload, err := json.Marshal(map[string]string{"session_id": "guide", "cwd": repo, "hook_event_name": event, "prompt": prompt})
				if err != nil {
					t.Fatal(err)
				}
				hook.Handle(ctx, hook.HostClaude, event, payload, repo)
			}
			call("UserPromptSubmit", "Edit the label")
			write("label.txt", "two\n")
			call("Stop", "")
			call("UserPromptSubmit", "Use lower-case product labels")
			write("label.txt", "three\n")
			write("AGENTS.md", strings.Replace(guide, "<!-- flinch:end -->", "Use lower-case product labels.\n<!-- flinch:end -->", 1))
			call("Stop", "")
			st, err := gitutil.FlinchStateDir(ctx, repo)
			if err != nil {
				t.Fatal(err)
			}
			log, err := os.ReadFile(filepath.Join(st, "log", "authoring.jsonl"))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			if got := strings.Contains(string(log), `"kind":"line_recorded"`); got != tc.want {
				t.Fatalf("credited = %v, want %v; log %s", got, tc.want, log)
			}
		})
	}
}
