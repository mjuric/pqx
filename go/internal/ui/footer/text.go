package footer

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/styled"
)

// Commas is n with thousands separators (Python's f"{n:,}").
func Commas(n int64) string {
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

// CommasF is x rounded to an integer with thousands separators (Python's
// f"{x:,.0f}", which rounds half to even).
func CommasF(x float64) string {
	s := strconv.FormatFloat(x, 'f', 0, 64)
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return Commas(n)
	}
	return s
}

// Width is the terminal cells s takes.
func Width(s string) int { return ansi.StringWidth(s) }

// Slice is runes [a, b) of t, keeping their styles.
func Slice(t styled.Text, a, b int) styled.Text {
	r := []rune(t.Plain)
	a, b = max(0, min(a, len(r))), max(0, min(b, len(r)))
	out := styled.Text{Plain: string(r[a:b]), Style: t.Style, Justify: t.Justify}
	for _, sp := range t.Spans {
		s, e := max(sp.Start, a), min(sp.End, b)
		if s < e {
			out.Spans = append(out.Spans, styled.Span{Start: s - a, End: e - a, Style: sp.Style})
		}
	}
	return out
}

// Wrap breaks t into lines of at most w cells, at "\n" and between words
// (dropping the spaces at a break), cutting words longer than a line, as
// Textual wraps a Static's text.
func Wrap(t styled.Text, w int) []styled.Text { return wrap(t, w, false) }

// WrapCell is Wrap as Rich wraps a table cell's text: the spaces at a break
// stay at the end of the line as far as they fit.
func WrapCell(t styled.Text, w int) []styled.Text { return wrap(t, w, true) }

func wrap(t styled.Text, w int, keep bool) []styled.Text {
	w = max(1, w)
	var out []styled.Text
	for _, line := range t.Lines() {
		r := []rune(line.Plain)
		if len(r) == 0 {
			out = append(out, line)
			continue
		}
		for start := 0; start < len(r); {
			end, cw := start, 0
			for end < len(r) {
				rw := ansi.StringWidth(string(r[end]))
				if cw+rw > w {
					break
				}
				cw += rw
				end++
			}
			if end == start { // a character wider than the line
				end++
			}
			// break before the last word that doesn't fit; the spaces
			// before it stay on the line as far as they fit (Rich's
			// divide_line, then rstrip_end)
			e, next := end, end
			if end < len(r) && r[end] != ' ' {
				for i := end - 1; i > start; i-- {
					if r[i] == ' ' {
						e, next = i, i
						for keep && e < end && r[e] == ' ' {
							e++
						}
						break
					}
				}
			}
			for !keep && e > start && end < len(r) && r[e-1] == ' ' {
				e--
			}
			out = append(out, Slice(line, start, e))
			for next < len(r) && r[next] == ' ' {
				next++
			}
			start = next
		}
	}
	return out
}

// Pad is s (rendered, w0 cells wide) padded to w cells as j says.
func Pad(s string, w0, w int, j styled.Justify) string {
	gap := max(0, w-w0)
	switch j {
	case styled.Right:
		return strings.Repeat(" ", gap) + s
	case styled.Center:
		return strings.Repeat(" ", gap/2) + s + strings.Repeat(" ", gap-gap/2)
	}
	return s + strings.Repeat(" ", gap)
}

// Fit pads or cuts s (which may hold SGR sequences) to exactly w cells.
func Fit(s string, w int) string {
	n := ansi.StringWidth(s)
	if n > w {
		return ansi.Truncate(s, w, "")
	}
	return s + strings.Repeat(" ", w-n)
}
