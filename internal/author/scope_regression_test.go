package author_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/hook"
	"github.com/angelnicolasc/flinch/internal/reflex"
	"github.com/angelnicolasc/flinch/internal/testutil"
)

func TestProductOnlyRepeatInTurnAndNativeClone(t *testing.T) {
	runProductOnlyRepeat(t, nativeCheckScenario{}, "pytest", "vitest")
}

func TestProductOnlyRepeatWithTwoAssertionsInTurnAndNativeClone(t *testing.T) {
	runProductOnlyRepeat(t, nativeCheckScenario{twoAssertions: true}, "pytest", "vitest")
}

// The first Stop can also request a check that was absent from the fix.
func TestRequestedTwoAssertionCheckRunsInLaterTurnAndNativeClone(t *testing.T) {
	runProductOnlyRepeat(t, nativeCheckScenario{twoAssertions: true, requestFirst: true}, "pytest")
}

type nativeCheckScenario struct {
	twoAssertions bool
	requestFirst  bool
}

func runProductOnlyRepeat(t *testing.T, scenario nativeCheckScenario, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		t.Run(tool, func(t *testing.T) {
			ctx := context.Background()
			repo := t.TempDir()
			testutil.HookProfile(t, repo)
			if _, _, err := gitutil.Run(ctx, repo, nil, "init"); err != nil {
				t.Fatal(err)
			}
			write := func(root, path, content string) {
				t.Helper()
				fixtureWrite(t, root, path, content)
			}
			var sourcePath, testPath, brokenCode, fixedCode, neighbor, selected, selectedName string
			var nativeArgs []string
			var deps string
			linkDeps := func(root string) {
				t.Helper()
				if deps != "" {
					if err := os.Symlink(deps, filepath.Join(root, "node_modules")); err != nil {
						t.Fatal(err)
					}
				}
			}
			switch tool {
			case "pytest":
				if _, err := exec.LookPath("pytest"); err != nil {
					t.Skip("pytest not installed")
				}
				write(repo, "pytest.ini", "[pytest]\npythonpath = .\n")
				sourcePath, testPath = "src/delay.py", "tests/test_delay.py"
				brokenCode = "def delay(value):\n    return value or 8\n"
				fixedCode = "def delay(value):\n    return 8 if value is None else value\n"
				neighbor = "from src.delay import delay\n\ndef test_omitted():\n    assert delay(None) == 8\n"
				selected = "\ndef test_explicit_zero():\n    assert delay(0) == 0, 'explicit zero must be preserved'\n"
				if scenario.twoAssertions {
					selected = "\ndef test_explicit_zero():\n    assert delay(None) == 8\n    assert delay(0) == 0, 'explicit zero must be preserved'\n"
				}
				selectedName = "test_explicit_zero"
				nativeArgs = []string{"pytest", "-q"}
			case "vitest":
				bin := os.Getenv("FLINCH_REAL_VITEST")
				if bin == "" {
					t.Skip("FLINCH_REAL_VITEST is not set")
				}
				deps = filepath.Dir(filepath.Dir(bin))
				linkDeps(repo)
				lock, err := os.ReadFile(filepath.Join(filepath.Dir(deps), "package-lock.json"))
				if err != nil {
					t.Fatal(err)
				}
				write(repo, "package-lock.json", string(lock))
				write(repo, "package.json", "{\"type\":\"module\",\"scripts\":{\"test\":\"vitest run\"},\"devDependencies\":{\"vitest\":\"3.2.4\"}}\n")
				sourcePath, testPath = "src/delay.ts", "tests/delay.test.ts"
				brokenCode = "export const delay = (value?: number) => value || 8;\n"
				fixedCode = "export const delay = (value?: number) => value ?? 8;\n"
				neighbor = "import { expect, test } from 'vitest';\nimport { delay } from '../src/delay';\ntest('omitted', () => { expect(delay()).toBe(8); });\n"
				selected = "test('explicit zero', () => { expect(delay(0), 'explicit zero must be preserved').toBe(0); });\n"
				if scenario.twoAssertions {
					selected = "test('explicit zero', () => {\n  expect(delay()).toBe(8);\n  expect(delay(0), 'explicit zero must be preserved').toBe(0);\n});\n"
				}
				selectedName = "explicit zero"
				nativeArgs = []string{"npm", "test"}
			}
			write(repo, ".gitignore", "node_modules\n__pycache__/\n.pytest_cache/\n")
			write(repo, "wip.txt", "initial\n")
			// Begin with a distinct implementation so the following human correction
			// actually replaces a line introduced during the previous agent turn.
			seedCode := strings.Replace(brokenCode, "value or 8", "8", 1)
			if tool == "vitest" {
				seedCode = strings.Replace(brokenCode, "value || 8", "8", 1)
			}
			write(repo, sourcePath, seedCode)
			write(repo, testPath, neighbor)
			fixtureCommit(t, repo, "seed native project", ".")
			fixtureDirtyWork(t, repo)
			start := fixtureState(t, repo)
			fixtureEvent(t, repo, hook.HostClaude, "author", "UserPromptSubmit", "Use the configured delay.", "")
			start.expect(t, repo, "")
			write(repo, sourcePath, brokenCode)
			initialEdit := fixtureState(t, repo)
			fixtureEvent(t, repo, hook.HostClaude, "author", "Stop", "", "Updated the default.")
			initialEdit.expect(t, repo, "")
			fixtureEvent(t, repo, hook.HostClaude, "author", "UserPromptSubmit", "Zero is a valid delay. Keep it.", "")
			initialEdit.expect(t, repo, "")
			write(repo, sourcePath, fixedCode)
			if scenario.requestFirst {
				fixedOnly := fixtureState(t, repo)
				request := fixtureEvent(t, repo, hook.HostClaude, "author", "Stop", "", "Preserved the explicit value.")
				var continuation struct{ Decision, Reason string }
				if err := json.Unmarshal(request.Stdout, &continuation); err != nil || continuation.Decision != "block" || continuation.Reason == "" {
					t.Fatalf("expected one authoring continuation, got %s (%v)", request.Stdout, err)
				}
				fixedOnly.expect(t, repo, "")
			}
			write(repo, testPath, neighbor+selected)
			fix := fixtureState(t, repo)
			proved := fixtureEvent(t, repo, hook.HostClaude, "author", "Stop", "", "Preserved explicit zero and added a test.")
			if scenario.requestFirst {
				events := fixtureAuthoringEvents(t, repo)
				if len(events) == 0 || events[len(events)-1].Kind != "written" || !strings.Contains(events[len(events)-1].Selector, selectedName) {
					t.Fatalf("follow-up Stop did not register its new check: %+v", events)
				}
			} else {
				var notice struct {
					SystemMessage string `json:"systemMessage"`
				}
				if err := json.Unmarshal(proved.Stdout, &notice); err != nil || !strings.Contains(notice.SystemMessage, selectedName) {
					t.Fatalf("missing proof notice for %s: %s, %v", selectedName, proved.Stdout, err)
				}
			}
			records, err := reflex.LoadAll(repo)
			if err != nil || len(records) != 1 {
				t.Fatalf("expected one proposed record: %+v, %v", records, err)
			}
			record := records[0]
			if !record.MatchesPath(sourcePath) || record.MatchesPath("unrelated/file.txt") {
				t.Fatalf("unexpected scope %q for product file %s", record.Scope, sourcePath)
			}
			recordPath := ".flinch/checks/" + record.ID + ".yml"
			fix.expect(t, repo, recordPath)
			fixtureCommit(t, repo, "integrate native check", sourcePath, testPath, recordPath)
			integrated := fixtureState(t, repo)
			fixtureEvent(t, repo, hook.HostCodex, "next", "UserPromptSubmit", "Simplify the delay calculation.", "")
			integrated.expect(t, repo, "")
			write(repo, sourcePath, brokenCode)
			repeat := fixtureState(t, repo)
			response := fixtureEvent(t, repo, hook.HostCodex, "next", "Stop", "", "Simplified the delay.")
			var decision struct{ Decision, Reason string }
			if err := json.Unmarshal(response.Stdout, &decision); err != nil || decision.Decision != "block" || !strings.Contains(decision.Reason, selectedName) {
				t.Fatalf("product-only repeat did not request repair for %s: %s, %v", selectedName, response.Stdout, err)
			}
			repeat.expect(t, repo, "")
			clone := filepath.Join(t.TempDir(), "fresh")
			if _, _, err := gitutil.Run(ctx, repo, nil, "clone", "--local", repo, clone); err != nil {
				t.Fatal(err)
			}
			linkDeps(clone)
			runNative := func(wantFailure bool) {
				t.Helper()
				command := exec.CommandContext(ctx, nativeArgs[0], nativeArgs[1:]...)
				command.Dir = clone
				result, err := command.CombinedOutput()
				var failed *exec.ExitError
				if (!wantFailure && err != nil) || (wantFailure && (!errors.As(err, &failed) || !strings.Contains(string(result), selectedName))) {
					t.Fatalf("native check wantFailure=%v: %v\n%s", wantFailure, err, result)
				}
			}
			runNative(false)
			write(clone, sourcePath, brokenCode)
			runNative(true)
		})
	}
}
