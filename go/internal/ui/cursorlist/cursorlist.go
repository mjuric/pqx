// Package cursorlist is Python pqx's CursorList (an OptionList whose
// highlight is real reverse video) for the Go UI: a scrolling list of
// styled items with one highlighted, drawn in exactly w × h cells. The
// Stats column list and the Plot drop-down use it.
package cursorlist

import (
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/cells"
	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/scrollbar"
)

// Item is one entry: an id (what the list is about, e.g. a column name)
// and its text.
type Item struct {
	ID   string
	Text styled.Text
}

// List is the list. The zero List is empty with nothing highlighted.
type List struct {
	items []Item
	// Width pads each item to this many cells (CursorList.set_items'
	// width); 0 pads to the list's width.
	Width int
	// Bar is the scrollbar's style (the Look's "scrollbar" role: Fg the
	// thumb, Bg the track); zero for the border colour.
	Bar styled.Style
	hl  int // highlighted index, -1 for none
	top int // first line shown
	h   int // rows shown at the last View
	// first is each item's first line at the last View (items wrap onto
	// several lines, as OptionList wraps its prompts), with the total
	// lines at the end; nil before the first View
	first []int
}

// SetItems replaces the items; nothing is highlighted afterwards (like
// OptionList.clear_options) and the list scrolls to the top.
func (l *List) SetItems(items []Item) {
	l.items = items
	l.hl = -1
	l.top = 0
	l.first = nil
}

// Len is the number of items.
func (l *List) Len() int { return len(l.items) }

// Items are the items.
func (l *List) Items() []Item { return l.items }

// Highlighted is the highlighted index, -1 for none.
func (l *List) Highlighted() int {
	if len(l.items) == 0 {
		return -1
	}
	return l.hl
}

// HighlightedID is the highlighted item's id, "" for none.
func (l *List) HighlightedID() string {
	if i := l.Highlighted(); i >= 0 {
		return l.items[i].ID
	}
	return ""
}

// Index is the index of the item with id, -1 if none.
func (l *List) Index(id string) int {
	for i, it := range l.items {
		if it.ID == id {
			return i
		}
	}
	return -1
}

// Highlight highlights item i (clamped), scrolling it into view.
func (l *List) Highlight(i int) {
	if len(l.items) == 0 {
		l.hl = -1
		return
	}
	l.hl = max(0, min(len(l.items)-1, i))
	l.scrollTo(l.hl)
}

// Move moves the highlight by d (from the first item when none is
// highlighted), and reports whether it moved.
func (l *List) Move(d int) bool {
	if len(l.items) == 0 {
		return false
	}
	old := l.hl
	if l.hl < 0 {
		l.Highlight(0)
	} else {
		l.Highlight(l.hl + d)
	}
	return l.hl != old
}

// page is how many rows a page key moves.
func (l *List) page() int { return max(1, l.h) }

// Key handles OptionList's keys (↑ ↓ PgUp PgDn Home End) and reports whether
// it used the key and whether the highlight moved.
func (l *List) Key(k tea.KeyPressMsg) (used, moved bool) {
	switch k.String() {
	case "up":
		return true, l.Move(-1)
	case "down":
		return true, l.Move(1)
	case "pgup":
		return true, l.movePage(-1)
	case "pgdown":
		return true, l.movePage(1)
	case "home":
		return true, l.Move(-len(l.items))
	case "end":
		return true, l.Move(len(l.items))
	}
	return false, false
}

// movePage moves the highlight a page of lines up or down (Textual's
// OptionList._move_page): to the item on the line a list's height from the
// highlighted item's first line; with nothing highlighted, to the first or
// the last item.
func (l *List) movePage(dir int) bool {
	if len(l.items) == 0 {
		return false
	}
	old := l.hl
	if l.hl < 0 {
		if dir < 0 {
			l.Highlight(0)
		} else {
			l.Highlight(len(l.items) - 1)
		}
		return l.hl != old
	}
	a, _ := l.span(l.hl)
	y := max(0, min(a+dir*l.page(), l.lines()-1))
	for i := range l.items {
		if s, e := l.span(i); y >= s && y < e {
			l.Highlight(i)
			break
		}
	}
	return l.hl != old
}

// Scroll scrolls the view by d rows without moving the highlight (the
// mouse wheel).
func (l *List) Scroll(d int) {
	l.top = max(0, min(max(0, l.lines()-l.page()), l.top+d))
}

// lines is the number of lines the items took at the last View (one each
// before it).
func (l *List) lines() int {
	if len(l.first) == len(l.items)+1 {
		return l.first[len(l.items)]
	}
	return len(l.items)
}

// span is item i's lines [a, b) at the last View.
func (l *List) span(i int) (int, int) {
	if len(l.first) == len(l.items)+1 {
		return l.first[i], l.first[i+1]
	}
	return i, i + 1
}

// At is the index of the item on row y of the list as last drawn, -1 if
// none.
func (l *List) At(y int) int {
	if y < 0 || y >= l.h {
		return -1
	}
	line := l.top + y
	for i := range l.items {
		if a, b := l.span(i); line >= a && line < b {
			return i
		}
	}
	return -1
}

func (l *List) scrollTo(i int) {
	if l.h == 0 {
		return // not drawn yet: View scrolls it into view
	}
	h := l.page()
	a, b := l.span(i)
	if a < l.top {
		l.top = a
	} else if b > l.top+h {
		l.top = min(a, b-h) // (an item taller than the list: its top)
	}
}

// View draws the list in w × h cells with render turning styled text into
// terminal output. When the items don't fit, the last column is Textual's
// scrollbar (OptionList's scrollbar-size 1) in the colours of Bar, and the
// items are a cell narrower.
func (l *List) View(w, h int, render func(styled.Text) string) string {
	l.h = h
	// the items' lines, wrapped as Rich wraps an option's prompt; a cell
	// narrower when a scrollbar is needed
	var lines [][]styled.Text
	layout := func(iw int) int {
		pad := l.Width
		if pad <= 0 || pad > iw {
			pad = iw
		}
		lines = lines[:0]
		l.first = make([]int, 0, len(l.items)+1)
		n := 0
		for _, it := range l.items {
			t := it.Text
			if k := ansi.StringWidth(t.Plain); k < pad {
				t.Append(strings.Repeat(" ", pad-k), styled.Style{})
			}
			ls := Wrap(t, max(1, iw))
			l.first = append(l.first, n)
			lines = append(lines, ls)
			n += len(ls)
		}
		l.first = append(l.first, n)
		return n
	}
	iw := w
	total := layout(iw)
	if total > h && w > 1 && h > 0 {
		iw = w - 1
		total = layout(iw)
	}
	if l.hl >= 0 {
		l.scrollTo(l.hl)
	}
	l.top = max(0, min(l.top, max(0, total-h)))
	var bar []styled.Text
	if iw < w {
		st := l.Bar
		if st == (styled.Style{}) {
			st = styled.Style{Fg: "border"}
		}
		bar = scrollbar.Vertical(h, total, l.top, st, "")
	}
	out := make([]string, h)
	for r := 0; r < h; r++ {
		line := l.top + r
		s := strings.Repeat(" ", iw)
		for i := range l.items {
			if a, b := l.span(i); line >= a && line < b {
				t := lines[i][line-a]
				if i == l.hl {
					t.Spans = append(t.Spans, styled.Span{Start: 0, End: len([]rune(t.Plain)), Style: styled.Style{Reverse: true}})
				}
				s = Fit(render(t), iw)
				break
			}
		}
		if bar != nil {
			s += render(bar[r])
		}
		out[r] = s
	}
	return strings.Join(out, "\n")
}

// Wrap splits t into lines of at most width cells as Textual wraps an
// option's prompt (Content._wrap_and_format with Rich's divide_line): words
// with the spaces after them stay on a line while their text fits; a word
// longer than a line is cut into pieces of the line's width (chop_cells,
// its spaces kept, so the next line can start with them); every line but
// the last loses its trailing whitespace, and lines are cut to the width
// (a wide character cut in half becomes a space).
func Wrap(t styled.Text, width int) []styled.Text {
	runes := []rune(t.Plain)
	var cuts []int
	offset := 0
	for _, wd := range words(runes) {
		start := wd[0]
		word := runes[wd[0]:wd[1]]
		wordLen := cellLen(trimRightSpace(word))
		switch {
		case width-offset >= wordLen:
			offset += cellLen(word)
		case wordLen > width:
			pieces := chopCells(word, width)
			for k, piece := range pieces {
				if start > 0 {
					cuts = append(cuts, start)
				}
				if k == len(pieces)-1 {
					offset = cellLen(piece)
				} else {
					start += len(piece)
				}
			}
		case offset > 0 && start > 0:
			cuts = append(cuts, start)
			offset = cellLen(word)
		}
	}
	out := make([]styled.Text, 0, len(cuts)+1)
	prev := 0
	ends := append(cuts, len(runes))
	for k, c := range ends {
		line := slice(t, prev, c)
		if k < len(ends)-1 {
			line = trimRight(line)
		}
		out = append(out, rstripEnd(truncate(line, width), width))
		prev = c
	}
	return out
}

// words are Rich's words: [start, end) of each match of \s*\S+\s*.
func words(r []rune) [][2]int {
	var out [][2]int
	i := 0
	for i < len(r) {
		a := i
		for i < len(r) && unicode.IsSpace(r[i]) {
			i++
		}
		if i == len(r) {
			break // only spaces left: no word
		}
		for i < len(r) && !unicode.IsSpace(r[i]) {
			i++
		}
		for i < len(r) && unicode.IsSpace(r[i]) {
			i++
		}
		out = append(out, [2]int{a, i})
	}
	return out
}

func trimRightSpace(r []rune) []rune {
	e := len(r)
	for e > 0 && unicode.IsSpace(r[e-1]) {
		e--
	}
	return r[:e]
}

// trimRight is Content.rstrip: the trailing whitespace dropped.
func trimRight(t styled.Text) styled.Text {
	r := []rune(t.Plain)
	return slice(t, 0, len(trimRightSpace(r)))
}

// rstripEnd is Content.rstrip_end: trailing whitespace beyond size
// characters dropped.
func rstripEnd(t styled.Text, size int) styled.Text {
	r := []rune(t.Plain)
	if len(r) <= size {
		return t
	}
	excess := len(r) - size
	ws := len(r) - len(trimRightSpace(r))
	return slice(t, 0, len(r)-min(ws, excess))
}

// grapheme is one of Rich's split_graphemes spans: runes [a, b) and cells.
type grapheme struct{ a, b, cells int }

// graphemes is Rich's split_graphemes: zero-width characters join the one
// before, a ZWJ the one after it too, a VS16 widens a narrow-to-wide one.
func graphemes(r []rune) []grapheme {
	var out []grapheme
	var last rune
	haveLast := false
	for i := 0; i < len(r); {
		c := r[i]
		if c == 0x200D || c == 0xFE0F {
			if len(out) == 0 {
				out = append(out, grapheme{i, i + 1, 0})
				i++
				continue
			}
			g := &out[len(out)-1]
			if c == 0x200D {
				if i < len(r)-1 {
					i += 2
				} else {
					i++
				}
			} else {
				i++
				if haveLast && cells.Width(string([]rune{last, 0xFE0F})) > cells.Width(string(last)) {
					g.cells++
					haveLast = false
				}
			}
			g.b = i
			continue
		}
		if w := cells.Width(string(c)); w > 0 {
			last, haveLast = c, true
			out = append(out, grapheme{i, i + 1, w})
		} else if len(out) > 0 {
			out[len(out)-1].b = i + 1
		} else {
			out = append(out, grapheme{i, i + 1, 0})
		}
		i++
	}
	return out
}

func cellLen(r []rune) int { return cells.Width(string(r)) }

// singleCells reports whether every rune is in Rich's single-cell ranges.
func singleCells(r []rune) bool {
	for _, c := range r {
		if !(c >= 0x20 && c <= 0x7E || c >= 0xA0 && c <= 0xAC || c >= 0xAE && c <= 0x2FF ||
			c >= 0x370 && c <= 0x482 || c >= 0x2500 && c <= 0x25FC || c >= 0x2800 && c <= 0x28FF) {
			return false
		}
	}
	return true
}

// chopCells is Rich's chop_cells: r cut into pieces of at most width cells.
func chopCells(r []rune, width int) [][]rune {
	var out [][]rune
	if singleCells(r) {
		for i := 0; i < len(r); i += width {
			out = append(out, r[i:min(len(r), i+width)])
		}
		return out
	}
	size, from := 0, 0
	for _, g := range graphemes(r) {
		if size+g.cells > width {
			out = append(out, r[from:g.a])
			from, size = g.a, 0
		}
		size += g.cells
	}
	if size > 0 {
		out = append(out, r[from:])
	}
	return out
}

// truncate is Content.truncate (no ellipsis): t cut to width cells, a wide
// character cut in half replaced by a space (Rich's set_cell_size).
func truncate(t styled.Text, width int) styled.Text {
	r := []rune(t.Plain)
	n := cellLen(r)
	if n <= width {
		return t
	}
	var keep []rune
	if singleCells(r) {
		keep = r[:width]
	} else if width > 0 {
		// (Rich splits by graphemes, whose widths can add up to less than
		// the cell length: then nothing is cut)
		keep = r
		size := 0
		for _, g := range graphemes(r) {
			if size+g.cells > width {
				keep = append(append([]rune{}, r[:g.a]...), []rune(strings.Repeat(" ", width-size))...)
				break
			}
			size += g.cells
		}
	}
	out := slice(t, 0, min(len(keep), len(r)))
	out.Plain = string(keep)
	return out
}

// slice is runes [a, b) of t with their styles.
func slice(t styled.Text, a, b int) styled.Text {
	r := []rune(t.Plain)
	out := styled.Text{Plain: string(r[a:b]), Style: t.Style, Justify: t.Justify}
	for _, sp := range t.Spans {
		if s, e := max(sp.Start, a), min(sp.End, b); s < e {
			out.Spans = append(out.Spans, styled.Span{Start: s - a, End: e - a, Style: sp.Style})
		}
	}
	return out
}

// Fit pads or cuts s (which may hold SGR sequences) to exactly w cells.
func Fit(s string, w int) string {
	n := ansi.StringWidth(s)
	if n > w {
		return ansi.Truncate(s, w, "")
	}
	return s + strings.Repeat(" ", w-n)
}
