package fmtx

import (
	"math"
	"strconv"
	"strings"
)

// Python's float to text (CPython's PyOS_double_to_string and
// format_float_short). Go's strconv rounds correctly, as Python's dtoa does,
// so only the layout has to be copied.

// pyRepr is Python's repr of a float: the shortest text that reads back as
// v, in fixed notation from 1e-4 to 1e16, with ".0" on whole numbers. bits
// is 32 for a float32 value (numpy's shortest float32 digits, as
// fmt.shortest gives them), else 64.
func pyRepr(v float64, bits int) string {
	switch {
	case math.IsNaN(v):
		return "nan"
	case math.IsInf(v, 1):
		return "inf"
	case math.IsInf(v, -1):
		return "-inf"
	}
	digits, decpt := shortestDigits(v, bits)
	return layoutFloat(math.Signbit(v), digits, decpt, 'r', 0, false, true, false)
}

// shortestDigits are the shortest digits that read back as |v| (no
// trailing zeros; "0" for zero) and the decimal point's position: |v| is
// 0.<digits> × 10^decpt.
func shortestDigits(v float64, bits int) (string, int) {
	return splitE(strconv.FormatFloat(math.Abs(v), 'e', -1, bits))
}

// precDigits are |v| correctly rounded to n significant digits, with
// trailing zeros removed (dtoa mode 2).
func precDigits(v float64, n int) (string, int) {
	d, p := splitE(strconv.FormatFloat(math.Abs(v), 'e', n-1, 64))
	return d, p
}

// splitE takes "d.ddde±XX" apart into digits (trailing zeros removed) and
// the decimal point's position.
func splitE(s string) (string, int) {
	e := strings.IndexByte(s, 'e')
	mant, exp := s[:e], s[e+1:]
	x, _ := strconv.Atoi(exp)
	digits := strings.Replace(mant, ".", "", 1)
	digits = strings.TrimRight(digits, "0")
	if digits == "" {
		return "0", 1
	}
	return digits, x + 1
}

// layoutFloat is format_float_short's layout of digits (decimal point at
// decpt) for format code 'e', 'f', 'g' or 'r', precision prec, alternate
// form alt, add_dot_0 (".0" on a whole number without exponent) and upper
// (E for e).
func layoutFloat(neg bool, digits string, decpt int, code byte, prec int, alt, addDot0, upper bool) string {
	digitsLen := len(digits)
	vEnd := digitsLen
	useExp := false
	switch code {
	case 'e':
		useExp = true
		vEnd = prec
	case 'f':
		vEnd = decpt + prec
	case 'g':
		lim := prec
		if addDot0 {
			lim = prec - 1
		}
		if decpt <= -4 || decpt > lim {
			useExp = true
		}
		if alt {
			vEnd = prec
		}
	case 'r':
		if decpt <= -4 || decpt > 16 {
			useExp = true
		}
	}
	exp := 0
	if useExp {
		exp = decpt - 1
		decpt = 1
	}
	vStart := 0
	if decpt <= 0 {
		vStart = decpt - 1
	}
	if !useExp && addDot0 {
		vEnd = max(vEnd, decpt+1)
	} else {
		vEnd = max(vEnd, decpt)
	}
	var b strings.Builder
	b.Grow(vEnd - vStart + 8)
	if neg {
		b.WriteByte('-')
	}
	if decpt <= 0 {
		b.WriteString(strings.Repeat("0", decpt-vStart))
		b.WriteByte('.')
		b.WriteString(strings.Repeat("0", -decpt))
	}
	if 0 < decpt && decpt <= digitsLen {
		b.WriteString(digits[:decpt])
		b.WriteByte('.')
		b.WriteString(digits[decpt:])
	} else {
		b.WriteString(digits)
	}
	if digitsLen < decpt {
		b.WriteString(strings.Repeat("0", decpt-digitsLen))
		b.WriteByte('.')
		b.WriteString(strings.Repeat("0", vEnd-decpt))
	} else {
		b.WriteString(strings.Repeat("0", vEnd-digitsLen))
	}
	s := b.String()
	if strings.HasSuffix(s, ".") && !alt {
		s = s[:len(s)-1]
	}
	if useExp {
		e := "e"
		if upper {
			e = "E"
		}
		sign := "+"
		if exp < 0 {
			sign = "-"
			exp = -exp
		}
		es := strconv.Itoa(exp)
		if len(es) < 2 {
			es = "0" + es
		}
		s += e + sign + es
	}
	return s
}

// pyFloatString is PyOS_double_to_string(v, code, prec, flags) for a
// finite or non-finite v: code is one of e E f F g G r; prec is ignored
// for r.
func pyFloatString(v float64, code byte, prec int, alt, addDot0, noNegZero bool) string {
	upper := code == 'E' || code == 'F' || code == 'G'
	lc := code | 0x20
	if math.IsNaN(v) || math.IsInf(v, 0) {
		s := "inf"
		if math.IsNaN(v) {
			s = "nan"
		}
		if upper {
			s = strings.ToUpper(s)
		}
		if math.Signbit(v) && !math.IsNaN(v) {
			s = "-" + s
		}
		return s
	}
	neg := math.Signbit(v)
	var digits string
	var decpt int
	switch lc {
	case 'e':
		prec++ // as PyOS_double_to_string does: the digits before the point count
		digits, decpt = precDigits(v, prec)
	case 'f':
		s := strconv.FormatFloat(math.Abs(v), 'f', prec, 64)
		if alt && prec == 0 {
			s += "."
		}
		if neg && !(noNegZero && allZero(s)) {
			s = "-" + s
		}
		return s
	case 'g':
		if prec == 0 {
			prec = 1
		}
		digits, decpt = precDigits(v, prec)
	case 'r':
		digits, decpt = shortestDigits(v, 64)
	}
	if neg && noNegZero && digits == "0" {
		neg = false
	}
	return layoutFloat(neg, digits, decpt, lc, prec, alt, addDot0, upper)
}

// allZero reports whether s's digits are all 0.
func allZero(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= '1' && c <= '9' {
			return false
		}
	}
	return true
}
