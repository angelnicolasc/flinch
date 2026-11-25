package usersetup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The copied test executable stands in for Flinch so the actual installed command
// must preserve arguments, stdin bytes, output and exit status across each shell.
func TestMain(m *testing.M) {
	if host := os.Getenv("FLINCH_TEST_HOOK_ECHO"); host != "" && len(os.Args) > 1 && os.Args[1] == "hook" {
		if strings.Join(os.Args[2:], " ") != "--host "+host+" --event Stop --user" {
			os.Exit(2)
		}
		if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestWindowsRegisteredHookCommands(t *testing.T) {
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	git, err := exec.LookPath("git.exe")
	if err != nil {
		t.Fatal("the Windows hook test requires Git for Windows:", err)
	}
	bash := filepath.Join(filepath.Dir(filepath.Dir(git)), "bin", "bash.exe")
	if _, err := os.Stat(bash); err != nil {
		bash = filepath.Join(os.Getenv("ProgramFiles"), "Git", "bin", "bash.exe")
	}
	root := filepath.Join(os.Getenv("SystemRoot"), "System32")
	shells := [][]string{
		{filepath.Join(root, "cmd.exe"), "/d", "/c"},
		{filepath.Join(root, "WindowsPowerShell", "v1.0", "powershell.exe"), "-NoLogo", "-NoProfile", "-NonInteractive", "-Command"},
		{bash, "-c"},
	}

	// Place each metacharacter late enough for NTFS to generate a usable 8.3 alias.
	// When the volume has no compatible alias, setup must reject that fixture.
	names := []string{"common", "portable folder", "café"}
	for _, char := range "&%!^();,='$`" {
		names = append(names, "portable"+string(char)+"bin")
	}
	dir := t.TempDir()
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			binary := filepath.Join(dir, name, "flinch.exe")
			writeFixture(t, binary, contents)
			for _, host := range []string{"codex", "antigravity"} {
				t.Run(host, func(t *testing.T) {
					registered, err := registeredBinary(host, binary)
					if err != nil {
						short, shortErr := shortBinaryPath(binary)
						if windowsLiteral.MatchString(filepath.ToSlash(binary)) || (shortErr == nil && windowsLiteral.MatchString(filepath.ToSlash(short))) {
							t.Fatal("rejected a usable path:", err)
						}
						if !strings.Contains(err.Error(), "C:/Tools/Flinch") {
							t.Fatalf("missing installation guidance: %v", err)
						}
						t.Log("setup rejected the path before registration; no safe short name")
						return
					}
					if !windowsLiteral.MatchString(registered) {
						t.Fatalf("unsafe Windows hook path %q", registered)
					}
					h := handler(host, hookEvent{"Stop", 30}, registered)
					key := "command"
					if host == "codex" {
						key = "commandWindows"
					}
					line := h[key].(string)
					if line != registered+" hook --host "+host+" --event Stop --user" {
						t.Fatalf("indirect or invalid Windows hook: %q", line)
					}
					input := []byte("{\"value\":\"bytes ñ\"}\n")
					for _, shell := range shells {
						t.Run(filepath.Base(shell[0]), func(t *testing.T) {
							ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
							defer cancel()
							cmd := exec.CommandContext(ctx, shell[0], append(shell[1:], line)...)
							cmd.Env = append(os.Environ(), "FLINCH_TEST_HOOK_ECHO="+host)
							cmd.Stdin = bytes.NewReader(input)
							var stderr bytes.Buffer
							cmd.Stderr = &stderr
							out, err := cmd.Output()
							if err != nil || !bytes.Equal(input, out) {
								t.Fatalf("hook output %q, error %v, stderr %s", out, err, stderr.Bytes())
							}
						})
					}
				})
			}
		})
	}
}

func TestWindowsRecognizesPreviousWrapper(t *testing.T) {
	h := previewHandler("codex", hookEvent{"Stop", 30}, "C:/Tools/Flinch/flinch.exe")
	_, payload, ok := strings.Cut(h["commandWindows"].(string), " -EncodedCommand ")
	script, err := base64.StdEncoding.DecodeString(payload)
	// SHA-256 of this definition's UTF-16LE script from the previous setup preview.
	const prior = "cf6c3c0077aa1f37d328ec744f208624283fa51b3a2ffe9fe22db09c6e6d0431"
	if !ok || err != nil || fmt.Sprintf("%x", sha256.Sum256(script)) != prior {
		t.Fatal("removal would leave the previously registered wrapper in place")
	}
}
func TestWindowsScoopUpgrade(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Scoop")
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "Local AppData"))
	dir, err := StateDir()
	if err != nil || dir != filepath.Join(root, "Local AppData", "Flinch") {
		t.Fatalf("state dir %q, %v", dir, err)
	}
	shim := filepath.Join(root, "shims", "flinch.exe")
	writeFixture(t, shim, []byte("shim"))
	var before string
	for _, version := range []string{"1.0", "1.1"} {
		bin := filepath.Join(root, "apps", "flinch", version, "flinch.exe")
		writeFixture(t, bin, []byte(version))
		writeFixture(t, filepath.Join(root, "shims", "flinch.shim"), []byte("path = \""+bin+"\"\r\n"))
		path, err := stableBinaryFor(bin, bin)
		if err != nil || path != shim {
			t.Fatalf("shim %q, %v", path, err)
		}
		registered, err := registeredBinary("codex", path)
		if err != nil {
			t.Fatal(err)
		}
		h := handler("codex", hookEvent{"Stop", 30}, registered)
		data, _ := jsonBytes(h)
		if before != "" && string(data) != before {
			t.Fatal("Scoop upgrade changed the Codex hook")
		}
		before = string(data)
	}
}
