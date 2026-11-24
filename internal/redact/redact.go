package redact

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

const placeholder = "[REDACTED]"

type pattern struct {
	re   *regexp.Regexp
	repl string
}

var secretPatterns = []pattern{
	// PEM private key blocks
	{
		re:   regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`),
		repl: placeholder,
	},
	// GitHub tokens (classic, OAuth, user-to-server, server-to-server, refresh, fine-grained)
	{
		re:   regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,})\b`),
		repl: placeholder,
	},
	// OpenAI / Anthropic / Stripe style API keys
	{
		re:   regexp.MustCompile(`\b(?:sk-(?:ant-|proj-|live-|test-)?[A-Za-z0-9_\-]{20,})\b`),
		repl: placeholder,
	},
	// AWS access key IDs
	{
		re:   regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
		repl: placeholder,
	},
	// Slack tokens
	{
		re:   regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9\-]{10,}\b`),
		repl: placeholder,
	},
	// Bearer authorization headers
	{
		re:   regexp.MustCompile(`(?i)\b(Bearer\s+)[A-Za-z0-9\-._~+/]+=*`),
		repl: "${1}" + placeholder,
	},
	// Credentials embedded in URLs: scheme://user:pass@host
	{
		re:   regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^/\s:@]+:)([^/\s@]+)(@)`),
		repl: "${1}" + placeholder + "${3}",
	},
	// Key=value or key: value secret assignments
	{
		re:   regexp.MustCompile(`(?i)\b((?:api[_-]?key|access[_-]?token|auth[_-]?token|secret[_-]?key|client[_-]?secret|password|passwd|private[_-]?key)\s*[:=]\s*["']?)([^\s"',;}{]{6,})(["']?)`),
		repl: "${1}" + placeholder + "${3}",
	},
}

// Text replaces recognized secret patterns with a deterministic placeholder.
func Text(input string) string {
	out := input
	for _, p := range secretPatterns {
		out = p.re.ReplaceAllString(out, p.repl)
	}
	return out
}

// Truncate trims s to at most maxRunes Unicode code points.
func Truncate(s string, maxRunes int) string {
	if maxRunes <= 0 || utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	var b strings.Builder
	b.Grow(maxRunes)
	count := 0
	for _, r := range s {
		if count >= maxRunes {
			break
		}
		b.WriteRune(r)
		count++
	}
	return b.String()
}

// TruncateLines keeps at most maxLines lines from s.
func TruncateLines(s string, maxLines int) string {
	if maxLines <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	return strings.Join(lines[:maxLines], "\n") + "\n... [truncated]"
}

// Prompt redacts secrets and truncates user prompt text for local checkpoint storage.
func Prompt(input string, maxRunes int) string {
	if maxRunes <= 0 {
		maxRunes = 500
	}
	return Truncate(strings.TrimSpace(Text(input)), maxRunes)
}
