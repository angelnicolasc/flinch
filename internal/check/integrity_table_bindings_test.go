package check

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/angelnicolasc/flinch/internal/reflex"
)

func TestIntegrityGoTableTracksReferencedLocalDeclarations(t *testing.T) {
	const trusted = `package retry
import ("testing"; "time")
func TestDelay(t *testing.T) {
	unit := time.Nanosecond
	zero := 0 * unit
	scale := time.Duration(1)
	cases := []struct { name string; delay, want time.Duration }{
		{"zero", zero, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if Delay(tc.delay) != tc.want * scale { t.Fatal("wrong delay") }
		})
	}
}
`
	repo, _ := setupCheckRepo(t)
	r := reflex.Reflex{ID: "explicit-zero", Version: 1, Rung: reflex.RungTest, Scope: "*.go",
		Obligation: "Keep an explicit zero delay.", Artifact: reflex.Artifact{Tool: "go-test", Path: "retry_test.go", Selector: "TestDelay/zero"}}
	if _, err := reflex.Save(repo, r); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, r.Artifact.Path)
	if err := os.WriteFile(path, []byte(trusted), 0o644); err != nil {
		t.Fatal(err)
	}
	commitRepo(t, repo, "Protect explicit zero")
	withSibling := strings.Replace(trusted, "cases :=", "negative := -time.Nanosecond\n\tcases :=", 1)
	withSibling = strings.Replace(withSibling, `{"zero", zero, 0},`, `{"zero", zero, 0},
		{"negative", negative, 0},`, 1)
	for _, tc := range []struct {
		name, current string
		intact        bool
	}{
		{"sibling_variable", withSibling, true},
		{"row_variable", strings.Replace(trusted, "zero := 0", "zero := 1", 1), false},
		{"row_dependency", strings.Replace(trusted, "unit := time.Nanosecond", "unit := time.Second", 1), false},
		{"loop_variable", strings.Replace(trusted, "scale := time.Duration(1)", "scale := time.Duration(2)", 1), false},
		{"loop_assertion", strings.Replace(trusted, "!= tc.want", "== tc.want", 1), false},
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
