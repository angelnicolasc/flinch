package runner

import (
	"runtime"
	"slices"
	"strings"
	"testing"
)

// FuzzLiteralCommandWords checks the contract command approval relies on.
// An accepted command has a non-empty executable, no word that any supported
// shell would expand or redirect, and words that survive an independent
// re-quoting: rebuilding the command from the words gives the same words back.
func FuzzLiteralCommandWords(f *testing.F) {
	for _, seed := range []string{
		"go test ./...", `sh "scripts/check guard.sh"`, "echo ok", "python3 tools/check.py --fast",
		`node "a b.js" ""`, "a;b", "a && b", "$(x)", "`x`", "%PATH%", "x > y", "'single'",
		`"unterminated`, `x\y`, "cmd /c check.cmd", "pwsh -file check.ps1", "\t go\tvet ",
		`"quoted"tail`, `say 'it"s'`,
	} {
		f.Add(seed)
	}
	const forbidden = "$%`^!;&|<>(){}[]*?~\r\n\x00"
	f.Fuzz(func(t *testing.T, command string) {
		words, err := literalCommandWords(command)
		if err != nil {
			return
		}
		if len(words) == 0 || words[0] == "" {
			t.Fatalf("%q accepted without an executable: %q", command, words)
		}
		for _, w := range words {
			if strings.ContainsAny(w, forbidden) {
				t.Fatalf("%q accepted a shell metacharacter in %q", command, w)
			}
			if runtime.GOOS != "windows" && strings.Contains(w, `\`) {
				t.Fatalf("%q accepted a backslash in %q", command, w)
			}
		}
		parts := make([]string, 0, len(words))
		for _, w := range words {
			switch {
			case strings.Contains(w, `"`):
				parts = append(parts, "'"+w+"'")
			case w == "" || strings.ContainsAny(w, " \t'"):
				parts = append(parts, `"`+w+`"`)
			default:
				parts = append(parts, w)
			}
		}
		again, err := literalCommandWords(strings.Join(parts, " "))
		if err != nil || !slices.Equal(again, words) {
			t.Fatalf("%q gave %q, but re-quoting gave %q (%v)", command, words, again, err)
		}
	})
}
