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
	top int // first item shown
	h   int // rows shown at the last View
}

// SetItems replaces the items; nothing is highlighted afterwards (like
// OptionList.clear_options) and the list scrolls to the top.
func (l *List) SetItems(items []Item) {
	l.items = items
	l.hl = -1
	l.top = 0
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
	l.top = max(0, min(max(0, len(l.items)-l.page()), l.top+d))
}

// At is the index of the item on row y of the list as last drawn, -1 if
// none.
func (l *List) At(y int) int {
	i := l.top + y
	if y < 0 || y >= l.h || i >= len(l.items) {
		return -1
	}
	return i
}

func (l *List) scrollTo(i int) {
	if l.h == 0 {
		return // not drawn yet: View scrolls it into view
	}
	h := l.page()
	if i < l.top {
		l.top = i
	} else if i >= l.top+h {
		l.top = i - h + 1
	}
}

// View draws the list in w × h cells with render turning styled text into
// terminal output. When the items don't fit, the last column is Textual's
// scrollbar (OptionList's scrollbar-size 1) in the colours of Bar, and the
// items are a cell narrower.
func (l *List) View(w, h int, render func(styled.Text) string) string {
	l.h = h
	if l.hl >= 0 {
		l.scrollTo(l.hl)
	}
	l.top = max(0, min(l.top, max(0, len(l.items)-h)))
	var bar []styled.Text
	iw := w
	if len(l.items) > h && w > 1 && h > 0 {
		st := l.Bar
		if st == (styled.Style{}) {
			st = styled.Style{Fg: "border"}
		}
		bar = scrollbar.Vertical(h, len(l.items), l.top, st, "")
		iw = w - 1
	}
	pad := l.Width
	if pad <= 0 || pad > iw {
		pad = iw
	}
	lines := make([]string, h)
	for r := 0; r < h; r++ {
		i := l.top + r
		var s string
		if i >= len(l.items) {
			s = strings.Repeat(" ", iw)
		} else {
			t := l.items[i].Text
			if n := ansi.StringWidth(t.Plain); n < pad {
				t.Append(strings.Repeat(" ", pad-n), styled.Style{})
			}
			if i == l.hl {
				t.Spans = append(t.Spans, styled.Span{Start: 0, End: len([]rune(t.Plain)), Style: styled.Style{Reverse: true}})
			}
			s = Fit(render(t), iw)
		}
		if bar != nil {
			s += render(bar[r])
		}
		lines[r] = s
	}
	return strings.Join(lines, "\n")
}

// Fit pads or cuts s (which may hold SGR sequences) to exactly w cells.
func Fit(s string, w int) string {
	n := ansi.StringWidth(s)
	if n > w {
		return ansi.Truncate(s, w, "")
	}
	return s + strings.Repeat(" ", w-n)
}
