package main

import (
	"os"
	"strings"
	"testing"
)

func TestExtractReturnsTheMarkedBlock(t *testing.T) {
	doc := "# Guide\n\n<!-- install-block: posix -->\n```sh\necho one\necho two\n```\n\n```sh\necho other\n```\n"
	got, err := extract(doc, "posix", "sh")
	if err != nil || got != "echo one\necho two\n" {
		t.Fatalf("got %q, %v", got, err)
	}
	if got, err := extract(strings.ReplaceAll(doc, "\n", "\r\n"), "posix", "sh"); err != nil || got != "echo one\necho two\n" {
		t.Fatalf("CRLF guide: got %q, %v", got, err)
	}
}

func TestExtractRejectsAmbiguousGuides(t *testing.T) {
	for name, doc := range map[string]string{
		"missing marker":    "```sh\necho\n```\n",
		"repeated marker":   "<!-- install-block: posix -->\n```sh\na\n```\n<!-- install-block: posix -->\n```sh\nb\n```\n",
		"other language":    "<!-- install-block: posix -->\n```powershell\nWrite-Host hi\n```\n",
		"text after marker": "<!-- install-block: posix -->\nRun this:\n```sh\necho\n```\n",
		"unclosed block":    "<!-- install-block: posix -->\n```sh\necho\n",
	} {
		if _, err := extract(doc, "posix", "sh"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The real guide must keep both blocks the weekly smoke test runs.
func TestInstallGuideKeepsItsSmokeBlocks(t *testing.T) {
	data, err := os.ReadFile("../../docs/install.md")
	if err != nil {
		t.Fatal(err)
	}
	for marker, lang := range map[string]string{"posix-archive": "sh", "windows-archive": "powershell"} {
		block, err := extract(string(data), marker, lang)
		if err != nil || !strings.Contains(block, "setup --plan") {
			t.Fatalf("%s: %v\n%s", marker, err, block)
		}
	}
}
