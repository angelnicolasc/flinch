package check

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/angelnicolasc/flinch/internal/reflex"
)

func TestIntegrityResolvesNestedGoSubtest(t *testing.T) {
	const trusted = `package retry
import "testing"
func TestDelay(t *testing.T) {
	t.Run("other", func(t *testing.T) {
		t.Run("inner", func(t *testing.T) { if Delay(-1) != 8 { t.Fatal("wrong default") } })
	})
	t.Run("outer", func(t *testing.T) {
		t.Run("inner", func(t *testing.T) { if Delay(0) != 0 { t.Fatal("wrong zero") } })
	})
}
`
	repo, _ := setupCheckRepo(t)
	r := reflex.Reflex{ID: "explicit-zero", Version: 1, Rung: reflex.RungTest, Scope: "*.go",
		Obligation: "Keep an explicit zero delay.", Artifact: reflex.Artifact{Tool: "go-test", Path: "retry_test.go", Selector: "TestDelay/outer/inner"}}
	if _, err := reflex.Save(repo, r); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, r.Artifact.Path)
	if err := os.WriteFile(path, []byte(trusted), 0o644); err != nil {
		t.Fatal(err)
	}
	commitRepo(t, repo, "Protect explicit zero")
	for _, tc := range []struct {
		name, current string
		intact        bool
	}{
		{"add_sibling", strings.Replace(trusted, `t.Run("outer", func(t *testing.T) {`, `t.Run("outer", func(t *testing.T) {
		t.Run("sibling", func(t *testing.T) { if Delay(2) != 2 { t.Fatal("wrong delay") } })`, 1), true},
		{"change_inner", strings.Replace(trusted, "Delay(0) != 0", "Delay(0) != 8", 1), false},
		{"remove_outer", strings.Replace(trusted, `t.Run("outer"`, `t.Run("missing"`, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.current), 0o644); err != nil {
				t.Fatal(err)
			}
			violations, err := CheckIntegrity(context.Background(), repo, "HEAD", []reflex.Reflex{r}, []string{r.Artifact.Path})
			if err != nil || tc.intact && len(violations) != 0 || !tc.intact && (len(violations) != 1 || violations[0].Kind != "artifact_modified") {
				t.Fatalf("expected intact=%t for %s, got %+v, %v", tc.intact, r.Artifact.Selector, violations, err)
			}
		})
	}
}
