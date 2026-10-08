package dialogs

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// formatDialog asks for a column's format (Python's FormatScreen): a number
// of digits or a Python format spec, checked against a value of the column;
// empty resets it to automatic.
type formatDialog struct {
	env    *kit.Env
	col    data.Column
	kind   fmtx.Kind
	sample data.Value
	in     *field
	err    string // why the last entry was refused
}

func newFormat(env *kit.Env, col data.Column, cur fmtx.Override, sample data.Value) *formatDialog {
	value := ""
	if cur.Set {
		value = cur.Spec
		if value == "" {
			value = strconv.Itoa(cur.Digits)
		}
	}
	d := &formatDialog{env: env, col: col, kind: fmtx.KindFor(col.Name, col.Arrow, col.Unit), sample: sample,
		in: newField(value, "automatic")}
	d.in.focus()
	return d
}

// content rows: title, hint, the input (3), the error line
const formatRows = 6

func (d *formatDialog) Size(w, h int) (int, int) { return dialogSize(smallW, formatRows, w, h) }

func (d *formatDialog) Keys() []kit.KeyHint { return nil }

// hint is what can be typed for the column's kind.
func (d *formatDialog) hint() string {
	h := ",d · x · >12"
	switch {
	case d.kind == fmtx.KindTime:
		h = "%Y-%m-%d %H:%M"
	case fmtx.DefaultDigits(d.kind) != 0:
		h = ".2f · .3e · ,d · .1% · 4 (digits)"
	}
	return h + " · empty = automatic"
}

func (d *formatDialog) View(w, h int) string {
	look := d.env.Look
	title := styled.New("Format of", styled.Style{Bold: true})
	title.Append(" ", styled.Style{})
	title.Append(fmtx.Sanitize(d.col.Name, false), styled.Style{Fg: "cyan"})
	content := []string{look.Render(title), text(look, d.hint(), look.Style("dim"))}
	content = append(content, d.in.lines(look, innerW(w))...)
	if d.err != "" {
		e := styled.New("✗", look.Style("error"))
		e.Append(" "+fmtx.Sanitize(d.err, false), styled.Style{})
		content = append(content, look.Render(e))
	}
	return box(look, content, w, h)
}

func (d *formatDialog) Cursor() *tea.Cursor { return d.in.cursor(1+padX, 1+padY+2) }

func (d *formatDialog) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case tea.KeyPressMsg:
		switch m.String() {
		case "esc":
			return closeWith()
		case "enter":
			return d.submit()
		}
		return d.in.update(m)
	case tea.PasteMsg:
		return d.in.update(m)
	}
	return nil
}

func (d *formatDialog) submit() tea.Cmd {
	o := fmtx.ParseOverride(strings.TrimSpace(d.in.Value()))
	if o.Set {
		if err := fmtx.OverrideError(o, d.kind, d.sample); err != "" {
			d.err = err
			return nil
		}
	}
	return closeWith(kit.FormatSetMsg{Column: d.col.Name, Override: o})
}
