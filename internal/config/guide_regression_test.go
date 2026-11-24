package config

import "testing"

func TestTeamGuideRequiresRepositoryRelativePath(t *testing.T) {
	cfg := Default()
	for _, name := range []string{"../outside.md", `..\outside.md`, "/outside.md", `C:\outside.md`, `\\host\outside.md`} {
		cfg.ContextGuide.File = name
		if err := cfg.Validate(); err == nil {
			t.Errorf("accepted an external instruction file %q", name)
		}
	}
	cfg.ContextGuide.File = ".claude/CLAUDE.md"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("rejected a project guide: %v", err)
	}
}
