// Package cursorlist is Python pqx's CursorList (an OptionList whose
// highlight is real reverse video) for the Go UI: a scrolling list of
// styled items with one highlighted, drawn in exactly w × h cells. The
// Stats column list and the Plot drop-down use it.
package cursorlist

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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
		return true, l.Move(-l.page())
	case "pgdown":
		return true, l.Move(l.page())
	case "home":
		return true, l.Move(-len(l.items))
	case "end":
		return true, l.Move(len(l.items))
	}
	return false, false
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

// Wrap splits t into lines of at most width cells as Rich wraps text
// (Text.wrap with fold): words with their trailing spaces are kept on a
// line while their text fits; a word longer than a line is cut into
// pieces of the line's width. A line broken off drops the spaces that end
// it.
func Wrap(t styled.Text, width int) []styled.Text {
	runes := []rune(t.Plain)
	w := func(a, b int) int { return ansi.StringWidth(string(runes[a:b])) }
	// words: [start, end of text, end with spaces)
	type word struct{ a, text, b int }
	var words []word
	for i := 0; i < len(runes); {
		a := i
		for i < len(runes) && runes[i] != ' ' {
			i++
		}
		e := i
		for i < len(runes) && runes[i] == ' ' {
			i++
		}
		if e == a && i == a {
			i++
		}
		words = append(words, word{a, e, i})
	}
	var cuts []int // line starts after the first
	pos := 0
	for _, wd := range words {
		tw := w(wd.a, wd.text)
		if pos > 0 && pos+tw > width {
			cuts = append(cuts, wd.a)
			pos = 0
		}
		if tw > width {
			// fold: pieces of the line's width
			start := wd.a
			for w(start, wd.text) > width {
				end := start
				for end < wd.text && w(start, end+1) <= width {
					end++
				}
				end = max(end, start+1)
				cuts = append(cuts, end)
				start = end
			}
			pos = w(start, wd.b)
			continue
		}
		pos += w(wd.a, wd.b)
	}
	if len(cuts) == 0 {
		return []styled.Text{t}
	}
	out := make([]styled.Text, 0, len(cuts)+1)
	prev := 0
	for k, c := range append(cuts, len(runes)) {
		e := c
		if k < len(cuts) {
			// a line broken off keeps no trailing spaces (Rich drops them:
			// a highlight doesn't cover them)
			for e > prev && runes[e-1] == ' ' {
				e--
			}
		}
		out = append(out, slice(t, prev, e))
		prev = c
	}
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
