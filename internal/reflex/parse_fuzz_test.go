package reflex

import (
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

// FuzzParseBytesRoundTrip feeds arbitrary YAML to the check record parser. An
// accepted record, written back the way Save writes it and parsed again,
// keeps the same fields, so a reviewed record means the same thing every time
// Flinch reads it.
func FuzzParseBytesRoundTrip(f *testing.F) {
	f.Add([]byte(`id: retrydelay-zero
version: 1
scope: '{src/retry.ts,tests/retry.test.ts}'
rung: test
obligation: Preserve behavior checked by retryDelay › treats zero as immediate.
artifact:
  tool: vitest
  path: tests/retry.test.ts
  selector: retryDelay › treats zero as immediate
cases:
  allowed: retryDelay › uses the default when omitted
surfaces:
  pull_request:
    mode: block
  agent_turn:
    mode: repair_if_supported
`))
	f.Add([]byte("id: x\nrung: lint\nartifact:\n  tool: ruff\n  rule: F401\n"))
	f.Add([]byte("version: 0\n"))
	f.Add([]byte("{}"))
	f.Fuzz(func(t *testing.T, data []byte) {
		first, err := ParseBytes(data, "fuzz.yml")
		if err != nil {
			return
		}
		written, err := yaml.Marshal(first)
		if err != nil {
			t.Fatalf("an accepted record does not encode: %v", err)
		}
		second, err := ParseBytes(written, "fuzz.yml")
		if err != nil {
			t.Fatalf("an accepted record was rejected after encoding: %v\n%s", err, written)
		}
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("the record changed through encoding:\nfirst  %+v\nsecond %+v", first, second)
		}
	})
}
