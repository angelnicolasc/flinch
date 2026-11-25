package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// FuzzLoadFileRoundTrip feeds arbitrary TOML to the policy loader. An accepted
// configuration saved with Save and loaded again is identical, so the
// settings init writes for a team mean the same thing to every clone.
func FuzzLoadFileRoundTrip(f *testing.F) {
	f.Add([]byte("[policy]\nretention_days = 30\n"))
	f.Add([]byte("[runners.vitest]\ncommand = \"npx vitest\"\n"))
	f.Add([]byte("unknown = 1\n"))
	f.Add([]byte(""))
	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, data []byte) {
		input := filepath.Join(dir, "input.toml")
		if err := os.WriteFile(input, data, 0o600); err != nil {
			t.Fatal(err)
		}
		first, err := LoadFile(input)
		if err != nil {
			return
		}
		repo := t.TempDir()
		if err := Save(repo, first); err != nil {
			t.Fatalf("an accepted configuration does not save: %v", err)
		}
		second, err := LoadFile(filepath.Join(repo, filepath.FromSlash(ConfigRelPath)))
		if err != nil {
			t.Fatalf("a saved configuration does not load: %v", err)
		}
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("the configuration changed through Save:\nfirst  %+v\nsecond %+v", first, second)
		}
	})
}
