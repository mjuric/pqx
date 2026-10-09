package app

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// borderError is a pane whose border shows an error in red (Python's
// #filterbox.error).
type borderError interface{ BorderError() bool }

// tabHint follows the tab strip: Ctrl+← and Ctrl+→ step through the tabs.
const tabHint = "^← ^→"

// tabStrip is "1 Data ─ 2 Schema ─ 3 Stats ─ 4 Plot ─ 5 Meta   ^← ^→", the
// active tab bold in the accent colour (Python pqx's _tab_strip).
func (a *App) tabStrip() styled.Text {
	look := a.env.Look
	dim := look.Style("dim")
	var t styled.Text
	for i, name := range kit.TabNames {
		if i > 0 {
			t.Append(" ─ ", dim)
		}
		t.Append(strconv.Itoa(i+1)+" ", dim)
		st := dim
		if kit.Tab(i) == a.tab {
			st = styled.Style{Bold: true, Fg: "accent"}
		}
		t.Append(name, st)
	}
	t.Append("   ", styled.Style{})
	t.Append(tabHint, dim)
	return t
}

// tabAt is the tab whose number or name is at (x, y) on the top border of
// the tab panel: the strip starts three cells in (corner, rule, space).
func (a *App) tabAt(x, y int) (kit.Tab, bool) {
	if y != bodyTop {
		return 0, false
	}
	x -= margin + 3
	pos := 0
	for i, name := range kit.TabNames {
		if i > 0 {
			pos += 3
		}
		end := pos + len(strconv.Itoa(i+1)+" ") + len([]rune(name))
		if x >= pos && x < end {
			return kit.Tab(i), true
		}
		pos = end
	}
	return 0, false
}

// frame draws inner (lines of exactly w-4 cells, or fewer lines) in a
// square border w × h with a title in the top border and a subtitle in
// the bottom one; the border is the accent colour when focused, and red
// while the pane reports an error (the filter box after a failed query).
func (a *App) frame(inner string, w, h int, title, subtitle styled.Text, focused bool, p kit.Pane) string {
	look := a.env.Look
	bs := look.Style("border")
	if focused {
		bs = look.Style("border-focus")
	}
	if e, ok := p.(borderError); ok && e.BorderError() {
		bs = look.Style("error")
	}
	edge := func(s string) string { return look.Render(styled.New(s, bs)) }
	iw := max(0, w-4)
	lines := strings.Split(inner, "\n")
	var b strings.Builder
	b.WriteString(borderLine(look, "┌", "┐", w, title, bs))
	for i := 0; i < h-2; i++ {
		b.WriteString("\n")
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		b.WriteString(edge("│") + " " + fitLine(l, iw) + " " + edge("│"))
	}
	b.WriteString("\n")
	b.WriteString(borderLine(look, "└", "┘", w, subtitle, bs))
	return b.String()
}

// borderLine is a top or bottom border of width w with t in it: a title
// left-aligned after "┌─", a subtitle (bottom border) right-aligned before
// "─┘", as Textual aligns them by default.
func borderLine(look kit.Look, l, r string, w int, t styled.Text, bs styled.Style) string {
	if w < 2 {
		return strings.Repeat(" ", max(0, w))
	}
	edge := func(s string) string { return look.Render(styled.New(s, bs)) }
	if t.Plain == "" || w <= 6 {
		return edge(l + strings.Repeat("─", w-2) + r)
	}
	tt := t
	if room := w - 6; lipgloss.Width(t.Plain) > room {
		tt = styled.Text{Plain: ansi.Truncate(t.Plain, room, "…")}
	}
	// the spaces around the title are the title's (Textual's border titles),
	// not the border's colour
	mid := " " + look.Render(tt) + " "
	fill := strings.Repeat("─", max(0, w-4-lipgloss.Width(tt.Plain)-1))
	if l == "└" {
		return edge(l+fill) + mid + edge("─"+r)
	}
	return edge(l+"─") + mid + edge(fill+r)
}

// fitLine pads or cuts s (which may hold SGR sequences) to exactly w cells.
func fitLine(s string, w int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	n := ansi.StringWidth(s)
	if n > w {
		return ansi.Truncate(s, w, "")
	}
	return s + strings.Repeat(" ", w-n)
}

// placeholder stands in for a part not built yet.
type placeholderPane string

func placeholder(name string) kit.Pane { return placeholderPane(name) }

func (p placeholderPane) Update(tea.Msg) tea.Cmd { return nil }
func (p placeholderPane) Keys() []kit.KeyHint    { return nil }
func (p placeholderPane) View(w, h int) string {
	lines := make([]string, max(1, h))
	lines[0] = fitLine("("+string(p)+": not built yet)", w)
	for i := 1; i < len(lines); i++ {
		lines[i] = strings.Repeat(" ", w)
	}
	return strings.Join(lines, "\n")
}

// basicChrome is the title bar, status line and key bar until WP10's.
type basicChrome struct {
	env    *kit.Env
	status kit.StatusMsg
}

func (c *basicChrome) Update(msg tea.Msg) tea.Cmd {
	if m, ok := msg.(kit.StatusMsg); ok {
		c.status = m
	}
	return nil
}

func (c *basicChrome) TitleBar(w int) string {
	ds := c.env.DS
	s := "pqx " + c.env.Opts.Version + " · " + fmtx.Sanitize(baseName(ds.Path()), false) +
		" · " + strconv.FormatInt(ds.NumRows(), 10) + " rows · " + strconv.Itoa(len(ds.Columns())) + " columns"
	return ansi.Truncate(s, w, "…")
}

func (c *basicChrome) StatusLine(w int) string { return ansi.Truncate(c.status.Text, w, "…") }

func (c *basicChrome) KeyBar(w int, hints []kit.KeyHint) string {
	var parts []string
	for _, h := range hints {
		parts = append(parts, h.Key+" "+h.Help)
	}
	parts = append(parts, "1-5 tabs", "? help", "q quit")
	return ansi.Truncate(strings.Join(parts, "   "), w, "…")
}

func (c *basicChrome) Toasts(w, h int) []kit.Overlay { return nil }
