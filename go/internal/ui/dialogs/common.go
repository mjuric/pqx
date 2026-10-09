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
	"time"

	tea "charm.land/bubbletea/v2"
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

// inputKeys are the key bar's hints while a dialog's text input has focus:
// Python's key bar shows the filter box's keys whenever an Input has focus,
// a dialog's too.
var inputKeys = []kit.KeyHint{{Key: "enter", Help: "apply"}, {Key: "esc", Help: "back"},
	{Key: "ctrl+x", Help: "clear"}, {Key: "↑↓", Help: "history"}, {Key: "→", Help: "complete"},
	{Key: "select … from t", Help: "full query"}}

// textualTimeout is Textual's default notification timeout, which Python's
// notices without a timeout of their own get (kit's default is 3 s, 8 s for
// errors).
const textualTimeout = 5 * time.Second

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

// button is a Textual Button as pqx styles it: at least 10 cells, the label
// centred; the label and a cell each side of it bold, the primary one in
// the accent colour, reverse video when focused.
func button(look kit.Look, label string, primary, focused bool) string {
	lw := ansi.StringWidth(label)
	n := max(10, lw+2)
	l := (n - lw) / 2
	s := styled.Style{Bold: true, Reverse: focused}
	if primary {
		s.Fg = look.Style("accent").Fg
	}
	var t styled.Text
	t.Append(strings.Repeat(" ", l-1), styled.Style{})
	t.Append(" "+label+" ", s)
	t.Append(strings.Repeat(" ", n-l-lw-1), styled.Style{})
	return look.Render(t)
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

// toggle is a check box or radio button as Textual draws one with the ANSI
// theme: "▐X▌ label", the half blocks black, the mark in the accent colour
// and bold on a black background when on, dim white when off. Focus doesn't
// show (it doesn't in Python either).
func toggle(look kit.Look, mark, label string, on bool) string {
	ms := styled.Style{Fg: "white", Bg: "black", Dim: true}
	if on {
		ms = styled.Style{Fg: look.Style("accent").Fg, Bg: "black", Bold: true}
	}
	var t styled.Text
	t.Append("▐", styled.Style{Fg: "black"})
	t.Append(mark, ms)
	t.Append("▌", styled.Style{Fg: "black"})
	t.Append(" "+label, styled.Style{})
	return look.Render(t)
}

// selection is a SelectionList entry's box: "▐X▌", the X green when
// chosen, all plain otherwise (Textual with the ANSI theme).
func selection(look kit.Look, on bool) string {
	var t styled.Text
	t.Append("▐", styled.Style{})
	if on {
		t.Append("X", look.Style("success"))
	} else {
		t.Append("X", styled.Style{})
	}
	t.Append("▌", styled.Style{})
	return look.Render(t)
}

// line renders parts, alternating text and style: line(look, "a", st, "b", st2).
func line(look kit.Look, parts ...any) string {
	var t styled.Text
	for i := 0; i+1 < len(parts); i += 2 {
		t.Append(parts[i].(string), parts[i+1].(styled.Style))
	}
	return look.Render(t)
}

// text renders s in style st.
func text(look kit.Look, s string, st styled.Style) string { return line(look, s, st) }

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
