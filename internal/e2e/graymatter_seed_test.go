package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/angelnicolasc/flinch/internal/check"
	"github.com/angelnicolasc/flinch/internal/cli"
	"github.com/angelnicolasc/flinch/internal/config"
	"github.com/angelnicolasc/flinch/internal/evidence"
	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/prove"
	"github.com/angelnicolasc/flinch/internal/reflex"
)

func gitCommit(t *testing.T, repo, msg string) string {
	t.Helper()
	ctx := context.Background()
	env := []string{
		"GIT_AUTHOR_NAME=seed-maintainer",
		"GIT_AUTHOR_EMAIL=seed@example.com",
		"GIT_COMMITTER_NAME=seed-maintainer",
		"GIT_COMMITTER_EMAIL=seed@example.com",
	}
	if _, _, err := gitutil.Run(ctx, repo, env, "add", "-A"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, _, err := gitutil.Run(ctx, repo, env, "-c", "commit.gpgsign=false", "commit", "-m", msg); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	sha, _, err := gitutil.HeadSHA(ctx, repo)
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}
	return sha
}

func writeRepoFile(t *testing.T, repo, rel, content string) {
	t.Helper()
	abs := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// naiveBodyAnalyzerSource flags every io.ReadAll(x.Body) call indiscriminately,
// including locally constructed 2-byte in-memory responses (§11 audit counterexample).
const naiveBodyAnalyzerSource = `package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type diag struct {
	Rule      string ` + "`json:\"rule\"`" + `
	Selector  string ` + "`json:\"selector\"`" + `
	Path      string ` + "`json:\"path\"`" + `
	StartLine int    ` + "`json:\"start_line\"`" + `
	EndLine   int    ` + "`json:\"end_line\"`" + `
	Message   string ` + "`json:\"message\"`" + `
}

func main() {
	var out []diag
	fset := token.NewFileSet()
	_ = filepath.WalkDir("pkg", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "ReadAll" {
				return true
			}
			pkgIdent, ok := sel.X.(*ast.Ident)
			if !ok || pkgIdent.Name != "io" {
				return true
			}
			argSel, ok := call.Args[0].(*ast.SelectorExpr)
			if ok && argSel.Sel.Name == "Body" {
				pos := fset.Position(call.Pos())
				out = append(out, diag{
					Rule:      "unbounded-response-body-read",
					Selector:  "unbounded-response-body-read",
					Path:      filepath.ToSlash(path),
					StartLine: pos.Line,
					EndLine:   pos.Line,
					Message:   "unbounded io.ReadAll on response Body; wrap with io.LimitReader",
				})
			}
			return true
		})
		return nil
	})
	_ = json.NewEncoder(os.Stdout).Encode(out)
	if len(out) > 0 {
		os.Exit(1)
	}
}
`

// refinedBodyAnalyzerSource flags unbounded io.ReadAll(resp.Body) calls on network responses
// while permitting responses whose Body is locally constructed in the same function with
// io.NopCloser(bytes.NewReader(...)) / strings.NewReader(...) (§11 2-byte near-miss case).
const refinedBodyAnalyzerSource = `package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type diag struct {
	Rule      string ` + "`json:\"rule\"`" + `
	Selector  string ` + "`json:\"selector\"`" + `
	Path      string ` + "`json:\"path\"`" + `
	StartLine int    ` + "`json:\"start_line\"`" + `
	EndLine   int    ` + "`json:\"end_line\"`" + `
	Message   string ` + "`json:\"message\"`" + `
}

func isLocalInMemoryResponse(fn *ast.FuncDecl, identName string) bool {
	if fn == nil || fn.Body == nil {
		return false
	}
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		lhs, ok := assign.Lhs[0].(*ast.Ident)
		if !ok || lhs.Name != identName {
			return true
		}
		// Match &http.Response{Body: io.NopCloser(...)} or http.Response{Body: io.NopCloser(...)}
		rhs := assign.Rhs[0]
		if un, ok := rhs.(*ast.UnaryExpr); ok {
			rhs = un.X
		}
		comp, ok := rhs.(*ast.CompositeLit)
		if !ok {
			return true
		}
		for _, elt := range comp.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			k, ok := kv.Key.(*ast.Ident)
			if !ok || k.Name != "Body" {
				continue
			}
			call, ok := kv.Value.(*ast.CallExpr)
			if !ok {
				continue
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "NopCloser" {
				found = true
			}
		}
		return true
	})
	return found
}

func main() {
	var out []diag
	fset := token.NewFileSet()
	_ = filepath.WalkDir("pkg", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) != 1 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "ReadAll" {
					return true
				}
				pkgIdent, ok := sel.X.(*ast.Ident)
				if !ok || pkgIdent.Name != "io" {
					return true
				}
				argSel, ok := call.Args[0].(*ast.SelectorExpr)
				if !ok || argSel.Sel.Name != "Body" {
					return true
				}
				if xIdent, ok := argSel.X.(*ast.Ident); ok && isLocalInMemoryResponse(fn, xIdent.Name) {
					return true
				}
				pos := fset.Position(call.Pos())
				out = append(out, diag{
					Rule:      "unbounded-response-body-read",
					Selector:  "unbounded-response-body-read",
					Path:      filepath.ToSlash(path),
					StartLine: pos.Line,
					EndLine:   pos.Line,
					Message:   "unbounded io.ReadAll on response Body; wrap with io.LimitReader",
				})
				return true
			})
		}
		return nil
	})
	_ = json.NewEncoder(os.Stdout).Encode(out)
	if len(out) > 0 {
		os.Exit(1)
	}
}
`

const brokenPR27Client = `package provider

import (
	"io"
	"net/http"
)

func FetchCompletion(client *http.Client, url string) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
`

const fixedPR27Client = `package provider

import (
	"io"
	"net/http"
)

const maxResponseBodyBytes = 10 << 20

func FetchCompletion(client *http.Client, url string) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes))
}
`

// §11 legitimate 2-byte locally constructed in-memory response near-miss case.
const allowedTwoByteCase = `package provider

import (
	"bytes"
	"io"
	"net/http"
)

func ReadLocalTwoByteProbe() ([]byte, error) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader([]byte("ok"))),
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
`

// Historical GrayMatter #55 reintroduction variant in a different package/file.
const reintroPR55Consolidate = `package memory

import (
	"io"
	"net/http"
)

func Consolidate(client *http.Client, endpoint string) ([]byte, error) {
	resp, err := client.Get(endpoint)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
`

const fixedPR55Consolidate = `package memory

import (
	"io"
	"net/http"
)

const maxConsolidateBytes = 4 << 20

func Consolidate(client *http.Client, endpoint string) ([]byte, error) {
	resp, err := client.Get(endpoint)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, maxConsolidateBytes))
}
`

// TestGrayMatterSeed27And55EndToEnd implements the §10.1 reproducible validation harness:
//  1. Reconstructs GrayMatter #27 (broken unbounded io.ReadAll -> fixed io.LimitReader).
//  2. Refutes an overbroad candidate rule that flags the §11 2-byte in-memory allowed case.
//  3. Verifies the refined repository-native analyzer via `flinch prove`.
//  4. Accredits the integrated SHA via `flinch check --accredit` (trigger_pending -> active with PR witness).
//  5. Clones into a clean second repository and verifies that the #55 reintroduction variant
//     in pkg/memory/consolidate.go is blocked both on `--turn` and `--ci`, while the 2-byte
//     legitimate case and bounded fix pass cleanly.
func TestGrayMatterSeed27And55EndToEnd(t *testing.T) {
	ctx := context.Background()
	originRepo := t.TempDir()
	if _, _, err := gitutil.Run(ctx, originRepo, nil, "init"); err != nil {
		t.Fatal(err)
	}

	writeRepoFile(t, originRepo, "go.mod", "module example.com/graymatter-seed\n\ngo 1.26\n")
	writeRepoFile(t, originRepo, "pkg/provider/client.go", brokenPR27Client)
	brokenPR27SHA := gitCommit(t, originRepo, "base before PR #27 (unbounded response read)")

	writeRepoFile(t, originRepo, "pkg/provider/client.go", fixedPR27Client)
	fixedPR27SHA := gitCommit(t, originRepo, "fix: bound provider HTTP response body reads (PR #27)")

	// Step A: Prove rejects the overbroad naive rule because it flags the §11 2-byte allowed case.
	naiveReport, err := prove.Verify(ctx, originRepo, prove.CandidateSpec{
		ID:         "unbounded-response-body-read",
		Version:    1,
		Rung:       reflex.RungLint,
		Scope:      "pkg/**",
		Obligation: "HTTP network response bodies must be bounded with io.LimitReader before io.ReadAll.",
		Artifact: reflex.Artifact{
			Tool:           "command",
			Path:           "tools/bodycheck/main.go",
			Rule:           "unbounded-response-body-read",
			Selector:       "unbounded-response-body-read",
			Command:        "go run ./tools/bodycheck",
			ExpectedSignal: "unbounded io.ReadAll",
		},
		OverlayFiles: map[string]string{
			"tools/bodycheck/main.go": naiveBodyAnalyzerSource,
		},
		AllowedCases: []prove.AllowedCase{
			{
				Name:       "local-two-byte-response",
				TargetPath: "pkg/provider/allowed_two_bytes.go",
				Content:    allowedTwoByteCase,
			},
		},
		BrokenRef: brokenPR27SHA,
		FixedRef:  fixedPR27SHA,
	}, prove.Options{})
	if err != nil {
		t.Fatalf("prove naive failed: %v", err)
	}
	if naiveReport.Verdict != prove.VerdictRefuted || naiveReport.Reason != prove.ReasonFlaggedAllowedCase {
		t.Fatalf("expected naive rule to be refuted for flagging 2-byte allowed case, got %s/%s (%s)",
			naiveReport.Verdict, naiveReport.Reason, naiveReport.ActionableError)
	}

	// Step B: Prove verifies the refined rule against #27 broken, #27 fixed, and the §11 2-byte allowed case.
	refinedReport, err := prove.Verify(ctx, originRepo, prove.CandidateSpec{
		ID:         "unbounded-response-body-read",
		Version:    1,
		Rung:       reflex.RungLint,
		Scope:      "pkg/**",
		Obligation: "HTTP network response bodies must be bounded with io.LimitReader before io.ReadAll.",
		Artifact: reflex.Artifact{
			Tool:           "command",
			Path:           "tools/bodycheck/main.go",
			Rule:           "unbounded-response-body-read",
			Selector:       "unbounded-response-body-read",
			Command:        "go run ./tools/bodycheck",
			ExpectedSignal: "unbounded io.ReadAll",
			FixHint:        "io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes))",
		},
		OverlayFiles: map[string]string{
			"tools/bodycheck/main.go": refinedBodyAnalyzerSource,
		},
		AllowedCases: []prove.AllowedCase{
			{
				Name:       "local-two-byte-response",
				TargetPath: "pkg/provider/allowed_two_bytes.go",
				Content:    allowedTwoByteCase,
			},
		},
		BrokenRef: brokenPR27SHA,
		FixedRef:  fixedPR27SHA,
	}, prove.Options{})
	if err != nil {
		t.Fatalf("prove refined failed: %v", err)
	}
	if refinedReport.Verdict != prove.VerdictVerified {
		t.Fatalf("expected refined rule verified, got %s/%s (%s)",
			refinedReport.Verdict, refinedReport.Reason, refinedReport.ActionableError)
	}
	if refinedReport.Census != 0 {
		t.Fatalf("expected census=0 on fixed tree, got %d", refinedReport.Census)
	}

	// Step C: Integrate the verified native analyzer, fixtures, and reflex record into originRepo.
	cfg := config.Default()
	cfg.Policy.Owner = "core-platform"
	if err := config.Save(originRepo, cfg); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, originRepo, "tools/bodycheck/main.go", refinedBodyAnalyzerSource)
	writeRepoFile(t, originRepo, ".flinch/cases/unbounded-response-body-read/broken.go", brokenPR27Client)
	writeRepoFile(t, originRepo, ".flinch/cases/unbounded-response-body-read/fixed.go", fixedPR27Client)
	writeRepoFile(t, originRepo, ".flinch/cases/unbounded-response-body-read/allowed-two-bytes.go", allowedTwoByteCase)

	r := reflex.Reflex{
		ID:         "unbounded-response-body-read",
		Version:    1,
		Owner:      "core-platform",
		Scope:      "pkg/**",
		Rung:       reflex.RungLint,
		Obligation: "HTTP network response bodies must be bounded with io.LimitReader before io.ReadAll.",
		Artifact: reflex.Artifact{
			Tool:           "command",
			Path:           "tools/bodycheck/main.go",
			Rule:           "unbounded-response-body-read",
			Selector:       "unbounded-response-body-read",
			Command:        "go run ./tools/bodycheck",
			ExpectedSignal: "unbounded io.ReadAll",
			FixHint:        "io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes))",
		},
		Cases: reflex.Cases{
			TargetPath: "pkg/provider/flinch_case_probe.go",
			Broken:     ".flinch/cases/unbounded-response-body-read/broken.go",
			Fixed:      ".flinch/cases/unbounded-response-body-read/fixed.go",
			Allowed:    reflex.StringList{".flinch/cases/unbounded-response-body-read/allowed-two-bytes.go"},
		},
		Surfaces: reflex.Surfaces{
			PullRequest: &reflex.PRSurface{Job: "lint-go", Mode: "block"},
			AgentTurn:   &reflex.TurnSurface{Mode: "repair_if_supported"},
		},
		Incidents: []reflex.IncidentRef{
			{Source: "fix", PR: 27, Broken: brokenPR27SHA, Fixed: fixedPR27SHA},
		},
		Proof: &reflex.ProofSummary{
			Verdict:    string(refinedReport.Verdict),
			Census:     refinedReport.Census,
			RuntimeMS:  refinedReport.TotalRuntimeMS,
			ReportHash: refinedReport.CandidateHash,
		},
	}
	fp, err := r.ComputeFingerprint(originRepo)
	if err != nil {
		t.Fatal(err)
	}
	r.Fingerprint = fp
	if _, err := reflex.Save(originRepo, r); err != nil {
		t.Fatal(err)
	}
	integratedSHA := gitCommit(t, originRepo, "add verified unbounded-response-body-read protection")

	// Step D: Accredit the integrated SHA through CLI (`flinch check --accredit`).
	var out, errBuf bytes.Buffer
	code := cli.Run(ctx, []string{
		"check", "--repo", originRepo, "--accredit",
		"--ref", integratedSHA, "--surface", "ci", "--format", "json",
	}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("accredit failed (%d): out=%s err=%s", code, out.String(), errBuf.String())
	}
	var accRecs []evidence.ActivationRecord
	if err := json.Unmarshal(out.Bytes(), &accRecs); err != nil || len(accRecs) != 1 {
		t.Fatalf("decode accreditation records: %v (raw=%s)", err, out.String())
	}
	if accRecs[0].Status != evidence.SurfaceTriggerPending {
		t.Fatalf("expected trigger_pending before PR trigger witness, got %s", accRecs[0].Status)
	}
	if accRecs[0].NegativeControl.AttributedID != "unbounded-response-body-read" {
		t.Fatalf("expected negative control attributed to unbounded-response-body-read, got %+v", accRecs[0].NegativeControl)
	}

	// Record real PR trigger witness -> active!
	out.Reset()
	errBuf.Reset()
	code = cli.Run(ctx, []string{
		"check", "--repo", originRepo, "--accredit",
		"--ref", integratedSHA, "--surface", "ci",
		"--required-for-merge",
		"--witness-event", "pull_request",
		"--witness-job", "lint-go",
		"--witness-run", "gh-run-1001",
		"--format", "json",
	}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("accredit with witness failed (%d): %s", code, errBuf.String())
	}
	if err := json.Unmarshal(out.Bytes(), &accRecs); err != nil || accRecs[0].Status != evidence.SurfaceActive {
		t.Fatalf("expected active after PR witness, got %+v (err=%v)", accRecs, err)
	}

	// Step E: Clean second clone with zero shared state receives the integrated protection,
	// catches the GrayMatter #55 reintroduction variant in pkg/memory/consolidate.go on both
	// `--turn` and `--ci`, and passes once fixed alongside the 2-byte legitimate near-miss case.
	secondClone := filepath.Join(t.TempDir(), "clone-2")
	if _, _, err := gitutil.Run(ctx, filepath.Dir(secondClone), nil, "clone", originRepo, secondClone); err != nil {
		t.Fatalf("git clone to second clone: %v", err)
	}

	// Capture turn checkpoint in second clone.
	out.Reset()
	errBuf.Reset()
	code = cli.Run(ctx, []string{
		"checkpoint", "--repo", secondClone, "--session", "clone2-codex", "--prompt", "implement memory consolidation",
	}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("checkpoint in clone2 failed (%d): %s", code, errBuf.String())
	}

	// Introduce #55 reintroduction variant in pkg/memory/consolidate.go + legitimate 2-byte case.
	writeRepoFile(t, secondClone, "pkg/memory/consolidate.go", reintroPR55Consolidate)
	writeRepoFile(t, secondClone, "pkg/memory/probe_allowed.go", strings.Replace(allowedTwoByteCase, "package provider", "package memory", 1))

	// Turn check in Clone 2 MUST block with actionable diagnostic pointing to Consolidate and LimitReader!
	out.Reset()
	errBuf.Reset()
	code = cli.Run(ctx, []string{
		"check", "--repo", secondClone, "--turn", "--session", "clone2-codex", "--format", "json",
	}, &out, &errBuf)
	if code != 1 {
		t.Fatalf("expected turn check exit 1 (block) on #55 variant, got %d: out=%s err=%s", code, out.String(), errBuf.String())
	}
	var turnRes check.Result
	if err := json.Unmarshal(out.Bytes(), &turnRes); err != nil {
		t.Fatalf("decode turn check result: %v", err)
	}
	if !turnRes.Blocking || len(turnRes.Findings) != 1 {
		t.Fatalf("expected exactly 1 blocking finding (ignoring 2-byte allowed file), got %+v", turnRes)
	}
	if turnRes.Findings[0].Finding.Path != "pkg/memory/consolidate.go" || turnRes.Findings[0].Finding.Symbol != "Consolidate" {
		t.Fatalf("unexpected finding location/symbol: %+v", turnRes.Findings[0].Finding)
	}
	if !strings.Contains(turnRes.DiagnosticMessage, "io.LimitReader") {
		t.Fatalf("expected LimitReader fix hint in diagnostic: %s", turnRes.DiagnosticMessage)
	}

	// Shared CI check in Clone 2 (works even without any local agent plugin or checkpoint!) MUST also block!
	out.Reset()
	errBuf.Reset()
	code = cli.Run(ctx, []string{
		"check", "--repo", secondClone, "--ci", "--format", "github",
	}, &out, &errBuf)
	if code != 1 || !strings.Contains(out.String(), "::error file=pkg/memory/consolidate.go") {
		t.Fatalf("expected CI check to block with GitHub annotation on #55 variant, code=%d out=%s err=%s", code, out.String(), errBuf.String())
	}

	// Fix #55 with io.LimitReader while keeping the 2-byte allowed file -> both `--turn` and `--ci` pass!
	writeRepoFile(t, secondClone, "pkg/memory/consolidate.go", fixedPR55Consolidate)
	out.Reset()
	errBuf.Reset()
	code = cli.Run(ctx, []string{
		"check", "--repo", secondClone, "--turn", "--session", "clone2-codex",
	}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("expected turn check exit 0 after fixing #55, got %d: out=%s err=%s", code, out.String(), errBuf.String())
	}

	out.Reset()
	errBuf.Reset()
	code = cli.Run(ctx, []string{
		"check", "--repo", secondClone, "--ci",
	}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("expected CI check exit 0 after fixing #55, got %d: out=%s err=%s", code, out.String(), errBuf.String())
	}
}

// TestLivePytestAndRuffVerification validates pytest and ruff integrations against real local binaries when installed.
func TestLivePytestAndRuffVerification(t *testing.T) {
	if _, err := exec.LookPath("pytest"); err != nil {
		t.Skip("pytest not installed on PATH")
	}
	ctx := context.Background()
	repo := t.TempDir()
	if _, _, err := gitutil.Run(ctx, repo, nil, "init"); err != nil {
		t.Fatal(err)
	}

	writeRepoFile(t, repo, "calc.py", "def clamp(n, maximum):\n    return n\n")
	brokenSHA := gitCommit(t, repo, "broken python clamp")

	writeRepoFile(t, repo, "calc.py", "def clamp(n, maximum):\n    return min(n, maximum)\n")
	writeRepoFile(t, repo, "test_calc.py", "import pytest\nfrom calc import clamp\n\ndef test_caps_above_max():\n    assert clamp(10, 5) == 5\n\ndef test_skip_on_broken():\n    if clamp(10, 5) != 5:\n        pytest.skip('skip when broken')\n")
	fixedSHA := gitCommit(t, repo, "fix python clamp")

	rep, err := prove.Verify(ctx, repo, prove.CandidateSpec{
		ID:         "py-clamp-caps",
		Version:    1,
		Rung:       reflex.RungTest,
		Obligation: "clamp caps values above maximum",
		Artifact: reflex.Artifact{
			Tool:     "pytest",
			Path:     "test_calc.py",
			Selector: "test_calc.py::test_caps_above_max",
		},
		BrokenRef: brokenSHA,
		FixedRef:  fixedSHA,
	}, prove.Options{})
	if err != nil {
		t.Fatalf("pytest prove failed: %v", err)
	}
	if rep.Verdict != prove.VerdictVerified {
		t.Fatalf("expected verified for pytest regression test, got %s/%s (%s)", rep.Verdict, rep.Reason, rep.ActionableError)
	}

	// Verify pytest skip->pass is rejected as inconclusive/skipped_on_broken!
	repSkip, err := prove.Verify(ctx, repo, prove.CandidateSpec{
		ID:         "py-clamp-skip",
		Version:    1,
		Rung:       reflex.RungTest,
		Obligation: "skip test must never verify",
		Artifact: reflex.Artifact{
			Tool:     "pytest",
			Path:     "test_calc.py",
			Selector: "test_calc.py::test_skip_on_broken",
		},
		BrokenRef: brokenSHA,
		FixedRef:  fixedSHA,
	}, prove.Options{})
	if err != nil {
		t.Fatalf("pytest skip prove failed: %v", err)
	}
	if repSkip.Verdict != prove.VerdictInconclusive || repSkip.Reason != prove.ReasonSkippedOnBroken {
		t.Fatalf("expected inconclusive/skipped_on_broken for pytest skip->pass, got %s/%s", repSkip.Verdict, repSkip.Reason)
	}

	// Verify live ruff linter proof if ruff is installed.
	if _, err := exec.LookPath("ruff"); err == nil {
		writeRepoFile(t, repo, "src/app.py", "def parse(raw):\n    try:\n        return int(raw)\n    except:\n        return 0\n")
		ruffBroken := gitCommit(t, repo, "broken bare except")

		writeRepoFile(t, repo, "src/app.py", "def parse(raw):\n    try:\n        return int(raw)\n    except ValueError:\n        return 0\n")
		ruffFixed := gitCommit(t, repo, "fix bare except")

		repRuff, err := prove.Verify(ctx, repo, prove.CandidateSpec{
			ID:         "no-bare-except",
			Version:    1,
			Rung:       reflex.RungLint,
			Scope:      "src/**",
			Obligation: "Catch specific exceptions instead of bare except.",
			Artifact: reflex.Artifact{
				Tool: "ruff",
				Rule: "E722",
			},
			AllowedCases: []prove.AllowedCase{
				{
					Name:       "specific-value-error",
					TargetPath: "src/allowed_parse.py",
					Content:    "def safe(x):\n    try:\n        return float(x)\n    except (TypeError, ValueError):\n        return 0.0\n",
				},
			},
			BrokenRef: ruffBroken,
			FixedRef:  ruffFixed,
		}, prove.Options{})
		if err != nil {
			t.Fatalf("ruff prove failed: %v", err)
		}
		if repRuff.Verdict != prove.VerdictVerified {
			t.Fatalf("expected ruff E722 verified, got %s/%s (%s)", repRuff.Verdict, repRuff.Reason, repRuff.ActionableError)
		}
	}
}
