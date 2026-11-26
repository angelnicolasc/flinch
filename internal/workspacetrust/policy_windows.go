package workspacetrust

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func systemRequirementsPath() (string, error) {
	override, err := requirementsOverride()
	return resolveWindowsRequirementsPath(override, err, func() (string, error) {
		return windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	}, func() (string, error) {
		return windows.KnownFolderPath(windows.FOLDERID_Profile, 0)
	})
}

func requirementsOverride() (string, error) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Policies\OpenAI\Codex`, registry.READ|registry.WOW64_64KEY)
	if err != nil {
		return "", err
	}
	defer key.Close()
	// winreg reads string and expandable-string values literally. It also
	// accepts a multi-string, preserving embedded NULs for path validation.
	path, kind, err := key.GetStringValue("RequirementsPath")
	if kind != registry.MULTI_SZ {
		return path, err
	}
	paths, _, err := key.GetStringsValue("RequirementsPath")
	return strings.Join(paths, "\x00"), err
}

func systemCodexConfigPath() string {
	base, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		base = `C:\ProgramData`
	}
	return filepath.Join(base, "OpenAI", "Codex", "config.toml")
}
