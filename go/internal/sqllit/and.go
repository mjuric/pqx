package sqllit

import "strings"

// needsParens reports whether a filter must be put in parentheses before a
// condition is added to it with "and": it has an OR at its top level (a
// token, outside literals, quoted names, comments and parentheses) or a
// comment. A filter it can't scan (an unclosed literal or comment) needs
// them too.
func needsParens(where string) bool {
	s, n := where, len(where)
	depth := 0
	for i := 0; i < n; {
		c := s[i]
		switch {
		case c == '-' && i+1 < n && s[i+1] == '-', c == '/' && i+1 < n && s[i+1] == '*':
			return true
		case c == '\'' || c == '"':
			backslash := c == '\'' && i > 0 && (s[i-1] == 'e' || s[i-1] == 'E') && !identByte(s, i-2)
			j, ok := skipQuoted(s, i, c, backslash)
			if !ok {
				return true
			}
			i = j
		case c == '$' && !identByte(s, i-1):
			tag, ok := dollarTag(s, i)
			if !ok {
				i++
				continue
			}
			end := strings.Index(s[i+len(tag):], tag)
			if end < 0 {
				return true
			}
			i += len(tag) + end + len(tag)
		case c == '(':
			depth++
			i++
		case c == ')':
			depth--
			i++
		case identByte(s, i):
			j := i
			for j < n && identByte(s, j) {
				j++
			}
			if depth <= 0 && strings.EqualFold(s[i:j], "or") {
				return true
			}
			i = j
		default:
			i++
		}
	}
	return false
}
