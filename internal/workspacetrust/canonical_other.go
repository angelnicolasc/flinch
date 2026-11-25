//go:build !windows

package workspacetrust

func codexCanonicalPath(physical, _ string) string {
	return physical
}

func systemRequirementsPath() (string, error) {
	return "/etc/codex/requirements.toml", nil
}
