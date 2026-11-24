package runner

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseJSReportJSONHandlesVitestJestSkipTodoAndLoadFailure(t *testing.T) {
	// Valid Vitest/Jest JSON including describe > it, test.each generated name, skipped, and todo.
	reportJSON := `{
  "success": false,
  "numTotalTests": 4,
  "testResults": [
    {
      "name": "/repo/packages/retry/src/retry.test.ts",
      "status": "failed",
      "assertionResults": [
        {
          "ancestorTitles": ["retryDelay"],
          "title": "uses explicit zero (delay=0)",
          "fullName": "retryDelay uses explicit zero (delay=0)",
          "status": "failed",
          "duration": 3,
          "failureMessages": ["AssertionError: expected 1000 to be 0"]
        },
        {
          "ancestorTitles": ["retryDelay"],
          "title": "omitted delay keeps default",
          "fullName": "retryDelay omitted delay keeps default",
          "status": "passed",
          "duration": 1,
          "failureMessages": []
        },
        {
          "ancestorTitles": ["retryDelay"],
          "title": "skipped case",
          "fullName": "retryDelay skipped case",
          "status": "skipped",
          "duration": 0,
          "failureMessages": []
        },
        {
          "ancestorTitles": ["retryDelay"],
          "title": "todo case",
          "fullName": "retryDelay todo case",
          "status": "todo",
          "duration": 0,
          "failureMessages": []
        }
      ]
    }
  ]
}`
	var res TestRunResult
	if err := ParseJSReportJSON([]byte(reportJSON), "vitest", &res); err != nil {
		t.Fatalf("ParseJSReportJSON: %v", err)
	}
	if res.CompileOnly {
		t.Fatalf("did not expect CompileOnly on normal assertion failure")
	}
	if len(res.Cases) != 4 {
		t.Fatalf("expected 4 cases, got %d", len(res.Cases))
	}
	if res.Cases[0].Name != "retryDelay › uses explicit zero (delay=0)" || res.Cases[0].Outcome != OutcomeFail {
		t.Fatalf("unexpected case 0: %+v", res.Cases[0])
	}
	if res.Cases[1].Outcome != OutcomePass {
		t.Fatalf("expected pass for neighbor, got %+v", res.Cases[1])
	}
	if res.Cases[2].Outcome != OutcomeSkip || res.Cases[3].Outcome != OutcomeSkip {
		t.Fatalf("skipped and todo MUST map to OutcomeSkip, got %+v / %+v", res.Cases[2], res.Cases[3])
	}

	// Suite load/syntax failure with empty assertionResults -> CompileOnly=true.
	brokenSuite := `{"success":false,"numTotalTests":0,"testResults":[{"name":"/repo/a.test.ts","status":"failed","message":"SyntaxError: Unexpected token","assertionResults":[]}]}`
	var resErr TestRunResult
	if err := ParseJSReportJSON([]byte(brokenSuite), "jest", &resErr); err != nil {
		t.Fatalf("ParseJSReportJSON broken suite: %v", err)
	}
	if !resErr.CompileOnly {
		t.Fatalf("expected CompileOnly=true when suite fails to load")
	}

	// Vitest TS typecheck error must not count as behavioral failure (D9).
	tsErrSuite := `{"success":false,"numTotalTests":1,"testResults":[{"name":"/repo/a.test.ts","status":"failed","assertionResults":[{"ancestorTitles":["suite"],"title":"t","status":"failed","failureMessages":["error TS2345: Argument of type 'number' is not assignable"]}]}]}`
	var resTS TestRunResult
	if err := ParseJSReportJSON([]byte(tsErrSuite), "vitest", &resTS); err != nil {
		t.Fatal(err)
	}
	if !resTS.CompileOnly {
		t.Fatalf("expected TS error to set CompileOnly=true")
	}
}

func TestBuildJSTestNameRegexAndMonorepoPackageDiscovery(t *testing.T) {
	re := BuildJSTestNameRegex("retryDelay › uses explicit zero (delay=0)")
	if re != `^retryDelay\s+'?uses explicit zero \(delay=0\)'?$` {
		t.Fatalf("unexpected regex: %q", re)
	}

	root := t.TempDir()
	pkgDir := filepath.Join(root, "packages", "retry")
	testDir := filepath.Join(pkgDir, "src")
	if err := os.MkdirAll(testDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"monorepo","private":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"name":"@demo/retry"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(testDir, "retry.test.ts"), []byte("// test\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	absPkg, relPkg := findOwningPackageDir(root, "packages/retry/src/retry.test.ts")
	if relPkg != "packages/retry" || cleanAbs(absPkg) != cleanAbs(pkgDir) {
		t.Fatalf("expected owning package packages/retry, got rel=%q abs=%q", relPkg, absPkg)
	}
}

// TestLiveNodeStubVitestAndJestFileReporter verifies file-based JSON reporting (immune to noisy stdout)
// and workspace CWD execution using the locally installed Node runtime (D9).
func TestLiveNodeStubVitestAndJestFileReporter(t *testing.T) {
	nodeInfo := ProbeTool(context.Background(), []string{"node"}, "--version")
	if !nodeInfo.Available {
		t.Skip("node not installed on PATH")
	}
	root := t.TempDir()
	pkgDir := filepath.Join(root, "packages", "retry")
	binDir := filepath.Join(root, "node_modules", ".bin")
	if err := os.MkdirAll(filepath.Join(pkgDir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"root"}`), 0o644)
	_ = os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"name":"@demo/retry"}`), 0o644)
	_ = os.WriteFile(filepath.Join(pkgDir, "src", "retry.test.ts"), []byte("// test\n"), 0o644)

	// Node script simulating vitest/jest: emits noisy stdout logs AND writes structured JSON to --outputFile.
	jsStub := `const fs = require('fs');
console.log('NOISY STDOUT LOG THAT WOULD BREAK STDOUT JSON PARSING {{{');
const outArg = process.argv.find(a => a.startsWith('--outputFile='));
const outPath = outArg.slice('--outputFile='.length);
const cwd = process.cwd().replace(/\\/g, '/');
const okCwd = cwd.endsWith('/packages/retry');
const report = {
  success: okCwd,
  numTotalTests: 1,
  testResults: [{
    name: cwd + '/src/retry.test.ts',
    status: okCwd ? 'passed' : 'failed',
    assertionResults: [{
      ancestorTitles: ['retryDelay'],
      title: 'explicit zero returns 0',
      fullName: 'retryDelay explicit zero returns 0',
      status: okCwd ? 'passed' : 'failed',
      duration: 2,
      failureMessages: okCwd ? [] : ['wrong cwd: ' + cwd]
    }]
  }]
};
fs.writeFileSync(outPath, JSON.stringify(report));
process.exit(okCwd ? 0 : 1);
`
	stubJSPath := filepath.Join(binDir, "stub.js")
	if err := os.WriteFile(stubJSPath, []byte(jsStub), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"vitest", "jest"} {
		if runtime.GOOS == "windows" {
			shim := "@echo off\r\nnode \"" + stubJSPath + "\" %*\r\n"
			if err := os.WriteFile(filepath.Join(binDir, kind+".cmd"), []byte(shim), 0o755); err != nil {
				t.Fatal(err)
			}
		} else {
			shim := "#!/bin/sh\nexec node \"" + stubJSPath + "\" \"$@\"\n"
			if err := os.WriteFile(filepath.Join(binDir, kind), []byte(shim), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}

	ctx := context.Background()
	vRes, err := RunVitest(ctx, root, root, []string{"packages/retry/src/retry.test.ts"}, "retryDelay › explicit zero returns 0", 15*time.Second)
	if err != nil {
		t.Fatalf("RunVitest failed: %v", err)
	}
	if len(vRes.Cases) != 1 || vRes.Cases[0].Outcome != OutcomePass {
		t.Fatalf("expected passing vitest case despite noisy stdout, got %+v", vRes)
	}

	jRes, err := RunJest(ctx, root, root, []string{"packages/retry/src/retry.test.ts"}, "retryDelay › explicit zero returns 0", 15*time.Second)
	if err != nil {
		t.Fatalf("RunJest failed: %v", err)
	}
	if len(jRes.Cases) != 1 || jRes.Cases[0].Outcome != OutcomePass {
		t.Fatalf("expected passing jest case despite noisy stdout, got %+v", jRes)
	}
}

// Regression test (D11, P10): Candidate commands receive a filtered environment without user secrets.
func TestFilteredEnvStripsSecretsAndScopesTempDir(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-super-secret-value")
	t.Setenv("GITHUB_TOKEN", "ghp_supersecret1234567890abcdef")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-secret")

	proofDir := t.TempDir()
	env := FilteredEnv(proofDir)
	joined := strings.Join(env, "\n")
	for _, leaked := range []string{"ANTHROPIC_API_KEY", "GITHUB_TOKEN", "AWS_SECRET_ACCESS_KEY", "sk-ant-super-secret-value", "ghp_supersecret"} {
		if strings.Contains(joined, leaked) {
			t.Fatalf("FilteredEnv leaked secret %q", leaked)
		}
	}
	if _, err := os.Stat(filepath.Join(proofDir, ".flinch-tmp")); !os.IsNotExist(err) {
		t.Fatal("constructing the environment changed the working tree")
	}
}

func TestLinkNodeModulesNormalizesCRLFOnWindowsAutocrlf(t *testing.T) {
	repo := t.TempDir()
	proof := t.TempDir()
	_ = os.MkdirAll(filepath.Join(repo, "node_modules"), 0o755)
	_ = os.WriteFile(filepath.Join(repo, "package-lock.json"), []byte("{\n  \"lockfileVersion\": 3\n}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(proof, "package-lock.json"), []byte("{\r\n  \"lockfileVersion\": 3\r\n}\r\n"), 0o644)

	cleanup, err := linkNodeModulesIfLockfileMatches(repo, proof, "")
	if err != nil {
		t.Fatalf("CRLF vs LF lockfile must match cleanly: %v", err)
	}
	defer cleanup()
}
