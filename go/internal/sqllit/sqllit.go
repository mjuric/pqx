// Package sqllit writes SQL text that goes into the filter box (Python
// pqx's sql_ident, sql_text_literal and sql_column_ref in pqx/data.py, and
// _filter_value's literals): names and values from the file are spelled so
// they can't become SQL of their own, and no control character of theirs
// is put in the box (they are spelled chr(N)). The filter bar's completion,
// the grid's "=" and the details pane's "=" use it.
package sqllit

import (
	"encoding/hex"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mjuric/pqx/go/internal/data"
)

// Keywords are DuckDB's keywords, lower case (keywords.go; a test checks
// them against the DuckDB pqx is built with).
var Keywords = func() map[string]bool {
	m := map[string]bool{}
	for _, k := range strings.Fields(keywordList) {
		m[k] = true
	}
	return m
}()

var plainIdent = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// IsPlainIdent reports whether name can go into SQL as it is: a plain
// identifier that isn't one of DuckDB's keywords.
func IsPlainIdent(name string) bool {
	return plainIdent.MatchString(name) && !Keywords[strings.ToLower(name)]
}

func quoteIdent(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

func quoteStr(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// Ident is name as it reads best in SQL: bare if that's safe, else quoted.
func Ident(name string) string {
	if IsPlainIdent(name) {
		return name
	}
	return quoteIdent(name)
}

// IsControl reports whether r is a C0 control, DEL or a C1 control (Python
// pqx's data._has_controls).
func IsControl(r rune) bool { return r < 0x20 || (r >= 0x7f && r < 0xa0) }

// HasControls reports whether s holds a character IsControl.
func HasControls(s string) bool { return strings.ContainsFunc(s, IsControl) }

// TextLiteral is a SQL expression for the string s with no control
// character in its text: those are spelled chr(N), joined to the rest with
// ||, so it can sit in the filter box and still match exactly.
func TextLiteral(s string) string {
	if !HasControls(s) {
		return quoteStr(s)
	}
	var parts []string
	var run strings.Builder
	for _, r := range s {
		if IsControl(r) {
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

// ColumnRef refers to the column DuckDB names name (data.Column.SQLName) in
// a filter shown in the box: Ident, unless the name has control
// characters, which no input box should hold; then COLUMNS(c -> c = <name
// spelled with chr()>), which matches it alone.
func ColumnRef(name string) string {
	if !HasControls(name) {
		return Ident(name)
	}
	return "COLUMNS(c -> c = " + TextLiteral(name) + ")"
}

// Equals is the condition "=" adds for value v of the column DuckDB names
// sqlName (Python pqx's _filter_value): col = v, or IS NULL, isnan() for
// NaN, typed literals for times and dates. ok is false for a value it can't
// match (binary, nested, times of day, durations: Python's "Can't filter
// on this value type").
//
// Unlike Python pqx, doubles of 16 or 17 digits match (see doubleLiteral),
// decimals of up to 38 digits match exactly (Python refuses decimals;
// DuckDB reads wider ones as doubles), timestamps with nanoseconds and at
// the ends of the range match (see timestampCond; Python's TIMESTAMP
// literal drops nanoseconds and matches nothing), infinities are written
// 'inf'::DOUBLE (Python
// writes inf, which DuckDB takes for a column name), and a UUID matches by
// its text (Python refuses it).
func Equals(sqlName string, v data.Value) (cond string, ok bool) {
	q := ColumnRef(sqlName)
	switch v := v.(type) {
	case nil:
		return q + " IS NULL", true
	case bool:
		if v {
			return q + " = true", true
		}
		return q + " = false", true
	case int64:
		return q + " = " + strconv.FormatInt(v, 10), true
	case uint64:
		return q + " = " + strconv.FormatUint(v, 10), true
	case float32:
		return floatCond(q, float64(v), true), true // (Python sees a float32 as the double it is)
	case float64:
		return floatCond(q, v, false), true
	case data.Decimal:
		// as the column's own DECIMAL(p,s): a number literal of 39 characters'
		// digits (0.000…) would be a DOUBLE
		if v.Unscaled == nil || v.Precision > 38 || v.Scale < 0 || v.Scale > v.Precision {
			return "", false
		}
		return q + " = '" + decimalText(v) + "'::DECIMAL(" + strconv.Itoa(int(v.Precision)) + "," + strconv.Itoa(int(v.Scale)) + ")", true
	case data.Timestamp:
		return timestampCond(q, v)
	case data.Date:
		switch {
		case v == math.MaxInt32:
			return q + " = 'infinity'::DATE", true
		case v == -math.MaxInt32:
			return q + " = '-infinity'::DATE", true
		}
		if y := v.Time().Year(); y < 1 || y > 9999 {
			return "", false
		}
		return q + " = DATE '" + v.Time().Format("2006-01-02") + "'", true
	case string:
		return q + " = " + TextLiteral(v), true
	case data.UUID:
		h := hex.EncodeToString(v[:])
		return q + " = '" + h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:] + "'", true
	}
	return "", false
}

// timestampCond matches a timestamp as DuckDB reads it from the file: its
// ±infinity (the int64 limits) by name; a nanosecond one with a
// TIMESTAMP_NS literal (a TIMESTAMP literal drops the nanoseconds), or by
// its count (epoch_ns) at the range's ends, which no literal reaches; a
// zoned nanosecond one before 1970, which DuckDB keeps in microseconds
// truncated toward zero, by epoch_us; others as Python's literals. A time outside years 1–9999 that isn't an
// infinity is refused.
func timestampCond(q string, v data.Timestamp) (string, bool) {
	typ := "TIMESTAMP"
	if v.Zoned {
		typ = "TIMESTAMPTZ"
	}
	if v.Unit == time.Nanosecond {
		inf := "TIMESTAMP_NS"
		if v.Zoned {
			inf = typ
		}
		ns := v.T.UnixNano()
		switch {
		case ns == math.MaxInt64:
			return q + " = 'infinity'::" + inf, true
		case ns == -math.MaxInt64:
			return q + " = '-infinity'::" + inf, true
		case v.Zoned && ns < 0 && ns%1000 != 0:
			// (a literal's extra digits are cut, which for a time before
			// 1970 isn't toward zero)
			return "epoch_us(" + q + ") = " + strconv.FormatInt(ns/1000, 10), true
		case !v.Zoned && (v.T.Year() <= 1677 || v.T.Year() >= 2262):
			return "epoch_ns(" + q + ") = " + strconv.FormatInt(ns, 10), true
		case !v.Zoned && ns%1000 != 0:
			return q + " = TIMESTAMP_NS '" + isoformat(v.T) + "'", true
		}
	}
	if v.Unit == time.Microsecond {
		switch us := v.T.UnixMicro(); {
		case v.T.Equal(time.UnixMicro(math.MaxInt64)):
			return q + " = 'infinity'::" + typ, true
		case v.T.Equal(time.UnixMicro(-math.MaxInt64)):
			return q + " = '-infinity'::" + typ, true
		case us == math.MaxInt64/1000 || us == -math.MaxInt64/1000:
			// DuckDB gives a nanosecond column's ±infinity read as
			// microseconds as these: either may be meant
			inf := "'infinity'"
			if us < 0 {
				inf = "'-infinity'"
			}
			return "(" + q + " = " + typ + " '" + isoformat(v.T) + offset(v) + "' or " + q + " = " + inf + "::" + typ + ")", true
		}
	}
	if y := v.T.Year(); y < 1 || y > 9999 {
		return "", false
	}
	return q + " = " + typ + " '" + isoformat(v.T) + offset(v) + "'", true
}

// offset is a zoned time's "+00:00" (pqx's times are UTC).
func offset(v data.Timestamp) string {
	if v.Zoned {
		return "+00:00"
	}
	return ""
}

func floatCond(q string, f float64, f32 bool) string {
	switch {
	case math.IsNaN(f):
		return "isnan(" + q + ")"
	case math.IsInf(f, 1):
		return q + " = 'inf'::DOUBLE"
	case math.IsInf(f, -1):
		return q + " = '-inf'::DOUBLE"
	}
	return q + " = " + doubleLiteral(f, f32)
}

// doubleLiteral is f as a literal DuckDB reads as exactly f: Python's repr,
// with "e0" added (a literal with an exponent is a DOUBLE, parsed exactly)
// when it has more than 15 significant digits, or always for a FLOAT
// column's value. DuckDB reads 1.9101520992509673 as a DECIMAL(17,16),
// whose cast to DOUBLE isn't correctly rounded for so many digits, and its
// cast of a DECIMAL to FLOAT rounds twice.
func doubleLiteral(f float64, f32 bool) string {
	s := pyRepr(f)
	if strings.ContainsRune(s, 'e') {
		return s
	}
	digits := strings.TrimLeft(strings.NewReplacer("-", "", ".", "").Replace(s), "0")
	if f32 || len(digits) > 15 {
		s += "e0"
	}
	return s
}

// pyRepr is Python's repr of a finite float.
func pyRepr(v float64) string {
	e := strconv.FormatFloat(v, 'e', -1, 64)
	x, _ := strconv.Atoi(e[strings.LastIndexByte(e, 'e')+1:])
	if -4 <= x && x < 16 {
		s := strconv.FormatFloat(v, 'f', -1, 64)
		if !strings.Contains(s, ".") {
			s += ".0"
		}
		return s
	}
	return e
}

// decimalText is d exactly, as a SQL number.
func decimalText(d data.Decimal) string {
	u := new(big.Int).Abs(d.Unscaled).String()
	if s := int(d.Scale); s > 0 {
		if len(u) <= s {
			u = strings.Repeat("0", s-len(u)+1) + u
		}
		u = u[:len(u)-s] + "." + u[len(u)-s:]
	} else if s < 0 {
		u += strings.Repeat("0", -s)
	}
	if d.Unscaled.Sign() < 0 {
		return "-" + u
	}
	return u
}

// isoformat is Python's datetime.isoformat() of t (UTC) without the zone:
// microseconds if any, nanoseconds if any (pandas' Timestamp).
func isoformat(t time.Time) string {
	t = t.UTC()
	s := t.Format("2006-01-02T15:04:05")
	switch ns := t.Nanosecond(); {
	case ns%1000 != 0:
		s += "." + strconv.Itoa(1_000_000_000 + ns)[1:]
	case ns != 0:
		s += "." + strconv.Itoa(1_000_000 + ns/1000)[1:]
	}
	return s
}

// And is cond added to the filter where. The filter is always put in
// parentheses, so an OR in it (however it is spaced) can't take the
// condition in, and its "--" comments become /* */ ones (OneLine), so they
// can't comment the condition out and the result holds on one line, as the
// filter box does. (Python pqx parenthesizes only a filter holding " or ",
// and a comment swallows the condition.)
func And(where, cond string) string {
	cur := strings.TrimSpace(OneLine(where))
	if cur == "" {
		return cond
	}
	return "(" + cur + ") and " + cond
}
