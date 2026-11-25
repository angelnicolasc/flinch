package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/reflex"
)

func TestParseGoTestJSONSeparatesBuildFailSkipAndSubtests(t *testing.T) {
	stream := `{"Action":"output","Package":"p","Test":"TestMath/add","Output":"    math_test.go:12: got 5 want 4\n"}
{"Action":"fail","Package":"p","Test":"TestMath/add","Elapsed":0.01}
{"Action":"skip","Package":"p","Test":"TestMath/skip_me","Elapsed":0.0}
{"Action":"pass","Package":"p","Test":"TestMath/sub","Elapsed":0.0}
{"Action":"build-output","ImportPath":"p/broken","Output":"./broken.go:4:2: undefined: NewHelper\n"}
{"Action":"build-fail","ImportPath":"p/broken"}
`
	var res TestRunResult
	ParseGoTestJSON([]byte(stream), &res)
	if !res.CompileOnly {
		t.Fatalf("expected CompileOnly=true on build-fail event")
	}
	if len(res.Cases) != 3 {
		t.Fatalf("expected 3 cases, got %d", len(res.Cases))
	}
	if res.Cases[0].Outcome != OutcomeFail || res.Cases[1].Outcome != OutcomeSkip || res.Cases[2].Outcome != OutcomePass {
		t.Fatalf("unexpected outcomes: %+v", res.Cases)
	}
}

func TestParseJUnitXMLSeparatesCollectionErrorFailureAndSkip(t *testing.T) {
	xmlData := `<?xml version="1.0" encoding="utf-8"?>
<testsuites>
  <testsuite name="pytest">
    <testcase classname="test_mod" name="test_fail" time="0.01">
      <failure message="assert 1 == 2">AssertionError</failure>
    </testcase>
    <testcase classname="test_mod" name="test_skip" time="0.00">
      <skipped message="conditional skip"/>
    </testcase>
    <testcase classname="" name="test_broken_import" time="0.00">
      <error message="collection failure">ImportError: cannot import name 'new_fn'</error>
    </testcase>
  </testsuite>
</testsuites>`
	var res TestRunResult
	if err := ParseJUnitXML([]byte(xmlData), &res); err != nil {
		t.Fatalf("ParseJUnitXML failed: %v", err)
	}
	if !res.CompileOnly {
		t.Fatalf("expected CompileOnly=true on pytest collection failure")
	}
	if len(res.Cases) != 3 {
		t.Fatalf("expected 3 cases, got %d", len(res.Cases))
	}
	if res.Cases[0].Outcome != OutcomeFail || res.Cases[1].Outcome != OutcomeSkip || res.Cases[2].Outcome != OutcomeError {
		t.Fatalf("unexpected cases: %+v", res.Cases)
	}
}

func TestParseLinterOutputs(t *testing.T) {
	// go vet -json
	vetOut := `{"example.com/p":{"printf":[{"posn":"/repo/main.go:14:2","message":"fmt.Printf format %d has arg s of wrong type string"}]}}`
	vetFindings := ParseGoVetJSON("/repo", []byte(vetOut))
	if len(vetFindings) != 1 || vetFindings[0].Rule != "printf" || vetFindings[0].StartLine != 14 {
		t.Fatalf("unexpected go vet findings: %#v", vetFindings)
	}

	// ast-grep NDJSON (0-based -> 1-based line conversion)
	sgOut := `{"ruleId":"unbounded-read","file":"pkg/client.go","message":"limit body","severity":"error","text":"io.ReadAll(resp.Body)","range":{"start":{"line":9,"column":4},"end":{"line":9,"column":25}}}`
	sgFindings := ParseAstGrepJSON("", []byte(sgOut))
	if len(sgFindings) != 1 || sgFindings[0].StartLine != 10 || sgFindings[0].Rule != "unbounded-read" {
		t.Fatalf("unexpected ast-grep findings: %#v", sgFindings)
	}

	// eslint JSON
	esOut := `[{"filePath":"/repo/src/renderer/app.ts","messages":[{"ruleId":"no-restricted-imports","severity":2,"message":"use IPC bridge","line":3,"endLine":3,"column":1}]}]`
	esFindings := ParseESLintJSON("/repo", []byte(esOut))
	if len(esFindings) != 1 || esFindings[0].Rule != "no-restricted-imports" || esFindings[0].StartLine != 3 {
		t.Fatalf("unexpected eslint findings: %#v", esFindings)
	}
}

func TestLiveGoTestRunner(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/sample\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sample_test.go"), []byte(`package sample

import "testing"

func TestDemo(t *testing.T) {
	t.Run("ok", func(t *testing.T) {})
	t.Run("skipped", func(t *testing.T) { t.Skip("not now") })
}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := RunGoTest(context.Background(), dir, []string{"."}, "TestDemo/ok", 30*time.Second)
	if err != nil {
		t.Fatalf("RunGoTest failed: %v", err)
	}
	if res.ExitCode != 0 || res.CompileOnly {
		t.Fatalf("unexpected result: %+v", res)
	}
	foundOK := false
	for _, c := range res.Cases {
		if c.Name == "TestDemo/ok" && c.Outcome == OutcomePass {
			foundOK = true
		}
	}
	if !foundOK {
		t.Fatalf("expected TestDemo/ok pass, got %+v", res.Cases)
	}
}

// Regression test: Executing a candidate command via RunArtifact must not expose user secrets.
func TestCommandArtifactUsesFilteredEnvironment(t *testing.T) {
	t.Setenv("FLINCH_TEST_SECRET_KEY", "never-leak-this-secret")
	dir := t.TempDir()
	if _, _, err := gitutil.Run(context.Background(), dir, nil, "init"); err != nil {
		t.Fatal(err)
	}
	path, script, command := "environment.cmd", "@echo off\r\necho %FLINCH_TEST_SECRET_KEY%\r\n", `.\environment.cmd`
	if os.PathSeparator == '/' {
		path, script, command = "environment.sh", "echo \"$FLINCH_TEST_SECRET_KEY\"\n", "sh ./environment.sh"
	}
	if err := os.WriteFile(filepath.Join(dir, path), []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	art := reflex.Artifact{Tool: "command", Path: path, Command: command}
	if _, err := AllowCandidateCommand(context.Background(), dir, art, nil); err != nil {
		t.Fatal(err)
	}
	res, err := RunArtifact(context.Background(), dir, art, nil, 10*time.Second)
	if err != nil {
		t.Fatalf("RunArtifact failed: %v", err)
	}
	if strings.Contains(res.Stdout+res.Stderr, "never-leak-this-secret") {
		t.Fatalf("command artifact leaked parent env secret: stdout=%q stderr=%q", res.Stdout, res.Stderr)
	}
}
