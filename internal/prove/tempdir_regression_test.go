package prove

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/angelnicolasc/flinch/internal/reflex"
)

// Proof trees live in the temp directory, which can be reached through a
// symlink, as macOS's /var is. Each tool reports the files it ran by their
// real path, so the selected case and its failure must still read the same
// in the broken and the fixed tree. On Windows the same holds for the 8.3
// short names in TEMP, which Jest spells out.
func TestRegressionProofThroughAnAliasedTempDir(t *testing.T) {
	for _, tc := range []struct {
		tool    string
		fixture func(*testing.T) (string, CandidateSpec)
	}{
		{"go-test", goClampProof},
		{"jest", func(t *testing.T) (string, CandidateSpec) { return jsDelayRepo(t, "jest") }},
		{"vitest", func(t *testing.T) (string, CandidateSpec) { return jsDelayRepo(t, "vitest") }},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			repo, spec := tc.fixture(t)
			alias := filepath.Join(t.TempDir(), "tmp-alias")
			if err := os.Symlink(t.TempDir(), alias); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
				t.Setenv(name, alias)
			}
			report, err := Verify(context.Background(), repo, spec, Options{})
			if err != nil || report.Verdict != VerdictVerified {
				t.Fatalf("%s could not prove through an aliased temp directory: %s/%s %s (%v)", tc.tool, report.Verdict, report.Reason, report.ActionableError, err)
			}
		})
	}
}

func goClampProof(t *testing.T) (string, CandidateSpec) {
	repo, broken, fixed := setupGoCalcRepo(t)
	artifact := reflex.Artifact{Tool: "go-test", Path: "calc_test.go", Selector: "TestClamp/caps_above_max", ExpectedSignal: "want 5"}
	return repo, CandidateSpec{ID: "clamp-caps-above-max", Version: 1, Rung: reflex.RungTest, Obligation: "Clamp must cap values above max.",
		Artifact: artifact, NeighborSelector: "TestClamp/below_max_unchanged", BrokenRef: broken, FixedRef: fixed}
}
