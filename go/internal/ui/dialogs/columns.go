package dialogs

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// columnsDialog picks the visible columns (Python's ColumnPicker): a filter
// input over a selection list, Apply and Cancel.
type columnsDialog struct {
	env    *kit.Env
	cols   []data.Column
	chosen map[string]bool
	flt    *field
	shown  []int // indexes into cols matching the filter
	cur    int   // highlighted entry of shown
	top    int   // first entry of shown drawn
	focus  int   // focusFilter … focusCancel

	listY, listH int // where the list was drawn last (dialog rows)
	buttonY      int
	buttons      [][2]int // the buttons' columns, relative to the dialog
}

const (
	focusFilter = iota
	focusList
	focusApply
	focusCancel
)

func newColumns(env *kit.Env, cols []data.Column, hidden map[string]bool, current string) *columnsDialog {
	d := &columnsDialog{env: env, cols: cols, chosen: map[string]bool{}, flt: newField("", "type to filter columns…")}
	for _, c := range cols {
		if !hidden[c.Name] {
			d.chosen[c.Name] = true
		}
	}
	d.refilter() // (the list starts at its top, as Python's does, not on current)
	d.flt.focus()
	return d
}

func (d *columnsDialog) Size(w, h int) (int, int) {
	return min(dialogW, w*95/100), max(12, h*80/100) // #picker-box: 80% high
}

// Keys: the filter's keys (Python shows them for any focused Input).
func (d *columnsDialog) Keys() []kit.KeyHint {
	if d.focus == focusFilter {
		return inputKeys
	}
	return nil
}

func (d *columnsDialog) refilter() {
	f := strings.ToLower(d.flt.Value())
	d.shown = d.shown[:0]
	for i, c := range d.cols {
		if strings.Contains(strings.ToLower(c.Name), f) {
			d.shown = append(d.shown, i)
		}
	}
	d.cur = min(d.cur, max(0, len(d.shown)-1))
}

func (d *columnsDialog) setFocus(f int) {
	d.focus = (f + 4) % 4
	if d.focus == focusFilter {
		d.flt.focus()
	} else {
		d.flt.blur()
	}
}

func (d *columnsDialog) View(w, h int) string {
	look := d.env.Look
	iw := innerW(w)
	content := []string{line(look, "Visible columns", styled.Style{Bold: true},
		"  ", styled.Style{}, "space toggles · Ctrl+A all · Ctrl+N none", look.Style("dim"))}
	content = append(content, d.flt.lines(look, iw)...)
	content = append(content, "") // the list's margin
	// the list fills what the header, filter, margins and buttons leave
	d.listH = max(1, h-2-2*padY-1-3-2-2)
	d.listY = 1 + padY + len(content)
	if d.cur < d.top {
		d.top = d.cur
	}
	if d.cur >= d.top+d.listH {
		d.top = d.cur - d.listH + 1
	}
	d.top = max(0, min(d.top, len(d.shown)-d.listH))
	for i := 0; i < d.listH; i++ {
		k := d.top + i
		if k >= len(d.shown) {
			content = append(content, "")
			continue
		}
		c := d.cols[d.shown[k]]
		label := fmtx.Sanitize(c.Name, false)
		typ := fmtx.Sanitize(fmtx.ShortType(c.Arrow), false)
		// (the highlighted entry doesn't show with the ANSI theme, in Python
		// either)
		content = append(content, selection(look, d.chosen[c.Name])+line(look, " "+label, styled.Style{},
			"  ", styled.Style{}, typ, look.Style("dim")))
	}
	content = append(content, "", "") // the list's margin, the buttons' margin
	row, spans := buttonsRow(look, iw, []string{"Apply", "Cancel"}, 0, d.focus-focusApply)
	content = append(content, row)
	d.buttonY = 1 + padY + len(content) - 1
	d.buttons = d.buttons[:0]
	for _, s := range spans {
		d.buttons = append(d.buttons, [2]int{s[0] + 1 + padX, s[1] + 1 + padX})
	}
	return box(look, content, w, h)
}

func (d *columnsDialog) Cursor() *tea.Cursor { return d.flt.cursor(1+padX, 1+padY+1) }

func (d *columnsDialog) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case tea.KeyPressMsg:
		return d.key(m)
	case tea.PasteMsg:
		if d.focus == focusFilter {
			cmd := d.flt.update(m)
			d.refilter()
			return cmd
		}
	case tea.MouseClickMsg:
		return d.click(m)
	case tea.MouseWheelMsg:
		d.move(3 * wheel(m))
	}
	return nil
}

func (d *columnsDialog) key(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		return closeWith()
	case "ctrl+a", "ctrl+n": // the shown entries, from anywhere
		for _, j := range d.shown {
			d.chosen[d.cols[j].Name] = k.String() == "ctrl+a"
		}
		return nil
	case "tab":
		d.setFocus(d.focus + 1)
		return nil
	case "shift+tab":
		d.setFocus(d.focus - 1)
		return nil
	}
	switch d.focus {
	case focusFilter:
		if k.String() == "enter" {
			d.setFocus(focusList)
			return nil
		}
		cmd := d.flt.update(k)
		d.refilter()
		return cmd
	case focusList:
		switch k.String() {
		case "up":
			d.move(-1)
		case "down":
			d.move(1)
		case "pgup":
			d.move(-max(1, d.listH-1))
		case "pgdown":
			d.move(max(1, d.listH-1))
		case "home":
			d.move(-len(d.shown))
		case "end":
			d.move(len(d.shown))
		case "space", "enter":
			d.toggleCur()
		}
	case focusApply:
		if s := k.String(); s == "enter" || s == "space" {
			return d.apply()
		}
	case focusCancel:
		if s := k.String(); s == "enter" || s == "space" {
			return closeWith()
		}
	}
	return nil
}

func (d *columnsDialog) move(n int) {
	if len(d.shown) == 0 {
		return
	}
	d.cur = max(0, min(len(d.shown)-1, d.cur+n))
}

func (d *columnsDialog) toggleCur() {
	if d.cur < len(d.shown) {
		name := d.cols[d.shown[d.cur]].Name
		d.chosen[name] = !d.chosen[name]
	}
}

func (d *columnsDialog) click(m tea.MouseClickMsg) tea.Cmd {
	x, y, ok := clicked(m)
	if !ok {
		return nil
	}
	switch {
	case y >= d.listY && y < d.listY+d.listH:
		if k := d.top + y - d.listY; k < len(d.shown) {
			d.setFocus(focusList)
			d.cur = k
			d.toggleCur()
		}
	case y >= d.listY-4 && y < d.listY-1:
		d.setFocus(focusFilter)
	case y == d.buttonY:
		for i, b := range d.buttons {
			if x >= b[0] && x < b[1] {
				if i == 0 {
					return d.apply()
				}
				return closeWith()
			}
		}
	}
	return nil
}

// apply closes with the chosen columns in the file's order; none chosen is
// refused.
func (d *columnsDialog) apply() tea.Cmd {
	var vis []string
	for _, c := range d.cols {
		if d.chosen[c.Name] {
			vis = append(vis, c.Name)
		}
	}
	if len(vis) == 0 {
		return kit.Send(kit.NotifyMsg{Severity: kit.Warning, Text: "Select at least one column", Timeout: textualTimeout})
	}
	return closeWith(kit.ColumnsPickedMsg{Visible: vis})
}
