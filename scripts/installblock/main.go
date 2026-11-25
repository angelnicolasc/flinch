// Command installblock prints the code block that follows a marker in a
// Markdown guide, so the weekly smoke test runs exactly what a reader copies.
// It fails when the marker is missing or repeated, or when the block that
// follows it isn't fenced in the expected language.
//
// Usage: go run ./scripts/installblock -marker posix-archive -lang sh docs/install.md
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	marker := flag.String("marker", "", "name in <!-- install-block: NAME -->")
	lang := flag.String("lang", "", "the language the block must be fenced in")
	flag.Parse()
	if flag.NArg() != 1 || *marker == "" || *lang == "" {
		fmt.Fprintln(os.Stderr, "usage: installblock -marker NAME -lang LANG FILE")
		os.Exit(2)
	}
	data, err := os.ReadFile(flag.Arg(0))
	if err == nil {
		var block string
		if block, err = extract(string(data), *marker, *lang); err == nil {
			fmt.Print(block)
			return
		}
	}
	fmt.Fprintln(os.Stderr, "installblock:", err)
	os.Exit(1)
}

func extract(doc, marker, lang string) (string, error) {
	tag := "<!-- install-block: " + marker + " -->"
	lines := strings.Split(strings.ReplaceAll(doc, "\r\n", "\n"), "\n")
	at := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == tag {
			if at >= 0 {
				return "", fmt.Errorf("marker %q appears more than once", marker)
			}
			at = i
		}
	}
	if at < 0 {
		return "", fmt.Errorf("marker %q not found", marker)
	}
	if at+1 >= len(lines) || strings.TrimSpace(lines[at+1]) != "```"+lang {
		return "", fmt.Errorf("marker %q is not followed by a ```%s block", marker, lang)
	}
	var body strings.Builder
	for _, line := range lines[at+2:] {
		if strings.TrimSpace(line) == "```" {
			return body.String(), nil
		}
		body.WriteString(line + "\n")
	}
	return "", errors.New("the block after marker " + marker + " never closes")
}
