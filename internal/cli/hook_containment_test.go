package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/angelnicolasc/flinch/internal/hook"
	"github.com/angelnicolasc/flinch/internal/usersetup"
)

// isolateUserState points every user directory Flinch resolves at a temporary
// home, so a contained failure can never write to the developer's real profile.
func isolateUserState(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "local"))
	return home
}

// installedState creates the user state directory, as setup would.
func installedState(t *testing.T) string {
	t.Helper()
	dir, err := usersetup.StateDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

type panicReader struct{}

func (panicReader) Read([]byte) (int, error) { panic("PRIVATE-READ-PANIC-5193") }

// hostEvents lists the two events setup registers for each host.
var hostEvents = map[hook.Host][]string{
	hook.HostClaude:      {"UserPromptSubmit", "Stop"},
	hook.HostCodex:       {"UserPromptSubmit", "Stop"},
	hook.HostAntigravity: {"PreInvocation", "Stop"},
}

func TestHookContainsAPanicInEveryStage(t *testing.T) {
	isolateUserState(t)
	payload := `{"session_id":"PRIVATE-SESSION-5193","prompt":"PRIVATE-PROMPT-5193"}`
	never := func(context.Context, hook.Host, string, []byte, string) hook.Response {
		t.Error("the handler ran after an earlier stage failed")
		return hook.Response{}
	}
	for host, events := range hostEvents {
		for _, event := range events {
			for _, stage := range []string{"read", "registration", "handle"} {
				t.Run(string(host)+"/"+event+"/"+stage, func(t *testing.T) {
					in := hookDelivery{
						stdin:      strings.NewReader(payload),
						handle:     never,
						registered: func(string, string) bool { return false },
					}
					switch stage {
					case "read":
						in.stdin = panicReader{}
					case "registration":
						in.registered = func(string, string) bool { panic("PRIVATE-REGISTRATION-PANIC-5193") }
					case "handle":
						in.handle = func(context.Context, hook.Host, string, []byte, string) hook.Response {
							panic("PRIVATE-HANDLE-PANIC-5193")
						}
					}
					var stdout bytes.Buffer
					code := runHookDelivery(context.Background(), []string{"--host", string(host), "--event", event}, &stdout, in)
					if code != 0 {
						t.Fatalf("exit code %d, want 0", code)
					}
					if want := hook.AllowResponse(host).Stdout; !bytes.Equal(stdout.Bytes(), want) {
						t.Fatalf("stdout %q, want the %s allow response %q", stdout.Bytes(), host, want)
					}
				})
			}
		}
	}
}

func TestHookIncidentKeepsOnlyEnumeratedFields(t *testing.T) {
	isolateUserState(t)
	dir := installedState(t)
	in := hookDelivery{
		stdin:      strings.NewReader(`{"session_id":"PRIVATE-SESSION-8361","prompt":"PRIVATE-PROMPT-8361","cwd":"/PRIVATE/PATH/8361"}`),
		registered: func(string, string) bool { return false },
		handle: func(context.Context, hook.Host, string, []byte, string) hook.Response {
			panic(errors.New("PRIVATE-PANIC-8361 at /PRIVATE/PATH/8361"))
		},
	}
	var stdout bytes.Buffer
	if code := runHookDelivery(context.Background(), []string{"--host", "codex", "--event", "Stop"}, &stdout, in); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	data, err := os.ReadFile(filepath.Join(dir, "hook-incident.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("PRIVATE")) {
		t.Fatalf("the incident record kept private data: %s", data)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 5 || fields["host"] != "codex" || fields["event"] != "Stop" || fields["stage"] != "handle" {
		t.Fatalf("unexpected incident record: %s", data)
	}
	if info, err := os.Stat(filepath.Join(dir, "hook-incident.json")); err != nil || (info.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/') {
		t.Fatalf("the incident record must be private: %v %v", info.Mode(), err)
	}

	var report bytes.Buffer
	if code := runDoctor(context.Background(), []string{"--repo", t.TempDir(), "--json"}, &report, &bytes.Buffer{}); code != 0 {
		t.Fatalf("doctor exit %d: %s", code, report.String())
	}
	var parsed struct {
		Incident *hook.Incident `json:"last_hook_incident"`
	}
	if err := json.Unmarshal(report.Bytes(), &parsed); err != nil || parsed.Incident == nil || parsed.Incident.Stage != "handle" {
		t.Fatalf("doctor did not report the incident: %s", report.String())
	}
	report.Reset()
	if code := runDoctor(context.Background(), []string{"--repo", t.TempDir()}, &report, &bytes.Buffer{}); code != 0 || !strings.Contains(report.String(), "Last contained hook failure") {
		t.Fatalf("doctor text omitted the incident: %s", report.String())
	}
}

func TestHookIncidentNeedsAnInstallation(t *testing.T) {
	isolateUserState(t)
	in := hookDelivery{
		stdin:      strings.NewReader(`{}`),
		registered: func(string, string) bool { return false },
		handle: func(context.Context, hook.Host, string, []byte, string) hook.Response {
			panic("contained")
		},
	}
	if code := runHookDelivery(context.Background(), []string{"--host", "claude", "--event", "Stop"}, &bytes.Buffer{}, in); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	dir, err := usersetup.StateDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("a failure outside an installation created %s", dir)
	}
}

func TestHookDropsADecisionThatMissesTheBudget(t *testing.T) {
	isolateUserState(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	in := hookDelivery{
		stdin:      strings.NewReader(`{}`),
		registered: func(string, string) bool { return false },
		handle: func(ctx context.Context, host hook.Host, _ string, _ []byte, _ string) hook.Response {
			<-ctx.Done()
			time.Sleep(50 * time.Millisecond)
			return hook.Response{Stdout: []byte(`{"decision":"block","reason":"late"}` + "\n")}
		},
	}
	var stdout bytes.Buffer
	if code := runHookDelivery(ctx, []string{"--host", "claude", "--event", "Stop"}, &stdout, in); code != 0 || stdout.Len() != 0 {
		t.Fatalf("exit %d, stdout %q; want the allow response", code, stdout.Bytes())
	}
}

// TestHookProcessExitsZeroAfterAPanic runs each registered host and event in a
// real child process with real pipes, where the Go runtime would otherwise
// print the panic on stderr and exit with status 2.
func TestHookProcessExitsZeroAfterAPanic(t *testing.T) {
	home := isolateUserState(t)
	for host, events := range hostEvents {
		for _, event := range events {
			t.Run(string(host)+"/"+event, func(t *testing.T) {
				cmd := exec.Command(os.Args[0])
				cmd.Env = append(os.Environ(),
					"FLINCH_HOOK_PANIC_HELPER=1",
					"FLINCH_HOOK_HELPER_HOST="+string(host),
					"FLINCH_HOOK_HELPER_EVENT="+event,
					"HOME="+home, "USERPROFILE="+home,
					"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
					"LOCALAPPDATA="+filepath.Join(home, "local"),
				)
				cmd.Stdin = strings.NewReader(`{"session_id":"helper"}`)
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				err := cmd.Run()
				if err != nil {
					t.Fatalf("helper exited with %v; stderr %q", err, stderr.String())
				}
				if stderr.Len() != 0 {
					t.Fatalf("stderr %q, want nothing", stderr.String())
				}
				if want := hook.AllowResponse(host).Stdout; !bytes.Equal(stdout.Bytes(), want) {
					t.Fatalf("stdout %q, want %q", stdout.Bytes(), want)
				}
			})
		}
	}
}

// TestMain lets the test binary act as the child process above. The child runs
// before any test starts, because the testing package treats os.Exit(0)
// during a test as a failure.
func TestMain(m *testing.M) {
	if os.Getenv("FLINCH_HOOK_PANIC_HELPER") == "1" {
		in := hookDelivery{
			stdin:      os.Stdin,
			registered: func(string, string) bool { return false },
			handle: func(context.Context, hook.Host, string, []byte, string) hook.Response {
				panic("helper panic")
			},
		}
		args := []string{"--host", os.Getenv("FLINCH_HOOK_HELPER_HOST"), "--event", os.Getenv("FLINCH_HOOK_HELPER_EVENT")}
		os.Exit(runHookDelivery(context.Background(), args, os.Stdout, in))
	}
	os.Exit(m.Run())
}
