package data

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// quoteIdent quotes a column name for SQL (like pqx's quote_ident). DuckDB
// takes a NUL for the end of the query, so such a name can't be referred to.
func quoteIdent(name string) (string, error) {
	if strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("column %s has a NUL character in its name, which SQL can't refer to", Sanitize(name))
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`, nil
}

// quoteStr is s as a SQL string literal.
func quoteStr(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

var globEscapes = strings.NewReplacer("*", "[*]", "?", "[?]", "[", "[[]")

// pathLiteral is path as a string literal for DuckDB's read_parquet, which
// takes it for a glob pattern: *, ? and [ are escaped, so it reads exactly
// that one file (pqx's path_literal).
func pathLiteral(path string) string {
	return quoteStr(globEscapes.Replace(path))
}

// ErrFilter is wrapped by CheckWhere's errors for filters refused before
// DuckDB sees them.
var ErrFilter = errors.New("filter refused")

type filterError struct{ msg string }

func (e *filterError) Error() string { return e.msg }
func (e *filterError) Unwrap() error { return ErrFilter }

var sqlStart = regexp.MustCompile(`(?i)^\s*(select|with|from|pivot|unpivot|describe|summarize)\b`)

// whereSQL is a filter as a condition to put in a query, parenthesized so it
// can't reach out (pqx's where_sql): its parentheses must balance (counted
// outside string literals, quoted names and comments), it must be one
// expression (no ; outside literals), and the closing parenthesis goes on a
// new line, out of reach of a trailing -- comment.
func whereSQL(where string) (string, error) {
	if strings.ContainsRune(where, 0) {
		return "", &filterError{"a filter can't hold a NUL character"}
	}
	if sqlStart.MatchString(where) {
		return "", &filterError{"a filter is a WHERE expression, not a query"}
	}
	depth := 0
	err := scanSQL(where, func(pos int, c byte) error {
		switch c {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return &filterError{fmt.Sprintf("unbalanced parentheses: the ) at character %d closes nothing", pos+1)}
			}
		case ';':
			return &filterError{fmt.Sprintf("a filter is one expression: the ; at character %d would end it", pos+1)}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if depth > 0 {
		return "", &filterError{fmt.Sprintf("unbalanced parentheses: %d ( left open", depth)}
	}
	return "(" + where + "\n)", nil
}

// scanSQL calls fn with each '(', ')' and ';' of s (byte offset and byte)
// that is outside string literals ('...', E'...' with backslash escapes,
// $tag$...$tag$), quoted identifiers ("...") and comments (-- to the end of
// the line, /* */, which nest), following DuckDB's (Postgres's) lexer. An
// unterminated literal, name or comment is an error.
func scanSQL(s string, fn func(pos int, c byte) error) error {
	n := len(s)
	for i := 0; i < n; {
		c := s[i]
		switch {
		case c == '(' || c == ')' || c == ';':
			if err := fn(i, c); err != nil {
				return err
			}
			i++
		case c == '-' && i+1 < n && s[i+1] == '-':
			for i < n && s[i] != '\n' && s[i] != '\r' {
				i++
			}
		case c == '/' && i+1 < n && s[i+1] == '*':
			start, depth := i, 0
			for {
				if i+1 >= n {
					return &filterError{fmt.Sprintf("the comment at character %d is not closed", start+1)}
				}
				if s[i] == '/' && s[i+1] == '*' {
					depth++
					i += 2
				} else if s[i] == '*' && s[i+1] == '/' {
					depth--
					i += 2
					if depth == 0 {
						break
					}
				} else {
					i++
				}
			}
		case c == '\'' || c == '"':
			backslash := c == '\'' && i > 0 && (s[i-1] == 'e' || s[i-1] == 'E') && !identByte(s, i-2)
			j, ok := skipQuoted(s, i, c, backslash)
			if !ok {
				what := "string"
				if c == '"' {
					what = "quoted name"
				}
				return &filterError{fmt.Sprintf("the %s at character %d is not closed", what, i+1)}
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
				return &filterError{fmt.Sprintf("the $-quoted string at character %d is not closed", i+1)}
			}
			i += 2*len(tag) + end
		case identByte(s, i):
			for i < n && identByte(s, i) {
				i++
			}
		default:
			i++
		}
	}
	return nil
}

// identByte reports whether s[i] can be part of an unquoted identifier or
// number (false outside s).
func identByte(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

// skipQuoted returns the offset just past the literal or name that starts
// with the quote q at s[i]; a doubled quote stands for itself.
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

// dollarTag returns the $tag$ (or $$) that starts at s[i], if one does.
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
