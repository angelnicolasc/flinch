package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/reflex"
	"github.com/angelnicolasc/flinch/internal/testutil"
)

func TestRegressionDirtyCheckoutKeepsNativeRunnerCachesOutside(t *testing.T) {
	for _, name := range []string{"pytest", "ruff"} {
		testutil.RequireTool(t, name)
	}
	repo := t.TempDir()
	ctx := context.Background()
	if _, _, err := gitutil.Run(ctx, repo, nil, "init"); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string]string{"test_keep.py": "def test_keep():\n    assert True\n", "user.txt": "my unstaged work\n"} {
		if err := os.WriteFile(filepath.Join(repo, path), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	before, _, err := gitutil.Run(ctx, repo, nil, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		t.Fatal(err)
	}
	tr, err := RunPytest(ctx, repo, nil, "test_keep.py::test_keep", 10*time.Second)
	if err != nil || tr.ExitCode != 0 {
		t.Fatalf("pytest: %+v %v", tr, err)
	}
	if _, err = RunArtifact(ctx, repo, reflex.Artifact{Tool: "ruff"}, []string{"test_keep.py"}, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	after, _, err := gitutil.Run(ctx, repo, nil, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Errorf("status changed:\n%s\n%s", before, after)
	}
	for _, p := range []string{".flinch-tmp", ".pytest_cache", ".ruff_cache", "__pycache__"} {
		if _, err := os.Stat(filepath.Join(repo, p)); !os.IsNotExist(err) {
			t.Errorf("left %s in checkout", p)
		}
	}
	if got, err := os.ReadFile(filepath.Join(repo, "user.txt")); err != nil || strings.TrimSpace(string(got)) != "my unstaged work" {
		t.Fatal("changed existing user content")
	}
}
