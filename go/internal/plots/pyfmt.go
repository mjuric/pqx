package plots

import (
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mjuric/pqx/go/internal/styled"
)

// Python's number formatting and string helpers, as plots.py uses them.

// pyG is Python's f"{v:.{prec}g}".
func pyG(v float64, prec int) string {
	switch {
	case math.IsNaN(v):
		return "nan"
	case math.IsInf(v, 1):
		return "inf"
	case math.IsInf(v, -1):
		return "-inf"
	}
	if prec <= 0 {
		prec = 1
	}
	e := strconv.FormatFloat(v, 'e', prec-1, 64)
	k := strings.LastIndexByte(e, 'e')
	x, _ := strconv.Atoi(e[k+1:])
	if -4 <= x && x < prec {
		return stripZeros(strconv.FormatFloat(v, 'f', prec-1-x, 64))
	}
	return stripZeros(e[:k]) + e[k:]
}

// stripZeros drops trailing zeros after a decimal point, and the point.
func stripZeros(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

// pyRepr is Python's repr of a float.
func pyRepr(v float64) string {
	switch {
	case math.IsNaN(v):
		return "nan"
	case math.IsInf(v, 1):
		return "inf"
	case math.IsInf(v, -1):
		return "-inf"
	}
	e := strconv.FormatFloat(v, 'e', -1, 64)
	k := strings.LastIndexByte(e, 'e')
	x, _ := strconv.Atoi(e[k+1:])
	if -4 <= x && x < 16 {
		s := strconv.FormatFloat(v, 'f', -1, 64)
		if !strings.Contains(s, ".") {
			s += ".0"
		}
		return s
	}
	return e
}

// pyF is Python's f"{v:.{prec}f}".
func pyF(v float64, prec int) string {
	switch {
	case math.IsNaN(v):
		return "nan"
	case math.IsInf(v, 1):
		return "inf"
	case math.IsInf(v, -1):
		return "-inf"
	}
	return strconv.FormatFloat(v, 'f', prec, 64)
}

// commas is Python's f"{n:,}".
func commas(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

// rjust is Python's f"{s:>{w}}".
func rjust(s string, w int) string {
	if n := runeLen(s); n < w {
		return strings.Repeat(" ", w-n) + s
	}
	return s
}

// center is Python's str.center (the odd space goes left when width is odd).
func center(s string, width int) string {
	marg := width - runeLen(s)
	if marg <= 0 {
		return s
	}
	left := marg/2 + (marg & width & 1)
	return strings.Repeat(" ", left) + s + strings.Repeat(" ", marg-left)
}

// splice is Python's lst[a:a+len(items)] = items: the slice bounds clip to
// the list, so items reaching past its end make it longer.
func splice(lst []rune, a int, items string) []rune {
	it := []rune(items)
	b := min(a+len(it), len(lst))
	a = min(a, len(lst))
	out := make([]rune, 0, len(lst)+len(it))
	out = append(out, lst[:a]...)
	out = append(out, it...)
	return append(out, lst[b:]...)
}

// pyMod is Python's float %: the result takes the sign of b.
func pyMod(a, b float64) float64 {
	r := math.Mod(a, b)
	if r != 0 && (r < 0) != (b < 0) {
		r += b
	}
	return r
}

// pyMax and pyMin are Python's max(a, b) and min(a, b): the first argument
// unless the second compares greater (smaller), so a NaN second loses.
func pyMax(a, b float64) float64 {
	if b > a {
		return b
	}
	return a
}

func pyMin(a, b float64) float64 {
	if b < a {
		return b
	}
	return a
}

// Runtime constants, so Go's exact constant arithmetic doesn't round
// differently from Python's float64 arithmetic.
var (
	pi      = math.Pi
	rad2deg = 180.0 / pi
	deg2rad = pi / 180.0
)

// builder accumulates a styled.Text, merging runs of one style into one
// span (styled.Text.Append copies the whole string each time).
type builder struct {
	b     strings.Builder
	n     int // runes written
	spans []styled.Span
}

func (t *builder) add(s string, st styled.Style) {
	k := utf8.RuneCountInString(s)
	if k == 0 {
		return
	}
	t.b.WriteString(s)
	if st != (styled.Style{}) {
		if m := len(t.spans); m > 0 && t.spans[m-1].End == t.n && t.spans[m-1].Style == st {
			t.spans[m-1].End += k
		} else {
			t.spans = append(t.spans, styled.Span{Start: t.n, End: t.n + k, Style: st})
		}
	}
	t.n += k
}

func (t *builder) addText(u styled.Text) {
	n := t.n
	t.add(u.Plain, u.Style)
	for _, sp := range u.Spans {
		t.spans = append(t.spans, styled.Span{Start: sp.Start + n, End: sp.End + n, Style: sp.Style})
	}
}

func (t *builder) text() styled.Text {
	return styled.Text{Plain: t.b.String(), Spans: t.spans}
}
