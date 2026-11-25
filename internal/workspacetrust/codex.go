package workspacetrust

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/BurntSushi/toml"
)

type codexProject struct {
	Trust codexTrustLevel `toml:"trust_level"`
}

type codexTrustLevel string

func (t *codexTrustLevel) UnmarshalText(value []byte) error {
	switch string(value) {
	case "trusted", "untrusted":
		*t = codexTrustLevel(value)
		return nil
	default:
		return errors.New("invalid Codex trust level")
	}
}

type codexProjects map[string]codexProject

type codexLayer struct {
	Projects codexProjects `toml:"projects"`
}

type codexUser struct {
	Projects codexProjects         `toml:"projects"`
	Profile  string                `toml:"profile"`
	Profiles map[string]codexLayer `toml:"profiles"`
}

func codexReason(ctx context.Context, c checkout, original string) string {
	home := codexHome()
	if !filepath.IsAbs(home) {
		return "the Codex home directory is unavailable"
	}
	if reason := managedCodexReason(ctx, home); reason != "" {
		return reason
	}
	source, err := readProfile(filepath.Join(home, "config.toml"), c)
	var user codexUser
	if err != nil || toml.Unmarshal(source, &user) != nil {
		return "the saved Codex trust configuration is unavailable"
	}
	const windows = runtime.GOOS == "windows"
	keys := codexKeys(codexCanonicalPath(c.cwd, original), original, windows)
	if entry, _ := user.Projects.lookup(keys, windows); entry.Trust != "trusted" {
		if entry.Trust == "untrusted" {
			return "Codex explicitly marks this working directory untrusted"
		}
		if !samePath(c.cwd, c.root) {
			root, _ := user.Projects.lookup(codexKeys(codexCanonicalPath(c.root, c.root), c.root, windows), windows)
			if root.Trust == "trusted" {
				return "Codex trusts the repository root only. A more specific denial is possible. Open the session at the trusted repository root"
			}
		}
		return "Codex has no saved trusted entry for the exact working directory"
	}
	if user.Profile == "" {
		return ""
	}
	if !safeProfileName(user.Profile) {
		return "the saved Codex profile is invalid"
	}
	if profile, ok := user.Profiles[user.Profile]; ok && profileVetoes(profile, keys) {
		return "the saved Codex profile does not trust this working directory"
	}
	file := filepath.Join(home, user.Profile+".config.toml")
	data, err := readProfile(file, c)
	if errors.Is(err, os.ErrNotExist) {
		// A legacy profile can be embedded in config.toml.
		if _, exists := user.Profiles[user.Profile]; exists {
			return ""
		}
		return "the saved Codex profile is unavailable"
	}
	var profile codexLayer
	if err != nil || toml.Unmarshal(data, &profile) != nil {
		return "the saved Codex profile is unreadable"
	}
	if profileVetoes(profile, keys) {
		return "the saved Codex profile does not trust this working directory"
	}
	return ""
}

// Codex 0.172.0 tries the physical cwd, then the original event spelling. On
// Windows it folds only ASCII case. It preserves all stored TOML keys verbatim.
func codexKeys(physical, original string, windows bool) []string {
	if windows {
		physical = asciiLower(physical)
		original = asciiLower(original)
	}
	if physical == original {
		return []string{physical}
	}
	return []string{physical, original}
}

// Codex uses dunce 1.0.5. It simplifies a verbatim DOS drive path only if all
// names and the full length are representable in ordinary Win32 syntax. UNC
// paths always keep their verbatim prefix.
func codexDiskPath(path string) string {
	dos, ok := strings.CutPrefix(path, `\\?\`)
	if !ok || len(dos) < 3 || dos[1:3] != `:\` || len(utf16.Encode([]rune(path))) > 260 {
		return path
	}
	for name := range strings.SplitSeq(dos[3:], `\`) {
		if name == "" {
			continue
		}
		if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") || strings.ContainsAny(name, `<>:"/|?*`) {
			return path
		}
		for _, ch := range name {
			if ch < ' ' {
				return path
			}
		}
		stem := name
		if ext := strings.LastIndexByte(stem, '.'); ext > 0 {
			stem = stem[:ext]
		}
		stem = asciiLower(strings.TrimRight(stem, ". "))
		switch stem {
		case "con", "prn", "aux", "nul":
			return path
		}
		if len(stem) == 4 && (stem[:3] == "com" || stem[:3] == "lpt") && stem[3] >= '1' && stem[3] <= '9' {
			return path
		}
	}
	return dos
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, ch := range b {
		if ch >= 'A' && ch <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

func (projects codexProjects) lookup(keys []string, windows bool) (codexProject, bool) {
	for _, key := range keys {
		if project := projects[key]; project.Trust != "" {
			return project, true
		}
		if windows {
			var first string
			for stored, project := range projects {
				if project.Trust != "" && asciiLower(stored) == key && (first == "" || stored < first) {
					first = stored
				}
			}
			if first != "" {
				return projects[first], true
			}
		}
	}
	return codexProject{}, false
}

func profileVetoes(layer codexLayer, keys []string) bool {
	p, ok := layer.Projects.lookup(keys, runtime.GOOS == "windows")
	return ok && p.Trust != "trusted"
}

// ManagedCodexReason explains when Flinch cannot use saved Codex trust on this machine.
func ManagedCodexReason() string {
	return managedCodexReason(context.Background(), codexHome())
}

func managedCodexReason(ctx context.Context, home string) string {
	requirements, err := systemRequirementsPath()
	if err != nil {
		return "Codex workspace trust is unavailable because the system policy location is unknown"
	}
	var mdm func() (bool, error)
	if runtime.GOOS == "darwin" {
		mdm = func() (bool, error) { return codexMDM(ctx) }
	}
	if reason := managedReason(managedPaths(runtime.GOOS, requirements, home), mdm); reason != "" {
		return reason
	}
	return cloudCacheReason(home)
}

// The hook has no account or policy-server identity. Inspect only Codex's
// bundle caches, including the SHA-256 namespace of a configured policy server.
func cloudCacheReason(home string) string {
	const unavailable = "Codex is managed by the organization or its policy cache is unreadable"
	if !filepath.IsAbs(home) {
		return unavailable
	}
	files, err := os.ReadDir(home)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		return unavailable
	}
	for _, file := range files {
		name := file.Name()
		if runtime.GOOS == "windows" {
			name = asciiLower(name)
		}
		if name != "cloud-config-bundle-cache.json" && !namespacedCache(name) {
			continue
		}
		data, err := readRegular(filepath.Join(home, file.Name()), 16<<20)
		if err != nil {
			return unavailable
		}
		type fragments struct {
			Enterprise []json.RawMessage `json:"enterprise_managed"`
		}
		var envelope struct {
			Payload struct {
				Version int `json:"version"`
				Bundle  struct {
					Config       fragments `json:"config_toml"`
					Requirements fragments `json:"requirements_toml"`
				} `json:"bundle"`
			} `json:"signed_payload"`
		}
		if json.Unmarshal(data, &envelope) != nil || envelope.Payload.Version != 1 {
			return unavailable
		}
		bundle := envelope.Payload.Bundle
		if bundle.Config.Enterprise == nil || bundle.Requirements.Enterprise == nil {
			return unavailable
		}
		if len(bundle.Config.Enterprise) > 0 || len(bundle.Requirements.Enterprise) > 0 {
			return "Codex is managed by the organization (cloud policy cache)"
		}
	}
	return ""
}

func namespacedCache(name string) bool {
	namespace, ok := strings.CutPrefix(name, "managed-config-")
	if !ok {
		return false
	}
	namespace, ok = strings.CutSuffix(namespace, ".json")
	return ok && len(namespace) == 64 && strings.Trim(namespace, "0123456789abcdef") == ""
}

func managedPaths(platform, requirements, home string) []string {
	paths := []string{requirements}
	if platform != "windows" {
		paths = append(paths, filepath.Join(filepath.Dir(requirements), "managed_config.toml"), filepath.Join(home, "managed_config.toml"))
	}
	return paths
}

// Codex falls back from the 64-bit registry view to the ProgramData known
// folder. A successfully read override must identify an absolute policy file.
func resolveWindowsRequirementsPath(override string, registryErr error, programData, userProfile func() (string, error)) (string, error) {
	if registryErr == nil {
		if override == "~" || strings.HasPrefix(override, "~/") || strings.HasPrefix(override, `~\`) {
			home, err := userProfile()
			if err != nil {
				return "", err
			}
			override = filepath.Join(home, strings.TrimLeft(override[1:], `/\`))
		}
		if !filepath.IsAbs(override) || strings.ContainsRune(override, 0) {
			return "", errors.New("invalid Codex requirements path")
		}
		return filepath.Clean(override), nil
	}
	dir, err := programData()
	if err != nil {
		dir = `C:\ProgramData`
	}
	return filepath.Join(dir, "OpenAI", "Codex", "requirements.toml"), nil
}

func managedReason(paths []string, mdm func() (bool, error)) string {
	for _, path := range paths {
		_, err := os.Lstat(path)
		if err == nil {
			return "Codex workspace trust is inactive because managed configuration is present (" + filepath.Base(path) + ")"
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "Codex workspace trust is inactive because managed configuration cannot be inspected"
		}
	}
	if mdm != nil {
		managed, err := mdm()
		if err != nil {
			return "Codex workspace trust is inactive because macOS managed preferences cannot be inspected"
		}
		if managed {
			return "Codex workspace trust is inactive because macOS manages Codex preferences"
		}
	}
	return ""
}

// Query forced preferences through Foundation. Reading plist files directly misses
// values applied from a device profile. Never print the contents of managed settings.
func codexMDM(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	const script = `ObjC.import('Foundation');
var prefs = $.NSUserDefaults.standardUserDefaults;
var domain = $('com.openai.codex');
var managed = ['config_toml_base64','requirements_toml_base64'].some(function(key) {
  return prefs.objectIsForcedForKeyInDomain($(key), domain);
});
managed ? 'managed' : 'clear';`
	out, err := exec.CommandContext(ctx, "/usr/bin/osascript", "-l", "JavaScript", "-e", script).Output()
	if err != nil {
		return false, err
	}
	switch string(bytes.TrimSpace(out)) {
	case "managed":
		return true, nil
	case "clear":
		return false, nil
	default:
		return false, errors.New("unexpected managed preference status")
	}
}
