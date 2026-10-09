package footer

import (
	"math"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// Table is a table with a header and a row cursor, drawn as Textual's
// DataTable draws Schema's and Metadata's tables: each column as wide as its
// widest cell, one cell of padding on each side, the header bold, the
// cursor row in reverse video. It scrolls to keep the cursor in sight, and
// sideways when it is wider than its space.
type Table struct {
	Header []styled.Text
	rows   [][]styled.Text
	widths []int
	Cursor int // the cursor row (0 when there are no rows)
	Top    int // the first row shown
	X      int // cells scrolled sideways
}

// NewTable makes an empty table with header labels (right-justified where
// right says so).
func NewTable(labels []string, right []bool) *Table {
	t := &Table{}
	for i, l := range labels {
		c := styled.New(l, styled.Style{Bold: true})
		if right[i] {
			c.Justify = styled.Right
		}
		t.Header = append(t.Header, c)
		t.widths = append(t.widths, Width(l))
	}
	return t
}

// Add appends rows.
func (t *Table) Add(rows ...[]styled.Text) {
	for _, r := range rows {
		for i, c := range r {
			if i < len(t.widths) {
				t.widths[i] = max(t.widths[i], Width(c.Plain))
			}
		}
		t.rows = append(t.rows, r)
	}
}

// Len is the number of rows.
func (t *Table) Len() int { return len(t.rows) }

// Row is row i's cells.
func (t *Table) Row(i int) []styled.Text { return t.rows[i] }

// Width is the table's width in cells.
func (t *Table) Width() int {
	n := 0
	for _, w := range t.widths {
		n += w + 2
	}
	return n
}

// MoveTo puts the cursor on row i (clamped), scrolling it into a view of h
// rows (the header not counted).
func (t *Table) MoveTo(i, h int) {
	if len(t.rows) == 0 {
		t.Cursor = 0
		return
	}
	t.Cursor = max(0, min(i, len(t.rows)-1))
	t.scroll(h)
}

func (t *Table) scroll(h int) {
	h = max(1, h)
	if t.Cursor < t.Top {
		t.Top = t.Cursor
	}
	if t.Cursor >= t.Top+h {
		t.Top = t.Cursor - h + 1
	}
	t.Top = max(0, min(t.Top, len(t.rows)-h))
}

// ScrollBy moves the view d rows without moving the cursor (the wheel).
func (t *Table) ScrollBy(d, h int) {
	t.Top = max(0, min(t.Top+d, len(t.rows)-max(1, h)))
}

// ScrollX moves the view d cells sideways within w cells.
func (t *Table) ScrollX(d, w int) {
	t.X = max(0, min(t.X+d, t.Width()-w))
}

// Key handles a movement key with h rows in view: true if it was one.
func (t *Table) Key(k string, h, w int) bool {
	switch k {
	case "up", "k":
		t.MoveTo(t.Cursor-1, h)
	case "down", "j":
		t.MoveTo(t.Cursor+1, h)
	case "pgup":
		t.MoveTo(t.Cursor-max(1, h), h)
	case "pgdown", "space":
		t.MoveTo(t.Cursor+max(1, h), h)
	case "home", "ctrl+home":
		t.MoveTo(0, h)
	case "end", "ctrl+end":
		t.MoveTo(len(t.rows)-1, h)
	case "left":
		t.ScrollX(-4, w)
	case "right":
		t.ScrollX(4, w)
	default:
		return false
	}
	return true
}

// line is one row of the table drawn in fillW cells or more (the fill in
// style base), as Rich pads DataTable cells: a right-justified or centred
// cell's padding takes the cell's style, a left-justified cell's doesn't.
func (t *Table) line(cells []styled.Text, base styled.Style, fillW int) styled.Text {
	var lt styled.Text
	for i, w := range t.widths {
		c := styled.Text{}
		if i < len(cells) {
			c = cells[i]
		}
		u := c
		u.Style = base.Plus(c.Style)
		pad := max(0, w-Width(c.Plain))
		lt.Append(" ", base)
		switch c.Justify {
		case styled.Right:
			lt.Append(strings.Repeat(" ", pad), u.Style)
			lt.AppendText(u)
		case styled.Center:
			lt.Append(strings.Repeat(" ", pad/2), u.Style)
			lt.AppendText(u)
			lt.Append(strings.Repeat(" ", pad-pad/2), u.Style)
		default:
			lt.AppendText(u)
			lt.Append(strings.Repeat(" ", pad), base)
		}
		lt.Append(" ", base)
	}
	if n := Width(lt.Plain); n < fillW {
		lt.Append(strings.Repeat(" ", fillW-n), base)
	}
	return lt
}

// View draws the table in w × h cells, as Textual's DataTable with
// scrollbars of one cell: the header (bold across the width), the rows from
// Top, the cursor row in style cur across the width (nothing if cur is the
// zero Style), and a scrollbar on the right and/or at the bottom when the
// table is taller or wider than its space (in the colour of look's border).
func (t *Table) View(look kit.Look, w, h int, cur styled.Style) []string {
	if h <= 0 || w <= 0 {
		return nil
	}
	vw, vh := t.Width(), len(t.rows)+1
	showH, showV := vw > w, vh > h
	if showV && !showH {
		showH = vw > w-1
	}
	if showH && !showV {
		showV = vh > h-1
	}
	cw, ch := w, h
	if showV {
		cw--
	}
	if showH {
		ch--
	}
	cw, ch = max(1, cw), max(1, ch)
	t.scroll(ch - 1)
	t.X = max(0, min(t.X, vw-cw))
	fill := max(vw, t.X+cw)
	sb := look.Style("scrollbar")
	var thumbBg styled.Color // a focused table's thumb is on its tinted background
	if cur != (styled.Style{}) {
		thumbBg = look.Style("focus-background").Bg
	}
	var vbar []styled.Text
	if showV {
		vbar = scrollbar(ch, vh, ch, t.Top, true, sb, thumbBg)
	}
	row := func(lt styled.Text, y int) string {
		s := ansi.Cut(look.Render(lt), t.X, t.X+cw)
		s = Fit(s, cw)
		if showV {
			s += look.Render(vbar[y])
		}
		return s
	}
	out := []string{row(t.line(t.Header, styled.Style{Bold: true}, fill), 0)}
	for i := t.Top; i < len(t.rows) && len(out) < ch; i++ {
		base := styled.Style{}
		if i == t.Cursor {
			base = cur
		}
		out = append(out, row(t.line(t.rows[i], base, fill), len(out)))
	}
	for len(out) < ch {
		out = append(out, row(styled.Text{}, len(out)))
	}
	if showH {
		var b strings.Builder
		for _, c := range scrollbar(cw, vw, cw, t.X, false, sb, thumbBg) {
			b.WriteString(look.Render(c))
		}
		if showV {
			b.WriteString(look.Render(styled.New(" ", styled.Style{Bg: sb.Bg}))) // the corner
		}
		out = append(out, b.String())
	}
	return out
}

// scrollbar is Textual's ScrollBarRender.render_bar: size cells of a bar
// for a window of window cells at position over virtual cells, the thumb in
// st's Fg (in reverse, on thumbBg) on a track in its Bg, the thumb's ends in
// eighths of a cell.
func scrollbar(size, virtual, window, position int, vertical bool, st styled.Style, thumbBg styled.Color) []styled.Text {
	bar, back := st.Fg, st.Bg
	bars := []string{"▉", "▊", "▋", "▌", "▍", "▎", "▏", " "}
	if vertical {
		bars = []string{"▁", "▂", "▃", "▄", "▅", "▆", "▇", " "}
	}
	out := make([]styled.Text, size)
	for i := range out {
		out[i] = styled.New(" ", styled.Style{Bg: back})
	}
	if window >= virtual {
		window = 0
	}
	if window == 0 || size == 0 || virtual == 0 || size == virtual {
		return out
	}
	n := len(bars)
	thumb := math.Max(1, float64(window)/(float64(virtual)/float64(size)))
	pos := (float64(size) - thumb) * (float64(position) / float64(virtual-window))
	start := int(pos * float64(n))
	end := start + int(math.Ceil(thumb*float64(n)))
	si, sb := max(0, start)/n, max(0, start)%n
	ei, eb := max(0, end)/n, max(0, end)%n
	for i := si; i < min(ei, size); i++ {
		out[i] = styled.New(" ", styled.Style{Fg: bar, Bg: thumbBg, Reverse: true})
	}
	if si < size {
		if c := bars[n-1-sb]; c != " " {
			out[si] = styled.New(c, styled.Style{Fg: bar, Bg: back, Reverse: !vertical})
		}
	}
	if ei < size {
		if c := bars[n-1-eb]; c != " " {
			out[ei] = styled.New(c, styled.Style{Fg: bar, Bg: back, Reverse: vertical})
		}
	}
	return out
}
