// Package dialogs is pqx's modal dialogs (kit.Dialogs, WP10): go to row,
// a column's format, the column picker, export and the help, ports of
// Python pqx's screens.py. Each is drawn as Textual drew it (app.tcss): a
// box with a border in the accent colour, padding 1 2, inputs in their own
// bordered box, buttons right-aligned at the bottom. Text from the file
// (names, the filter, the path) goes through fmtx.Sanitize and is never
// parsed as markup.
package dialogs

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// Factory implements kit.Dialogs.
type Factory struct{ env *kit.Env }

// New makes the dialogs for env.
func New(env *kit.Env) *Factory { return &Factory{env: env} }

var _ kit.Dialogs = (*Factory)(nil)

// Goto implements kit.Dialogs.
func (f *Factory) Goto(total int64) kit.Dialog { return newGoto(f.env, total) }

// Format implements kit.Dialogs.
func (f *Factory) Format(col data.Column, cur fmtx.Override, sample data.Value) kit.Dialog {
	return newFormat(f.env, col, cur, sample)
}

// Columns implements kit.Dialogs.
func (f *Factory) Columns(cols []data.Column, hidden map[string]bool, current string) kit.Dialog {
	return newColumns(f.env, cols, hidden, current)
}

// Export implements kit.Dialogs.
func (f *Factory) Export() kit.Dialog { return newExport(f.env) }

// Help implements kit.Dialogs.
func (f *Factory) Help() kit.Dialog { return newHelp(f.env) }

// The dialogs' sizes (app.tcss: .dialog 80 wide, at most 95% of the
// screen; .small 60; height auto, at most 90%).
const (
	dialogW = 80
	smallW  = 60
	padX    = 2 // padding 1 2
	padY    = 1
)

// dialogSize is a dialog's size for content of height contentH (rows
// inside the padding) on a w × h screen.
func dialogSize(want, contentH, w, h int) (int, int) {
	return min(want, w*95/100), min(contentH+2+2*padY, h*90/100)
}

// innerW is the content width of a dialog w wide.
func innerW(w int) int { return max(1, w-2-2*padX) }

// close closes the dialog, then sends msgs.
func closeWith(msgs ...tea.Msg) tea.Cmd {
	cmds := []tea.Cmd{kit.Send(kit.CloseDialogMsg{})}
	for _, m := range msgs {
		cmds = append(cmds, kit.Send(m))
	}
	return tea.Sequence(cmds...)
}

// box draws content (rendered lines, at most iw cells wide) in a dialog
// box w × h: the border in the accent colour, padding 1 2, the rest blank so
// nothing behind it shows through.
func box(look kit.Look, content []string, w, h int) string {
	bs := look.Style("border-focus")
	edge := func(s string) string { return look.Render(styled.New(s, bs)) }
	iw := innerW(w)
	var b strings.Builder
	b.WriteString(edge("┌" + strings.Repeat("─", max(0, w-2)) + "┐"))
	blank := strings.Repeat(" ", max(0, w-2))
	for i := 0; i < h-2; i++ {
		b.WriteString("\n" + edge("│"))
		j := i - padY
		if j >= 0 && j < len(content) && i < h-2-padY {
			b.WriteString(strings.Repeat(" ", padX) + fit(content[j], iw) + strings.Repeat(" ", max(0, w-2-padX-iw)))
		} else {
			b.WriteString(blank)
		}
		b.WriteString(edge("│"))
	}
	b.WriteString("\n" + edge("└"+strings.Repeat("─", max(0, w-2))+"┘"))
	return b.String()
}

// fit pads or cuts a rendered line to exactly w cells.
func fit(s string, w int) string {
	n := ansi.StringWidth(s)
	if n > w {
		return ansi.Truncate(s, w, "…")
	}
	return s + strings.Repeat(" ", w-n)
}

// field is a one-line text input in its own bordered box (3 rows), as
// Textual's Input with pqx's border: dim when unfocused, accent focused.
type field struct {
	ti textinput.Model
}

func newField(value, placeholder string) *field {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Placeholder = placeholder
	ti.SetVirtualCursor(false) // the terminal's own cursor
	s := textinput.Styles{}
	s.Focused.Placeholder = lipgloss.NewStyle().Faint(true)
	s.Blurred.Placeholder = lipgloss.NewStyle().Faint(true)
	s.Cursor.Shape = tea.CursorBar
	ti.SetStyles(s)
	km := textinput.DefaultKeyMap()
	km.Paste = key.NewBinding(key.WithDisabled()) // bracketed paste still works
	km.NextSuggestion = key.NewBinding(key.WithDisabled())
	km.PrevSuggestion = key.NewBinding(key.WithDisabled())
	ti.KeyMap = km
	ti.SetValue(value)
	ti.CursorEnd()
	return &field{ti: ti}
}

func (f *field) Value() string     { return f.ti.Value() }
func (f *field) SetValue(v string) { f.ti.SetValue(v); f.ti.CursorEnd() }
func (f *field) focus()            { f.ti.Focus() }
func (f *field) blur()             { f.ti.Blur() }
func (f *field) focused() bool     { return f.ti.Focused() }
func (f *field) update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	f.ti, cmd = f.ti.Update(msg)
	return cmd
}

// lines draws the field w cells wide: border, " text ", border.
func (f *field) lines(look kit.Look, w int) []string {
	bs := look.Style("border")
	if f.focused() {
		bs = look.Style("border-focus")
	}
	edge := func(s string) string { return look.Render(styled.New(s, bs)) }
	f.ti.SetWidth(max(1, w-5))
	return []string{
		edge("┌" + strings.Repeat("─", max(0, w-2)) + "┐"),
		edge("│") + " " + fit(f.ti.View(), max(0, w-4)) + " " + edge("│"),
		edge("└" + strings.Repeat("─", max(0, w-2)) + "┘"),
	}
}

// cursor is the text cursor for the field drawn with its top-left corner at
// (x, y) of the dialog, or nil when it hasn't focus.
func (f *field) cursor(x, y int) *tea.Cursor {
	c := f.ti.Cursor()
	if c == nil {
		return nil
	}
	c.Position.X += x + 2
	c.Position.Y += y + 1
	return c
}

// button is a Textual Button as pqx styles it: bold, at least 10 cells,
// the label centred, the primary one in the accent colour, reverse video
// when focused.
func button(look kit.Look, label string, primary, focused bool) string {
	n := max(10, ansi.StringWidth(label)+2)
	l := (n - ansi.StringWidth(label)) / 2
	s := styled.Style{Bold: true, Reverse: focused}
	if primary {
		s.Fg = look.Style("accent").Fg
	}
	return look.Render(styled.New(strings.Repeat(" ", l)+label+strings.Repeat(" ", n-l-ansi.StringWidth(label)), s))
}

// buttonsRow is buttons right-aligned in iw cells, two cells apart, and
// the cell ranges they cover.
func buttonsRow(look kit.Look, iw int, labels []string, primary int, focus int) (string, [][2]int) {
	var parts []string
	width := 0
	for i, l := range labels {
		b := button(look, l, i == primary, i == focus)
		parts = append(parts, b)
		width += 2 + ansi.StringWidth(b)
	}
	x := max(0, iw-width)
	var spans [][2]int
	var b strings.Builder
	b.WriteString(strings.Repeat(" ", x))
	for _, p := range parts {
		b.WriteString("  ")
		x += 2
		w := ansi.StringWidth(p)
		spans = append(spans, [2]int{x, x + w})
		b.WriteString(p)
		x += w
	}
	return b.String(), spans
}

// toggle is a check box or radio button: "▐X▌ label" (Textual's toggle
// button), the mark in the accent colour when on, the label reversed when
// it has focus.
func toggle(look kit.Look, mark, label string, on, focused bool) string {
	ms := look.Style("dim")
	if on {
		ms = styled.Style{Bold: true, Fg: look.Style("accent").Fg}
	}
	t := styled.New("▐"+mark+"▌", ms)
	t.Append(" ", styled.Style{})
	t.Append(label, styled.Style{Reverse: focused})
	return look.Render(t)
}

// text renders s in style st.
func text(look kit.Look, s string, st styled.Style) string { return look.Render(styled.New(s, st)) }

// clicked reports a left click's position, relative to the dialog.
func clicked(msg tea.Msg) (int, int, bool) {
	m, ok := msg.(tea.MouseClickMsg)
	if !ok || m.Button != tea.MouseLeft {
		return 0, 0, false
	}
	return m.X, m.Y, true
}

// wheel is -1 or +1 for a wheel message, 0 otherwise.
func wheel(msg tea.Msg) int {
	m, ok := msg.(tea.MouseWheelMsg)
	if !ok {
		return 0
	}
	switch m.Button {
	case tea.MouseWheelUp:
		return -1
	case tea.MouseWheelDown:
		return 1
	}
	return 0
}
