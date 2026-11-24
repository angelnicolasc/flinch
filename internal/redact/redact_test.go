package redact

import (
	"strings"
	"testing"
)

func TestTextRedactsKnownSecretFormats(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		secret string
	}{
		{
			name:   "github token",
			input:  "Authorization: token ghp_1234567890abcdefghij1234567890",
			secret: "ghp_1234567890abcdefghij1234567890",
		},
		{
			name:   "anthropic key",
			input:  "ANTHROPIC_API_KEY=sk-ant-api03-abcdefghijklmnopqrstuvwx12345",
			secret: "sk-ant-api03-abcdefghijklmnopqrstuvwx12345",
		},
		{
			name:   "bearer header",
			input:  "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.abc.def",
			secret: "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
		},
		{
			name:   "url credentials",
			input:  "https://alice:supersecret99@example.com/repo.git",
			secret: "supersecret99",
		},
		{
			name:   "pem key",
			input:  "-----BEGIN OPENSSH PRIVATE KEY-----\nabcdef123456\n-----END OPENSSH PRIVATE KEY-----",
			secret: "abcdef123456",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := Text(tc.input)
			if strings.Contains(out, tc.secret) {
				t.Fatalf("expected secret %q to be redacted in %q", tc.secret, out)
			}
			if !strings.Contains(out, placeholder) {
				t.Fatalf("expected placeholder in %q", out)
			}
		})
	}
}

func TestPromptTruncatesAndRedacts(t *testing.T) {
	raw := "Fix auth with password=mysecretpassword123 " + strings.Repeat("x", 600)
	got := Prompt(raw, 500)
	if len([]rune(got)) != 500 {
		t.Fatalf("expected 500 runes, got %d", len([]rune(got)))
	}
	if strings.Contains(got, "mysecretpassword123") {
		t.Fatalf("expected password to be redacted")
	}
}
