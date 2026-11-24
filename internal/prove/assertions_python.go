package prove

import "strings"

// pythonSuiteEnd skips complete expressions, so an unindented closing parenthesis
// on a multiline assertion cannot end the selected function or class.
func pythonSuiteEnd(tokens []sourceToken, start int) int {
	indent := tokens[start].col
	for i := start + 1; i < len(tokens); i++ {
		if tokens[i].line > tokens[i-1].line && tokens[i].col <= indent {
			return i
		}
		if tokens[i].end > i {
			i = tokens[i].end
		}
	}
	return len(tokens)
}

func pythonAssertionLines(source, selector string) []int {
	tokens := sourceTokens(source, true)
	parts := strings.Split(selector, "::")
	name, _, _ := strings.Cut(parts[len(parts)-1], "[")
	var class string
	if len(parts) >= 3 {
		class = parts[len(parts)-2]
	}
	start, end := 0, len(tokens)
	if class != "" {
		foundClass := false
		for i, t := range tokens {
			if t.text == "class" && tokenAt(tokens, i+1) == class {
				foundClass = true
				start, end = i+2, pythonSuiteEnd(tokens, i)
				break
			}
		}
		if !foundClass {
			return nil
		}
	}
	var lines []int
	found := false
	for i := start; i < end; i++ {
		if tokens[i].text != "def" || tokenAt(tokens, i+1) != name || tokenAt(tokens, i+2) != "(" {
			continue
		}
		if found {
			return nil
		}
		found = true
		begin := i
		if tokenAt(tokens, i-1) == "async" && tokens[i-1].line == tokens[i].line {
			begin--
		}
		stop := pythonSuiteEnd(tokens, begin)
		for j := tokens[i+2].end + 1; j < stop; j++ {
			t := tokens[j]
			if t.text == "def" || t.text == "class" {
				j = pythonSuiteEnd(tokens, j) - 1
				continue
			}
			if t.text == "assert" {
				// pytest may attribute an expression to its first or an inner source line.
				last := j
				for k := j + 1; k < stop && tokens[k].line == tokens[last].line && tokens[k].text != ";"; k++ {
					last = k
					if tokens[k].end > k {
						last = tokens[k].end
						k = last
					}
				}
				for l := t.line; l <= tokens[last].line; l++ {
					lines = append(lines, l)
				}
			}
			if t.text == "with" && tokenAt(tokens, j+1) == "pytest" && tokenAt(tokens, j+2) == "." && tokenAt(tokens, j+3) == "raises" && tokenAt(tokens, j+4) == "(" {
				for l := t.line; l <= tokens[tokens[j+4].end].line; l++ {
					lines = append(lines, l)
				}
			}
		}
		i = stop - 1
	}
	return lines
}
