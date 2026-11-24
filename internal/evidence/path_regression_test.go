package evidence

import (
	"errors"
	"os"
	"testing"
)

func TestRegressionEvidenceRequiresLocalIDAndKnownSurface(t *testing.T) {
	s := NewStore(t.TempDir())
	for _, rec := range []ActivationRecord{
		{ReflexID: "one/two", Surface: SurfaceCI},
		{ReflexID: "..\\two", Surface: SurfaceCI},
		{ReflexID: "one", Surface: SurfaceKind("../unknown")},
	} {
		if _, err := s.Load(rec.ReflexID, rec.Surface); err == nil || errors.Is(err, os.ErrNotExist) {
			t.Errorf("invalid evidence key reached filesystem lookup: %q %q (%v)", rec.ReflexID, rec.Surface, err)
		}
		if _, err := s.Save(rec); err == nil {
			t.Errorf("saved invalid evidence key: %q %q", rec.ReflexID, rec.Surface)
		}
	}
}
