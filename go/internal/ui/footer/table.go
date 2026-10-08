package footer

import (
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

func (t *Table) line(look kit.Look, cells []styled.Text, base styled.Style) string {
	var b strings.Builder
	for i, w := range t.widths {
		c := styled.Text{}
		if i < len(cells) {
			c = cells[i]
		}
		c.Style = base.Plus(c.Style)
		c.Spans = append([]styled.Span(nil), c.Spans...)
		for j := range c.Spans {
			c.Spans[j].Style = base.Plus(c.Spans[j].Style)
		}
		pad := look.Render(styled.New(" ", base))
		b.WriteString(pad)
		b.WriteString(Pad(look.Render(c), Width(c.Plain), w, c.Justify))
		b.WriteString(pad)
	}
	return b.String()
}

// View draws the header and the rows from Top in w × h cells (h counts the
// header), the cursor row in style cur (nothing if cur is the zero Style).
func (t *Table) View(look kit.Look, w, h int, cur styled.Style) []string {
	if h <= 0 {
		return nil
	}
	t.scroll(h - 1)
	t.X = max(0, min(t.X, t.Width()-w))
	out := []string{t.cut(t.line(look, t.Header, styled.Style{}), w)}
	for i := t.Top; i < len(t.rows) && len(out) < h; i++ {
		base := styled.Style{}
		if i == t.Cursor {
			base = cur
		}
		out = append(out, t.cut(t.line(look, t.rows[i], base), w))
	}
	for len(out) < h {
		out = append(out, strings.Repeat(" ", w))
	}
	return out
}

func (t *Table) cut(s string, w int) string {
	if t.X > 0 {
		s = ansi.Cut(s, t.X, t.X+w)
	}
	return Fit(s, w)
}
