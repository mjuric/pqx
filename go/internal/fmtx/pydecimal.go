package fmtx

import (
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mjuric/pqx/go/internal/data"
)

// Python's decimal.Decimal: str() and format() (the semantics of
// _pydecimal, which CPython's C decimal module shares, except that the C
// module refuses "_" grouping; every error is "invalid format string", as
// the C module says).

// pyDec is a finite decimal: (-1)^neg × coeff × 10^exp, coeff a string of
// digits without leading zeros ("0" for zero).
type pyDec struct {
	neg   bool
	coeff string
	exp   int
}

func decOf(d data.Decimal) pyDec {
	if d.Unscaled == nil {
		return pyDec{coeff: "0", exp: -int(d.Scale)}
	}
	return pyDec{neg: d.Unscaled.Sign() < 0, coeff: new(big.Int).Abs(d.Unscaled).Text(10), exp: -int(d.Scale)}
}

func (d pyDec) isZero() bool { return d.coeff == "0" }

func (d pyDec) adjusted() int { return d.exp + len(d.coeff) - 1 }

// decString is str(Decimal): scientific notation when the exponent is
// positive or the adjusted exponent is below -6.
func decString(d data.Decimal) string {
	x := decOf(d)
	sign := ""
	if x.neg {
		sign = "-"
	}
	left := x.exp + len(x.coeff)
	var dot int
	if x.exp <= 0 && left > -6 {
		dot = left
	} else {
		dot = 1
	}
	var intPart, frac string
	switch {
	case dot <= 0:
		intPart, frac = "0", "."+strings.Repeat("0", -dot)+x.coeff
	case dot >= len(x.coeff):
		intPart = x.coeff + strings.Repeat("0", dot-len(x.coeff))
	default:
		intPart, frac = x.coeff[:dot], "."+x.coeff[dot:]
	}
	exp := ""
	if left != dot {
		exp = "E" + signed(left-dot)
	}
	return sign + intPart + frac + exp
}

// signed is n with its sign ("%+d").
func signed(n int) string {
	if n < 0 {
		return strconv.Itoa(n)
	}
	return "+" + strconv.Itoa(n)
}

// decFloat is float(Decimal), correctly rounded.
func decFloat(d data.Decimal) float64 {
	x := decOf(d)
	s := x.coeff + "e" + strconv.Itoa(x.exp)
	if x.neg {
		s = "-" + s
	}
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

// rescale is Decimal._rescale with ROUND_HALF_EVEN: d quantized to exponent exp.
func (d pyDec) rescale(exp int) pyDec {
	if d.isZero() {
		return pyDec{neg: d.neg, coeff: "0", exp: exp}
	}
	if d.exp >= exp {
		return pyDec{neg: d.neg, coeff: d.coeff + strings.Repeat("0", d.exp-exp), exp: exp}
	}
	coeff := d.coeff
	digits := len(coeff) + d.exp - exp
	if digits < 0 {
		coeff = "1"
		digits = 0
	}
	// round half even at position digits
	up := false
	rest := coeff[digits:]
	switch {
	case rest[0] > '5':
		up = true
	case rest[0] == '5':
		if strings.Trim(rest[1:], "0") != "" {
			up = true
		} else {
			up = digits > 0 && (coeff[digits-1]-'0')%2 == 1
		}
	}
	kept := coeff[:digits]
	if kept == "" {
		kept = "0"
	}
	if up {
		n, _ := new(big.Int).SetString(kept, 10)
		kept = n.Add(n, big.NewInt(1)).Text(10)
	}
	kept = strings.TrimLeft(kept, "0")
	if kept == "" {
		kept = "0"
	}
	return pyDec{neg: d.neg, coeff: kept, exp: exp}
}

// round is Decimal._round: d to places significant digits.
func (d pyDec) round(places int) pyDec {
	if d.isZero() {
		return d
	}
	ans := d.rescale(d.adjusted() + 1 - places)
	if ans.adjusted() != d.adjusted() {
		ans = ans.rescale(ans.adjusted() + 1 - places)
	}
	return ans
}

var decSpecRE = regexp.MustCompile(`^(?s)(?:(?P<fill>.)?(?P<align>[<>=^]))?(?P<sign>[-+ ])?(?P<z>z)?(?P<alt>#)?(?P<zero>0)?(?P<width>\p{Nd}+)?(?P<sep>,)?(?:\.(?P<prec>\p{Nd}+))?(?P<type>[eEfFgGn%])?$`)

var errDecSpec = pyError("invalid format string")

// formatDecimal is format(Decimal, spec).
func formatDecimal(dv data.Decimal, specText string) (string, error) {
	if specText == "" {
		return decString(dv), nil
	}
	if !utf8.ValidString(specText) {
		return "", errDecSpec
	}
	m := decSpecRE.FindStringSubmatch(specText)
	if m == nil {
		return "", errDecSpec
	}
	g := func(name string) string { return m[decSpecRE.SubexpIndex(name)] }
	fill, align := g("fill"), g("align")
	zeropad := g("zero") != ""
	if zeropad && (fill != "" || align != "") {
		return "", errDecSpec
	}
	if fill == "" {
		fill = " "
	}
	if align == "" {
		align = ">"
	}
	sign := g("sign")
	width := 0
	if w := g("width"); w != "" {
		width = ndInt(w)
	}
	prec := -1
	if p := g("prec"); p != "" {
		prec = ndInt(p)
	}
	typ := g("type")
	alt := g("alt") != ""
	noNegZero := g("z") != ""
	if prec == 0 && (typ == "" || strings.Contains("gGn", typ)) {
		prec = 1
	}
	sep := g("sep")
	if typ == "n" {
		if sep != "" {
			return "", errDecSpec
		}
		typ = "g" // the C locale: no grouping, "." for the point
	}
	if typ == "" {
		typ = "G"
	}
	d := decOf(dv)
	if typ == "%" {
		d.exp += 2
	}
	if prec >= 0 {
		switch typ {
		case "e", "E":
			d = d.round(prec + 1)
		case "f", "F", "%":
			d = d.rescale(-prec)
		case "g", "G":
			if len(d.coeff) > prec {
				d = d.round(prec)
			}
		}
	}
	if d.isZero() && d.exp > 0 && strings.Contains("fF%", typ) {
		d = d.rescale(0)
	}
	neg := d.neg
	if d.isZero() && noNegZero {
		neg = false
	}
	left := d.exp + len(d.coeff)
	var dot int
	switch typ {
	case "e", "E":
		if d.isZero() && prec >= 0 {
			dot = 1 - prec
		} else {
			dot = 1
		}
	case "f", "F", "%":
		dot = left
	default: // g, G
		if d.exp <= 0 && left > -6 {
			dot = left
		} else {
			dot = 1
		}
	}
	var intPart, frac string
	switch {
	case dot < 0:
		intPart, frac = "0", strings.Repeat("0", -dot)+d.coeff
	case dot > len(d.coeff):
		intPart = d.coeff + strings.Repeat("0", dot-len(d.coeff))
	default:
		intPart, frac = d.coeff[:dot], d.coeff[dot:]
		if intPart == "" {
			intPart = "0"
		}
	}
	exp := left - dot
	// _format_number
	signText := ""
	switch {
	case neg:
		signText = "-"
	case sign == "+":
		signText = "+"
	case sign == " ":
		signText = " "
	}
	if frac != "" || alt {
		frac = "." + frac
	}
	if exp != 0 || typ == "e" || typ == "E" {
		e := map[string]string{"E": "E", "e": "e", "G": "E", "g": "e"}[typ]
		frac += e + signed(exp)
	}
	if typ == "%" {
		frac += "%"
	}
	minWidth := 1
	if zeropad {
		minWidth = width - utf8.RuneCountInString(frac) - len(signText)
	}
	group := 3
	if sep == "" {
		group = 0
	}
	// _insert_thousands_sep: with no separator the digits are still padded.
	body := insertGrouping(intPart, minWidth, group, sep) + frac
	if zeropad {
		fill, align = "0", "="
	}
	padding := strings.Repeat(fill, max(0, width-len(signText)-utf8.RuneCountInString(body)))
	switch align {
	case "<":
		return signText + body + padding, nil
	case "=":
		return signText + padding + body, nil
	case "^":
		half := utf8.RuneCountInString(padding) / 2
		pr := []rune(padding)
		return string(pr[:half]) + signText + body + string(pr[half:]), nil
	}
	return padding + signText + body, nil
}
