package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A replay can correlate the reason returned at Stop with the next input without
// retaining the conversation, its identity or its location.
func TestRecordingAliasesCorrelateOnlyWithinClone(t *testing.T) {
	const secret = "PRIVATE-identifier-text-path"
	var otherClone string
	for i := 0; i < 2; i++ {
		state := t.TempDir()
		p := Payload{ConversationID: secret, CWD: secret, WorkspacePaths: []string{secret}, LastUserInput: secret, TerminationReason: secret, ErrorText: secret, Origin: secret}
		raw, err := json.Marshal(map[string]any{"conversationId": secret, "cwd": secret, "workspacePaths": []string{secret}, "lastUserInput": secret, "terminationReason": secret, "error": secret, "origin": secret, "fullyIdle": false})
		if err != nil {
			t.Fatal(err)
		}
		recordDelivery(state, HostAntigravity, "Stop", p, raw, continueResponse(HostAntigravity, secret), time.Now(), false, false)
		files, err := filepath.Glob(filepath.Join(state, "recordings", "*.json"))
		if err != nil || len(files) != 1 {
			t.Fatalf("record files: %v, %v", files, err)
		}
		var got struct {
			Input  map[string]any
			Output map[string]string
		}
		b, err := os.ReadFile(files[0])
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		if _, exists := got.Input["invocationNum"]; exists || got.Input["fullyIdle"] != false {
			t.Fatalf("lost field presence: %s", b)
		}
		alias := got.Output["reason"]
		if got.Output["decision"] != "continue" || alias == "" || alias == secret || alias != got.Input["lastUserInput"] || alias == got.Input["conversationId"] || alias == got.Input["cwd"] || got.Input["origin"] != "other" || got.Input["terminationReason"] != "other" || got.Input["error"] != "present" {
			t.Fatalf("invalid or identifying recording: %s", b)
		}
		if i == 1 && alias == otherClone {
			t.Fatal("two clones shared a recording identity")
		}
		otherClone = alias
	}
}
