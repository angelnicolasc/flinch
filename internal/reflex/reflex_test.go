package reflex

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadBlueprintExampleReflex(t *testing.T) {
	dir := t.TempDir()
	reflexDir := filepath.Join(dir, ".flinch", "reflexes")
	if err := os.MkdirAll(reflexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `id: renderer-window-via-ipc
version: 1
owner: desktop-platform
scope: "src/renderer/**"
obligation: "Renderer accesses windows through the IPC bridge."
artifact:
  tool: eslint
  path: eslint.config.mjs
  rule: no-restricted-imports
  selector: renderer-window-via-ipc
cases:
  broken: .flinch/cases/renderer-window-via-ipc/direct-import.ts
  fixed: .flinch/cases/renderer-window-via-ipc/through-bridge.ts
  allowed: .flinch/cases/renderer-window-via-ipc/bridge-use.ts
surfaces:
  pull_request: { job: lint-desktop, mode: block }
  agent_turn: { mode: repair_if_supported }
`
	if err := os.WriteFile(filepath.Join(reflexDir, "renderer-window-via-ipc.yml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	all, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll failed: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 reflex, got %d", len(all))
	}
	r := all[0]
	if r.EffectiveRung() != RungLint {
		t.Fatalf("expected rung lint, got %s", r.EffectiveRung())
	}
	if !r.IsDeterministic() {
		t.Fatalf("expected eslint reflex to be deterministic")
	}
	if !r.MatchesPath("src/renderer/ui/window.ts") {
		t.Fatalf("expected src/renderer/ui/window.ts to match scope")
	}
	if r.MatchesPath("src/main/ipc.ts") {
		t.Fatalf("did not expect src/main/ipc.ts to match scope")
	}
	if len(r.Cases.Allowed) != 1 || r.Cases.Allowed[0] != ".flinch/cases/renderer-window-via-ipc/bridge-use.ts" {
		t.Fatalf("unexpected allowed cases: %#v", r.Cases.Allowed)
	}
	fp1, err := r.ComputeFingerprint(dir)
	if err != nil {
		t.Fatalf("ComputeFingerprint failed: %v", err)
	}
	if fp1 == "" {
		t.Fatalf("expected non-empty fingerprint")
	}
}

func TestLineGuidanceIsNonDeterministic(t *testing.T) {
	r := Reflex{
		ID:         "no-bg-context",
		Version:    1,
		Obligation: "Pass request context through handlers.",
		Rung:       RungLine,
		Artifact:   Artifact{Tool: "agents-md", Text: "Use req.Context() in HTTP handlers."},
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if r.IsDeterministic() {
		t.Fatalf("expected agents-md line guidance to be non-deterministic")
	}
}

func TestRejectionScopedMatching(t *testing.T) {
	dir := t.TempDir()
	rej := Rejection{
		ID:          "r1",
		Version:     1,
		Scope:       "pkg/**",
		Fingerprint: "sha256:abc",
		Reason:      "too noisy",
	}
	if err := AppendRejection(dir, rej); err != nil {
		t.Fatalf("AppendRejection failed: %v", err)
	}
	rf, err := LoadRejections(dir)
	if err != nil {
		t.Fatalf("LoadRejections failed: %v", err)
	}
	if _, ok := rf.IsRejected("sha256:abc", 1, "pkg/**"); !ok {
		t.Fatalf("expected matching rejection")
	}
	if _, ok := rf.IsRejected("sha256:abc", 2, "pkg/**"); ok {
		t.Fatalf("did not expect version 2 to match version 1 rejection")
	}
}
