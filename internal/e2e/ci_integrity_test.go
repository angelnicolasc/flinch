package e2e

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/angelnicolasc/flinch/internal/check"
	"github.com/angelnicolasc/flinch/internal/cli"
	"github.com/angelnicolasc/flinch/internal/gitutil"
)

func TestCIIntegrityAgainstPullRequestBase(t *testing.T) {
	ctx := t.Context()
	baseRepo := t.TempDir()
	if _, _, err := gitutil.Run(ctx, baseRepo, nil, "init"); err != nil {
		t.Fatal(err)
	}
	const record = ".flinch/checks/zero-delay.yml"
	const product = "package retry\n\nfunc Delay(ms int) int { return ms }\n"
	const table = `package retry

import "testing"

func TestDelay(t *testing.T) {
	for _, tc := range []struct {
		name string
		ms, want int
	}{
		{"zero", 0, 0},
		{"positive", 10, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Delay(tc.ms); got != tc.want {
				t.Errorf("Delay(%d) = %d, want %d", tc.ms, got, tc.want)
			}
		})
	}
}
`
	writeRepoFile(t, baseRepo, "go.mod", "module example.com/retry\n\ngo 1.26\n")
	writeRepoFile(t, baseRepo, "retry.go", product)
	writeRepoFile(t, baseRepo, "retry_test.go", table)
	writeRepoFile(t, baseRepo, record, `id: zero-delay
version: 1
scope: '*.go'
rung: test
obligation: An explicit zero retries immediately.
artifact:
  tool: go-test
  path: retry_test.go
  selector: TestDelay/zero
cases:
  allowed: TestDelay/positive
`)
	base := gitCommit(t, baseRepo, "Protect immediate retries")

	t.Run("missing base", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := cli.Run(ctx, []string{"check", "--ci", "--repo", baseRepo, "--base", "missing-history", "--format", "json"}, &stdout, &stderr)
		if code == 0 || !strings.Contains(stderr.String(), "missing-history") || stdout.Len() != 0 {
			t.Fatalf("expected an error naming the missing base, got exit %d, stdout=%s, stderr=%s", code, &stdout, &stderr)
		}
	})

	for _, tc := range []struct {
		name, path, kind string
	}{
		{"weakened row", "retry_test.go", "artifact_modified"},
		{"deleted record", record, "check_deleted"},
		{"added suppression", "retry.go", "suppression_added"},
		{"product fix", "retry.go", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			if _, _, err := gitutil.Run(ctx, repo, nil, "clone", baseRepo, "."); err != nil {
				t.Fatal(err)
			}
			switch tc.kind {
			case "artifact_modified":
				writeRepoFile(t, repo, "retry.go", strings.Replace(product, "return ms", "if ms <= 0 { return 1000 }; return ms", 1))
				writeRepoFile(t, repo, tc.path, strings.Replace(table, `{"zero", 0, 0}`, `{"zero", 0, 1000}`, 1))
			case "check_deleted":
				if err := os.Remove(filepath.Join(repo, tc.path)); err != nil {
					t.Fatal(err)
				}
			case "suppression_added":
				writeRepoFile(t, repo, tc.path, product+"//nolint:gocritic\n")
			default:
				writeRepoFile(t, repo, tc.path, strings.Replace(product, "return ms", "if ms < 0 { return 1000 }; return ms", 1))
			}
			gitCommit(t, repo, tc.name)

			var stdout, stderr bytes.Buffer
			code := cli.Run(ctx, []string{"check", "--ci", "--base", base, "--repo", repo, "--format", "json"}, &stdout, &stderr)
			var result check.Result
			if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
				t.Fatalf("decode check result (exit %d): %v, stdout=%s, stderr=%s", code, err, stdout.String(), stderr.String())
			}
			if len(result.Findings) != 0 || len(result.SkippedControls) != 0 {
				t.Fatalf("unexpected native test result: %+v", result)
			}
			if tc.kind == "" {
				if code != 0 || result.Blocking || len(result.IntegrityViolations) != 0 {
					t.Fatalf("product fix returned exit %d: %+v", code, result)
				}
				return
			}
			violations := result.IntegrityViolations
			if code != 1 || !result.Blocking || len(violations) != 1 || violations[0].Kind != tc.kind || violations[0].Path != tc.path {
				t.Fatalf("want exit 1 with %s at %s, got exit %d: %+v", tc.kind, tc.path, code, result)
			}
		})
	}
}
