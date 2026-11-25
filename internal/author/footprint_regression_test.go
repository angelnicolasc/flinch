package author_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/angelnicolasc/flinch/internal/author"
	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/hook"
	"github.com/angelnicolasc/flinch/internal/testutil"
)

func fixtureGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, _, err := gitutil.Run(context.Background(), repo, nil, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func fixtureCommit(t *testing.T, repo, message string, paths ...string) {
	t.Helper()
	fixtureGit(t, repo, append([]string{"add", "--"}, paths...)...)
	args := []string{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--only", "-m", message, "--"}
	fixtureGit(t, repo, append(args, paths...)...)
}

func fixtureWrite(t *testing.T, repo, path, value string) {
	t.Helper()
	p := filepath.Join(repo, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fixtureDirtyWork(t *testing.T, repo string) {
	t.Helper()
	fixtureWrite(t, repo, "wip.txt", "already staged\n")
	fixtureGit(t, repo, "add", "--", "wip.txt")
	fixtureWrite(t, repo, "wip.txt", "more work after staging\n")
	fixtureWrite(t, repo, "scratch.txt", "untracked user notes\n")
}

func fixtureEvent(t *testing.T, repo string, host hook.Host, session, event, prompt, last string) hook.Response {
	t.Helper()
	payload, err := json.Marshal(map[string]string{
		"session_id": session, "cwd": repo, "hook_event_name": event, "prompt": prompt, "last_assistant_message": last,
	})
	if err != nil {
		t.Fatal(err)
	}
	response := hook.Handle(context.Background(), host, event, payload, repo)
	if response.ExitCode != 0 {
		t.Fatalf("%s %s returned %d: %s", host, event, response.ExitCode, response.Stdout)
	}
	return response
}

type fixtureFootprint struct {
	porcelain  []string
	indexDiff  string
	dirtyBytes map[string]string
}

func fixtureState(t *testing.T, repo string) fixtureFootprint {
	t.Helper()
	raw := fixtureGit(t, repo, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	var status []string
	if raw != "" {
		status = strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00")
	}
	sort.Strings(status)
	bytes, err := gitutil.WorkingTreeStatus(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	return fixtureFootprint{status, fixtureGit(t, repo, "diff", "--cached", "--binary"), bytes}
}

func (before fixtureFootprint) expect(t *testing.T, repo, newCheck string) {
	t.Helper()
	after := fixtureState(t, repo)
	if newCheck != "" {
		if _, exists := before.dirtyBytes[newCheck]; exists {
			t.Fatalf("expected a new check, found preexisting %s", newCheck)
		}
		before.porcelain = append(append([]string(nil), before.porcelain...), "?? "+newCheck)
		sort.Strings(before.porcelain)
		delete(after.dirtyBytes, newCheck)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("unexpected working tree or index change\nbefore: %+v\nafter:  %+v", before, after)
	}
	if got := fixtureGit(t, repo, "show", ":wip.txt"); got != "already staged\n" {
		t.Fatalf("staged user edit changed: %q", got)
	}
}

func fixtureAuthoringEvents(t *testing.T, repo string) []author.AuthoringEvent {
	t.Helper()
	state, err := gitutil.FlinchStateDir(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(state, "log", "authoring.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var events []author.AuthoringEvent
	for _, line := range strings.FieldsFunc(string(data), func(r rune) bool { return r == '\n' }) {
		var event author.AuthoringEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	return events
}

func TestAuthoringOutcomesPreserveDirtyCheckout(t *testing.T) {
	for _, tc := range []struct{ name, kind string }{
		{"no-op", ""}, {"skip", "skipped"}, {"inconclusive", "inconclusive_closed"}, {"guide", "line_recorded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			testutil.HookProfile(t, repo)
			fixtureGit(t, repo, "init")
			guide := "<!-- flinch:start -->\n<!-- flinch:end -->\n"
			fixtureWrite(t, repo, "AGENTS.md", guide)
			fixtureWrite(t, repo, "CLAUDE.md", "@AGENTS.md\n")
			fixtureWrite(t, repo, "value.py", "value = 0\n")
			fixtureWrite(t, repo, "wip.txt", "initial\n")
			fixtureCommit(t, repo, "seed fixture", "AGENTS.md", "CLAUDE.md", "value.py", "wip.txt")
			fixtureDirtyWork(t, repo)
			start := fixtureState(t, repo)
			fixtureEvent(t, repo, hook.HostClaude, "case", "UserPromptSubmit", "Set the value.", "")
			start.expect(t, repo, "")
			fixtureWrite(t, repo, "value.py", "value = 1\n")
			initialEdit := fixtureState(t, repo)
			fixtureEvent(t, repo, hook.HostClaude, "case", "Stop", "", "Set the value.")
			initialEdit.expect(t, repo, "")
			fixtureEvent(t, repo, hook.HostClaude, "case", "UserPromptSubmit", "Preserve the zero.", "")
			initialEdit.expect(t, repo, "")
			last := "Done."
			if tc.name != "no-op" {
				fixtureWrite(t, repo, "value.py", "value = 0  # explicit zero\n")
			}
			switch tc.name {
			case "skip":
				last = "flinch skip: one-time value"
			case "inconclusive":
				fixtureWrite(t, repo, "test_value.py", "from value import value\n\ndef test_zero():\n    assert value == 0\n")
			case "guide":
				fixtureWrite(t, repo, "AGENTS.md", strings.Replace(guide, "<!-- flinch:end -->", "Keep explicit zero values.\n<!-- flinch:end -->", 1))
			}
			before := fixtureState(t, repo)
			fixtureEvent(t, repo, hook.HostClaude, "case", "Stop", "", last)
			before.expect(t, repo, "")
			events := fixtureAuthoringEvents(t, repo)
			if tc.kind == "" {
				if len(events) != 0 {
					t.Fatalf("no-op produced authoring events: %+v", events)
				}
			} else if len(events) == 0 || events[len(events)-1].Kind != tc.kind {
				t.Fatalf("expected %q, got %+v", tc.kind, events)
			}
		})
	}
}
