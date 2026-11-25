package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureChecksums(t *testing.T) (string, map[string]string) {
	t.Helper()
	archives := map[string]string{}
	var lines strings.Builder
	for _, platform := range []string{"darwin", "linux", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			suffix := "tar.gz"
			if platform == "windows" {
				suffix = "zip"
			}
			name := "flinch_1.2.3_" + platform + "_" + arch + "." + suffix
			sum := sha256.Sum256([]byte(name))
			archives[name] = hex.EncodeToString(sum[:])
			lines.WriteString(archives[name] + "  " + name + "\n")
		}
	}
	path := filepath.Join(t.TempDir(), "checksums.txt")
	if err := os.WriteFile(path, []byte(lines.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, archives
}

func TestReleaseArchivesAndDigestsMatchForEachArchitecture(t *testing.T) {
	checksums, archives := fixtureChecksums(t)
	output := filepath.Join(t.TempDir(), "manifests")
	if err := run("v1.2.3", checksums, output); err != nil {
		t.Fatal(err)
	}
	formula, err := os.ReadFile(filepath.Join(output, "Formula", "flinch.rb"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(output, "flinch.json"))
	if err != nil {
		t.Fatal(err)
	}
	var scoop struct {
		Architecture map[string]archive `json:"architecture"`
		Bin          string             `json:"bin"`
	}
	if err := json.Unmarshal(data, &scoop); err != nil {
		t.Fatal(err)
	}
	for name, digest := range archives {
		url := "https://github.com/angelnicolasc/flinch/releases/download/v1.2.3/" + name
		if !strings.Contains(name, "windows") {
			if !strings.Contains(string(formula), `url "`+url+"\"\n") || !strings.Contains(string(formula), `sha256 "`+digest+`"`) {
				t.Fatalf("formula lacks %s", name)
			}
			continue
		}
		arch := "64bit"
		if strings.Contains(name, "arm64") {
			arch = "arm64"
		}
		spec := scoop.Architecture[arch]
		if spec.URL != url || spec.Hash != digest || spec.ExtractDir != strings.TrimSuffix(name, ".zip") {
			t.Fatalf("scoop %s: got %+v", arch, spec)
		}
	}
	if scoop.Bin != "flinch.exe" {
		t.Fatalf("bin = %q", scoop.Bin)
	}
}

func TestRejectsPrereleaseAndIncompleteChecksums(t *testing.T) {
	checksums, _ := fixtureChecksums(t)
	output := filepath.Join(t.TempDir(), "manifests")
	if err := run("v1.2.3-rc.1", checksums, output); err == nil {
		t.Fatal("accepted a prerelease tag")
	}
	data, err := os.ReadFile(checksums)
	if err != nil {
		t.Fatal(err)
	}
	first, _, _ := strings.Cut(string(data), "\n")
	if err := os.WriteFile(checksums, []byte(first+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run("v1.2.3", checksums, output); err == nil {
		t.Fatal("accepted incomplete checksums")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("a rejected input left output behind")
	}
}

func TestRejectsMalformedAndDuplicateEntries(t *testing.T) {
	checksums, _ := fixtureChecksums(t)
	data, err := os.ReadFile(checksums)
	if err != nil {
		t.Fatal(err)
	}
	first, _, _ := strings.Cut(string(data), "\n")
	for name, content := range map[string]string{
		"duplicate": string(data) + first + "\n",
		"path":      strings.Replace(string(data), "  flinch_1.2.3_linux_amd64", "  dist/flinch_1.2.3_linux_amd64", 1),
		"uppercase": strings.ToUpper(first[:64]) + first[64:] + "\n" + string(data),
	} {
		if err := os.WriteFile(checksums, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := manifests("v1.2.3", []byte(content)); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}
