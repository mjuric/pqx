package dialogs

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/chrome"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// gotoDialog asks for a row (Python's GotoScreen).
type gotoDialog struct {
	env   *kit.Env
	total int64
	in    *field
	inY   int // the input's row, as drawn last
}

func newGoto(env *kit.Env, total int64) *gotoDialog {
	d := &gotoDialog{env: env, total: total, in: newField("", "row")}
	d.in.focus()
	return d
}

// hintLines is the hint wrapped to the dialog's width.
func (d *gotoDialog) hintLines(iw int) []string {
	var out []string
	for _, l := range wrapText(styled.New("1234 · 1.5M · 50% · -1 (last)   "+d.hint(), d.env.Look.Style("dim")), iw) {
		out = append(out, d.env.Look.Render(l))
	}
	return out
}

// Size: the title, the hint, the input (3 rows).
func (d *gotoDialog) Size(w, h int) (int, int) {
	return dialogSize(smallW, 1+len(d.hintLines(innerW(min(smallW, w*95/100))))+3, w, h)
}

// Keys: the filter's keys (Python shows them for any focused Input).
func (d *gotoDialog) Keys() []kit.KeyHint { return inputKeys }

func (d *gotoDialog) hint() string {
	if d.total < 0 {
		return "(row count still being computed)"
	}
	return "of " + chrome.Commas(d.total)
}

func (d *gotoDialog) View(w, h int) string {
	look := d.env.Look
	iw := innerW(w)
	content := []string{text(look, "Go to row", styled.Style{Bold: true})}
	content = append(content, d.hintLines(iw)...)
	d.inY = 1 + padY + len(content)
	content = append(content, d.in.lines(look, iw)...)
	return box(look, content, w, h)
}

func (d *gotoDialog) Cursor() *tea.Cursor { return d.in.cursor(1+padX, d.inY) }

func (d *gotoDialog) Update(msg tea.Msg) tea.Cmd {
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

// submit goes to the row typed; nothing typed just closes, and a bad spec
// closes with an error notice (Python's action_goto).
func (d *gotoDialog) submit() tea.Cmd {
	spec := strings.TrimSpace(d.in.Value())
	if spec == "" {
		return closeWith()
	}
	total := d.total
	if total < 0 {
		total = 1 << 62
	}
	row, err := parseRowSpec(spec, total)
	if err != nil {
		return closeWith(kit.NotifyMsg{Severity: kit.Error, Text: "Not a row number: " + fmtx.Sanitize(spec, false)})
	}
	return closeWith(kit.GotoMsg{Row: row})
}
