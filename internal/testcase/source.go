// Package testcase identifies test declarations and their owned source regions.
package testcase

import (
	"regexp"
	"slices"
	"strings"

	"github.com/angelnicolasc/flinch/internal/gitutil"
)

// Region identifies the source owned by one runnable test case.
type Region struct {
	Name   string
	Source string
	Shared string // callback used by each row of a JS test.each table
	Lines  gitutil.LineRange
	leaf   string // unqualified runtime title for older JS selectors
}

// Tokens retain literal contents and matched delimiters so a comment, string,
// or sibling case cannot supply part of a selected test body.
type caseToken struct {
	text            string
	line, last, col int
	offset          int
	close           int
}

const quotedStrings = `"(?:\\[\s\S]|[^"\\])*"|'(?:\\[\s\S]|[^'\\])*'`
const otherTokens = `[[:space:]]+|[\pL\pN_$]+|(?s:.)`

var (
	pythonToken = regexp.MustCompile(`^(?:#[^\n]*|(?s:"""(?:\\.|[^\\])*?"""|'''(?:\\.|[^\\])*?''')|` + quotedStrings + `|` + otherTokens + `)`)
	jsToken     = regexp.MustCompile(`^(?://[^\n]*|(?s:/\*.*?\*/)|\x60(?:\\[\s\S]|[^\x60\\])*\x60|` + quotedStrings + `|` + otherTokens + `)`)
	jsRegexp    = regexp.MustCompile(`^/(?:\\[^\r\n]|\[(?:\\[^\r\n]|[^\]\\\r\n])*\]|[^/\\\[\r\n])+/[A-Za-z]*`)
)

func lexCaseSource(src string, python bool) []caseToken {
	lexer := jsToken
	if python {
		lexer = pythonToken
	}
	var ts []caseToken
	var stack []int
	line, col, offset := 1, 0, 0
	for len(src) > 0 {
		word := lexer.FindString(src)
		if !python && word == "/" && (len(ts) == 0 || slices.Contains([]string{"(", "[", "{", "=", ":", ",", "!", "?", ">", "return", "throw", "case"}, ts[len(ts)-1].text)) {
			if literal := jsRegexp.FindString(src); literal != "" {
				word = literal
			}
		}
		src = src[len(word):]
		t := caseToken{text: word, line: line, col: col, close: -1, offset: offset}
		offset += len(word)
		if n := strings.Count(word, "\n"); n > 0 {
			line += n
			col = len(word) - strings.LastIndexByte(word, '\n') - 1
		} else {
			col += len(word)
		}
		t.last = line
		if strings.TrimSpace(word) == "" || python && strings.HasPrefix(word, "#") || !python && (strings.HasPrefix(word, "//") || strings.HasPrefix(word, "/*")) {
			continue
		}
		switch word {
		case "(", "[", "{":
			stack = append(stack, len(ts))
		case ")", "]", "}":
			if len(stack) == 0 {
				return nil
			}
			open := stack[len(stack)-1]
			if !(ts[open].text == "(" && word == ")" || ts[open].text == "[" && word == "]" || ts[open].text == "{" && word == "}") {
				return nil
			}
			ts[open].close = len(ts)
			stack = stack[:len(stack)-1]
		}
		ts = append(ts, t)
	}
	if len(stack) != 0 {
		return nil
	}
	return ts
}

func at(ts []caseToken, i int) string {
	if i < 0 || i >= len(ts) {
		return ""
	}
	return ts[i].text
}

func regionText(ts []caseToken, python bool) string {
	var b strings.Builder
	indents := []int{0}
	depth := 0
	inPythonBody := false
	for i, t := range ts {
		if python && t.text == "\\" && i+1 < len(ts) && ts[i+1].line > t.last {
			continue
		}
		if python && !inPythonBody && depth == 0 && at(ts, i-1) == ":" {
			// A one-line test and its indented equivalent have the same body.
			inPythonBody = true
			indents[0] = t.col
		} else if python && depth == 0 && i > 0 && t.line > ts[i-1].last && ts[i-1].text != "\\" {
			// Indentation depth is meaningful in Python. Its width is formatting.
			for len(indents) > 1 && t.col < indents[len(indents)-1] {
				indents = indents[:len(indents)-1]
				b.WriteByte('\x02')
			}
			if t.col > indents[len(indents)-1] {
				indents = append(indents, t.col)
				b.WriteByte('\x01')
			}
			b.WriteByte('\n')
		} else if !python && i > 0 && t.line > ts[i-1].last {
			switch ts[i-1].text {
			case "return", "break", "continue", "yield":
				b.WriteByte('\n') // JavaScript restricts a line break after these keywords.
			}
		}
		if t.close >= 0 {
			depth++
		} else if t.text == ")" || t.text == "]" || t.text == "}" {
			depth--
		}
		b.WriteString(t.text)
		b.WriteByte(0)
	}
	return b.String()
}

// Python returns top-level pytest functions for reuse discovery.
func Python(path string, src []byte) []Region {
	ts := lexCaseSource(string(src), true)
	var out []Region
	for i := 0; i < len(ts); i++ {
		start := i
		if ts[i].col == 0 && ts[i].text == "async" && at(ts, i+1) == "def" {
			i++
		}
		if ts[start].col != 0 || ts[i].text != "def" || !strings.HasPrefix(at(ts, i+1), "test_") || at(ts, i+2) != "(" {
			continue
		}
		name := path + "::" + ts[i+1].text
		end := i + 1
		for end < len(ts) && !(ts[end].line > ts[end-1].last && ts[end].col == 0 && ts[end-1].text != "\\") {
			if ts[end].close >= 0 {
				end = ts[end].close
			}
			end++
		}
		out = append(out, Region{Name: name, Source: regionText(ts[start:end], true),
			Lines: gitutil.LineRange{Start: ts[start].line, End: ts[end-1].last}})
		i = end - 1
	}
	return out
}

func literalTitle(word string) string {
	if len(word) < 2 || word[0] != '"' && word[0] != '\'' && word[0] != 96 {
		return ""
	}
	return strings.TrimSpace(word[1 : len(word)-1])
}

func jsTableRowName(ts []caseToken, start int) string {
	if ts[start].text == "[" {
		return literalTitle(at(ts, start+1))
	}
	for i := start + 1; i < ts[start].close; i++ {
		switch ts[i].text {
		case "name", "desc", "title", "case":
			if at(ts, i+1) == ":" {
				return literalTitle(at(ts, i+2))
			}
		}
		if ts[i].close >= 0 {
			i = ts[i].close
		}
	}
	return ""
}

// JavaScript returns test declarations and named table rows for reuse discovery.
func JavaScript(src []byte) []Region {
	return javaScriptRegions(string(src), func(ts []caseToken) string { return regionText(ts, false) })
}

// JavaScriptCase returns the exact source of a uniquely identified case and its
// shared table callback. The raw text preserves line breaks significant to JavaScript.
func JavaScriptCase(src []byte, selector string) (Region, bool) {
	source := string(src)
	regions := javaScriptRegions(source, func(ts []caseToken) string {
		last := ts[len(ts)-1]
		return source[ts[0].offset : last.offset+len(last.text)]
	})
	var selected, legacy Region
	var leafMatches int
	for _, r := range regions {
		if r.leaf == selector {
			legacy = r
			leafMatches++
		}
		if r.Name != selector && strings.ReplaceAll(r.Name, " › ", " > ") != selector && strings.ReplaceAll(r.Name, " › ", " ") != selector {
			continue
		}
		if selected.Name != "" {
			return Region{}, false
		}
		selected = r
	}
	if selected.Name != "" {
		return selected, true
	}
	return legacy, leafMatches == 1
}

func javaScriptRegions(src string, contents func([]caseToken) string) []Region {
	ts := lexCaseSource(src, false)
	var out []Region
	type suite struct {
		prefix string
		end    int
	}
	var suites []suite
	for i, t := range ts {
		for len(suites) > 0 && suites[len(suites)-1].end < i {
			suites = suites[:len(suites)-1]
		}
		if t.text != "describe" && t.text != "test" && t.text != "it" {
			continue
		}
		if at(ts, i-1) == "." {
			continue
		}
		open, table := i+1, -1
		if at(ts, open) == "." && at(ts, open+1) == "each" {
			open += 2
			if at(ts, open) == "<" {
				for open < len(ts) && ts[open].text != ">" {
					open++
				}
				open++
			}
			if at(ts, open) != "(" || at(ts, open+1) != "[" {
				continue
			}
			table, open = open+1, ts[open].close+1
		}
		if at(ts, open) != "(" || at(ts, open+2) != "," {
			continue
		}
		title := literalTitle(at(ts, open+1))
		if title == "" {
			continue
		}
		prefix := ""
		if len(suites) > 0 {
			prefix = suites[len(suites)-1].prefix + " › "
		}
		close := ts[open].close
		if t.text == "describe" {
			suites = append(suites, suite{prefix + title, close})
			continue
		}
		body := contents(ts[open : close+1])
		if table < 0 {
			out = append(out, Region{Name: prefix + title, leaf: title, Source: body,
				Lines: gitutil.LineRange{Start: t.line, End: ts[close].last}})
			continue
		}
		for j := table + 1; j < ts[table].close; j++ {
			end := ts[j].close
			if end < 0 {
				continue
			}
			if ts[j].text == "{" || ts[j].text == "[" {
				row := ts[j : end+1]
				if name := jsTableRowName(ts, j); name != "" {
					expanded := title
					for _, ph := range []string{"$name", "$title", "$desc", "$case", "%s"} {
						expanded = strings.Replace(expanded, ph, name, 1)
					}
					out = append(out, Region{Name: prefix + expanded, leaf: expanded, Source: contents(row), Shared: body,
						Lines: gitutil.LineRange{Start: ts[j].line, End: ts[end].last}})
				}
			}
			j = end
		}
	}
	return out
}
