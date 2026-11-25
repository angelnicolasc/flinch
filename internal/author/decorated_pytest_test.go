package author_test

import (
	"context"
	"strings"
	"testing"

	"github.com/angelnicolasc/flinch/internal/author"
	"github.com/angelnicolasc/flinch/internal/check"
	"github.com/angelnicolasc/flinch/internal/config"
	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/reflex"
)

// editProtectedCase checks discovery and integrity for the same Git edit.
func editProtectedCase(t *testing.T, tool, path, selector, before, after string, candidate, modified bool) {
	t.Helper()
	ctx := context.Background()
	repo := t.TempDir()
	fixtureGit(t, repo, "init")
	fixtureWrite(t, repo, path, before)
	record := reflex.Reflex{ID: "protected-delay", Version: 1, Rung: reflex.RungTest, Scope: "**",
		Obligation: "Preserve the delay.", Artifact: reflex.Artifact{Tool: tool, Path: path, Selector: selector}}
	if _, err := reflex.Save(repo, record); err != nil {
		t.Fatal(err)
	}
	fixtureCommit(t, repo, "Protect the delay", ".")
	fixtureWrite(t, repo, path, after)
	fixed, err := gitutil.CaptureWorkingTreeSnapshot(ctx, repo, "edit the delay test")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Tests.Runners = []config.TestRunner{{Kind: tool}}
	candidates, err := author.ExtractTestCandidates(ctx, repo, cfg, "HEAD", fixed)
	if err != nil || candidate && (len(candidates) != 1 || candidates[0].Selector != selector) || !candidate && len(candidates) != 0 {
		t.Errorf("expected candidate %q=%t, got %+v, %v", selector, candidate, candidates, err)
	}
	findings, err := check.CheckIntegrity(ctx, repo, "HEAD", []reflex.Reflex{record}, []string{path})
	if err != nil || modified && (len(findings) != 1 || findings[0].Kind != "artifact_modified") || !modified && len(findings) != 0 {
		t.Errorf("expected artifact_modified=%t for %q, got %+v, %v", modified, selector, findings, err)
	}
}

func TestDecoratedPytestCase(t *testing.T) {
	const source = `import pytest

@pytest.mark.parametrize("want", [0])
def test_zero(want):
    assert delay(0) == want

def test_omitted():
    assert delay(None) == 8
`
	for _, edit := range []struct{ name, before, after string }{
		{"assertion", "== want", ">= want"},
		{"parameter", "[0]", "[8]"},
	} {
		t.Run(edit.name, func(t *testing.T) {
			editProtectedCase(t, "pytest", "test_delay.py", "test_delay.py::test_zero",
				source, strings.Replace(source, edit.before, edit.after, 1), true, true)
		})
	}
	t.Run("multiline_parameter", func(t *testing.T) {
		multiline := strings.Replace(source, `("want", [0])`, "(\n    \"want\", [0]\n)", 1)
		editProtectedCase(t, "pytest", "test_delay.py", "test_delay.py::test_zero",
			multiline, strings.Replace(multiline, "[0]", "[8]", 1), true, true)
	})
	t.Run("following_decorator", func(t *testing.T) {
		const helper = "@functools.lru_cache(maxsize=32)\ndef cached_delay(value):\n    return delay(value)\n\n"
		before := "import functools\n" + strings.Replace(source, "def test_omitted():", helper+"def test_omitted():", 1)
		editProtectedCase(t, "pytest", "test_delay.py", "test_delay.py::test_zero",
			before, strings.Replace(before, "maxsize=32", "maxsize=64", 1), false, false)
	})
}
