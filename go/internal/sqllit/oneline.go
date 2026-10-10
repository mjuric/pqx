package sqllit

import "strings"

// OneLine is the filter where with each "--" comment (to the end of its
// line) written as a /* */ comment, and the line break after it as a
// space, so the filter holds on one line (the filter box has one) and a
// condition added after it isn't commented out. Literals, quoted names and
// block comments are left as they are, as DuckDB's lexer reads them; a
// filter it can't scan (an unclosed literal) is returned as it is.
func OneLine(where string) string {
	if !strings.Contains(where, "--") {
		return where
	}
	var b strings.Builder
	s, n := where, len(where)
	for i := 0; i < n; {
		c := s[i]
		switch {
		case c == '-' && i+1 < n && s[i+1] == '-':
			j := i + 2
			for j < n && s[j] != '\n' && s[j] != '\r' {
				j++
			}
			text := strings.NewReplacer("*/", "* /", "/*", "/ *").Replace(s[i+2 : j])
			b.WriteString("/*" + text + " */")
			if j < n {
				b.WriteByte(' ') // (the line break)
				j++
				if s[j-1] == '\r' && j < n && s[j] == '\n' {
					j++
				}
			}
			i = j
		case c == '/' && i+1 < n && s[i+1] == '*':
			j, depth := i, 0
			for {
				if j+1 >= n {
					return where
				}
				if s[j] == '/' && s[j+1] == '*' {
					depth++
					j += 2
				} else if s[j] == '*' && s[j+1] == '/' {
					depth--
					j += 2
					if depth == 0 {
						break
					}
				} else {
					j++
				}
			}
			b.WriteString(s[i:j])
			i = j
		case c == '\'' || c == '"':
			backslash := c == '\'' && i > 0 && (s[i-1] == 'e' || s[i-1] == 'E') && !identByte(s, i-2)
			j, ok := skipQuoted(s, i, c, backslash)
			if !ok {
				return where
			}
			b.WriteString(s[i:j])
			i = j
		case c == '$' && !identByte(s, i-1):
			tag, ok := dollarTag(s, i)
			if !ok {
				b.WriteByte(c)
				i++
				continue
			}
			end := strings.Index(s[i+len(tag):], tag)
			if end < 0 {
				return where
			}
			j := i + 2*len(tag) + end
			b.WriteString(s[i:j])
			i = j
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// (DuckDB's lexer, as internal/data's scanSQL follows it)

func identByte(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

func skipQuoted(s string, i int, q byte, backslash bool) (int, bool) {
	for j := i + 1; j < len(s); j++ {
		switch {
		case backslash && s[j] == '\\':
			j++
		case s[j] == q:
			if j+1 < len(s) && s[j+1] == q {
				j++
				continue
			}
			return j + 1, true
		}
	}
	return 0, false
}

func dollarTag(s string, i int) (string, bool) {
	j := i + 1
	if j < len(s) && s[j] >= '0' && s[j] <= '9' {
		return "", false // $1: a parameter
	}
	for j < len(s) && s[j] != '$' {
		c := s[j]
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80) {
			return "", false
		}
		j++
	}
	if j >= len(s) {
		return "", false
	}
	return s[i : j+1], true
}
