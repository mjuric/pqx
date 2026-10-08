// Package app is pqx's root Bubble Tea model: it lays out the screen (title
// bar, filter bar, the tabs' panels, the details pane, status line, key
// bar), routes keys, paste and mouse to the part with focus, switches tabs,
// stacks dialogs over the screen without moving it, runs the Esc rules, and
// broadcasts the parts' messages (kit) to every part.
//
// The parts come from Parts; a nil part shows a placeholder. Until the grid
// is ported (WP7), the prototype's model (internal/ui) is the Data tab as a
// Legacy part: it draws everything below the title bar itself.
package app

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// Parts are the UI's parts. Names (for FocusMsg and focus order): "filter",
// "grid", "detail", "schema", "stats", "plot", "meta".
type Parts struct {
	Filter, Grid, Detail      kit.Pane
	Schema, Stats, Plot, Meta kit.Pane
	Chrome                    kit.Chrome
	// Legacy, if set, is the Data tab drawn whole below the title bar, with
	// every key but the tab keys (the prototype, until WP7 replaces it).
	Legacy kit.Pane
}

// Layout constants (Python pqx's layout).
const (
	titleRows  = 1
	filterRows = 3 // a bordered one-line input
	keyRows    = 1
	detailW    = 53 // the details pane's width (PR #24)
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
		"legacy": a.p.Legacy,
	}
}

// focused is the pane with focus, or nil.
func (a *App) focused() kit.Pane {
	if a.legacyData() {
		return a.p.Legacy
	}
	return a.parts()[a.focus]
}

func (a *App) legacyData() bool { return a.p.Legacy != nil && a.tab == kit.TabData }

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
	for _, name := range []string{"filter", "grid", "detail", "schema", "stats", "plot", "meta", "legacy"} {
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
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.w, a.h = msg.Width, msg.Height
		var cmd tea.Cmd
		if a.p.Legacy != nil {
			cmd = a.p.Legacy.Update(tea.WindowSizeMsg{Width: a.w, Height: max(1, a.h-titleRows)})
		}
		return a, tea.Batch(cmd, a.broadcast(msg))
	case tea.KeyPressMsg:
		return a, a.onKey(msg)
	case tea.PasteMsg:
		if len(a.dialogs) > 0 {
			return a, a.dialogs[len(a.dialogs)-1].Update(msg)
		}
		if p := a.focused(); p != nil {
			return a, p.Update(msg)
		}
		return a, nil
	case tea.MouseMsg:
		return a, a.onMouse(msg)
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
	switch s {
	case "ctrl+left":
		return a.switchTab((a.tab + 4) % 5)
	case "ctrl+right":
		return a.switchTab((a.tab + 1) % 5)
	}
	if !a.typing() {
		if len(s) == 1 && s >= "1" && s <= "5" {
			return a.switchTab(kit.Tab(s[0] - '1'))
		}
	}
	if a.legacyData() {
		return a.p.Legacy.Update(k)
	}
	if !a.typing() {
		switch s {
		case "q":
			return a.quit()
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
			d := 1
			if s == "shift+tab" {
				d = -1
			}
			if f, ok := a.focused().(InnerFocus); ok && f.CycleFocus(d) {
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
	a.env.Tasks.CancelAll()
	return tea.Quit
}

func tabPane(t kit.Tab) string {
	return [...]string{"grid", "schema", "stats", "plot", "meta"}[t]
}

func (a *App) switchTab(t kit.Tab) tea.Cmd {
	a.tab = t
	return a.setFocus(tabPane(t))
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
	if a.legacyData() {
		if m.Y < titleRows {
			return nil
		}
		return a.p.Legacy.Update(shift(msg, 0, titleRows))
	}
	if _, ok := msg.(tea.MouseClickMsg); ok && m.Y >= titleRows+filterRows {
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
	if q, ok := a.p.Legacy.(interface{ Quitting() bool }); ok && q.Quitting() {
		a.quitting = true
	}
	if a.quitting || a.w <= 0 || a.h <= 0 {
		return v
	}
	base, cur := a.render()
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

// compose draws overlays over base without moving it.
func compose(w, h int, base string, over []kit.Overlay) string {
	layers := []*lipgloss.Layer{lipgloss.NewLayer(base)}
	for i, o := range over {
		layers = append(layers, lipgloss.NewLayer(o.Content).X(o.X).Y(o.Y).Z(i+1))
	}
	c := lipgloss.NewCanvas(w, h)
	c.Compose(lipgloss.NewCompositor(layers...))
	return c.Render()
}

// render draws the screen and returns the text cursor, if a part shows one.
func (a *App) render() (string, *tea.Cursor) {
	a.regions = a.regions[:0]
	ch := a.p.Chrome
	var b strings.Builder
	b.WriteString(fitLine(ch.TitleBar(a.w), a.w))
	if a.legacyData() {
		b.WriteString("\n")
		b.WriteString(a.p.Legacy.View(a.w, a.h-titleRows))
		var cur *tea.Cursor
		if c, ok := a.p.Legacy.(kit.Cursored); ok {
			if cur = c.Cursor(); cur != nil {
				cur.Position.Y += titleRows
			}
		}
		return b.String(), cur
	}
	var cur *tea.Cursor
	place := func(name string, p kit.Pane, x, y, w, h int) string {
		if p == nil {
			p = placeholder(name)
		}
		a.regions = append(a.regions, region{name, p, x, y, w, h})
		if c, ok := p.(kit.Cursored); ok && a.focus == name {
			if cc := c.Cursor(); cc != nil {
				cc.Position.X += x
				cc.Position.Y += y
				cur = cc
			}
		}
		return p.View(w, h)
	}

	// filter bar: a bordered one-line panel
	b.WriteString("\n")
	inner := place("filter", a.p.Filter, 2, titleRows+1, max(1, a.w-4), 1)
	b.WriteString(a.frame(inner, a.w, filterRows, styled.Text{}, styled.Text{}, a.focus == "filter", a.p.Filter))

	bodyH := max(3, a.h-titleRows-filterRows-keyRows)
	top := titleRows + filterRows
	tabs := a.tabStrip()
	b.WriteString("\n")
	switch a.tab {
	case kit.TabData:
		gw := a.w
		if a.env.State.DetailOpen {
			gw = max(10, a.w-detailW)
		}
		gridH := max(1, bodyH-3) // borders and the status line
		inner := place("grid", a.p.Grid, 2, top+1, max(1, gw-4), gridH)
		inner += "\n" + fitLine(ch.StatusLine(max(1, gw-4)), max(1, gw-4))
		left := a.frame(inner, gw, bodyH, tabs, nil2(a.p.Grid), a.focus == "grid", a.p.Grid)
		if a.env.State.DetailOpen {
			dinner := place("detail", a.p.Detail, gw+2, top+1, detailW-4, bodyH-2)
			right := a.frame(dinner, a.w-gw, bodyH, title(a.p.Detail), styled.Text{}, a.focus == "detail", a.p.Detail)
			b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, left, right))
		} else {
			b.WriteString(left)
		}
	default:
		name := tabPane(a.tab)
		p := a.parts()[name]
		if pp, ok := p.(Paneled); ok {
			a.regions = append(a.regions, region{name, p, 0, top, a.w, bodyH})
			lines := strings.Split(a.panels(pp, a.w, bodyH, tabs, a.focus == name), "\n")
			for i := range lines {
				lines[i] = fitLine(lines[i], a.w)
			}
			b.WriteString(strings.Join(lines, "\n"))
			break
		}
		inner := place(name, p, 2, top+1, max(1, a.w-4), bodyH-2)
		b.WriteString(a.frame(inner, a.w, bodyH, tabs, nil2(p), a.focus == name, p))
	}
	b.WriteString("\n")
	var hints []kit.KeyHint
	if p := a.focused(); p != nil {
		hints = p.Keys()
	}
	b.WriteString(fitLine(ch.KeyBar(a.w, hints), a.w))
	return b.String(), cur
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
