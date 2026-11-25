package runner

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/angelnicolasc/flinch/internal/finding"
)

// FuzzTestReportParsers feeds arbitrary bytes to every test-result parser.
// Whatever a test runner prints, parsing never panics and never reports a
// passing case for input that contains no passing status at all.
func FuzzTestReportParsers(f *testing.F) {
	f.Add([]byte(`{"Action":"pass","Package":"p","Test":"TestA"}`))
	f.Add([]byte(`{"testResults":[{"name":"a.test.ts","assertionResults":[{"title":"t","status":"passed"}]}]}`))
	f.Add([]byte(`<testsuite name="s"><testcase classname="c" name="t"><skipped/></testcase></testsuite>`))
	f.Add([]byte(`<testsuites><testsuite name="s"><testcase name="t"/></testsuite></testsuites>`))
	f.Add([]byte(`{"Action":"skip","Package":"p","Test":"TestA"}` + "\n" + `{"Action":"output"`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var gores, jsres, junit TestRunResult
		ParseGoTestJSON(data, &gores)
		_ = ParseJSReportJSON(data, "vitest", &jsres)
		_ = ParseJUnitXML(data, &junit)
		text := string(data)
		if strings.Contains(text, `\u`) {
			return // a JSON escape can spell "pass" without the literal bytes
		}
		if !strings.Contains(text, `"pass"`) && hasOutcome(gores, OutcomePass) {
			t.Fatalf("go test JSON reported a pass with no pass action: %q", data)
		}
		if !strings.Contains(strings.ToLower(text), "passed") && hasOutcome(jsres, OutcomePass) {
			t.Fatalf("a JS report reported a pass with no passed status: %q", data)
		}
	})
}

func hasOutcome(res TestRunResult, outcome TestOutcome) bool {
	for _, c := range res.Cases {
		if c.Outcome == outcome {
			return true
		}
	}
	return false
}

// testName turns fuzz input into a name each report format can carry intact.
// Real runners emit valid UTF-8, and encoding/json replaces invalid bytes with
// U+FFFD, so invalid bytes are dropped and the name is cut by runes, never in
// the middle of one.
func testName(raw string, i int) string {
	var runes []rune
	for _, r := range raw {
		if r != utf8.RuneError && unicode.IsPrint(r) && r != '›' && r != '/' && len(runes) < 40 {
			runes = append(runes, r)
		}
	}
	return fmt.Sprintf("Test%d%s", i, strings.Join(strings.Fields(string(runes)), "_"))
}

// FuzzTestOutcomesSurviveEachFormat builds the same cases in the Go, JS and
// JUnit formats and checks that each case keeps its outcome. A skipped,
// pending, todo or erroring case must never come back as a pass, and a pass
// must never be lost.
func FuzzTestOutcomesSurviveEachFormat(f *testing.F) {
	f.Add("retry", []byte{0, 1, 2, 3})
	f.Add("zero means immediate", []byte{2, 2, 0})
	f.Add("", []byte{3})
	f.Fuzz(func(t *testing.T, raw string, codes []byte) {
		if len(codes) == 0 || len(codes) > 8 {
			return
		}
		type tc struct {
			name string
			want TestOutcome
			code byte
		}
		var cases []tc
		for i, c := range codes {
			code := c % 4
			want := []TestOutcome{OutcomePass, OutcomeFail, OutcomeSkip, OutcomeError}[code]
			cases = append(cases, tc{testName(raw, i), want, code})
		}

		var stream strings.Builder
		for _, c := range cases {
			action := []string{"pass", "fail", "skip", "fail"}[c.code]
			for _, ev := range []map[string]string{{"Action": "run"}, {"Action": action}} {
				ev["Package"], ev["Test"] = "example.com/p", c.name
				line, _ := json.Marshal(ev)
				stream.Write(append(line, '\n'))
			}
		}
		var gores TestRunResult
		ParseGoTestJSON([]byte(stream.String()), &gores)
		if len(gores.Cases) != len(cases) {
			t.Fatalf("go test JSON: %d cases, want %d", len(gores.Cases), len(cases))
		}
		for i, c := range cases {
			want := c.want
			if want == OutcomeError {
				want = OutcomeFail // go test reports an erroring test as a failure
			}
			if gores.Cases[i].Name != c.name || gores.Cases[i].Outcome != want {
				t.Fatalf("go test JSON case %d: %+v, want %s %s", i, gores.Cases[i], c.name, want)
			}
		}

		type assertion struct {
			Title           string   `json:"title"`
			FullName        string   `json:"fullName"`
			Status          string   `json:"status"`
			FailureMessages []string `json:"failureMessages"`
		}
		var assertions []assertion
		for _, c := range cases {
			status := []string{"passed", "failed", "skipped", "todo"}[c.code]
			a := assertion{Title: c.name, FullName: c.name, Status: status}
			if c.code == 1 {
				a.FailureMessages = []string{"expected 1 to be 2"}
			}
			assertions = append(assertions, a)
		}
		report, _ := json.Marshal(map[string]any{"testResults": []any{map[string]any{"name": "retry.test.ts", "status": "passed", "assertionResults": assertions}}})
		var jsres TestRunResult
		if err := ParseJSReportJSON(report, "vitest", &jsres); err != nil || len(jsres.Cases) != len(cases) {
			t.Fatalf("JS report: %v, %d cases", err, len(jsres.Cases))
		}
		for i, c := range cases {
			want := c.want
			if c.code == 3 {
				want = OutcomeSkip // todo is a skip in a JS report
			}
			if jsres.Cases[i].Outcome != want {
				t.Fatalf("JS report case %d (%s): %s, want %s", i, c.name, jsres.Cases[i].Outcome, want)
			}
		}

		var x strings.Builder
		x.WriteString(`<testsuite name="s">`)
		for _, c := range cases {
			var name strings.Builder
			_ = xml.EscapeText(&name, []byte(c.name))
			x.WriteString(`<testcase name="` + name.String() + `">`)
			x.WriteString([]string{"", `<failure message="assert"/>`, `<skipped/>`, `<error message="boom"/>`}[c.code])
			x.WriteString(`</testcase>`)
		}
		x.WriteString(`</testsuite>`)
		var junit TestRunResult
		if err := ParseJUnitXML([]byte(x.String()), &junit); err != nil || len(junit.Cases) != len(cases) {
			t.Fatalf("JUnit: %v, %d cases", err, len(junit.Cases))
		}
		for i, c := range cases {
			if junit.Cases[i].Name != c.name || junit.Cases[i].Outcome != c.want {
				t.Fatalf("JUnit case %d: %+v, want %s %s", i, junit.Cases[i], c.name, c.want)
			}
		}
	})
}

// FuzzLinterReportParsers feeds arbitrary bytes to every linter and compiler
// parser. Parsing never panics and is deterministic.
func FuzzLinterReportParsers(f *testing.F) {
	f.Add([]byte(`[{"code":"F401","filename":"a.py","location":{"row":1,"column":1}}]`))
	f.Add([]byte(`[{"filePath":"/repo/a.js","messages":[{"ruleId":"no-undef","line":2}]}]`))
	f.Add([]byte(`{"Issues":[{"FromLinter":"errcheck","Pos":{"Filename":"a.go","Line":3}}]}`))
	f.Add([]byte(`{"ruleId":"r","file":"a.go","range":{"start":{"line":0}}}` + "\n"))
	f.Add([]byte(`{"example.com/p":{"printf":[{"posn":"/repo/a.go:3:2","message":"bad"}]}}`))
	f.Add([]byte("a.go:3:2: undefined: x\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, parse := range []func([]byte) []finding.Finding{
			func(d []byte) []finding.Finding { return ParseRuffJSON("/repo", d) },
			func(d []byte) []finding.Finding { return ParseESLintJSON("/repo", d) },
			func(d []byte) []finding.Finding { return ParseGolangCILintJSON("/repo", d) },
			func(d []byte) []finding.Finding { return ParseAstGrepJSON("/repo", d) },
			func(d []byte) []finding.Finding { return ParseGoVetJSON("/repo", d) },
			func(d []byte) []finding.Finding { return ParseCompilerDiagnostics("/repo", "go-build", "", string(d)) },
		} {
			first, second := parse(data), parse(data)
			if !reflect.DeepEqual(first, second) {
				t.Fatalf("a parser is not deterministic for %q", data)
			}
		}
	})
}

// FuzzRuffFindingsKeepRuleAndPosition builds a Ruff report from fuzz input and
// checks that each finding keeps its rule, line and normalized path.
func FuzzRuffFindingsKeepRuleAndPosition(f *testing.F) {
	f.Add("F401", "pkg/a.py", uint16(3))
	f.Add("E501", "/repo/b.py", uint16(1))
	f.Fuzz(func(t *testing.T, rule, file string, line uint16) {
		if !utf8.ValidString(rule) || !utf8.ValidString(file) {
			return // Ruff writes UTF-8 JSON; invalid bytes can't reach the parser intact
		}
		entry := map[string]any{"code": rule, "filename": file, "message": "m", "location": map[string]int{"row": int(line), "column": 1}}
		data, _ := json.Marshal([]any{entry})
		got := ParseRuffJSON("/repo", data)
		if len(got) != 1 {
			t.Fatalf("%d findings, want 1", len(got))
		}
		if got[0].Rule != rule || got[0].StartLine != int(line) || got[0].Path != finding.NormalizePath("/repo", file) {
			t.Fatalf("finding %+v lost its rule, line or path", got[0])
		}
	})
}
