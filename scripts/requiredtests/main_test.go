package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func events(lines ...string) *strings.Reader {
	return strings.NewReader(strings.Join(lines, "\n") + "\n")
}

func TestRequiredTestsMustPass(t *testing.T) {
	stream := []string{
		`{"Action":"output","Package":"m/internal/a","Test":"TestPass","Output":"=== RUN TestPass\n"}`,
		`{"Action":"pass","Package":"m/internal/a","Test":"TestPass"}`,
		`{"Action":"output","Package":"m/internal/a","Test":"TestSkip","Output":"    a_test.go:9: pytest is not on PATH\n"}`,
		`{"Action":"skip","Package":"m/internal/a","Test":"TestSkip"}`,
		`{"Action":"output","Package":"m/internal/b","Test":"TestFail","Output":"    b_test.go:3: boom\n"}`,
		`{"Action":"fail","Package":"m/internal/b","Test":"TestFail"}`,
		`{"Action":"pass","Package":"m/internal/a","Test":"TestParent/sub"}`,
	}
	for _, tc := range []struct {
		name     string
		required []requirement
		ok       bool
		mention  string
	}{
		{"a passing test satisfies the list", []requirement{{"internal/a", "TestPass"}}, true, "1 required"},
		{"a subtest can be required", []requirement{{"internal/a", "TestParent/sub"}}, true, "1 required"},
		{"a skipped test fails the list", []requirement{{"internal/a", "TestSkip"}}, false, "TestSkip: skip"},
		{"a failing test fails the list", []requirement{{"internal/b", "TestFail"}}, false, "TestFail: fail"},
		{"a renamed or missing test fails the list", []requirement{{"internal/a", "TestRenamed"}}, false, "did not run"},
		{"the package must match", []requirement{{"internal/b", "TestPass"}}, false, "did not run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := check(events(stream...), &out, tc.required)
			if (err == nil) != tc.ok {
				t.Fatalf("ok = %t, want %t (%v)\n%s", err == nil, tc.ok, err, out.String())
			}
			got := out.String()
			if err != nil {
				got += err.Error()
			}
			if !strings.Contains(got, tc.mention) {
				t.Fatalf("output does not mention %q:\n%s", tc.mention, got)
			}
		})
	}
	var out bytes.Buffer
	_ = check(events(stream...), &out, nil)
	for _, want := range []string{"1 failed", "FAIL m/internal/b TestFail", "boom", "pytest is not on PATH"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("summary lacks %q:\n%s", want, out.String())
		}
	}
}

func TestListSelectsTheOperatingSystem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "required.txt")
	content := "# comment\nall internal/a TestA\nWindows internal/b TestB\nLinux internal/c TestC\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readList(path, "Windows")
	if err != nil || len(got) != 2 || got[1].test != "TestB" {
		t.Fatalf("Windows list: %v %v", got, err)
	}
	if err := os.WriteFile(path, []byte("all only-two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readList(path, "Linux"); err == nil {
		t.Fatal("accepted a malformed line")
	}
}
