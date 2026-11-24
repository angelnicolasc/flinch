package author

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/angelnicolasc/flinch/internal/config"
	"github.com/angelnicolasc/flinch/internal/gitutil"
)

func TestNominationRequiresReplacingPreviouslyAddedLines(t *testing.T) {
	repo := t.TempDir()
	ctx := context.Background()
	if _, _, err := gitutil.Run(ctx, repo, nil, "init"); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(repo, "values.txt")
	write := func(lines string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(lines), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("one\n")
	before := commitSeed(t, repo, "start")
	write("one\ntwo\n")
	first := commitSeed(t, repo, "add two")
	for _, tc := range []struct {
		name, lines string
		want        bool
	}{
		{"append", "one\ntwo\nthree\n", false},
		{"adjacent", "changed one\ntwo\n", false},
		{"replace", "one\nchanged two\n", true},
		{"delete", "one\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			write(tc.lines)
			end, err := gitutil.CaptureWorkingTreeSnapshot(ctx, repo, "")
			if err != nil {
				t.Fatal(err)
			}
			nom, err := NominateCorrection(ctx, repo, before, first, first, end)
			if err != nil || nom.Nominated != tc.want {
				t.Fatalf("nomination = %+v, error = %v, want %v", nom, err, tc.want)
			}
		})
	}
}

func TestGeneratedIDsDistinguishPackagesAndCompleteSelectors(t *testing.T) {
	a := makeReflexID("billing/retry_test.go", "TestRetry/zero")
	b := makeReflexID("search/retry_test.go", "TestRetry/zero")
	c := makeReflexID("billing/retry_test.go", "TestRetry/zero")
	if a == b || a != c {
		t.Fatalf("package identity was lost or changed: %q, %q, %q", a, b, c)
	}
}

func TestManagedLineRequiresOneInsertionAndPreservesBytes(t *testing.T) {
	base := "Header  \r\n" + ManagedStartMarker + "\r\nKeep this line.\r\n" + ManagedEndMarker + "\r\n"
	valid := "Header  \r\n" + ManagedStartMarker + "\r\nKeep this line.\r\nAdd this line.\r\n" + ManagedEndMarker + "\r\n"
	for _, tc := range []struct {
		name, before, after string
		want                bool
	}{
		{"one", base, valid, true},
		{"new markers", "Team guide.\n", "Team guide.\n" + ManagedStartMarker + "\nRule.\n" + ManagedEndMarker + "\n", false},
		{"change line endings", base, "Header  \n" + ManagedStartMarker + "\nKeep this line.\nAdd this line.\n" + ManagedEndMarker + "\n", false},
		{"replace previous entry", base, "Header  \r\n" + ManagedStartMarker + "\r\nNew text.\r\n" + ManagedEndMarker + "\r\n", false},
		{"two entries", base, "Header  \r\n" + ManagedStartMarker + "\r\nKeep this line.\r\nAdd one.\r\nAdd two.\r\n" + ManagedEndMarker + "\r\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if result := ValidateManagedLine([]byte(tc.before), []byte(tc.after), 30); result.Valid != tc.want {
				t.Fatalf("validation = %+v, want valid %v", result, tc.want)
			}
		})
	}
	if result := ValidateManagedLine(nil, []byte(ManagedStartMarker+"\nNew.\n"+ManagedEndMarker+"\n"), 30); result.Valid {
		t.Fatalf("new instructions file credited as guidance: %+v", result)
	}
}

func TestInconclusiveCandidateNeverFallsBackToGuidance(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	if _, _, err := gitutil.Run(ctx, repo, nil, "init"); err != nil {
		t.Fatal(err)
	}
	write := func(name, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("AGENTS.md", ManagedStartMarker+"\n"+ManagedEndMarker+"\n")
	write("value.go", "package value\nfunc Value() int {return 0}\n")
	broken := commitSeed(t, repo, "before")
	write("AGENTS.md", ManagedStartMarker+"\nKeep zero explicit.\n"+ManagedEndMarker+"\n")
	write("value.go", "package value\nfunc Value() int {return 1}\n")
	write("value_test.go", "package value\nimport \"testing\"\nfunc TestOnly(t *testing.T) { if Value()!=1 {t.Fatal(\"wrong value\")} }\n")
	fixed := commitSeed(t, repo, "after")
	out, err := ReuseFirst(ctx, repo, config.Default(), broken, fixed, AuthoringBudgetCap)
	if err != nil || out.Status != ReuseInconclusive {
		t.Fatalf("unprovable test credited as %q, error = %v", out.Status, err)
	}
}
