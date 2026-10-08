package plot

import (
	"strconv"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/cursorlist"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// Dropdown is Python pqx's FieldDropdown: a list under a settings field;
// with more than eight options a filter line narrows it as you type. ↑↓
// PgUp PgDn move, Enter or a click picks, Esc or a click outside closes.
type Dropdown struct {
	look       kit.Look
	title      string
	all        []string
	current    string
	atX, atY   int // where it was asked to open: under the field
	maxH       int
	width      int
	searchable bool

	filter []rune
	pos    int // the filter's cursor
	shown  []string
	list   cursorlist.List

	placed bool
	x, y   int
	boxW   int

	pick func(value string) tea.Msg // the message a pick sends
}

// maxDropdownH is the most options shown at once.
const maxDropdownH = 16

// NewDropdown makes a drop-down titled title over options, current
// marked, opening at screen cell (x, y); a pick sends pick(value).
func NewDropdown(look kit.Look, title string, options []string, current string, x, y int, pick func(string) tea.Msg) *Dropdown {
	w := max(len([]rune(title))+12, 16)
	for _, o := range options {
		w = max(w, len([]rune(o)))
	}
	d := &Dropdown{look: look, title: title, all: options, current: current, atX: x, atY: y,
		maxH: maxDropdownH, width: w + 4, searchable: len(options) > 8, pick: pick}
	d.boxW = d.width
	d.fill()
	return d
}

func (d *Dropdown) extra() int {
	if d.searchable {
		return 1
	}
	return 0
}

// Size implements kit.Dialog.
func (d *Dropdown) Size(w, h int) (int, int) {
	d.boxW = min(d.width, max(20, w-2))
	return d.boxW, max(1, min(len(d.shown), d.maxH)) + d.extra() + 2
}

// Position implements kit.Positioned: under the field, or above it when
// there is no room below; fixed when first shown.
func (d *Dropdown) Position(w, h int) (int, int) {
	if !d.placed {
		bw, _ := d.Size(w, h)
		bh := min(len(d.all), d.maxH) + d.extra() + 2
		d.x = max(0, min(d.atX, w-bw))
		d.y = d.atY
		if d.y+bh > h {
			d.y = max(0, d.atY-bh-1)
		}
		d.placed = true
	}
	return d.x, d.y
}

// fill lists the options containing the filter text (_fill).
func (d *Dropdown) fill() {
	f := strings.ToLower(string(d.filter))
	d.shown = d.shown[:0]
	for _, o := range d.all {
		if strings.Contains(strings.ToLower(o), f) {
			d.shown = append(d.shown, o)
		}
	}
	items := make([]cursorlist.Item, len(d.shown))
	cur := 0
	for i, o := range d.shown {
		st := styled.Style{}
		if o == d.current {
			st.Bold = true
			cur = i
		}
		items[i] = cursorlist.Item{ID: o, Text: styled.New(fmtx.Sanitize(o, false), st)}
	}
	d.list.SetItems(items)
	if len(items) > 0 {
		d.list.Highlight(cur)
	}
}

// Title is the border title: "title  N", or "title  n of N" while filtered.
func (d *Dropdown) Title() string {
	n := strconv.Itoa(len(d.all))
	if len(d.filter) > 0 {
		n = strconv.Itoa(len(d.shown)) + " of " + n
	}
	return d.title + "  " + n
}

// Shown are the options listed.
func (d *Dropdown) Shown() []string { return d.shown }

// Searchable reports whether it has a filter line.
func (d *Dropdown) Searchable() bool { return d.searchable }

// Highlighted is the highlighted option, "" for none.
func (d *Dropdown) Highlighted() string { return d.list.HighlightedID() }

func (d *Dropdown) close() tea.Cmd { return kit.Send(kit.CloseDialogMsg{}) }

func (d *Dropdown) choose() tea.Cmd {
	v := d.list.HighlightedID()
	if d.list.Highlighted() < 0 {
		return nil
	}
	return tea.Batch(d.close(), kit.Send(d.pick(v)))
}

func (d *Dropdown) move(n int) {
	if d.list.Len() == 0 {
		return
	}
	cur := max(0, d.list.Highlighted())
	d.list.Highlight(cur + n)
}

// Update implements kit.Pane.
func (d *Dropdown) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case tea.KeyPressMsg:
		return d.onKey(m)
	case tea.PasteMsg:
		if d.searchable {
			d.insert([]rune(fmtx.Sanitize(m.Content, false)))
		}
	case tea.MouseClickMsg:
		w, h := d.boxW, max(1, min(len(d.shown), d.maxH))+d.extra()+2
		if m.X < 0 || m.Y < 0 || m.X >= w || m.Y >= h {
			return d.close() // a click outside closes it, like a native drop-down
		}
		if i := d.list.At(m.Y - 1 - d.extra()); i >= 0 && m.Y < h-1 && m.X > 0 && m.X < w-1 {
			d.list.Highlight(i)
			return d.choose()
		}
	case tea.MouseWheelMsg:
		if m.Button == tea.MouseWheelUp {
			d.list.Scroll(-1)
		} else {
			d.list.Scroll(1)
		}
	}
	return nil
}

func (d *Dropdown) onKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		return d.close()
	case "up":
		d.move(-1)
		return nil
	case "down":
		d.move(1)
		return nil
	case "pgup":
		d.move(-10)
		return nil
	case "pgdown":
		d.move(10)
		return nil
	case "enter":
		return d.choose()
	}
	if !d.searchable {
		switch k.String() {
		case "home":
			d.list.Highlight(0)
		case "end":
			d.list.Highlight(d.list.Len() - 1)
		case "space":
			return d.choose()
		}
		return nil
	}
	switch k.String() {
	case "backspace":
		if d.pos > 0 {
			d.filter = append(d.filter[:d.pos-1], d.filter[d.pos:]...)
			d.pos--
			d.fill()
		}
	case "delete":
		if d.pos < len(d.filter) {
			d.filter = append(d.filter[:d.pos], d.filter[d.pos+1:]...)
			d.fill()
		}
	case "left":
		d.pos = max(0, d.pos-1)
	case "right":
		d.pos = min(len(d.filter), d.pos+1)
	case "home", "ctrl+a":
		d.pos = 0
	case "end", "ctrl+e":
		d.pos = len(d.filter)
	default:
		if k.Mod&(tea.ModCtrl|tea.ModAlt) == 0 && k.Text != "" {
			d.insert([]rune(k.Text))
		}
	}
	return nil
}

func (d *Dropdown) insert(r []rune) {
	r = []rune(strings.Map(func(c rune) rune {
		if unicode.IsControl(c) {
			return -1
		}
		return c
	}, string(r)))
	if len(r) == 0 {
		return
	}
	d.filter = append(d.filter[:d.pos], append(r, d.filter[d.pos:]...)...)
	d.pos += len(r)
	d.fill()
}

// View implements kit.Pane.
func (d *Dropdown) View(w, h int) string {
	look := d.look
	bs := look.Style("border-focus")
	edge := func(s string) string { return look.Render(styled.New(s, bs)) }
	iw := max(1, w-4) // border and padding
	var top styled.Text
	top.Append("╭─ ", bs)
	title := ansi.Truncate(d.Title(), max(1, w-6), "…")
	top.Append(title, look.Style("dim"))
	top.Append(" "+strings.Repeat("─", max(0, w-5-ansi.StringWidth(title)))+"╮", bs)
	lines := []string{look.Render(top)}
	row := func(s string) { lines = append(lines, edge("│")+" "+cursorlist.Fit(s, iw)+" "+edge("│")) }
	if d.searchable {
		if len(d.filter) == 0 {
			row(look.Render(styled.New("type to filter…", look.Style("dim"))))
		} else {
			row(fmtx.Sanitize(string(d.filter), false))
		}
	}
	d.list.Width = d.boxW - 4
	lh := max(0, h-2-d.extra())
	if lh > 0 {
		for _, l := range strings.Split(d.list.View(iw, lh, look.Render), "\n") {
			row(l)
		}
	}
	lines = append(lines, edge("╰"+strings.Repeat("─", max(0, w-2))+"╯"))
	return strings.Join(lines, "\n")
}

// Cursor implements kit.Cursored: in the filter line.
func (d *Dropdown) Cursor() *tea.Cursor {
	if !d.searchable {
		return nil
	}
	return tea.NewCursor(2+ansi.StringWidth(string(d.filter[:d.pos])), 1)
}

// Keys implements kit.Pane (the key bar while it is open).
func (d *Dropdown) Keys() []kit.KeyHint {
	return []kit.KeyHint{{Key: "type", Help: "to filter"}, {Key: "↑↓", Help: "move"},
		{Key: "enter/click", Help: "pick"}, {Key: "esc", Help: "close"}}
}
