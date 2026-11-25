package hook

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
)

// FuzzHandleWithoutConsent sends arbitrary event bytes to every host adapter
// on a machine where setup never ran. Whatever the host sends, and whichever
// event or directory it names, the hook answers with that host's allow
// response and never panics.
func FuzzHandleWithoutConsent(f *testing.F) {
	f.Add([]byte(`{"session_id":"s","cwd":"/","prompt":"hi","hook_event_name":"UserPromptSubmit"}`), "UserPromptSubmit")
	f.Add([]byte(`{"conversationId":"c","workspacePaths":["/a","/b"],"hookEventName":"PreInvocation"}`), "")
	f.Add([]byte(`{"session_id":"s","stop_hook_active":true}`), "Stop")
	f.Add([]byte(`{`), "Stop")
	f.Add([]byte(`null`), "StopFailure")
	f.Add([]byte(`{"session_id":"s","cwd":"relative/dir"}`), "Unknown")
	home := f.TempDir()
	f.Setenv("HOME", home)
	f.Setenv("USERPROFILE", home)
	f.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	f.Setenv("LOCALAPPDATA", filepath.Join(home, "local"))
	f.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	f.Setenv("CLAUDE_CONFIG_DIR", "")
	f.Setenv("CLAUDE_CODE_ENTRYPOINT", "")
	f.Setenv("CLAUDE_PROJECT_DIR", "")
	f.Setenv("FLINCH", "")
	f.Setenv("FLINCH_RECORD", "")
	f.Fuzz(func(t *testing.T, data []byte, event string) {
		for _, host := range []Host{HostClaude, HostCodex, HostAntigravity} {
			got := Handle(context.Background(), host, event, data, "")
			want := AllowResponse(host)
			if got.ExitCode != 0 || !bytes.Equal(got.Stdout, want.Stdout) {
				t.Fatalf("%s %q %q: got %+v, want the allow response", host, event, data, got)
			}
		}
	})
}
