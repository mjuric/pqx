package detail

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
)

// "=" in the pane: a condition matching the field's value, added to the
// view's filter (Python's _filter_value). The SQL helpers are Python's
// sql_ident, sql_text_literal and sql_column_ref (pqx/data.py): a name goes
// bare when that is safe, and no control character of a name or value is
// put into the filter as is (they are spelled chr(N)).

// condition is the SQL condition "column = value" for value v of the column
// DuckDB names sqlName, or "" for a value "=" can't match (binary, nested,
// decimal, time of day, durations: Python's "Can't filter on this value
// type").
func condition(sqlName string, v data.Value) string {
	q := sqlColumnRef(sqlName)
	switch x := v.(type) {
	case nil:
		return q + " IS NULL"
	case bool:
		if x {
			return q + " = true"
		}
		return q + " = false"
	case int64:
		return q + " = " + strconv.FormatInt(x, 10)
	case uint64:
		return q + " = " + strconv.FormatUint(x, 10)
	case float64:
		if math.IsNaN(x) {
			return "isnan(" + q + ")"
		}
		return q + " = " + pyRepr(x)
	case float32:
		// Python sees a float32 as the double it is
		if math.IsNaN(float64(x)) {
			return "isnan(" + q + ")"
		}
		return q + " = " + pyRepr(float64(x))
	case data.Timestamp:
		if x.Zoned {
			return q + " = TIMESTAMPTZ '" + isoformat(x.T, true) + "'"
		}
		return q + " = TIMESTAMP '" + isoformat(x.T, false) + "'"
	case data.Date:
		return q + " = DATE '" + x.Time().Format("2006-01-02") + "'"
	case string:
		return q + " = " + sqlTextLiteral(x)
	}
	return ""
}

// pyRepr is Python's repr of a float.
func pyRepr(f float64) string {
	return fmtx.Format(f, fmtx.KindFloat, fmtx.Opts{Raw: true, Unsafe: true})
}

// isoformat is Python's datetime.isoformat: microseconds if any
// (nanoseconds if any, as pandas' Timestamp), "+00:00" for a zoned time
// (pqx's times are UTC).
func isoformat(t time.Time, zoned bool) string {
	t = t.UTC()
	s := t.Format("2006-01-02T15:04:05")
	switch ns := t.Nanosecond(); {
	case ns%1000 != 0:
		s += "." + strconv.Itoa(1_000_000_000 + ns)[1:]
	case ns != 0:
		s += "." + strconv.Itoa(1_000_000 + ns/1000)[1:]
	}
	if zoned {
		s += "+00:00"
	}
	return s
}

// combine adds cond to the filter cur (parenthesized when it has an OR).
func combine(cur, cond string) string {
	cur = strings.TrimSpace(cur)
	switch {
	case cur == "":
		return cond
	case strings.Contains(strings.ToLower(cur), " or "):
		return "(" + cur + ") and " + cond
	}
	return cur + " and " + cond
}

// isControl is Python pqx's _has_controls test for one character: C0 and C1
// controls and DEL.
func isControl(r rune) bool { return r < 0x20 || (r >= 0x7F && r < 0xA0) }

func hasControls(s string) bool {
	return strings.ContainsFunc(s, isControl)
}

func quoteStr(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// sqlTextLiteral is a SQL expression for s with no control character in its
// text: those are chr(N), joined to the rest with ||.
func sqlTextLiteral(s string) string {
	if !hasControls(s) {
		return quoteStr(s)
	}
	var parts []string
	var run strings.Builder
	for _, r := range s {
		if isControl(r) {
			if run.Len() > 0 {
				parts = append(parts, quoteStr(run.String()))
				run.Reset()
			}
			parts = append(parts, "chr("+strconv.Itoa(int(r))+")")
			continue
		}
		run.WriteRune(r)
	}
	if run.Len() > 0 {
		parts = append(parts, quoteStr(run.String()))
	}
	return "(" + strings.Join(parts, " || ") + ")"
}

var plainIdent = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// sqlIdent is name bare if it is a plain identifier and no keyword, else
// quoted.
func sqlIdent(name string) string {
	if plainIdent.MatchString(name) && !keywords[strings.ToLower(name)] {
		return name
	}
	return quoteIdent(name)
}

// sqlColumnRef refers to a column in a filter shown in the filter box:
// sqlIdent, or for a name with control characters a COLUMNS expression
// matching it alone.
func sqlColumnRef(name string) string {
	if !hasControls(name) {
		return sqlIdent(name)
	}
	return "COLUMNS(c -> c = " + sqlTextLiteral(name) + ")"
}

// keywords are DuckDB's keywords (keywords.go; a test checks them against
// the DuckDB pqx is built with).
var keywords = func() map[string]bool {
	m := map[string]bool{}
	for _, k := range strings.Fields(keywordList) {
		m[k] = true
	}
	return m
}()
