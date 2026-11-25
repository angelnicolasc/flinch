package usersetup

import (
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Used only to recognize and remove an exact, previously owned hook from the
// initial setup preview. Keep customized copies under the user's ownership.
//
//go:embed preview_windows_hook.txt
var previewWindowsHook string

func (r registration) installedHandler(host string, ev hookEvent) map[string]any {
	if r.UserHandler {
		return handler(host, ev, r.Binary)
	}
	return previewHandler(host, ev, r.Binary)
}

func previewHandler(host string, ev hookEvent, executable string) map[string]any {
	args := []string{"hook", "--host", host, "--event", ev.name}
	h := commandHandler(host, ev, executable, args)
	if runtime.GOOS == "windows" && host != "claude" {
		template := strings.ReplaceAll(strings.TrimSpace(previewWindowsHook), "\r\n", "\n")
		script := strings.NewReplacer(
			"{milliseconds}", strconv.Itoa(ev.timeout*1000-250),
			"{executable}", strings.ReplaceAll(executable, "'", "''"),
			"{arguments}", strings.Join(args, " "),
		).Replace(strings.ReplaceAll(template, "\n", ";"))
		units := utf16.Encode([]rune(script))
		data := make([]byte, len(units)*2)
		for i, unit := range units {
			binary.LittleEndian.PutUint16(data[2*i:], unit)
		}
		shell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		field := "command"
		if host == "codex" {
			field = "commandWindows"
		}
		h[field] = filepath.ToSlash(shell) + " -NoLogo -NoProfile -NonInteractive -EncodedCommand " + base64.StdEncoding.EncodeToString(data)
	}
	return h
}
