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
	"slices"
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
	Projects           codexProjects `toml:"projects"`
	ProjectRootMarkers []string      `toml:"project_root_markers"`
}

type codexUser struct {
	codexLayer
	Profile  string                `toml:"profile"`
	Profiles map[string]codexLayer `toml:"profiles"`
}

// Rust matches configuration field names exactly. The Go decoder also matches
// struct tags case-insensitively, so reject ambiguous trust fields before using
// the result. Directory keys and profile names keep their original spelling.
func decodeCodexTrust(data []byte, target any) error {
	meta, err := toml.NewDecoder(bytes.NewReader(data)).Decode(target)
	if err != nil {
		return err
	}
	wrongCase := func(key string, names ...string) bool {
		for _, name := range names {
			if key != name && strings.EqualFold(key, name) {
				return true
			}
		}
		return false
	}
	for _, key := range meta.Keys() {
		if wrongCase(key[0], "profile", "profiles") {
			return errors.New("incorrect capitalization in Codex profile configuration")
		}
		if key[0] == "profiles" {
			if len(key) < 3 {
				continue
			}
			key = key[2:]
		}
		if wrongCase(key[0], "projects", "project_root_markers") ||
			key[0] == "projects" && len(key) >= 3 && wrongCase(key[2], "trust_level") {
			return errors.New("incorrect capitalization in Codex trust configuration")
		}
	}
	return nil
}

func codexReason(ctx context.Context, c checkout, original string) string {
	home := codexHome()
	if !filepath.IsAbs(home) {
		return "the Codex home directory is unavailable"
	}
	if reason := managedCodexReason(ctx, home); reason != "" {
		return reason
	}
	var system codexLayer
	data, err := readProfile(systemCodexConfigPath(), c)
	if !errors.Is(err, os.ErrNotExist) && (err != nil || decodeCodexTrust(data, &system) != nil) {
		return "the system Codex configuration is unreadable"
	}
	source, err := readProfile(filepath.Join(home, "config.toml"), c)
	var user codexUser
	if err != nil || decodeCodexTrust(source, &user) != nil {
		return "the saved Codex trust configuration is unavailable"
	}
	var profiles []codexLayer
	if user.Profile != "" {
		if !safeProfileName(user.Profile) {
			return "the saved Codex profile is invalid"
		}
		embedded, hasEmbedded := user.Profiles[user.Profile]
		if hasEmbedded {
			profiles = append(profiles, embedded)
		}
		data, err := readProfile(filepath.Join(home, user.Profile+".config.toml"), c)
		var profile codexLayer
		switch {
		case errors.Is(err, os.ErrNotExist):
			if !hasEmbedded {
				return "the saved Codex profile is unavailable"
			}
		case err != nil || decodeCodexTrust(data, &profile) != nil:
			return "the saved Codex profile is unreadable"
		default:
			profiles = append(profiles, profile)
		}
	}
	return codexSavedReason(c, original, system, user.codexLayer, profiles)
}

func codexSavedReason(c checkout, original string, system, user codexLayer, profiles []codexLayer) string {
	markers := user.ProjectRootMarkers
	if markers == nil {
		markers = system.ProjectRootMarkers
	}
	inherit := defaultCodexMarkers(markers)
	for _, profile := range profiles {
		inherit = inherit && defaultCodexMarkers(profile.ProjectRootMarkers)
	}
	keys := codexWorkspaceKeys(c, original, inherit)
	const windows = runtime.GOOS == "windows"
	decision := func(projects codexProjects) codexTrustLevel {
		entry, _ := projects.lookup(keys, windows)
		if entry.Trust == "trusted" && inherit && codexDeniedProjectLayer(c, original, projects) {
			return "untrusted"
		}
		return entry.Trust
	}
	switch decision(user.Projects) {
	case "untrusted":
		return "Codex explicitly marks this workspace untrusted"
	case "":
		if !inherit {
			return "custom Codex project root markers require saved trust for the exact working directory"
		}
		return "Codex has no saved user trust for this workspace"
	}
	if len(system.Projects) == 0 && len(profiles) == 0 {
		return ""
	}
	// Codex merges layers by literal TOML key before looking up workspace paths.
	// A system entry for a more specific directory can deny an inherited user grant.
	effective := make(codexProjects)
	effective.apply(system.Projects)
	effective.apply(user.Projects)
	if decision(effective) != "trusted" {
		return "the system Codex configuration does not trust this workspace"
	}
	for _, profile := range profiles {
		effective.apply(profile.Projects)
		if profileVetoes(profile, keys) || decision(effective) != "trusted" {
			return "the saved Codex profile does not trust this workspace"
		}
	}
	return ""
}

// An exact cwd decision precedes any disabled project layer. When a grant is
// inherited, Codex also honors disabled .codex directories between the cwd and
// the checkout root, including empty layers. Inspect directory metadata only.
func codexDeniedProjectLayer(c checkout, original string, projects codexProjects) bool {
	const windows = runtime.GOOS == "windows"
	if c.cwd == c.root {
		return false
	}
	if _, exact := projects.lookup(codexKeys(codexCanonicalPath(c.cwd, original), original, windows), windows); exact {
		return false
	}
	raw := filepath.Dir(original)
	for directory := filepath.Dir(c.cwd); directory != c.root; directory = filepath.Dir(directory) {
		keys := codexKeys(codexCanonicalPath(directory, raw), raw, windows)
		if p, _ := projects.lookup(keys, windows); p.Trust == "untrusted" {
			info, err := os.Stat(filepath.Join(raw, ".codex"))
			if err == nil && info.IsDir() || err != nil && !errors.Is(err, os.ErrNotExist) {
				return true
			}
		}
		raw = filepath.Dir(raw)
	}
	return false
}

func (projects codexProjects) apply(layer codexProjects) {
	for path, entry := range layer {
		if entry.Trust != "" {
			projects[path] = entry
		}
	}
}

func defaultCodexMarkers(markers []string) bool {
	return markers == nil || slices.Equal(markers, []string{".git"})
}

func codexWorkspaceKeys(c checkout, original string, inherit bool) []string {
	const windows = runtime.GOOS == "windows"
	keys := codexKeys(codexCanonicalPath(c.cwd, original), original, windows)
	if !inherit {
		return keys
	}
	// Codex walks the original cwd's ancestors. A path through a symlink can
	// reach the event directory without passing through its physical checkout.
	// Only inherit when both walks reach the same directories through the root.
	project := filepath.Clean(original)
	for dir := c.cwd; ; dir = filepath.Dir(dir) {
		if !sameDirectory(project, dir) {
			return keys
		}
		if dir == c.root {
			break
		}
		project = filepath.Dir(project)
	}
	if c.cwd != c.root {
		keys = append(keys, codexKeys(codexCanonicalPath(c.root, project), project, windows)...)
	}
	if c.worktreeGitDir == "" {
		return keys
	}
	// The upstream Git trust key retains the main checkout spelling in the
	// linked worktree's gitdir pointer. Verify its owner before consulting it.
	admin := c.worktreeGitDir
	if !filepath.IsAbs(admin) {
		admin = filepath.Join(project, admin)
	}
	if !isDir(admin) {
		return keys
	}
	main := filepath.Dir(filepath.Dir(filepath.Dir(admin)))
	if !sameDirectory(filepath.Join(main, ".git"), c.common) || !sameDirectory(main, filepath.Dir(c.common)) {
		return keys
	}
	return append(keys, codexKeys(codexCanonicalPath(filepath.Dir(c.common), main), main, windows)...)
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
