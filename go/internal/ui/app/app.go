// Package app is pqx's root Bubble Tea model: it lays out the screen (title
// bar, filter bar, the tabs' panels, the details pane, status line, key
// bar), routes keys, paste and mouse to the part with focus, switches tabs,
// stacks dialogs over the screen without moving it, runs the Esc rules, and
// broadcasts the parts' messages (kit) to every part.
//
// The parts come from Parts; a nil part shows a placeholder.
package app

import (
	"github.com/charmbracelet/x/ansi"
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/analysis"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// Parts are the UI's parts. Names (for FocusMsg and focus order): "filter",
// "grid", "detail", "schema", "stats", "plot", "meta".
type Parts struct {
	Filter, Grid, Detail      kit.Pane
	Schema, Stats, Plot, Meta kit.Pane
	Chrome                    kit.Chrome
}

// Layout constants (Python pqx's layout).
const (
	margin     = 1 // the screen's side margin (Python's Screen padding)
	titleRows  = 2 // the title bar and a blank row (the filter box's top margin)
	filterRows = 3 // a bordered one-line input
	gapRows    = 1 // a blank row between the filter box and the tabs
	// bodyTop is the first row of the tabs' panels.
	bodyTop = titleRows + filterRows + gapRows
	keyRows = 1
	detailW = 53 // the details pane's width (PR #24), one column from the grid's panel
)

// App is the root model.
type App struct {
	env   *kit.Env
	p     Parts
	tab   kit.Tab
	focus string // the focused part's name

	dialogs []kit.Dialog
	regions []region // where each part was drawn last, for mouse routing
	w, h    int

	quitting bool

	// base is the screen behind the open dialogs, drawn once and kept
	// while only the dialog gets messages (baseOK).
	base   string
	baseOK bool
}

type region struct {
	name       string
	pane       kit.Pane
	x, y, w, h int
}

// New makes the root model.
func New(env *kit.Env, p Parts) *App {
	if p.Chrome == nil {
		p.Chrome = &basicChrome{env: env}
	}
	a := &App{env: env, p: p, focus: "grid"}
	return a
}

// Init implements tea.Model.
func (a *App) Init() tea.Cmd { return nil }

func (a *App) parts() map[string]kit.Pane {
	return map[string]kit.Pane{
		"filter": a.p.Filter, "grid": a.p.Grid, "detail": a.p.Detail,
		"schema": a.p.Schema, "stats": a.p.Stats, "plot": a.p.Plot, "meta": a.p.Meta,
	}
}

// filterClearer is the filter bar's x and Ctrl+X.
type filterClearer interface{ ClearFilter() tea.Cmd }

// focused is the pane with focus, or nil.
func (a *App) focused() kit.Pane {
	return a.parts()[a.focus]
}

// typing reports whether a text input has focus (single-letter keys are
// then text, not commands).
func (a *App) typing() bool {
	if len(a.dialogs) > 0 {
		return true
	}
	if in, ok := a.focused().(kit.Inputs); ok {
		return in.TypingFocused()
	}
	return false
}

// broadcast sends msg to every part (and the chrome).
func (a *App) broadcast(msg tea.Msg) tea.Cmd {
	var cmds []tea.Cmd
	for _, name := range []string{"filter", "grid", "detail", "schema", "stats", "plot", "meta"} {
		if p := a.parts()[name]; p != nil {
			cmds = append(cmds, p.Update(msg))
		}
	}
	for _, d := range a.dialogs {
		cmds = append(cmds, d.Update(msg))
	}
	cmds = append(cmds, a.p.Chrome.Update(msg))
	return tea.Batch(cmds...)
}

// Update implements tea.Model.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// keys, paste and mouse go to an open dialog and leave the screen behind
	// it as it was; anything else may change it
	switch msg.(type) {
	case tea.KeyPressMsg, tea.PasteMsg, tea.MouseMsg:
		if len(a.dialogs) == 0 {
			a.baseOK = false
		}
	default:
		a.baseOK = false
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.w, a.h = msg.Width, msg.Height
		return a, a.broadcast(msg)
	case tea.KeyPressMsg:
		// the chrome sees input too: a key may start a task, and its
		// spinner starts at once
		return a, tea.Batch(a.onKey(msg), a.p.Chrome.Update(msg))
	case tea.PasteMsg:
		if len(a.dialogs) > 0 {
			return a, a.dialogs[len(a.dialogs)-1].Update(msg)
		}
		if p := a.focused(); p != nil {
			return a, p.Update(msg)
		}
		return a, nil
	case tea.MouseMsg:
		return a, tea.Batch(a.onMouse(msg), a.p.Chrome.Update(msg))
	case kit.DoneMsg:
		if !a.env.Tasks.Done(msg) {
			return a, nil
		}
		return a, a.broadcast(msg)
	case kit.OpenDialogMsg:
		a.dialogs = append(a.dialogs, msg.Dialog)
		return a, nil
	case kit.CloseDialogMsg:
		if n := len(a.dialogs); n > 0 {
			a.dialogs = a.dialogs[:n-1]
		}
		return a, nil
	case kit.SwitchTabMsg:
		return a, a.switchTab(msg.Tab)
	case kit.FocusMsg:
		return a, a.setFocus(msg.Pane)
	case kit.ToggleDetailMsg:
		return a, a.toggleDetail()
	case kit.ColumnStatsMsg:
		a.env.State.Current = msg.Column
		return a, tea.Batch(a.broadcast(kit.ColumnChangedMsg{From: "root"}), a.switchTab(kit.TabStats))
	case kit.CopyMsg:
		return a, tea.SetClipboard(fmtx.Sanitize(msg.Text, true))
	}
	return a, a.broadcast(msg)
}

// onKey: the top dialog gets every key; then the tab keys, then the global
// keys (unless typing), then the focused part.
func (a *App) onKey(k tea.KeyPressMsg) tea.Cmd {
	if n := len(a.dialogs); n > 0 {
		return a.dialogs[n-1].Update(k)
	}
	s := k.String()
	if !a.typing() {
		// (while typing, Ctrl+← and Ctrl+→ jump words, as Python pqx's
		// check_action has it)
		switch s {
		case "ctrl+left":
			return a.switchTab((a.tab + 4) % 5)
		case "ctrl+right":
			return a.switchTab((a.tab + 1) % 5)
		}
		if len(s) == 1 && s >= "1" && s <= "5" {
			return a.switchTab(kit.Tab(s[0] - '1'))
		}
	}
	// x (unless typing) and Ctrl+X clear the filter from anywhere; the grid,
	// the filter and the details pane do it themselves
	if s == "ctrl+x" || (s == "x" && !a.typing()) {
		if a.focus != "grid" && a.focus != "filter" && a.focus != "detail" {
			if c, ok := a.p.Filter.(filterClearer); ok {
				return c.ClearFilter()
			}
		}
	}
	if !a.typing() {
		switch s {
		case "q":
			return a.quit()
		case "m":
			return analysis.ToggleSampling(a.env)
		case "/":
			return a.setFocus("filter")
		case "?":
			if a.env.Dialogs != nil {
				return kit.Send(kit.OpenDialogMsg{Dialog: a.env.Dialogs.Help()})
			}
		case "e":
			if a.env.Dialogs != nil {
				return kit.Send(kit.OpenDialogMsg{Dialog: a.env.Dialogs.Export()})
			}
		case "esc":
			return a.escape(k)
		case "tab", "shift+tab":
			if a.focus == "detail" {
				// the details pane hands focus back to the grid (Python's
				// DetailList binds Tab to detail_to_grid)
				return a.setFocus("grid")
			}
			d := 1
			if s == "shift+tab" {
				d = -1
			}
			if f, ok := a.focused().(kit.InnerFocus); ok && f.CycleFocus(d) {
				return nil
			}
			return a.cycleFocus(d)
		}
	} else if s == "esc" {
		return a.escape(k)
	}
	if p := a.focused(); p != nil {
		return p.Update(k)
	}
	return nil
}

// escape runs Python pqx's Esc rules (action_escape): the details pane
// closes first unless the user waits on something; anything running is
// cancelled; an input gives focus back.
func (a *App) escape(k tea.KeyPressMsg) tea.Cmd {
	st, tasks := a.env.State, a.env.Tasks
	onGrid := a.tab == kit.TabData && (a.focus == "grid" || a.focus == "detail")
	if onGrid && st.DetailOpen && !tasks.BusyUser() {
		return a.toggleDetail()
	}
	if tasks.Busy() {
		tags := tasks.CancelAll()
		return tea.Batch(a.broadcast(kit.CancelledMsg{Tags: tags}),
			kit.Notify(kit.Info, "Cancelled running queries"))
	}
	if a.typing() {
		if a.tab == kit.TabData {
			return a.setFocus("grid")
		}
		return a.setFocus(tabPane(a.tab))
	}
	if onGrid && st.DetailOpen {
		return a.toggleDetail()
	}
	if p := a.focused(); p != nil {
		return p.Update(k)
	}
	return nil
}

func (a *App) quit() tea.Cmd {
	// Bubble Tea leaves the alternate screen without erasing it; the last
	// frame is blank so nothing stays behind (GNU screen with altscreen off).
	a.quitting = true
	a.env.Tasks.Stop()
	return tea.Quit
}

func tabPane(t kit.Tab) string {
	return [...]string{"grid", "schema", "stats", "plot", "meta"}[t]
}

func (a *App) switchTab(t kit.Tab) tea.Cmd {
	changed := t != a.tab
	a.tab = t
	a.env.State.Tab = t
	cmd := a.setFocus(tabPane(t))
	if !changed {
		return cmd
	}
	return tea.Batch(cmd, a.broadcast(kit.TabChangedMsg{Tab: t}))
}

// focusOrder is the Tab key's order on each tab.
func (a *App) focusOrder() []string {
	if a.tab == kit.TabData {
		if a.env.State.DetailOpen {
			return []string{"grid", "detail", "filter"}
		}
		return []string{"grid", "filter"}
	}
	return []string{tabPane(a.tab), "filter"}
}

func (a *App) cycleFocus(d int) tea.Cmd {
	order := a.focusOrder()
	i := 0
	for j, n := range order {
		if n == a.focus {
			i = j
		}
	}
	return a.setFocus(order[(i+d+len(order))%len(order)])
}

func (a *App) setFocus(name string) tea.Cmd {
	if name == a.focus {
		return nil
	}
	if f, ok := a.parts()[a.focus].(kit.Focusable); ok {
		f.Blur()
	}
	a.focus = name
	if f, ok := a.parts()[name].(kit.Focusable); ok {
		return f.Focus()
	}
	return nil
}

func (a *App) toggleDetail() tea.Cmd {
	st := a.env.State
	st.DetailOpen = !st.DetailOpen
	if !st.DetailOpen && a.focus == "detail" {
		return tea.Batch(a.setFocus("grid"), a.broadcast(kit.ToggleDetailMsg{}))
	}
	return a.broadcast(kit.ToggleDetailMsg{})
}

// onMouse gives a mouse message to the top dialog, or to the part under the
// pointer (with coordinates relative to it), focusing it on a click.
func (a *App) onMouse(msg tea.MouseMsg) tea.Cmd {
	m := msg.Mouse()
	if n := len(a.dialogs); n > 0 {
		d := a.dialogs[n-1]
		x, y := a.dialogPos(d)
		return d.Update(shift(msg, x, y))
	}
	if _, ok := msg.(tea.MouseClickMsg); ok && m.Y >= bodyTop {
		if t, ok := a.tabAt(m.X, m.Y); ok {
			return a.switchTab(t)
		}
	}
	for _, r := range a.regions {
		if m.X >= r.x && m.X < r.x+r.w && m.Y >= r.y && m.Y < r.y+r.h {
			var cmd tea.Cmd
			if _, ok := msg.(tea.MouseClickMsg); ok {
				cmd = a.setFocus(r.name)
			}
			return tea.Batch(cmd, r.pane.Update(shift(msg, r.x, r.y)))
		}
	}
	return nil
}

// shift moves a mouse message's coordinates by (-x, -y).
func shift(msg tea.MouseMsg, x, y int) tea.Msg {
	m := msg.Mouse()
	m.X -= x
	m.Y -= y
	switch msg.(type) {
	case tea.MouseClickMsg:
		return tea.MouseClickMsg(m)
	case tea.MouseReleaseMsg:
		return tea.MouseReleaseMsg(m)
	case tea.MouseWheelMsg:
		return tea.MouseWheelMsg(m)
	case tea.MouseMotionMsg:
		return tea.MouseMotionMsg(m)
	}
	return msg
}

// View implements tea.Model.
func (a *App) View() tea.View {
	v := tea.NewView("")
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "pqx " + fmtx.Sanitize(baseName(a.env.DS.Path()), false)
	if a.quitting || a.w <= 0 || a.h <= 0 {
		return v
	}
	var base string
	var cur *tea.Cursor
	if len(a.dialogs) > 0 && a.baseOK {
		// nothing behind the dialog changed but the key bar, which follows
		// the dialog's focus
		base = a.base
		if i := strings.LastIndexByte(base, '\n'); i >= 0 {
			base = base[:i+1] + a.keyBar()
		}
	} else {
		base, cur = a.render()
		a.base, a.baseOK = base, len(a.dialogs) > 0
	}
	if len(a.dialogs) == 0 {
		if toasts := a.p.Chrome.Toasts(a.w, a.h); len(toasts) > 0 {
			base = compose(a.w, a.h, base, toasts)
		}
		v.SetContent(base)
		v.Cursor = cur
		return v
	}
	var over []kit.Overlay
	for _, d := range a.dialogs {
		x, y := a.dialogPos(d)
		dw, dh := d.Size(a.w, a.h)
		over = append(over, kit.Overlay{X: x, Y: y, Content: d.View(dw, dh)})
	}
	over = append(over, a.p.Chrome.Toasts(a.w, a.h)...) // notices show over dialogs too
	v.SetContent(compose(a.w, a.h, base, over))
	if c, ok := a.dialogs[len(a.dialogs)-1].(kit.Cursored); ok {
		if cc := c.Cursor(); cc != nil {
			x, y := a.dialogPos(a.dialogs[len(a.dialogs)-1])
			cc.Position.X += x
			cc.Position.Y += y
			v.Cursor = cc
		}
	}
	return v
}

func (a *App) dialogPos(d kit.Dialog) (int, int) {
	if p, ok := d.(kit.Positioned); ok {
		return p.Position(a.w, a.h)
	}
	dw, dh := d.Size(a.w, a.h)
	return max(0, (a.w-dw)/2), max(0, (a.h-dh)/2)
}

// compose draws overlays over base without moving it: each overlay line
// replaces the cells it covers in its row, and the rest of the row keeps
// its text and styles. Only the rows an overlay covers are touched, so a
// keystroke in a dialog costs about the dialog's height.
func compose(w, h int, base string, over []kit.Overlay) string {
	lines := strings.Split(base, "\n")
	for _, o := range over {
		for i, ol := range strings.Split(o.Content, "\n") {
			y := o.Y + i
			if y < 0 || y >= len(lines) || y >= h {
				continue
			}
			x := max(0, min(o.X, w))
			ow := min(ansi.StringWidth(ol), w-x)
			if ow <= 0 {
				continue
			}
			l := fitLine(lines[y], w)
			left := ansi.Cut(l, 0, x+ow)
			lines[y] = ansi.Cut(l, 0, x) + sgrReset + ansi.Truncate(ol, ow, "") + sgrReset +
				activeStyle(left) + ansi.Cut(l, x+ow, w)
		}
	}
	return strings.Join(lines, "\n")
}

const sgrReset = "\x1b[m"

var sgr = regexp.MustCompile(`\x1b\[[0-9;:]*m`)

// activeStyle is the SGR sequences of s from its last reset on: replayed,
// they give the style in force at its end (where the rest of a row resumes
// after an overlay).
func activeStyle(s string) string {
	seqs := sgr.FindAllString(s, -1)
	start := 0
	for i, q := range seqs {
		if q == "\x1b[m" || q == "\x1b[0m" {
			start = i + 1
		}
	}
	return strings.Join(seqs[start:], "")
}

// render draws the screen and returns the text cursor, if a part shows one.
func (a *App) render() (string, *tea.Cursor) {
	a.regions = a.regions[:0]
	ch := a.p.Chrome
	var b strings.Builder
	b.WriteString(edgeLine(ch.TitleBar(max(1, a.w-2*margin)), a.w))
	b.WriteString("\n" + strings.Repeat(" ", a.w)) // the blank row under the title
	var cur *tea.Cursor
	place := func(name string, p kit.Pane, x, y, w, h int) string {
		if p == nil {
			p = placeholder(name)
		}
		a.regions = append(a.regions, region{name, p, x, y, w, h})
		if pl, ok := p.(kit.Placed); ok {
			pl.Place(x, y)
		}
		if c, ok := p.(kit.Cursored); ok && a.focus == name {
			if cc := c.Cursor(); cc != nil {
				cc.Position.X += x
				cc.Position.Y += y
				cur = cc
			}
		}
		return p.View(w, h)
	}

	// Below the title everything sits inside the side margins: panels are
	// W wide and start at column margin.
	W := max(1, a.w-2*margin)
	var body strings.Builder

	// filter bar: a bordered one-line panel
	inner := place("filter", a.p.Filter, margin+2, titleRows+1, max(1, W-4), 1)
	body.WriteString(a.frame(inner, W, filterRows, a.filterTitle(), styled.Text{}, a.focus == "filter", a.p.Filter))
	body.WriteString("\n") // the blank row under the filter box

	bodyH := max(3, a.h-bodyTop-keyRows)
	top := bodyTop
	tabs := a.tabStrip()
	body.WriteString("\n")
	switch a.tab {
	case kit.TabData:
		gw := W
		if a.env.State.DetailOpen {
			gw = max(10, W-detailW-1)
		}
		// the grid, a blank row and the status line (Python's #status margin)
		gridH := max(1, bodyH-4)
		inner := place("grid", a.p.Grid, margin+2, top+1, max(1, gw-4), gridH)
		inner += "\n\n" + fitLine(ch.StatusLine(max(1, gw-4)), max(1, gw-4))
		left := a.frame(inner, gw, bodyH, tabs, nil2(a.p.Grid), a.focus == "grid", a.p.Grid)
		if a.env.State.DetailOpen {
			dw := W - gw - 1
			dinner := place("detail", a.p.Detail, margin+gw+1+2, top+1, max(1, dw-4), bodyH-2)
			right := a.frame(dinner, dw, bodyH, title(a.p.Detail), styled.Text{}, a.focus == "detail", a.p.Detail)
			gap := strings.TrimSuffix(strings.Repeat(" \n", bodyH), "\n")
			body.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, left, gap, right))
		} else {
			body.WriteString(left)
		}
	default:
		name := tabPane(a.tab)
		p := a.parts()[name]
		if pp, ok := p.(kit.Paneled); ok {
			a.regions = append(a.regions, region{name, p, margin, top, W, bodyH})
			if pl, ok := p.(kit.Placed); ok {
				pl.Place(margin, top)
			}
			body.WriteString(a.panels(pp, W, bodyH, tabs, a.focus == name))
			break
		}
		inner := place(name, p, margin+2, top+1, max(1, W-4), bodyH-2)
		body.WriteString(a.frame(inner, W, bodyH, tabs, nil2(p), a.focus == name, p))
	}
	pad := strings.Repeat(" ", margin)
	for _, l := range strings.Split(body.String(), "\n") {
		b.WriteString("\n" + pad + fitLine(l, W) + pad)
	}
	b.WriteString("\n")
	b.WriteString(a.keyBar())
	return b.String(), cur
}

// keyBar is the key bar's line: a dialog's own hints, if it has any; else
// the focused part's (Python leaves the screen's under a dialog whose focus
// isn't on a text input).
func (a *App) keyBar() string {
	var hints []kit.KeyHint
	if n := len(a.dialogs); n > 0 {
		hints = a.dialogs[n-1].Keys()
	}
	if p := a.focused(); len(hints) == 0 && p != nil {
		hints = p.Keys()
	}
	return edgeLine(a.p.Chrome.KeyBar(max(1, a.w-2*margin), hints), a.w)
}

// edgeLine is s inside the screen's margin, w cells. (The chrome's title
// bar and key bar keep one more cell themselves, Python's margin 0 1.)
func edgeLine(s string, w int) string {
	pad := strings.Repeat(" ", margin)
	return fitLine(pad+fitLine(s, max(0, w-2*margin))+pad, w)
}

func title(p kit.Pane) styled.Text {
	if f, ok := p.(kit.Framed); ok {
		return f.Title()
	}
	return styled.Text{}
}

func nil2(p kit.Pane) styled.Text {
	if f, ok := p.(kit.Framed); ok {
		return f.Subtitle()
	}
	return styled.Text{}
}

func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// filterTitle is the filter box's border title: "filter", dim (Python's
// #filterbox border_title), unless the filter pane has its own.
func (a *App) filterTitle() styled.Text {
	if t := title(a.p.Filter); t.Plain != "" {
		return t
	}
	return styled.New("filter", a.env.Look.Style("dim"))
}
