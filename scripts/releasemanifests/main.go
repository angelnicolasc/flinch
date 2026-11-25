// Command releasemanifests writes the Homebrew formula and Scoop manifest for a
// stable Flinch release from that release's checksums.txt.
//
// Usage: go run ./scripts/releasemanifests <tag> <checksums.txt> <output-dir>
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	project     = "https://github.com/angelnicolasc/flinch"
	description = "Turn corrections to coding agents into repository checks"
)

var (
	stableTag     = regexp.MustCompile(`^v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)$`)
	checksumEntry = regexp.MustCompile(`^([0-9a-f]{64})  ([^/\\\s]+)$`)
)

type archive struct {
	URL        string `json:"url"`
	Hash       string `json:"hash"`
	ExtractDir string `json:"extract_dir"`
}

type scoopManifest struct {
	Version      string `json:"version"`
	Homepage     string `json:"homepage"`
	License      string `json:"license"`
	Description  string `json:"description"`
	Architecture struct {
		Bit64 archive `json:"64bit"`
		Arm64 archive `json:"arm64"`
	} `json:"architecture"`
	Bin string `json:"bin"`
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: releasemanifests <tag> <checksums.txt> <output-dir>")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2], os.Args[3]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(tag, checksumPath, output string) error {
	sums, err := os.ReadFile(checksumPath)
	if err != nil {
		return err
	}
	formula, scoop, err := manifests(tag, sums)
	if err != nil {
		return err
	}
	// Both documents are complete before anything is written, so a bad input leaves no output.
	if err := os.MkdirAll(filepath.Join(output, "Formula"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(output, "Formula", "flinch.rb"), formula, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(output, "flinch.json"), scoop, 0o644)
}

// manifests renders the Homebrew formula and the Scoop manifest for tag.
func manifests(tag string, checksumFile []byte) (formula, scoop []byte, err error) {
	if !stableTag.MatchString(tag) {
		return nil, nil, errors.New("package managers require a stable release tag such as v1.2.3")
	}
	version := tag[1:]
	checksums := map[string]string{}
	lines := bufio.NewScanner(bytes.NewReader(checksumFile))
	for lines.Scan() {
		entry := checksumEntry.FindStringSubmatch(lines.Text())
		if entry == nil {
			return nil, nil, errors.New("invalid or duplicate entry in checksums.txt")
		}
		if _, seen := checksums[entry[2]]; seen {
			return nil, nil, errors.New("invalid or duplicate entry in checksums.txt")
		}
		checksums[entry[2]] = entry[1]
	}
	if err := lines.Err(); err != nil {
		return nil, nil, err
	}

	release := func(platform, arch string) (archive, error) {
		suffix := "tar.gz"
		if platform == "windows" {
			suffix = "zip"
		}
		directory := fmt.Sprintf("flinch_%s_%s_%s", version, platform, arch)
		filename := directory + "." + suffix
		hash, ok := checksums[filename]
		if !ok {
			return archive{}, fmt.Errorf("missing checksum for %s", filename)
		}
		return archive{
			URL:        fmt.Sprintf("%s/releases/download/%s/%s", project, tag, filename),
			Hash:       hash,
			ExtractDir: directory,
		}, nil
	}

	rb := []string{
		"class Flinch < Formula",
		fmt.Sprintf(`  desc "%s"`, description),
		fmt.Sprintf(`  homepage "%s"`, project),
		fmt.Sprintf(`  version "%s"`, version),
		`  license "Apache-2.0"`,
	}
	for _, target := range []struct{ platform, block string }{{"darwin", "on_macos"}, {"linux", "on_linux"}} {
		rb = append(rb, "", "  "+target.block+" do")
		for _, cpu := range []struct{ arch, name string }{{"amd64", "intel"}, {"arm64", "arm"}} {
			record, err := release(target.platform, cpu.arch)
			if err != nil {
				return nil, nil, err
			}
			rb = append(rb,
				"    on_"+cpu.name+" do",
				fmt.Sprintf(`      url "%s"`, record.URL),
				fmt.Sprintf(`      sha256 "%s"`, record.Hash),
				"    end",
			)
		}
		rb = append(rb, "  end")
	}
	rb = append(rb,
		"",
		"  def install",
		`    bin.install "flinch"`,
		"  end",
		"",
		"  test do",
		`    assert_match version.to_s, shell_output("#{bin}/flinch --version")`,
		"  end",
		"end",
	)

	manifest := scoopManifest{Version: version, Homepage: project, License: "Apache-2.0", Description: description, Bin: "flinch.exe"}
	if manifest.Architecture.Bit64, err = release("windows", "amd64"); err != nil {
		return nil, nil, err
	}
	if manifest.Architecture.Arm64, err = release("windows", "arm64"); err != nil {
		return nil, nil, err
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		return nil, nil, err
	}
	return []byte(strings.Join(rb, "\n") + "\n"), out.Bytes(), nil
}
