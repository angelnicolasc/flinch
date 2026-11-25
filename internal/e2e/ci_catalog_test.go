package e2e

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/angelnicolasc/flinch/internal/check"
	"github.com/angelnicolasc/flinch/internal/cli"
	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/reflex"
)

func TestCIUsesDirectCheckRecordsFromGit(t *testing.T) {
	for _, tc := range []struct {
		name, id, subdir string
		block            bool
	}{
		{"unicode", "demora-mínima", "", true},
		{"archived", "archived-delay", "archive", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			if _, _, err := gitutil.Run(t.Context(), repo, nil, "init", "-q"); err != nil {
				t.Fatal(err)
			}
			path, err := reflex.Save(repo, reflex.Reflex{
				ID: tc.id, Version: 1, Obligation: "Use the configured format string.", Rung: reflex.RungLint,
				Artifact: reflex.Artifact{Tool: "go-vet", Rule: "printf"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if tc.subdir != "" {
				dir := filepath.Join(filepath.Dir(path), tc.subdir)
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				archived := filepath.Join(dir, filepath.Base(path))
				if err := os.Rename(path, archived); err != nil {
					t.Fatal(err)
				}
				path = archived
			}
			base := gitCommit(t, repo, "Review check catalog")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			gitCommit(t, repo, "Remove record")
			var stdout, stderr bytes.Buffer
			code := cli.Run(t.Context(), []string{"check", "--ci", "--repo", repo, "--base", base, "--format", "json"}, &stdout, &stderr)
			var result check.Result
			if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
				t.Fatalf("decode check result (exit %d): %v, stderr: %s", code, err, stderr.String())
			}
			violations := result.IntegrityViolations
			if tc.block {
				if code != 1 || len(violations) != 1 || violations[0].Kind != "check_deleted" || violations[0].ReflexID != tc.id {
					t.Fatalf("expected deleted record %q, got exit %d and %+v", tc.id, code, violations)
				}
			} else if code != 0 || len(violations) != 0 {
				t.Fatalf("archived record affected CI: exit %d, %+v", code, violations)
			}
		})
	}
}
