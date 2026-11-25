package hook

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Recording uses a private per-clone key. Equal inputs retain equal aliases for
// replay, including a continuation reason delivered back as a prompt.
func recordingKey(dir string) []byte {
	name := filepath.Join(dir, "recording.key")
	if data, err := os.ReadFile(name); err == nil {
		if len(data) == 32 {
			return data
		}
		return nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil
	}
	f, err := os.CreateTemp(dir, ".recording-key-*")
	if err != nil {
		return nil
	}
	defer os.Remove(f.Name())
	_, err = f.Write(key)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return nil
	}
	if err := os.Link(f.Name(), name); err == nil {
		return key
	}
	data, err := os.ReadFile(name)
	if err != nil || len(data) != 32 {
		return nil
	}
	return data
}

func passiveNotice(text string) Response {
	b, _ := json.Marshal(map[string]string{"systemMessage": text})
	return Response{Stdout: append(b, '\n')}
}

func recordDelivery(stateDir string, host Host, event string, p Payload, raw []byte, resp Response, started time.Time, projectSameCheckout bool) {
	key := recordingKey(stateDir)
	if key == nil {
		return
	}
	fields := map[string]any{}
	alias := func(domain, value string) string {
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(domain))
		mac.Write([]byte{0})
		mac.Write([]byte(value))
		return "id-" + hex.EncodeToString(mac.Sum(nil)[:12])
	}
	id := func(field, domain, value string) {
		if value != "" {
			fields[field] = alias(domain, value)
		}
	}
	id("session_id", "session", p.SessionID)
	id("conversationId", "session", p.ConversationID)
	id("turn_id", "turn", p.TurnID)
	id("prompt_id", "promptID", p.PromptID)
	id("event_id", "event", p.EventID)
	id("cwd", "workspace", p.CWD)
	if len(p.WorkspacePaths) > 0 {
		paths := make([]string, 0, len(p.WorkspacePaths))
		for _, path := range p.WorkspacePaths {
			paths = append(paths, alias("workspace", path))
		}
		fields["workspacePaths"] = paths
	}
	id("prompt", "text", p.Prompt)
	id("lastUserInput", "text", p.LastUserInput)
	id("last_assistant_message", "text", p.LastAssistantMessage)
	for field, value := range map[string]string{"origin": p.Origin, "trigger": p.Trigger, "source": p.Source, "terminationReason": p.TerminationReason} {
		if value == "" {
			continue
		}
		switch strings.ToLower(value) {
		case "user", "human", "scheduled", "cron", "loop", "subagent", "synthetic", "hook", "automation", "model_stop", "user_cancelled", "max_steps_exceeded", "error":
			fields[field] = strings.ToLower(value)
		default:
			fields[field] = "other"
		}
	}
	if p.ErrorText != "" {
		fields["error"] = "present"
	}
	if host == HostAntigravity {
		fields["hookEventName"] = event
		fields["invocationNum"] = p.InvocationNum
		fields["executionNum"] = p.ExecutionNum
		fields["fullyIdle"] = p.FullyIdle
		fields["continuationActive"] = p.ContinuationActive
	} else {
		fields["hook_event_name"] = event
		fields["stop_hook_active"] = p.StopHookActive
	}
	// A missing flag and an explicit false both decode to false. Preserve which
	// fields this particular host supplied, without copying extra keys or values.
	var input map[string]json.RawMessage
	if json.Unmarshal(raw, &input) != nil {
		return
	}
	known := []string{"cwd", "session_id", "conversationId", "turn_id", "prompt_id", "event_id", "hook_event_name", "hookEventName", "workspacePaths", "prompt", "lastUserInput", "last_assistant_message", "origin", "trigger", "source", "terminationReason", "error", "invocationNum", "executionNum", "fullyIdle", "continuationActive", "stop_hook_active"}
	present := make([]string, 0, len(known))
	for _, name := range known {
		if _, ok := input[name]; ok {
			present = append(present, name)
		} else {
			delete(fields, name)
		}
	}
	sort.Strings(present)
	var response struct{ Decision, Reason, SystemMessage string }
	_ = json.Unmarshal(resp.Stdout, &response)
	out := map[string]string{}
	switch response.Decision {
	case "block", "continue":
		out["decision"] = response.Decision
	}
	if response.Reason != "" {
		out["reason"] = alias("text", response.Reason)
	}
	if response.SystemMessage != "" {
		out["systemMessage"] = "present"
	}
	record := map[string]any{"schema": 1, "host": host, "event": event, "present": present, "input": fields, "output": out, "elapsed_ms": time.Since(started).Milliseconds(), "started_utc": started.UTC()}
	if host == HostClaude {
		_, entrypoint := os.LookupEnv("CLAUDE_CODE_ENTRYPOINT")
		_, project := os.LookupEnv("CLAUDE_PROJECT_DIR")
		_, child := os.LookupEnv("CLAUDE_CODE_CHILD_SESSION")
		_, claudeCode := os.LookupEnv("CLAUDECODE")
		session := os.Getenv("CLAUDE_CODE_SESSION_ID")
		record["claude_environment"] = map[string]bool{
			"entrypoint_present":         entrypoint,
			"project_dir_present":        project,
			"project_dir_same_checkout":  projectSameCheckout,
			"child_session_present":      child,
			"claudecode_present":         claudeCode,
			"session_id_matches_payload": session != "" && session == p.SessionID,
		}
	}
	dir := filepath.Join(stateDir, "recordings")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return
	}
	f, err := os.CreateTemp(dir, "*.json")
	if err != nil {
		return
	}
	err = json.NewEncoder(f).Encode(record)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(f.Name())
	}
}
