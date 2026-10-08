package grid

import (
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/ui/app"
	"github.com/mjuric/pqx/go/internal/ui/filter"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// harness runs the root model with the grid and the filter bar the way
// Bubble Tea does: every command in its own goroutine, its message fed back
// to Update on the test goroutine. It records notices, status messages,
// copies and the dialogs opened.
type harness struct {
	t    *testing.T
	app  *app.App
	g    *Grid
	f    *filter.Filter
	env  *kit.Env
	ds   data.Dataset
	msgs chan tea.Msg
	out  int

	quits   int
	notes   []kit.NotifyMsg
	status  kit.StatusMsg
	copies  []string
	dialogs []string
}

// fakeDialogs records which dialog was asked for.
type fakeDialogs struct{ h *harness }

type fakeDialog struct{ name string }

func (d fakeDialog) Update(tea.Msg) tea.Cmd   { return nil }
func (d fakeDialog) View(w, h int) string     { return strings.Repeat(" ", w) }
func (d fakeDialog) Keys() []kit.KeyHint      { return nil }
func (d fakeDialog) Size(w, h int) (int, int) { return 10, 1 }
func (fd fakeDialogs) open(name string) kit.Dialog {
	fd.h.dialogs = append(fd.h.dialogs, name)
	return fakeDialog{name}
}
func (fd fakeDialogs) Goto(total int64) kit.Dialog { return fd.open("goto") }
func (fd fakeDialogs) Format(col data.Column, cur fmtx.Override, sample data.Value) kit.Dialog {
	return fd.open("format " + col.Name + " " + fmtx.Format(sample, fmtx.KindStr, fmtx.Opts{}))
}
func (fd fakeDialogs) Columns(cols []data.Column, hidden map[string]bool, current string) kit.Dialog {
	return fd.open("columns " + current)
}
func (fd fakeDialogs) Export() kit.Dialog { return fd.open("export") }
func (fd fakeDialogs) Help() kit.Dialog   { return fd.open("help") }

type hopts struct {
	where   string
	noDlg   bool
	formats map[string]fmtx.Override
	session map[string]fmtx.Override
}

func newHarness(t *testing.T, ds data.Dataset, w, h int, o ...hopts) *harness {
	t.Helper()
	var op hopts
	if len(o) > 0 {
		op = o[0]
	}
	formats := map[string]fmtx.Override{}
	for k, v := range op.formats {
		formats[k] = v
	}
	for k, v := range op.session {
		formats[k] = v
	}
	env := &kit.Env{
		DS:    ds,
		Opts:  kit.Options{Version: "test", Where: op.where, Formats: op.formats, SessionFormats: op.session},
		Look:  app.BasicLook{},
		State: &kit.State{Total: ds.NumRows(), Columns: ds.Columns(), Formats: formats, Hidden: map[string]bool{}},
		Tasks: kit.NewTasks(),
	}
	hs := &harness{t: t, env: env, ds: ds, msgs: make(chan tea.Msg, 1000)}
	if !op.noDlg {
		env.Dialogs = fakeDialogs{hs}
	}
	hs.g = New(env)
	hs.f = filter.New(env)
	hs.app = app.New(env, app.Parts{Grid: hs.g, Filter: hs.f})
	hs.g.Focus()
	hs.send(tea.WindowSizeMsg{Width: w, Height: h})
	hs.settle()
	return hs
}

func (h *harness) exec(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	h.out++
	go func() { h.msgs <- cmd() }()
}

var cmdSlice = reflect.TypeOf([]tea.Cmd(nil))

func (h *harness) send(msg tea.Msg) {
	switch m := msg.(type) {
	case kit.NotifyMsg:
		h.notes = append(h.notes, m)
	case kit.StatusMsg:
		h.status = m
	case kit.CopyMsg:
		h.copies = append(h.copies, m.Text)
	}
	_, cmd := h.app.Update(msg)
	h.exec(cmd)
	h.app.View() // as Bubble Tea draws after each message
}

func (h *harness) handle(msg tea.Msg) {
	h.out--
	switch msg := msg.(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range msg {
			h.exec(c)
		}
	case tea.QuitMsg:
		h.quits++
	default:
		// tea.Sequence: run its commands in order, each message delivered
		// before the next runs
		if v := reflect.ValueOf(msg); v.Kind() == reflect.Slice && v.Type().ConvertibleTo(cmdSlice) {
			for _, c := range v.Convert(cmdSlice).Interface().([]tea.Cmd) {
				if c != nil {
					h.out++
					h.handle(c())
				}
			}
			return
		}
		h.send(msg)
	}
}

// settle feeds messages back until no command is running, or until those
// still running (blocked reads) have been quiet for a while.
func (h *harness) settle() {
	h.t.Helper()
	for h.out > 0 {
		select {
		case msg := <-h.msgs:
			h.handle(msg)
		case <-time.After(100 * time.Millisecond):
			return
		}
	}
}

// waitFor feeds messages until cond holds.
func (h *harness) waitFor(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.After(3 * time.Second)
	for !cond() {
		select {
		case msg := <-h.msgs:
			h.handle(msg)
		case <-deadline:
			h.t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func kp(s string) tea.KeyPressMsg {
	switch s {
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	case "pgup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "home":
		return tea.KeyPressMsg{Code: tea.KeyHome}
	case "end":
		return tea.KeyPressMsg{Code: tea.KeyEnd}
	case "ctrl+home":
		return tea.KeyPressMsg{Code: tea.KeyHome, Mod: tea.ModCtrl}
	case "ctrl+end":
		return tea.KeyPressMsg{Code: tea.KeyEnd, Mod: tea.ModCtrl}
	case "ctrl+x":
		return tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	}
	r := []rune(s)
	if len(r) != 1 {
		panic("unknown key " + s)
	}
	return tea.KeyPressMsg{Code: r[0], Text: s}
}

func (h *harness) press(keys ...string) {
	for _, k := range keys {
		h.send(kp(k))
		h.settle()
	}
}

func (h *harness) typeText(s string) {
	for _, r := range s {
		h.send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	h.settle()
}

// filterWith types text into the filter bar and applies it.
func (h *harness) filterWith(text string) {
	h.press("/")
	for len(h.f.Value()) > 0 {
		h.send(kp("backspace"))
	}
	h.typeText(text)
	h.press("enter")
}

// raw is the whole screen as drawn.
func (h *harness) raw() string { return h.app.View().Content }

// screen is the whole screen without escape sequences.
func (h *harness) screen() string { return ansi.Strip(h.raw()) }

// grid is the grid pane as drawn, without escape sequences, one string
// per line.
func (h *harness) grid() []string {
	h.app.View()
	return strings.Split(ansi.Strip(h.g.View(h.g.w, h.g.h)), "\n")
}

// gridRaw is the grid pane as drawn, with escape sequences.
func (h *harness) gridRaw() []string {
	h.app.View()
	return strings.Split(h.g.View(h.g.w, h.g.h), "\n")
}

// noted reports whether a notice holding s was shown.
func (h *harness) noted(s string) bool {
	for _, n := range h.notes {
		if strings.Contains(n.Text, s) || strings.Contains(n.Title, s) {
			return true
		}
	}
	return false
}
