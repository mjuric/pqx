package fmtx

import (
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mjuric/pqx/go/internal/data"
)

// fixedDigits are the decimals of the fixed-point kinds, sigDigits the
// significant digits of the others (fmt.FIXED_DIGITS, fmt.SIG_DIGITS).
func fixedDigits(k Kind) (int, bool) {
	switch k {
	case KindMJD:
		return 7, true
	case KindAngle:
		return 6, true
	case KindMag:
		return 3, true
	}
	return 0, false
}

func sigDigits(k Kind) (int, bool) {
	switch k {
	case KindFlux:
		return 4, true
	case KindErr:
		return 3, true
	case KindFloat32:
		return 7, true
	case KindFloat:
		return 9, true
	}
	return 0, false
}

// noSpec: kinds a format spec doesn't apply to (fmt.NO_SPEC_KINDS).
func noSpec(k Kind) bool { return k == KindBool || k == KindBinary || k == KindNested }

// numericKind: kinds whose spec output is never cut to the width.
func numericKind(k Kind) bool {
	switch k {
	case KindInt, KindFloat, KindFloat32, KindMJD, KindAngle, KindMag, KindFlux, KindErr:
		return true
	}
	return false
}

// maxExactDecimal is the widest decimal (in digits) Python pqx holds as a
// Decimal; DuckDB gives it wider ones as floats.
const maxExactDecimal = 38

// formatValue is fmt.format_value.
func formatValue(v data.Value, k Kind, raw bool, width int, ov Override, safe bool) string {
	if v == nil {
		return Null
	}
	if d, ok := v.(data.Decimal); ok && d.Precision > maxExactDecimal && !raw {
		// Python pqx gets decimals wider than 38 digits as floats: the grid
		// shows them as such ("1e+46"); raw (details, copy) stays exact.
		v = decFloat(d)
	}
	var f float64
	isFloat := false
	switch x := v.(type) {
	case float64:
		f, isFloat = x, true
	case float32:
		f, isFloat = float64(x), true
	}
	if isFloat {
		if math.IsNaN(f) {
			return "NaN"
		}
		if math.IsInf(f, 0) {
			if f > 0 {
				return "∞"
			}
			return "-∞"
		}
		if raw {
			if x, ok := v.(float32); ok {
				return pyRepr(float64(x), 32)
			}
			return pyRepr(f, 64)
		}
	}
	if ov.Set && ov.Spec != "" && !raw && !noSpec(k) {
		if s, err := pyFormat(v, ov.Spec); err == nil {
			return finish(s, safe, width > 0 && !numericKind(k), width)
		}
	}
	digitsSet := ov.Set && ov.Spec == ""
	if d, ok := v.(data.Decimal); ok && digitsSet && !raw {
		f, isFloat = decFloat(d), true
	}
	if isFloat {
		if fd, ok := fixedDigits(k); ok {
			if digitsSet {
				fd = clampDigits(ov.Digits)
			}
			return pyFloatString(f, 'f', fd, false, false, false)
		}
		sd, ok := sigDigits(k)
		if !ok {
			sd = 9
		}
		if digitsSet {
			sd = max(1, clampDigits(ov.Digits))
		}
		return fmtFloat(f, sd)
	}
	var s string
	switch x := v.(type) {
	case bool:
		// ("✓" if v else "·" if not raw else str(v): true is ✓ even raw)
		switch {
		case x:
			return "✓"
		case raw:
			return "False"
		}
		return "·"
	case int64:
		return strconv.FormatInt(x, 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	case data.Timestamp:
		return strings.Replace(isoTimestamp(x), "+00:00", "Z", -1)
	case data.Date:
		return isoDate(x)
	case data.TimeOfDay:
		return isoTimeOfDay(x)
	case []byte:
		n := min(len(x), 16)
		more := ""
		if len(x) > 16 {
			more = "…"
		}
		return "0x" + hex.EncodeToString(x[:n]) + more + " (" + strconv.Itoa(len(x)) + " B)"
	case data.Struct:
		var b strings.Builder
		b.WriteByte('{')
		for i, fld := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(fld.Name) // (sanitized with the whole, below)
			b.WriteString(": ")
			b.WriteString(formatValue(fld.Value, guessKind(fld.Value), raw, DefaultWidth, Override{}, safe))
		}
		b.WriteByte('}')
		s = b.String()
	case data.List:
		return formatList(len(x), func(i int) data.Value { return x[i] }, raw, width, safe)
	case data.Map:
		return formatList(len(x), func(i int) data.Value { return data.List{x[i].Key, x[i].Value} }, raw, width, safe)
	default:
		s = pyStr(v)
	}
	return finish(s, safe, !raw && width > 0, width)
}

// finish sanitizes s (if safe) and cuts it to width with "…" (if cut). A
// long s is cut first: sanitizing never makes a character shorter, so the
// result is the same, without sanitizing megabytes for a 40-cell column.
func finish(s string, safe, cut bool, width int) string {
	if cut {
		if p := cutRunes(s, width); len(p) < len(s) {
			if safe {
				p = Sanitize(p, true)
			}
			return cutRunes(p, width-1) + "…"
		}
	}
	if safe {
		s = Sanitize(s, true)
	}
	if cut && utf8.RuneCountInString(s) > width {
		s = cutRunes(s, width-1) + "…"
	}
	return s
}

// formatList is format_value's list branch: items within the width's
// budget, "…" for the rest, and the count when there are more than 3.
func formatList(n int, at func(int) data.Value, raw bool, width int, safe bool) string {
	suffix := ""
	if n > 3 {
		suffix = " (" + strconv.Itoa(n) + ")"
	}
	budget := math.MaxInt
	if width > 0 && !raw {
		budget = width - len(suffix) - 3
	}
	var b strings.Builder
	b.WriteByte('[')
	used := 0
	for i := 0; i < n; i++ {
		x := at(i)
		it := formatValue(x, guessKind(x), raw, 0, Override{}, safe)
		l := utf8.RuneCountInString(it)
		if i > 0 {
			b.WriteString(", ")
		}
		if used+l+2 > budget {
			b.WriteString("…")
			break
		}
		b.WriteString(it)
		used += l + 2
	}
	b.WriteByte(']')
	b.WriteString(suffix)
	return b.String()
}

func guessKind(v data.Value) Kind {
	switch v.(type) {
	case float64, float32:
		return KindFloat
	}
	return KindStr
}

func clampDigits(d int) int { return max(0, min(d, MaxDigits)) }

// fmtFloat is fmt._fmt_float: sig significant digits, fixed-point for
// 1e-3 <= |v| < 1e9, else scientific.
func fmtFloat(v float64, sig int) string {
	if v == 0 {
		return "0"
	}
	a := math.Abs(v)
	if 1e-3 <= a && a < 1e9 {
		mag := floorLog10(a)
		dec := max(0, sig-1-mag)
		var s string
		if dec > 0 {
			// round(v, dec) (the double nearest the rounded decimal), then dec
			// decimals: the same text unless there are more digits than a
			// double holds
			s = strconv.FormatFloat(v, 'f', dec, 64)
			if sig > 15 {
				y, _ := strconv.ParseFloat(s, 64)
				s = strconv.FormatFloat(y, 'f', dec, 64)
			}
		} else {
			// round(v, sig-1-mag), a negative number of decimals, then no decimals
			s = roundToTens(v, mag+1-sig)
		}
		if strings.Contains(s, ".") {
			s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
		}
		return s
	}
	s := pyFloatString(v, 'e', max(sig-1, 1), false, false, false)
	e := strings.IndexByte(s, 'e')
	m, x := s[:e], s[e:]
	if strings.Contains(m, ".") {
		m = strings.TrimRight(strings.TrimRight(m, "0"), ".")
	}
	return m + x
}

// roundToTens is f"{round(v, -n):.0f}" for n >= 0: v correctly rounded
// (half to even) to a multiple of 10^n.
func roundToTens(v float64, n int) string {
	if n == 0 {
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	r := new(big.Rat)
	r.SetFloat64(v)
	p := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
	r.Quo(r, new(big.Rat).SetInt(p))
	q := roundHalfEven(r)
	q.Mul(q, p)
	// Python rounds to the nearest double, then prints it with no decimals.
	f, _ := new(big.Float).SetInt(q).Float64()
	if f == 0 && math.Signbit(v) {
		f = math.Copysign(0, -1)
	}
	return strconv.FormatFloat(f, 'f', 0, 64)
}

// roundHalfEven is r rounded to an integer, ties to even.
func roundHalfEven(r *big.Rat) *big.Int {
	num, den := r.Num(), r.Denom()
	q, m := new(big.Int).QuoRem(num, den, new(big.Int))
	if m.Sign() == 0 {
		return q
	}
	// |m|/den compared with 1/2
	twice := new(big.Int).Mul(new(big.Int).Abs(m), big.NewInt(2))
	c := twice.Cmp(den)
	away := c > 0 || (c == 0 && q.Bit(0) == 1)
	if away {
		if num.Sign() < 0 {
			q.Sub(q, big.NewInt(1))
		} else {
			q.Add(q, big.NewInt(1))
		}
	}
	return q
}

var pow10 = [...]float64{1e-3, 1e-2, 1e-1, 1, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9}

// log10Up[k] is the smallest double whose log10, as glibc rounds it, is k
// (for k where that is below 10^k): math.floor(math.log10(a)) in Python
// reads k for these values even though a < 10^k.
var log10Up = map[int]float64{
	-2: 0.009999999999999995, -1: 0.09999999999999998, 2: 99.99999999999999, 3: 999.9999999999994,
	4: 9999.999999999996, 5: 99999.99999999991, 6: 999999.999999999, 7: 9999999.99999999,
	8: 99999999.99999991, 9: 999999999.9999979,
}

// floorLog10 is math.floor(math.log10(a)) as Python computes it with
// glibc's correctly rounded log10, for 1e-3 <= a < 1e9.
func floorLog10(a float64) int {
	// the exact floor: the doubles nearest 1e-3, 1e-2 and 1e-1 lie above
	// the powers, so comparing with them is exact here
	e := -3
	for e < 9 && a >= pow10[e+1+3] {
		e++
	}
	if t, ok := log10Up[e+1]; ok && a >= t {
		return e + 1
	}
	return e
}

// maxFormatNumber caps a spec's width and precision: Python would make text
// of any size (a gigabyte for "1000000000d"); pqx's own checks stop at 64.
const maxFormatNumber = 10_000

// errTooBig is a spec asking for more than maxFormatNumber.
var errTooBig = pyError("widths and precisions are limited to 10000")

// pyFormat is Python's format(v, spec) for a value as Python pqx holds it.
func pyFormat(v data.Value, spec string) (string, error) {
	if m := stdSpec.FindStringSubmatch(spec); m != nil && (ndInt(m[1]) > maxFormatNumber || ndInt(m[2]) > maxFormatNumber) {
		switch v.(type) {
		case data.Timestamp, data.Date, data.TimeOfDay: // strftime: the digits are text
		default:
			return "", errTooBig
		}
	}
	switch x := v.(type) {
	case bool:
		if spec == "" {
			if x {
				return "True", nil
			}
			return "False", nil
		}
		n := int64(0)
		if x {
			n = 1
		}
		return formatInt(big.NewInt(n), spec, "bool")
	case int64:
		if spec == "" {
			return strconv.FormatInt(x, 10), nil
		}
		return formatInt(big.NewInt(x), spec, "int")
	case uint64:
		if spec == "" {
			return strconv.FormatUint(x, 10), nil
		}
		return formatInt(new(big.Int).SetUint64(x), spec, "int")
	case float64:
		return formatFloat(x, spec)
	case float32:
		return formatFloat(float64(x), spec)
	case string:
		return formatStr(x, spec)
	case data.UUID:
		return formatStr(uuidString(x), spec)
	case data.Decimal:
		return formatDecimal(x, spec)
	case data.Timestamp:
		if spec == "" {
			return isoTimestamp(x), nil
		}
		return strftime(pyTime{t: x.T.UTC(), zoned: x.Zoned, micros: x.T.Nanosecond() / 1000}, spec), nil
	case data.Date:
		if spec == "" {
			return isoDate(x), nil
		}
		return strftime(pyTime{t: x.Time()}, spec), nil
	case data.TimeOfDay:
		if spec == "" {
			return isoTimeOfDay(x), nil
		}
		ns := int64(x)
		t := time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(ns))
		return strftime(pyTime{t: t, micros: int(ns/1000) % 1_000_000}, spec), nil
	}
	if spec == "" {
		return pyStr(v), nil
	}
	return "", pyError(fmt.Sprintf("unsupported format string passed to %s.__format__", pyTypeName(v)))
}

// pyTypeName is the Python type of a value as Python pqx holds it.
func pyTypeName(v data.Value) string {
	switch v.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case int64, uint64:
		return "int"
	case float64, float32:
		return "float"
	case string, data.UUID:
		return "str"
	case []byte:
		return "bytes"
	case data.Decimal:
		return "decimal.Decimal"
	case data.Timestamp:
		return "datetime.datetime"
	case data.Date:
		return "datetime.date"
	case data.TimeOfDay:
		return "datetime.time"
	case data.Duration, data.Interval:
		return "datetime.timedelta"
	case data.List, data.Map:
		return "list"
	case data.Struct:
		return "dict"
	}
	return "object"
}

// isoTimestamp is datetime.isoformat(sep=" "): microseconds when there are
// any, all nine digits when there are nanoseconds (as pandas shows them),
// and +00:00 for a timestamp with a time zone.
func isoTimestamp(ts data.Timestamp) string {
	t := ts.T.UTC()
	s := fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d", t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Second())
	s += fraction(int64(t.Nanosecond()))
	if ts.Zoned {
		s += "+00:00"
	}
	return s
}

// fraction is ".ffffff" or ".fffffffff" for ns nanoseconds, "" for none.
func fraction(ns int64) string {
	switch {
	case ns == 0:
		return ""
	case ns%1000 == 0:
		return fmt.Sprintf(".%06d", ns/1000)
	}
	return fmt.Sprintf(".%09d", ns)
}

func isoDate(d data.Date) string {
	t := d.Time()
	return fmt.Sprintf("%04d-%02d-%02d", t.Year(), int(t.Month()), t.Day())
}

func isoTimeOfDay(x data.TimeOfDay) string {
	ns := int64(x)
	sec := ns / 1e9
	return fmt.Sprintf("%02d:%02d:%02d", sec/3600, sec/60%60, sec%60) + fraction(ns%1e9)
}

func uuidString(u data.UUID) string {
	h := hex.EncodeToString(u[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// pyStr is str(v) for the values format_value shows as text.
func pyStr(v data.Value) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case int64:
		return strconv.FormatInt(x, 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	case float64:
		return pyRepr(x, 64)
	case float32:
		return pyRepr(float64(x), 64)
	case data.Decimal:
		return decString(x)
	case data.UUID:
		return uuidString(x)
	case data.Timestamp:
		return isoTimestamp(x)
	case data.Date:
		return isoDate(x)
	case data.TimeOfDay:
		return isoTimeOfDay(x)
	case data.Duration:
		return time.Duration(x).String()
	case data.Interval:
		return timedeltaString(x)
	case []byte:
		return pyBytesRepr(x)
	}
	return fmt.Sprint(v)
}

// timedeltaString is str(timedelta) of an interval as DuckDB hands it to
// Python (a month is 30 days; nanoseconds cut to microseconds).
func timedeltaString(iv data.Interval) string {
	us := (int64(iv.Months)*30+int64(iv.Days))*86_400_000_000 + iv.Nanos/1000
	days := floorDiv(us, 86_400_000_000)
	rest := us - days*86_400_000_000
	sec, micro := rest/1_000_000, rest%1_000_000
	s := fmt.Sprintf("%d:%02d:%02d", sec/3600, sec/60%60, sec%60)
	if micro != 0 {
		s += fmt.Sprintf(".%06d", micro)
	}
	if days != 0 {
		plural := ""
		if days != 1 && days != -1 {
			plural = "s"
		}
		s = fmt.Sprintf("%d day%s, %s", days, plural, s)
	}
	return s
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// pyBytesRepr is repr(bytes).
func pyBytesRepr(b []byte) string {
	q := byte('\'')
	if strings.IndexByte(string(b), '\'') >= 0 && strings.IndexByte(string(b), '"') < 0 {
		q = '"'
	}
	var s strings.Builder
	s.WriteString("b")
	s.WriteByte(q)
	for _, c := range b {
		switch {
		case c == q || c == '\\':
			s.WriteByte('\\')
			s.WriteByte(c)
		case c == '\t':
			s.WriteString(`\t`)
		case c == '\n':
			s.WriteString(`\n`)
		case c == '\r':
			s.WriteString(`\r`)
		case c < 0x20 || c >= 0x7f:
			fmt.Fprintf(&s, `\x%02x`, c)
		default:
			s.WriteByte(c)
		}
	}
	s.WriteByte(q)
	return s.String()
}
