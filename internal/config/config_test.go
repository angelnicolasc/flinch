package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultValidatesAndRoundTrips(t *testing.T) {
	dir := t.TempDir()
	cfg := Default()
	cfg.Policy.Owner = "desktop-platform"
	cfg.Tests.Runners = []TestRunner{{Kind: "go", Paths: []string{"**/*.go"}}}
	cfg.Lint = []LintConfig{{Kind: "go-vet", Paths: []string{"**/*.go"}}}

	if err := Save(dir, cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.Policy.Owner != "desktop-platform" {
		t.Fatalf("expected owner desktop-platform, got %q", loaded.Policy.Owner)
	}
	if loaded.Loop.MaxContinuationsPerTurn != 2 {
		t.Fatalf("expected default max continuations per turn 2, got %d", loaded.Loop.MaxContinuationsPerTurn)
	}
	if loaded.Policy.AllowRemoteEvidence {
		t.Fatalf("expected allow_remote_evidence to default to false")
	}
	if loaded.Sources.RetrospectiveTranscripts {
		t.Fatalf("expected retrospective_transcripts to default to false")
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "flinch.toml")
	if err := os.WriteFile(p, []byte("schema = 1\n[policy]\nunknown_flag = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(p); err == nil {
		t.Fatalf("expected error on unknown TOML key")
	}
}

func TestParseDurationSupportsDays(t *testing.T) {
	d, err := ParseDuration("7d")
	if err != nil {
		t.Fatalf("ParseDuration(7d) failed: %v", err)
	}
	if d != 7*24*time.Hour {
		t.Fatalf("expected 168h, got %v", d)
	}
	d, err = ParseDuration("2s")
	if err != nil || d != 2*time.Second {
		t.Fatalf("expected 2s, got %v (err=%v)", d, err)
	}
}
