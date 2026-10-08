package grid

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/cells"
	"github.com/mjuric/pqx/go/internal/styled"
)

// The grid is drawn with SGR sequences learnt once per style from the
// theme (Look.Render of a marker character), rather than rendering each
// cell through Look.Render: a 300-column screen has thousands of cells.

// sgrPair is what goes before and after text in a style.
type sgrPair struct{ on, off string }

const marker = "X"

func (g *Grid) sgrOf(st styled.Style) sgrPair {
	if p, ok := g.sgr[st]; ok {
		return p
	}
	var p sgrPair
	if st != (styled.Style{}) {
		out := g.look.Render(styled.New(marker, st))
		if i := strings.Index(out, marker); i >= 0 {
			p = sgrPair{out[:i], out[i+len(marker):]}
		}
	}
	g.sgr[st] = p
	return p
}

// styledText is s in style st.
func (g *Grid) styledText(s string, st styled.Style) string {
	p := g.sgrOf(st)
	return p.on + s + p.off
}

// draw is a cell's text with its style (and spans, through the theme).
func (g *Grid) draw(t *cellTx, extra styled.Style) string {
	if len(t.spans) > 0 {
		return g.look.Render(styled.Text{Plain: t.plain, Style: t.style.Plus(extra), Spans: t.spans})
	}
	return g.styledText(t.plain, t.style.Plus(extra))
}

// View implements kit.Pane: the header, the body rows and the edge
// markers, in exactly w × h cells.
func (g *Grid) View(w, h int) string {
	if w != g.w || h != g.h {
		g.w, g.h = w, h
		g.scrollToCursor()
	}
	if w <= 0 || h <= 0 {
		return ""
	}
	g.fitVisible()
	var b strings.Builder
	b.Grow(w * h * 3)
	if len(g.cols) == 0 || !g.sized() {
		for i := 0; i < h; i++ {
			if i > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(spaces(w))
		}
		return b.String()
	}
	slots := g.layout()
	_, _, hl, hr := g.colWindow()
	bold := styled.Style{Bold: true}
	dim := g.look.Style("dim")
	right := g.right()
	lab := g.labelSlot()

	// header: names (bold) and types (dim)
	for line := 0; line < headerRows; line++ {
		if line > 0 {
			b.WriteByte('\n')
		}
		edge := " "
		if line == 0 && hl > 0 {
			edge = g.styledText("‹", bold)
		}
		b.WriteString(edge)
		b.WriteString(spaces(lab.sw))
		x := lab.x + lab.sw
		for _, s := range slots {
			c := g.cols[s.col]
			a, sub := g.header(c)
			text, st := a, bold
			if line == 1 {
				text, st = sub, dim
			}
			tw := cells.Width(text)
			jt := just(c)
			g.writeSlot(&b, s, g.styledText(text, st), tw, jt, styled.Style{})
			x += s.sw
		}
		b.WriteString(spaces(right - x))
		edge = " "
		if line == 0 && hr > 0 {
			edge = g.styledText("›", bold)
		}
		b.WriteString(edge)
	}

	n := g.bodyH()
	d := g.v
	lim := d.limit()
	cur := g.cursorStyle()
	for i := 0; i < n; i++ {
		b.WriteByte('\n')
		r := g.top + int64(i)
		b.WriteString(" ")
		if lim >= 0 && r >= lim {
			b.WriteString(spaces(right - edgeCells))
			b.WriteString(" ")
			continue
		}
		// row label: its file row (or position), dim, left aligned
		b.WriteString(spaces(pad))
		if d.loaded(r) {
			s := commas(g.labelOf(r))
			b.WriteString(g.styledText(s, dim))
			b.WriteString(spaces(lab.w - len(s)))
		} else {
			b.WriteString(g.styledText("·", dim))
			b.WriteString(spaces(lab.w - 1))
		}
		b.WriteString(spaces(pad))
		x := lab.x + lab.sw
		for _, s := range slots {
			t, ok := g.cellText(s.col, r)
			if !ok {
				ph := g.placeholder(g.cols[s.col])
				t = &ph
			}
			extra := styled.Style{}
			if r == g.curRow && s.col == g.curCol {
				extra = cur
			} else if s.col < g.pinned() {
				extra = bold
			}
			var out string
			switch {
			case extra != (styled.Style{}):
				out = g.draw(t, extra)
			case t.out != "":
				out = t.out
			default:
				out = g.draw(t, extra)
				t.out = out // (cached cells keep it)
			}
			g.writeSlot(&b, s, out, t.w, t.just, extra)
			x += s.sw
		}
		b.WriteString(spaces(right - x))
		b.WriteString(" ")
	}
	return b.String()
}

// cursorStyle is the cursor cell's: reverse video with focus, underlined
// without (Python pqx's #grid:blur cursor).
func (g *Grid) cursorStyle() styled.Style {
	if g.focused {
		return styled.Style{Reverse: true}
	}
	return styled.Style{Underline: true}
}

// writeSlot writes one column slot: padding, the text (drawn, tw cells
// wide) justified in the column width, padding. The cursor cell's padding
// takes its style too, as DataTable draws it. A clipped slot is cut at the
// screen's edge.
func (g *Grid) writeSlot(b *strings.Builder, s slot, out string, tw int, j styled.Justify, extra styled.Style) {
	full := s.w
	if s.clipped {
		full = g.colWidth(g.cols[s.col].Name)
	}
	gap := max(0, full-tw)
	var l, r int
	switch j {
	case styled.Right:
		l = gap
	case styled.Center:
		l, r = gap/2, gap-gap/2
	default:
		r = gap
	}
	if s.clipped {
		var cell strings.Builder
		g.writeCell(&cell, out, pad+l, r+pad, extra)
		b.WriteString(ansi.Truncate(cell.String(), s.sw, ""))
		return
	}
	g.writeCell(b, out, pad+l, r+pad, extra)
}

func (g *Grid) writeCell(b *strings.Builder, out string, l, r int, extra styled.Style) {
	if extra.Reverse || extra.Underline {
		b.WriteString(g.styledText(spaces(l), extra))
		b.WriteString(out)
		b.WriteString(g.styledText(spaces(r), extra))
		return
	}
	b.WriteString(spaces(l))
	b.WriteString(out)
	b.WriteString(spaces(r))
}

var spaceBuf = strings.Repeat(" ", 512)

func spaces(n int) string {
	if n <= 0 {
		return ""
	}
	if n <= len(spaceBuf) {
		return spaceBuf[:n]
	}
	return strings.Repeat(" ", n)
}

// commas formats n with thousands separators, as Python's f"{n:,}".
func commas(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [32]byte
	i := len(buf)
	for d := 0; ; d++ {
		if d > 0 && d%3 == 0 {
			i--
			buf[i] = ','
		}
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
		if n == 0 {
			break
		}
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// Subtitle implements kit.Framed: the column-position readout in the
// panel's bottom border, "‹ 11  ·  columns 12–21 of 64 · 3 pinned  ·
// 43 ›", empty when every column is on screen (Python's _render_hscroll).
func (g *Grid) Subtitle() styled.Text {
	n := len(g.cols)
	if !g.sized() || n == 0 {
		return styled.Text{}
	}
	first, last, hl, hr := g.colWindow()
	if hl == 0 && hr == 0 {
		return styled.Text{}
	}
	dim := g.look.Style("dim")
	bold := styled.Style{Bold: true}
	var t styled.Text
	sep := func() {
		if t.Plain != "" {
			t.Append("  ·  ", dim)
		}
	}
	if hl > 0 {
		t.Append("‹", bold)
		t.Append(" "+strconv.Itoa(hl), styled.Style{})
	}
	rng := strconv.Itoa(n) + " columns"
	if last >= first {
		rng = "columns " + strconv.Itoa(first+1) + "–" + strconv.Itoa(last+1) + " of " + strconv.Itoa(n)
	}
	if p := g.pinned(); p > 0 {
		rng += " · " + strconv.Itoa(p) + " pinned"
	}
	sep()
	t.Append(rng, dim)
	if hr > 0 {
		sep()
		t.Append(strconv.Itoa(hr)+" ", styled.Style{})
		t.Append("›", bold)
	}
	return t
}
