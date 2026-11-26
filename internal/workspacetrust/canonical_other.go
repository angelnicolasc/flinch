//go:build !windows

package workspacetrust

func codexCanonicalPath(physical, _ string) string {
	return physical
}

func systemRequirementsPath() (string, error) {
	return "/etc/codex/requirements.toml", nil
}

func systemCodexConfigPath() string {
	return "/etc/codex/config.toml"
}
