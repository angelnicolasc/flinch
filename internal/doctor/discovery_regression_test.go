package doctor

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/angelnicolasc/flinch/internal/config"
	"github.com/angelnicolasc/flinch/internal/runner"
)

func TestDoctorUsesEffectiveDefaultsWithoutTOML(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "setup.cfg"), []byte("[tool:pytest]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	described, _ := DiscoverConfig(context.Background(), repo)
	effective, present, err := config.LoadOrDefault(repo)
	if err != nil || present || !reflect.DeepEqual(described, effective) || len(effective.Tests.Runners) != 1 || effective.Tests.Runners[0].Kind != "pytest" {
		t.Fatalf("doctor discovery %+v differs from effective defaults %+v (config present %v, error %v)", described, effective, present, err)
	}
	for _, tc := range []struct {
		host string
		want CapabilityState
	}{
		{"codex", CapAvailableUntested},
		{"antigravity", CapAvailableUntested},
		{"opencode", CapUnavailable},
	} {
		tools := map[string]runner.ToolInfo{"git": {Available: true}, tc.host: {Available: true}}
		matrix := buildCapabilityMatrix(effective, false, tools, nil, false, false, false)
		if matrix.TurnEnforcement.State != tc.want || matrix.CandidatePreparation.State != tc.want {
			t.Errorf("capabilities for %s without TOML: %+v", tc.host, matrix)
		}
	}
}
