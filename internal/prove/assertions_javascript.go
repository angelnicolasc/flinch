package prove

import (
	"regexp"
	"strings"
)

type jsCase struct {
	title       string
	first, last int
	isSuite     bool
}

var rowPlaceholder = regexp.MustCompile(`\$(?:name|title|desc|case)\b|%s`)

// jsCases locates literal test declarations and table callbacks. The runtime
// still has to emit the same qualified test identity in both trees.
func jsCases(ts []sourceToken) []jsCase {
	var cases []jsCase
	for i, t := range ts {
		if t.text != "test" && t.text != "it" && t.text != "describe" {
			continue
		}
		if tokenAt(ts, i-1) == "." {
			continue
		}
		open := i + 1
		var rowValues []string
		isTable := tokenAt(ts, open) == "." && tokenAt(ts, open+1) == "each"
		if isTable {
			open += 2
			if tokenAt(ts, open) == "<" {
				for open < len(ts) && ts[open].text != ">" {
					open++
				}
				open++
			}
			if tokenAt(ts, open) != "(" {
				continue
			}
			for _, v := range ts[open+1 : ts[open].end] {
				if value, ok := quotedTitle(v.text); ok {
					rowValues = append(rowValues, value)
				}
			}
			open = ts[open].end + 1
		}
		if tokenAt(ts, open) != "(" || tokenAt(ts, open+2) != "," {
			continue
		}
		title, ok := quotedTitle(tokenAt(ts, open+1))
		if !ok {
			continue
		}
		end := ts[open].end
		// Locate the callback body, skipping parameters and destructuring.
		first := -1
		for j := open + 3; j < end; j++ {
			if ts[j].text == "=" && tokenAt(ts, j+1) == ">" {
				first = j + 2
				break
			}
			if ts[j].text == "function" {
				for j++; j < end; j++ {
					if ts[j].text == "(" {
						first = ts[j].end + 1
						break
					}
				}
				break
			}
			if ts[j].end > j {
				j = ts[j].end
			}
		}
		if first < 0 || first >= end {
			continue
		}
		last := end
		if ts[first].text == "{" {
			last = ts[first].end
		}
		if isTable {
			if !rowPlaceholder.MatchString(title) {
				continue
			}
			for _, value := range rowValues {
				expanded := rowPlaceholder.ReplaceAllStringFunc(title, func(string) string { return value })
				cases = append(cases, jsCase{expanded, first, last, false})
			}
		} else {
			cases = append(cases, jsCase{title, first, last, t.text == "describe"})
		}
	}
	return cases
}

func jsAssertionLines(source, selector string) []int {
	ts := sourceTokens(source, false)
	wanted := normalizeSelectorForCompare(selector)
	cases := jsCases(ts)
	first, last := -1, 0
	for _, c := range cases {
		if c.isSuite {
			continue
		}
		var ancestors []string
		for _, s := range cases {
			if s.isSuite && s.first < c.first && s.last >= c.last {
				ancestors = append(ancestors, s.title)
			}
		}
		qualified := strings.Join(append(ancestors, c.title), " › ")
		if normalizeSelectorForCompare(qualified) != wanted && strings.ReplaceAll(qualified, " › ", " ") != wanted {
			continue
		}
		if first >= 0 && first != c.first {
			return nil
		}
		first, last = c.first, c.last
	}
	if first < 0 {
		return nil
	}
	var lines []int
	for i := first; i < last; i++ {
		// A helper declared inside a case owns its assertions. Its stack frame can
		// point inside this file even when the selected test has no assertion of its own.
		if ts[i].text == "function" {
			for j := i + 1; j < last; j++ {
				if ts[j].text == "(" {
					close := ts[j].end + 1
					if tokenAt(ts, close) == "{" {
						i = ts[close].end
					}
					break
				}
			}
			continue
		}
		if ts[i].text == "=" && tokenAt(ts, i+1) == ">" {
			body := i + 2
			if tokenAt(ts, body) == "{" {
				i = ts[body].end
				continue
			}
			// In an expression-bodied helper, skip the remainder of this expression.
			for j := body; j < last; j++ {
				if j > body && (ts[j].text == ";" || ts[j].text == "," || ts[j].text == "}" || ts[j].text == ")" || ts[j].line > ts[j-1].line && tokenAt(ts, j) != ".") {
					break
				}
				i = j
				if ts[j].end > j {
					i = ts[j].end
					j = i
				}
			}
			continue
		}
		if ts[i].text != "expect" || tokenAt(ts, i-1) == "." || tokenAt(ts, i+1) != "(" {
			continue
		}
		close := ts[i+1].end
		j := close + 1
		for tokenAt(ts, j) == "." {
			modifier := tokenAt(ts, j+1)
			if modifier != "not" && modifier != "rejects" && modifier != "resolves" {
				break
			}
			j += 2
		}
		matcher := tokenAt(ts, j+1)
		if tokenAt(ts, j) != "." || len(matcher) < 3 || !strings.HasPrefix(matcher, "to") || matcher[2] < 'A' || matcher[2] > 'Z' || tokenAt(ts, j+2) != "(" {
			continue
		}
		for _, idx := range []int{i, close, j + 1} {
			lines = append(lines, ts[idx].line)
		}
		for member := close + 2; member < j; member += 2 {
			lines = append(lines, ts[member].line)
		}
		i = ts[j+2].end
	}
	return lines
}
