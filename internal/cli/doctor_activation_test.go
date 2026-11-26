package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/angelnicolasc/flinch/internal/author"
	"github.com/angelnicolasc/flinch/internal/config"
	"github.com/angelnicolasc/flinch/internal/gitutil"
	"github.com/angelnicolasc/flinch/internal/reflex"
	"github.com/angelnicolasc/flinch/internal/stats"
)

func TestClonePermissionCommandAndDiagnostics(t *testing.T) {
	isolateSetupHome(t)
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "")
	root := t.TempDir()
	if err := exec.Command("git", "init", "-q", root).Run(); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	run := func(args ...string) string {
		t.Helper()
		var out, stderr bytes.Buffer
		if rc := Run(context.Background(), args, &out, &stderr); rc != 0 {
			t.Fatalf("%v: %s", args, stderr.String())
		}
		return out.String()
	}
	run("setup", "--yes", "--host", "claude,codex,antigravity")
	for _, step := range []struct {
		action string
		state  string
	}{{"enable", "active"}, {"disable", "inactive"}} {
		message := run(step.action)
		if !strings.Contains(message, "Codex uses its own workspace trust") {
			t.Fatal(message)
		}
		diagnosis := run("doctor")
		for _, host := range []string{"claude", "codex", "antigravity"} {
			expected := step.state
			if host == "codex" {
				expected = "inactive"
			}
			if !slices.ContainsFunc(strings.Split(diagnosis, "\n"), func(line string) bool {
				return strings.HasPrefix(line, host+":") && strings.Contains(line, "| "+expected+" (")
			}) {
				t.Fatalf("after %s expected %s %s:\n%s", step.action, host, expected, diagnosis)
			}
		}
	}
}

func doctorOutput(t *testing.T, repo string, args ...string) string {
	t.Helper()
	var out, stderr bytes.Buffer
	if rc := Run(t.Context(), append([]string{"doctor", "--repo", repo}, args...), &out, &stderr); rc != 0 {
		t.Fatalf("doctor exited %d: %s", rc, stderr.String())
	}
	return out.String()
}

func TestDoctorReportsCheckSourcesAndLastHostTurns(t *testing.T) {
	isolateSetupHome(t)
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	type diagnosis struct {
		Disk         int                  `json:"deterministic_checks"`
		Trusted      int                  `json:"trusted_checks"`
		Local        int                  `json:"working_tree_only_checks"`
		Proved       int                  `json:"locally_proved_working_tree_checks"`
		TreeError    string               `json:"working_tree_checks_error"`
		Last         map[string]time.Time `json:"last_turn_checked_by_host"`
		Capabilities map[string]struct {
			State string `json:"state"`
		} `json:"capabilities"`
	}
	checkCounts := func(disk, trusted, local, proved int) diagnosis {
		t.Helper()
		raw := doctorOutput(t, repo, "--json")
		var got diagnosis
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatal(err)
		}
		if got.TreeError != "" || got.Disk != disk || got.Trusted != trusted || got.Local != local || got.Proved != proved {
			t.Fatalf("expected disk=%d trusted=%d local=%d proved=%d: %s", disk, trusted, local, proved, raw)
		}
		return got
	}
	save := func(id, verifiedRef string, proof *reflex.ProofSummary) string {
		t.Helper()
		path, err := reflex.SaveContext(context.Background(), repo, reflex.Reflex{
			ID: id, Version: 1, Obligation: "Use the configured format string.", Rung: reflex.RungLint,
			Artifact: reflex.Artifact{Tool: "go-vet", Rule: "printf"}, Proof: proof,
		}, verifiedRef)
		if err != nil {
			t.Fatal(err)
		}
		return path
	}

	reviewed := save("reviewed", "", nil)
	initial := checkCounts(1, 0, 1, 0)
	if len(initial.Last) != 0 || initial.Capabilities["local_capture"].State == "enabled_and_tested" {
		t.Fatalf("claimed a hook ran without a host event: %+v", initial)
	}
	git("add", ".flinch")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.org", "-c", "commit.gpgsign=false", "commit", "-qm", "Review check")
	store := stats.NewStore(filepath.Join(repo, ".git", "flinch"))
	first := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	record := func(at time.Time, surface string, kind stats.EventKind) {
		t.Helper()
		if err := store.Record(stats.Event{Timestamp: at, Surface: surface, Kind: kind}); err != nil {
			t.Fatal(err)
		}
	}
	record(first, "turn", stats.EventTurnChecked) // flinch check --turn
	record(first, "codex", stats.EventCheckSkipped)
	committed := checkCounts(1, 1, 0, 0)
	if len(committed.Last) != 0 || committed.Capabilities["local_capture"].State == "enabled_and_tested" {
		t.Fatalf("counted a manual check or a skip as a host turn: %+v", committed)
	}

	fixed, err := gitutil.ResolveRef(context.Background(), repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	save("local", fixed, &reflex.ProofSummary{Verdict: "verified"})
	save("disk-only", "", &reflex.ProofSummary{Verdict: "verified"})
	save("draft", "", nil)
	if err := os.Remove(reviewed); err != nil {
		t.Fatal(err)
	}
	second := first.Add(time.Minute)
	record(second, "claude", stats.EventTurnChecked)
	record(first, "codex", stats.EventTurnChecked)
	record(first, "claude", stats.EventTurnChecked)
	mixed := checkCounts(3, 1, 3, 1)
	if len(mixed.Last) != 2 || !mixed.Last["claude"].Equal(second) || !mixed.Last["codex"].Equal(first) ||
		mixed.Capabilities["local_capture"].State != "enabled_and_tested" || mixed.Capabilities["turn_enforcement"].State == "enabled_and_tested" {
		t.Fatalf("incorrect host evidence or accreditation: %+v", mixed)
	}
	out := doctorOutput(t, repo)
	for _, want := range []string{
		"1 in the trusted revision, 3 only in the working tree (1 locally proved)",
		"Last claude turn checked in this clone: " + second.Format(time.RFC3339),
		"Last codex turn checked in this clone: " + first.Format(time.RFC3339),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor omitted %q:\n%s", want, out)
		}
	}
	if err := os.WriteFile(reviewed, []byte("id: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var invalid diagnosis
	if err := json.Unmarshal([]byte(doctorOutput(t, repo, "--json")), &invalid); err != nil {
		t.Fatal(err)
	}
	if invalid.Trusted != 1 || !strings.Contains(invalid.TreeError, filepath.Base(reviewed)) ||
		!invalid.Last["claude"].Equal(second) || invalid.Capabilities["local_capture"].State != "enabled_and_tested" {
		t.Fatalf("lost diagnostics for a malformed working-tree record: %+v", invalid)
	}
	out = doctorOutput(t, repo)
	if !strings.Contains(out, "1 in the trusted revision") || !strings.Contains(out, "Working-tree check counts unavailable:") ||
		strings.Contains(out, "0 only in the working tree") || strings.Contains(out, "Probabilistic context guides: 0") {
		t.Fatalf("expected known counts and a working-tree error:\n%s", out)
	}
}

func TestDoctorCountsLocallyProvedTestCases(t *testing.T) {
	ctx, repo := t.Context(), t.TempDir()
	if _, _, err := gitutil.Run(ctx, repo, nil, "init"); err != nil {
		t.Fatal(err)
	}
	write := func(name, contents string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := func() string {
		t.Helper()
		ref, err := gitutil.CaptureWorkingTreeSnapshot(ctx, repo, "")
		if err != nil {
			t.Fatal(err)
		}
		return ref
	}
	const table = `package retry
import "testing"

func TestRetryDelay(t *testing.T) {
	cases := []struct { name string; delay, want int }{
		{"positive", 5, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RetryDelay(tc.delay); got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
}
`
	const row = `{"zero", 0, 0},`
	write("go.mod", "module example.com/retry\n\ngo 1.26\n")
	write("retry.go", "package retry\nfunc RetryDelay(d int) int { if d == 0 { return 1 }; return d }\n")
	write("retry_test.go", table)
	broken := snapshot()
	fixed := strings.Replace(table, `{"positive", 5, 5},`, `{"positive", 5, 5},`+"\n\t\t"+row, 1)
	write("retry.go", "package retry\nfunc RetryDelay(d int) int { return d }\n")
	write("retry_test.go", fixed)
	proof, err := author.ReuseFirst(ctx, repo, config.Default(), broken, snapshot(), 2*time.Minute)
	if err != nil || proof.Status != author.ReuseProven || proof.Reflex == nil || proof.Reflex.Artifact.Selector != "TestRetryDelay/zero" {
		t.Fatalf("expected a verified zero row, got %+v, err=%v", proof, err)
	}
	isolateSetupHome(t)
	withSibling := strings.Replace(fixed, row, row+"\n\t\t"+`{"another_positive", 7, 7},`, 1)
	for _, tc := range []struct {
		name, contents string
		proved         int
	}{
		{"as_proved", fixed, 1},
		{"sibling_added", withSibling, 1},
		{"protected_row_edited", strings.Replace(withSibling, row, `{"zero", 0, 1},`, 1), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			write("retry_test.go", tc.contents)
			want := "0 in the trusted revision, 1 only in the working tree (" + strconv.Itoa(tc.proved) + " locally proved)"
			if out := doctorOutput(t, repo); !strings.Contains(out, want) {
				t.Errorf("expected %q in doctor output:\n%s", want, out)
			}
			var got struct {
				Local  int `json:"working_tree_only_checks"`
				Proved int `json:"locally_proved_working_tree_checks"`
			}
			raw := doctorOutput(t, repo, "--json")
			if err := json.Unmarshal([]byte(raw), &got); err != nil || got.Local != 1 || got.Proved != tc.proved {
				t.Errorf("expected one checkout check and %d locally proved in doctor JSON: %s (%v)", tc.proved, raw, err)
			}
		})
	}
}

func TestDoctorReportsCodexSubdirectoryAndOutsideGit(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	sub := filepath.Join(root, "src")
	for name, value := range map[string]string{"HOME": home, "USERPROFILE": home, "LOCALAPPDATA": home, "XDG_CONFIG_HOME": home, "CODEX_HOME": filepath.Join(home, ".codex")} {
		t.Setenv(name, value)
	}
	cmd := exec.Command("git", "init", "-q", root)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(os.Getenv("CODEX_HOME"), 0700); err != nil {
		t.Fatal(err)
	}
	key, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("CODEX_HOME"), "config.toml"), []byte("[projects."+strconv.Quote(key)+"]\ntrust_level=\"trusted\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ dir, status string }{{sub, "inactive"}, {root, "active"}} {
		t.Chdir(tc.dir)
		var out, errs bytes.Buffer
		if code := Run(context.Background(), []string{"setup", "--plan", "--host", "codex"}, &out, &errs); code != 0 {
			t.Fatalf("plan: %s", errs.String())
		}
		var diagnostic string
		for _, line := range strings.Split(out.String(), "\n") {
			lower := strings.ToLower(line)
			if strings.Contains(lower, "codex") && strings.Contains(lower, "after setup") {
				diagnostic = lower
				break
			}
		}
		if !strings.Contains(diagnostic, "after setup: "+tc.status+" (") || tc.dir == sub && !strings.Contains(diagnostic, "trusted repository root") {
			t.Fatalf("missing current-directory diagnosis: %s", out.String())
		}
	}
	for _, dir := range []string{sub, home} {
		var out, errors bytes.Buffer
		if code := Run(context.Background(), []string{"doctor", "--all", "--repo", dir}, &out, &errors); code != 0 {
			t.Fatalf("doctor: %s", errors.String())
		}
		if msg := out.String(); !strings.Contains(msg, "codex") || !strings.Contains(msg, "inactive") {
			t.Fatalf("missing hook status: %s", msg)
		}
		if dir == sub && !strings.Contains(strings.ToLower(out.String()), "repository root") {
			t.Fatalf("missing actionable subdirectory reason: %s", out.String())
		}
	}
}
