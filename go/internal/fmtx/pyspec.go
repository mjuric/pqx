package fmtx

import (
	"fmt"
	"math"
	"math/big"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Python's format-spec mini-language for int, float and str (CPython's
// Python/formatter_unicode.c): [[fill]align][sign][z][#][0][width]
// [grouping][.precision][type]. Errors carry Python's messages, which the
// format dialog shows.

// pyError is a Python ValueError (or TypeError) message.
type pyError string

func (e pyError) Error() string { return string(e) }

// spec is a parsed format spec.
type spec struct {
	fill      rune
	align     rune // '<', '>', '=', '^'
	sign      rune // 0, '-', '+', ' '
	noNegZero bool
	alt       bool
	width     int // -1: none
	sep       int // sepNone, sepComma, sepUnder, sepUnderFour
	prec      int // -1: none
	typ       rune
}

const (
	sepNone = iota
	sepComma
	sepUnder
	sepUnderFour
)

const maxSpecInt = math.MaxInt64 // Py_ssize_t

func isAlign(c rune) bool { return c == '<' || c == '>' || c == '=' || c == '^' }

// parseSpec is parse_internal_render_format_spec: rs is the spec, defType
// and defAlign the object's defaults, typeName the Python type for messages.
func parseSpec(rs []rune, defType, defAlign rune, typeName string) (spec, error) {
	s := spec{fill: ' ', align: defAlign, width: -1, prec: -1, typ: defType}
	pos, end := 0, len(rs)
	fillSet, alignSet := false, false
	if end-pos >= 2 && isAlign(rs[pos+1]) {
		s.align, s.fill = rs[pos+1], rs[pos]
		fillSet, alignSet = true, true
		pos += 2
	} else if end-pos >= 1 && isAlign(rs[pos]) {
		s.align = rs[pos]
		alignSet = true
		pos++
	}
	if end-pos >= 1 && (rs[pos] == ' ' || rs[pos] == '+' || rs[pos] == '-') {
		s.sign = rs[pos]
		pos++
	}
	if end-pos >= 1 && rs[pos] == 'z' {
		s.noNegZero = true
		pos++
	}
	if end-pos >= 1 && rs[pos] == '#' {
		s.alt = true
		pos++
	}
	if !fillSet && end-pos >= 1 && rs[pos] == '0' {
		s.fill = '0'
		if !alignSet && defAlign == '>' {
			s.align = '='
		}
		pos++
	}
	n, consumed, err := getInteger(rs, &pos)
	if err != nil {
		return s, err
	}
	if consumed > 0 {
		s.width = n
	}
	if end-pos > 0 && rs[pos] == ',' {
		s.sep = sepComma
		pos++
	}
	if end-pos > 0 && rs[pos] == '_' {
		if s.sep != sepNone {
			return s, pyError("Cannot specify both ',' and '_'.")
		}
		s.sep = sepUnder
		pos++
	}
	if end-pos > 0 && rs[pos] == ',' && s.sep == sepUnder {
		return s, pyError("Cannot specify both ',' and '_'.")
	}
	if end-pos > 0 && rs[pos] == '.' {
		pos++
		n, consumed, err := getInteger(rs, &pos)
		if err != nil {
			return s, err
		}
		if consumed == 0 {
			return s, pyError("Format specifier missing precision")
		}
		s.prec = n
	}
	if end-pos > 1 {
		return s, pyError(fmt.Sprintf("Invalid format specifier '%s' for object of type '%s'", string(rs), typeName))
	}
	if end-pos == 1 {
		s.typ = rs[pos]
	}
	if s.sep != sepNone {
		switch s.typ {
		case 'd', 'e', 'f', 'g', 'E', 'G', '%', 'F', 0:
		case 'b', 'o', 'x', 'X':
			if s.sep == sepUnder {
				s.sep = sepUnderFour
				break
			}
			fallthrough
		default:
			c := ','
			if s.sep == sepUnder {
				c = '_'
			}
			if s.typ > 32 && s.typ < 128 {
				return s, pyError(fmt.Sprintf("Cannot specify '%c' with '%c'.", c, s.typ))
			}
			return s, pyError(fmt.Sprintf("Cannot specify '%c' with '\\x%x'.", c, s.typ))
		}
	}
	return s, nil
}

// getInteger reads the decimal digits at *pos (any Unicode decimal digit,
// as Python's Py_UNICODE_TODECIMAL).
func getInteger(rs []rune, pos *int) (n, consumed int, err error) {
	for *pos < len(rs) {
		d := decimalValue(rs[*pos])
		if d < 0 {
			break
		}
		if n > (maxSpecInt-d)/10 {
			return 0, 0, pyError("Too many decimal digits in format string")
		}
		n = n*10 + d
		*pos++
		consumed++
	}
	return n, consumed, nil
}

// decimalValue is the value of a Unicode decimal digit (category Nd), or -1.
func decimalValue(r rune) int {
	if r >= '0' && r <= '9' {
		return int(r - '0')
	}
	if r < 0x80 || !unicode.IsDigit(r) {
		return -1
	}
	// Nd characters come in runs of ten, 0 to 9: count the run back to its 0.
	k := 0
	for c := r - 1; unicode.IsDigit(c) && k < 64; c-- {
		k++
	}
	return k % 10
}

func unknownCode(typ rune, typeName string) error {
	if typ > 32 && typ < 128 {
		return pyError(fmt.Sprintf("Unknown format code '%c' for object of type '%s'", typ, typeName))
	}
	return pyError(fmt.Sprintf("Unknown format code '\\x%x' for object of type '%s'", typ, typeName))
}

// formatStr is format(s, spec) for a str.
func formatStr(s, specText string) (string, error) {
	if specText == "" {
		return s, nil
	}
	sp, err := parseSpec([]rune(specText), 's', '<', "str")
	if err != nil {
		return "", err
	}
	if sp.typ != 's' {
		return "", unknownCode(sp.typ, "str")
	}
	switch {
	case sp.sign == ' ':
		return "", pyError("Space not allowed in string format specifier")
	case sp.sign != 0:
		return "", pyError("Sign not allowed in string format specifier")
	case sp.noNegZero:
		return "", pyError("Negative zero coercion (z) not allowed in string format specifier")
	case sp.alt:
		return "", pyError("Alternate form (#) not allowed in string format specifier")
	case sp.align == '=':
		return "", pyError("'=' alignment not allowed in string format specifier")
	}
	n := utf8.RuneCountInString(s)
	if sp.prec >= 0 && n >= sp.prec {
		s = cutRunes(s, sp.prec)
		n = sp.prec
	}
	return pad(s, n, sp), nil
}

// pad pads s (n code points) to sp.width as calc_padding does.
func pad(s string, n int, sp spec) string {
	if sp.width <= n {
		return s
	}
	total := sp.width
	var l int
	switch sp.align {
	case '>':
		l = total - n
	case '^':
		l = (total - n) / 2
	}
	r := total - n - l
	f := string(sp.fill)
	return strings.Repeat(f, l) + s + strings.Repeat(f, r)
}

// cutRunes is the first n code points of s.
func cutRunes(s string, n int) string {
	i := 0
	for k := 0; k < n && i < len(s); k++ {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return s[:i]
}

// formatInt is format(v, spec) for an int (typeName "int", or "bool" for a
// bool, which formats as 0 or 1 under a non-empty spec).
func formatInt(v *big.Int, specText, typeName string) (string, error) {
	sp, err := parseSpec([]rune(specText), 'd', '>', typeName)
	if err != nil {
		return "", err
	}
	switch sp.typ {
	case 'b', 'c', 'd', 'o', 'x', 'X', 'n':
	case 'e', 'E', 'f', 'F', 'g', 'G', '%':
		f, _ := new(big.Float).SetInt(v).Float64()
		return formatFloatSpec(f, sp)
	default:
		return "", unknownCode(sp.typ, typeName)
	}
	if sp.prec != -1 {
		return "", pyError("Precision not allowed in integer format specifier")
	}
	if sp.noNegZero {
		return "", pyError("Negative zero coercion (z) not allowed in integer format specifier")
	}
	var digits, prefix string
	neg := v.Sign() < 0
	abs := new(big.Int).Abs(v)
	if sp.typ == 'c' {
		if sp.sign != 0 {
			return "", pyError("Sign not allowed with integer format specifier 'c'")
		}
		if sp.alt {
			return "", pyError("Alternate form (#) not allowed with integer format specifier 'c'")
		}
		if !v.IsInt64() || v.Int64() < 0 || v.Int64() > 0x10ffff {
			return "", pyError("%c arg not in range(0x110000)")
		}
		// The character is the whole number: no digits to group or pad with zeros.
		return fillNumber(false, "", string(rune(v.Int64())), "", "", 0, sp, true), nil
	}
	switch sp.typ {
	case 'b':
		digits, prefix = abs.Text(2), "0b"
	case 'o':
		digits, prefix = abs.Text(8), "0o"
	case 'x':
		digits, prefix = abs.Text(16), "0x"
	case 'X':
		digits, prefix = strings.ToUpper(abs.Text(16)), "0X"
	default:
		digits = abs.Text(10)
	}
	if !sp.alt {
		prefix = ""
	}
	if sp.sign != '+' && sp.sign != ' ' && sp.width == -1 && sp.typ != 'n' && sp.sep == sepNone {
		s := prefix + digits
		if neg {
			s = "-" + s
		}
		return s, nil
	}
	return fillNumber(neg, prefix, digits, "", "", groupSize(sp.sep), sp, false), nil
}

func groupSize(sep int) int {
	switch sep {
	case sepComma, sepUnder:
		return 3
	case sepUnderFour:
		return 4
	}
	return 0
}

func sepText(sep int) string {
	switch sep {
	case sepComma:
		return ","
	case sepUnder, sepUnderFour:
		return "_"
	}
	return ""
}

// formatFloat is format(v, spec) for a float.
func formatFloat(v float64, specText string) (string, error) {
	if specText == "" {
		return pyRepr(v, 64), nil
	}
	sp, err := parseSpec([]rune(specText), 0, '>', "float")
	if err != nil {
		return "", err
	}
	switch sp.typ {
	case 0, 'e', 'E', 'f', 'F', 'g', 'G', 'n', '%':
	default:
		return "", unknownCode(sp.typ, "float")
	}
	return formatFloatSpec(v, sp)
}

// formatFloatSpec is format_float_internal.
func formatFloatSpec(v float64, sp spec) (string, error) {
	typ := sp.typ
	prec := sp.prec
	addDot0 := false
	defaultPrec := 6
	if typ == 0 {
		addDot0 = true
		typ = 'r'
		defaultPrec = 0
	}
	if typ == 'n' {
		typ = 'g'
	}
	pct := false
	if typ == '%' {
		typ = 'f'
		v *= 100
		pct = true
	}
	if prec < 0 {
		prec = defaultPrec
	} else if typ == 'r' {
		typ = 'g'
	}
	buf := pyFloatString(v, byte(typ), prec, sp.alt, addDot0, sp.noNegZero)
	if pct {
		buf += "%"
	}
	if sp.sign != '+' && sp.sign != ' ' && sp.width == -1 && sp.typ != 'n' && sp.sep == sepNone {
		return buf, nil
	}
	neg := false
	if strings.HasPrefix(buf, "-") {
		neg = true
		buf = buf[1:]
	}
	i := 0
	for i < len(buf) && buf[i] >= '0' && buf[i] <= '9' {
		i++
	}
	digits, rest := buf[:i], buf[i:]
	dec := ""
	if strings.HasPrefix(rest, ".") {
		dec, rest = ".", rest[1:]
	}
	return fillNumber(neg, "", digits, dec, rest, groupSize(sp.sep), sp, false), nil
}

// fillNumber lays out a number as calc_number_widths and fill_number do:
// <lpadding><sign><prefix><spadding><grouped digits><decimal><remainder><rpadding>.
// literal is set for the 'c' type, whose "digit" is a character.
func fillNumber(neg bool, prefix, digits, dec, rest string, group int, sp spec, literal bool) string {
	sign := ""
	switch sp.sign {
	case '+':
		sign = "+"
		if neg {
			sign = "-"
		}
	case ' ':
		sign = " "
		if neg {
			sign = "-"
		}
	default:
		if neg {
			sign = "-"
		}
	}
	nonDigits := len([]rune(sign)) + len(prefix) + len(dec) + utf8.RuneCountInString(rest)
	minWidth := 0
	if sp.fill == '0' && sp.align == '=' {
		minWidth = sp.width - nonDigits
	}
	grouped := digits
	if !literal && digits != "" {
		grouped = insertGrouping(digits, minWidth, group, sepText(sp.sep))
	}
	n := nonDigits + utf8.RuneCountInString(grouped)
	var l, s, r int
	if p := sp.width - n; p > 0 {
		switch sp.align {
		case '<':
			r = p
		case '^':
			l = p / 2
			r = p - l
		case '=':
			s = p
		default:
			l = p
		}
	}
	f := string(sp.fill)
	var b strings.Builder
	b.WriteString(strings.Repeat(f, l))
	b.WriteString(sign)
	b.WriteString(prefix)
	b.WriteString(strings.Repeat(f, s))
	b.WriteString(grouped)
	b.WriteString(dec)
	b.WriteString(rest)
	b.WriteString(strings.Repeat(f, r))
	return b.String()
}

// insertGrouping is _PyUnicode_InsertThousandsGrouping: digits in groups of
// group (0: no grouping) separated by sep, padded with zeros (and
// separators) to at least minWidth characters.
func insertGrouping(digits string, minWidth, group int, sep string) string {
	var parts []string // right to left
	remaining := len(digits)
	end := len(digits)
	useSep := false
	emit := func(l int) {
		nZeros := max(0, l-remaining)
		nChars := max(0, min(remaining, l))
		if useSep {
			parts = append(parts, sep)
		}
		parts = append(parts, digits[end-nChars:end])
		if nZeros > 0 {
			parts = append(parts, strings.Repeat("0", nZeros))
		}
		end -= nChars
		remaining -= nChars
	}
	broken := false
	if group > 0 {
		for {
			l := min(group, max(remaining, minWidth, 1))
			emit(l)
			useSep = true
			minWidth -= l
			if remaining <= 0 && minWidth <= 0 {
				broken = true
				break
			}
			minWidth -= len(sep)
		}
	}
	if !broken {
		emit(max(remaining, minWidth, 1))
	}
	var b strings.Builder
	for i := len(parts) - 1; i >= 0; i-- {
		b.WriteString(parts[i])
	}
	return b.String()
}
