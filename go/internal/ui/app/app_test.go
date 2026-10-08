package app

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

type fakeDS struct{ data.Unimplemented }

func (fakeDS) Path() string           { return "/x/test.parquet" }
func (fakeDS) NumRows() int64         { return 10 }
func (fakeDS) Columns() []data.Column { return []data.Column{{Name: "a"}} }

// pane records what it gets.
type pane struct {
	name    string
	keys    []string
	mouse   []tea.Mouse
	msgs    []tea.Msg
	focused bool
	typing  bool
}

func (p *pane) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case tea.KeyPressMsg:
		p.keys = append(p.keys, m.String())
	case tea.MouseMsg:
		p.mouse = append(p.mouse, m.Mouse())
	default:
		p.msgs = append(p.msgs, msg)
	}
	return nil
}
func (p *pane) View(w, h int) string {
	l := make([]string, h)
	for i := range l {
		l[i] = strings.Repeat(string(p.name[0]), w)
	}
	return strings.Join(l, "\n")
}
func (p *pane) Keys() []kit.KeyHint { return []kit.KeyHint{{Key: p.name, Help: "keys"}} }
func (p *pane) Focus() tea.Cmd      { p.focused = true; return nil }
func (p *pane) Blur()               { p.focused = false }
func (p *pane) TypingFocused() bool { return p.typing }

type dialog struct{ pane }

func (d *dialog) Size(w, h int) (int, int) { return 10, 3 }

func setup(t *testing.T) (*App, map[string]*pane) {
	t.Helper()
	ps := map[string]*pane{}
	for _, n := range []string{"filter", "grid", "detail", "schema", "stats", "plot", "meta"} {
		ps[n] = &pane{name: n}
	}
	ps["filter"].typing = true
	env := &kit.Env{DS: fakeDS{}, Look: BasicLook{}, State: &kit.State{Total: 10}, Tasks: kit.NewTasks()}
	a := New(env, Parts{Filter: ps["filter"], Grid: ps["grid"], Detail: ps["detail"],
		Schema: ps["schema"], Stats: ps["stats"], Plot: ps["plot"], Meta: ps["meta"]})
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return a, ps
}

func key(s string) tea.KeyPressMsg {
	switch s {
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "ctrl+right":
		return tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModCtrl}
	}
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

// run feeds msg and the messages its commands yield (not ticks).
func run(a *App, msg tea.Msg) {
	queue := []tea.Msg{msg}
	for len(queue) > 0 {
		m := queue[0]
		queue = queue[1:]
		_, cmd := a.Update(m)
		queue = append(queue, drain(cmd)...)
	}
}

func drain(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if b, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range b {
			out = append(out, drain(c)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

// screen is the screen's text, trailing blanks dropped (the overlay
// compositor drops them).
func screen(a *App) []string {
	l := strings.Split(ansi.Strip(a.View().Content), "\n")
	for i := range l {
		l[i] = strings.TrimRight(l[i], " ")
	}
	return l
}

func TestKeysGoToFocusedPane(t *testing.T) {
	a, ps := setup(t)
	run(a, key("s"))
	if len(ps["grid"].keys) != 1 || ps["grid"].keys[0] != "s" {
		t.Fatalf("grid keys %v", ps["grid"].keys)
	}
	run(a, key("/"))
	if !ps["filter"].focused {
		t.Fatal("/ didn't focus the filter")
	}
	// typing: digits and q are text, not tab switches or quit
	run(a, key("3"))
	run(a, key("q"))
	if a.tab != kit.TabData || a.quitting || strings.Join(ps["filter"].keys, "") != "3q" {
		t.Fatalf("tab %d quitting %v filter keys %v", a.tab, a.quitting, ps["filter"].keys)
	}
	run(a, key("esc"))
	if a.focus != "grid" {
		t.Fatalf("esc from the filter: focus %s", a.focus)
	}
}

func TestTabs(t *testing.T) {
	a, ps := setup(t)
	run(a, key("3"))
	if a.tab != kit.TabStats || a.focus != "stats" || !ps["stats"].focused {
		t.Fatalf("tab %d focus %s", a.tab, a.focus)
	}
	if s := strings.Join(screen(a), "\n"); !strings.Contains(s, "sssss") || !strings.Contains(s, "1 Data ─ 2 Schema ─ 3 Stats") {
		t.Fatalf("stats tab not shown:\n%s", s)
	}
	run(a, key("ctrl+right"))
	if a.tab != kit.TabPlot {
		t.Fatalf("ctrl+right: tab %d", a.tab)
	}
	// clicking a tab name on the panel border
	x := 3 + strings.Index("1 Data ─ 2 Schema", "Schema")
	run(a, tea.MouseClickMsg{X: x, Y: titleRows + filterRows, Button: tea.MouseLeft})
	if a.tab != kit.TabSchema {
		t.Fatalf("click on Schema: tab %d", a.tab)
	}
}

func TestEscRules(t *testing.T) {
	a, _ := setup(t)
	st := a.env.State
	run(a, kit.ToggleDetailMsg{})
	if !st.DetailOpen {
		t.Fatal("detail didn't open")
	}
	// own work (loading rows) runs: Esc closes the pane, the work carries on
	a.env.Tasks.Run("page", "loading", false, func(ctx context.Context) tea.Msg { <-ctx.Done(); return nil })
	run(a, key("esc"))
	if st.DetailOpen || !a.env.Tasks.Running("page") {
		t.Fatalf("detail %v page running %v", st.DetailOpen, a.env.Tasks.Running("page"))
	}
	// user work runs: Esc cancels everything and leaves the pane open
	run(a, kit.ToggleDetailMsg{})
	a.env.Tasks.Run("locate", "finding the record", true, func(ctx context.Context) tea.Msg { <-ctx.Done(); return nil })
	run(a, key("esc"))
	if !st.DetailOpen || a.env.Tasks.Busy() {
		t.Fatalf("detail %v busy %v", st.DetailOpen, a.env.Tasks.Busy())
	}
	run(a, key("esc"))
	if st.DetailOpen {
		t.Fatal("second esc didn't close the pane")
	}
}

func TestStaleTaskResultsDropped(t *testing.T) {
	a, ps := setup(t)
	tasks := a.env.Tasks
	old := tasks.Run("stats", "", true, func(context.Context) tea.Msg { return "old" })
	cur := tasks.Run("stats", "", true, func(context.Context) tea.Msg { return "new" })
	run(a, old())
	run(a, cur())
	var got []tea.Msg
	for _, m := range ps["stats"].msgs {
		if d, ok := m.(kit.DoneMsg); ok {
			got = append(got, d.Msg)
		}
	}
	if len(got) != 1 || got[0] != "new" {
		t.Fatalf("got %v", got)
	}
}

func TestDialogOverlayKeepsScreen(t *testing.T) {
	a, ps := setup(t)
	before := screen(a)
	d := &dialog{pane{name: "Zdialog"}}
	run(a, kit.OpenDialogMsg{Dialog: d})
	after := screen(a)
	if len(after) != len(before) {
		t.Fatalf("%d lines, was %d", len(after), len(before))
	}
	x, y := a.dialogPos(d)
	changed := 0
	for i := range before {
		if i == len(before)-1 {
			// the key bar shows the dialog's keys
			if !strings.Contains(after[i], "Zdialog keys") {
				t.Fatalf("key bar %q", after[i])
			}
			continue
		}
		if before[i] != after[i] {
			changed++
			if i < y || i >= y+3 {
				t.Fatalf("line %d changed outside the dialog:\n%s\n%s", i, before[i], after[i])
			}
			if !strings.Contains(after[i], strings.Repeat("Z", 10)) || after[i][:x] != before[i][:x] {
				t.Fatalf("line %d: %q", i, after[i])
			}
		}
	}
	if changed != 3 {
		t.Fatalf("%d lines changed", changed)
	}
	run(a, key("x"))
	if len(d.keys) != 1 || len(ps["grid"].keys) != 0 {
		t.Fatal("keys didn't go to the dialog")
	}
	run(a, kit.CloseDialogMsg{})
	if strings.Join(screen(a), "\n") != strings.Join(before, "\n") {
		t.Fatal("screen not restored")
	}
}

func TestMouseRouting(t *testing.T) {
	a, ps := setup(t)
	screen(a)         // lays out the regions
	r := a.regions[1] // grid
	if r.name != "grid" {
		t.Fatalf("region %s", r.name)
	}
	run(a, tea.MouseClickMsg{X: r.x + 5, Y: r.y + 2, Button: tea.MouseLeft})
	if len(ps["grid"].mouse) != 1 || ps["grid"].mouse[0].X != 5 || ps["grid"].mouse[0].Y != 2 {
		t.Fatalf("grid mouse %v", ps["grid"].mouse)
	}
	run(a, tea.MouseClickMsg{X: 3, Y: titleRows + 1, Button: tea.MouseLeft})
	if a.focus != "filter" {
		t.Fatalf("click on the filter: focus %s", a.focus)
	}
}

func TestDetailLayout(t *testing.T) {
	a, _ := setup(t)
	run(a, kit.ToggleDetailMsg{})
	s := screen(a)
	row := s[titleRows+filterRows+2]
	if !strings.Contains(row, "ggg") || !strings.Contains(row, "ddd") {
		t.Fatalf("row %q", row)
	}
	for i, l := range strings.Split(ansi.Strip(a.View().Content), "\n") {
		if w := ansi.StringWidth(l); w != 100 {
			t.Fatalf("line %d is %d wide: %q", i, w, l)
		}
	}
	if len(s) != 30 {
		t.Fatalf("%d lines", len(s))
	}
}

func TestRenderStyled(t *testing.T) {
	var tx styled.Text
	tx.Append("a", styled.Style{Bold: true})
	tx.Append("b", styled.Style{})
	if got := ansi.Strip(BasicLook{}.Render(tx)); got != "ab" {
		t.Fatalf("%q", got)
	}
}

// fakeDialogs makes named dialogs.
type fakeDialogs struct{}

func (fakeDialogs) Goto(int64) kit.Dialog { return &dialog{pane{name: "goto"}} }
func (fakeDialogs) Format(data.Column, fmtx.Override, data.Value) kit.Dialog {
	return &dialog{pane{name: "format"}}
}
func (fakeDialogs) Columns([]data.Column, map[string]bool, string) kit.Dialog {
	return &dialog{pane{name: "columns"}}
}
func (fakeDialogs) Export() kit.Dialog { return &dialog{pane{name: "export"}} }
func (fakeDialogs) Help() kit.Dialog   { return &dialog{pane{name: "help"}} }

// "?" opens the help and "e" the export from any tab, but are text while
// typing (tests/test_app.py::test_quit_key_is_text_in_filter).
func TestHelpAndExportKeys(t *testing.T) {
	a, ps := setup(t)
	a.env.Dialogs = fakeDialogs{}
	for _, tc := range []struct{ key, tab, dialog string }{{"?", "1", "help"}, {"e", "1", "export"}, {"?", "3", "help"}} {
		run(a, key(tc.tab))
		run(a, key(tc.key))
		if len(a.dialogs) != 1 || a.dialogs[0].(*dialog).name != tc.dialog {
			t.Fatalf("%s on tab %s: %v", tc.key, tc.tab, a.dialogs)
		}
		run(a, kit.CloseDialogMsg{})
	}
	run(a, key("/"))
	run(a, key("?"))
	run(a, key("e"))
	if len(a.dialogs) != 0 || strings.Join(ps["filter"].keys, "") != "?e" {
		t.Fatalf("dialogs %v filter keys %v", a.dialogs, ps["filter"].keys)
	}
}

// chrome records what it gets (for the spinner, it must see keys).
type recChrome struct {
	basicChrome
	keys int
}

func (c *recChrome) Update(msg tea.Msg) tea.Cmd {
	if _, ok := msg.(tea.KeyPressMsg); ok {
		c.keys++
	}
	return c.basicChrome.Update(msg)
}

type placedPane struct {
	pane
	x, y int
}

func (p *placedPane) Place(x, y int) { p.x, p.y = x, y }

func TestRootV2(t *testing.T) {
	a, ps := setup(t)
	ch := &recChrome{basicChrome: basicChrome{env: a.env}}
	a.p.Chrome = ch
	run(a, key("s"))
	if ch.keys != 1 {
		t.Fatalf("chrome saw %d keys", ch.keys)
	}
	// m toggles sampling from any tab, and announces it
	run(a, key("3"))
	run(a, key("m"))
	if !a.env.State.Sampling {
		t.Fatal("m didn't toggle sampling")
	}
	got := false
	for _, m := range ps["plot"].msgs {
		if _, ok := m.(kit.SamplingChangedMsg); ok {
			got = true
		}
	}
	if !got {
		t.Fatal("no SamplingChangedMsg")
	}
	// the tab shown is announced
	var tabs []kit.Tab
	for _, m := range ps["grid"].msgs {
		if tc, ok := m.(kit.TabChangedMsg); ok {
			tabs = append(tabs, tc.Tab)
		}
	}
	if len(tabs) != 1 || tabs[0] != kit.TabStats || a.env.State.Tab != kit.TabStats {
		t.Fatalf("tabs %v state %d", tabs, a.env.State.Tab)
	}
	// background work survives Esc and isn't busy; quit stops it
	a.env.Tasks.RunBackground("footer", func(ctx context.Context) tea.Msg { <-ctx.Done(); return nil })
	if a.env.Tasks.Busy() {
		t.Fatal("background work counts as busy")
	}
	run(a, key("esc"))
	if !a.env.Tasks.Running("footer") {
		t.Fatal("esc cancelled background work")
	}
	a.quit()
	if a.env.Tasks.Running("footer") {
		t.Fatal("quit left background work running")
	}
}

func TestPlacedAndInnerFocus(t *testing.T) {
	a, _ := setup(t)
	pp := &placedPane{pane: pane{name: "plot"}}
	a.p.Plot = pp
	run(a, key("4"))
	screen(a)
	if pp.x != 2 || pp.y != titleRows+filterRows+1 {
		t.Fatalf("placed at %d,%d", pp.x, pp.y)
	}
}
