package prove

import "strings"

// Tokens retain source lines and matched delimiters. Comments and string bodies
// are opaque so assertion-shaped text inside them cannot become a proof site.
type sourceToken struct {
	text string
	line int
	col  int
	end  int
}

func sourceTokens(src string, python bool) []sourceToken {
	var tokens []sourceToken
	var stack []int
	line, lineStart := 1, 0
	for i := 0; i < len(src); {
		start, firstLine, column := i, line, i-lineStart
		c := src[i]
		switch {
		case c == '\n':
			i++
			line++
			lineStart = i
			continue
		case c == ' ' || c == '\t' || c == '\r':
			i++
			continue
		case (python && c == '#') || (!python && strings.HasPrefix(src[i:], "//")):
			if n := strings.IndexByte(src[i:], '\n'); n >= 0 {
				i += n
			} else {
				i = len(src)
			}
			continue
		case !python && strings.HasPrefix(src[i:], "/*"):
			n := strings.Index(src[i+2:], "*/")
			if n < 0 {
				return nil
			}
			i += n + 4
		case !python && c == '/' && jsRegexAllowed(tokenAt(tokens, len(tokens)-1)):
			i = jsRegexEnd(src, i)
		case c == '\'' || c == '"' || c == 96:
			delim := src[i : i+1]
			if python && strings.HasPrefix(src[i:], strings.Repeat(delim, 3)) {
				delim = strings.Repeat(delim, 3)
			}
			i += len(delim)
			closed := false
			for i < len(src) {
				if strings.HasPrefix(src[i:], delim) {
					i += len(delim)
					closed = true
					break
				}
				if src[i] == '\\' && i+1 < len(src) {
					i++
				}
				i++
			}
			if !closed {
				return nil
			}
		case wordByte(c):
			i++
			for i < len(src) && wordByte(src[i]) {
				i++
			}
		default:
			i++
		}
		word := src[start:i]
		if n := strings.Count(word, "\n"); n > 0 {
			line += n
			lineStart = start + strings.LastIndexByte(word, '\n') + 1
		}
		if !python && strings.HasPrefix(word, "/*") {
			continue
		}
		idx := len(tokens)
		tokens = append(tokens, sourceToken{word, firstLine, column, -1})
		switch word {
		case "(", "[", "{":
			stack = append(stack, idx)
		case ")", "]", "}":
			if len(stack) == 0 {
				return nil
			}
			open := stack[len(stack)-1]
			if !(tokens[open].text == "(" && word == ")" || tokens[open].text == "[" && word == "]" || tokens[open].text == "{" && word == "}") {
				return nil
			}
			stack = stack[:len(stack)-1]
			tokens[open].end = idx
		}
	}
	if len(stack) != 0 {
		return nil
	}
	return tokens
}

func jsRegexAllowed(previous string) bool {
	switch previous {
	case "", "(", "[", "{", "=", ":", ",", "!", "?", ">", "return", "throw", "case":
		return true
	}
	return false
}

// A regex literal is opaque: a bracket or escaped parenthesis in its pattern
// cannot close the surrounding matcher call or test body.
func jsRegexEnd(src string, start int) int {
	inClass := false
	for i := start + 1; i < len(src) && src[i] != '\n' && src[i] != '\r'; i++ {
		switch src[i] {
		case '\\':
			i++
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '/':
			if !inClass {
				i++
				for i < len(src) && wordByte(src[i]) {
					i++
				}
				return i
			}
		}
	}
	return start + 1
}

func wordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '$' || c >= 0x80
}

func tokenAt(tokens []sourceToken, i int) string {
	if i < 0 || i >= len(tokens) {
		return ""
	}
	return tokens[i].text
}

func quotedTitle(s string) (string, bool) {
	if len(s) < 2 || s[0] != '\'' && s[0] != '"' && s[0] != 96 {
		return "", false
	}
	return s[1 : len(s)-1], true
}
