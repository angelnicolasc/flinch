package finding

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnrichNeverReadsOutsideTheCheckout(t *testing.T) {
	outer := t.TempDir()
	repo := filepath.Join(outer, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outer, "secret.txt")
	if err := os.WriteFile(secret, []byte("SECRET-LINE-7710\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n\nfunc Run() {\n\tdo()\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reported := []Finding{
		{Tool: "ruff", Rule: "R1", Path: "../secret.txt", StartLine: 1},
		{Tool: "ruff", Rule: "R2", Path: secret, StartLine: 1},
		{Tool: "ruff", Rule: "R3", Path: "sub/../../secret.txt", StartLine: 1},
		{Tool: "go-vet", Rule: "R4", Path: "main.go", StartLine: 4},
	}
	link := filepath.Join(repo, "link.txt")
	if err := os.Symlink(secret, link); err == nil {
		reported = append(reported, Finding{Tool: "ruff", Rule: "R5", Path: "link.txt", StartLine: 1})
	} else {
		t.Logf("symlinks unavailable here (%v); the link case runs on other platforms", err)
	}

	for _, f := range Enrich(repo, reported) {
		if strings.Contains(f.Context, "SECRET") {
			t.Fatalf("%s: read a file outside the checkout: %+v", f.Rule, f)
		}
		if f.Rule == "R4" && (f.Context != "do()" || f.Symbol != "Run") {
			t.Fatalf("an interior finding lost its context: %+v", f)
		}
		if f.Rule != "R4" && (f.Context != "" || f.Symbol != "<file>") {
			t.Fatalf("%s: an outside location must stay without context: %+v", f.Rule, f)
		}
	}
}

func TestEnrichSkipsOversizedAndNonRegularFiles(t *testing.T) {
	repo := t.TempDir()
	big := make([]byte, maxEnrichedFile+1)
	copy(big, "BIG-FIRST-LINE\n")
	if err := os.WriteFile(filepath.Join(repo, "big.txt"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range Enrich(repo, []Finding{
		{Rule: "big", Path: "big.txt", StartLine: 1},
		{Rule: "dir", Path: "dir", StartLine: 1},
	}) {
		if f.Context != "" {
			t.Fatalf("%s: read a file it must skip: %+v", f.Rule, f)
		}
	}
}
