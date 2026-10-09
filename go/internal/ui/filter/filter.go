// Package filter is the filter bar (the prototype's, kept small until WP11
// rewrites it): a one-line input where Enter checks a WHERE expression (or
// a full query) with the dataset and applies it, with history, Ctrl+X to
// clear, and the error shown inline. It applies every view change, its own
// and those other parts ask for with kit.SetViewMsg (sort, "=", clear):
// it sets State.View, Columns and Total, broadcasts ViewChangedMsg, and
// counts the rows of a filtered view in the background (TotalMsg).
package filter

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

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

// Part is the filter's name for focus.
const Part = "filter"

// sqlStart is how a full query starts (Python's _SQL_START).
var sqlStart = regexp.MustCompile(`(?i)^\s*(select|with|from|pivot|unpivot|describe|summarize)\b`)

// IsSQL reports whether text is a full query rather than a WHERE expression.
func IsSQL(text string) bool { return sqlStart.MatchString(text) }

// Filter is the filter bar. It implements kit.Pane, kit.Focusable,
// kit.Inputs and kit.Cursored.
type Filter struct {
	env *kit.Env
	st  *kit.State

	in      textinput.Model
	focused bool
	err     string // the inline error, until the text changes

	history   []string
	histPos   int
	histDraft string

	hint    string
	hintSet bool

	pending *kit.SetViewMsg // the view being checked ("validate")
	counts  map[string]int64
	w       int // the input's width
	y       int // the row the root draws it on (kit.Placed)
}

// New makes the filter bar; opts.Where, if set, is applied at the first
// window size.
func New(env *kit.Env) *Filter {
	f := &Filter{env: env, st: env.State, counts: map[string]int64{}}
	f.in = newInput()
	f.hint = "SQL WHERE expression, e.g. " + example + " — or a full query: select … from t"
	f.in.Placeholder = f.hint
	f.histPos = 0
	return f
}

// example stands in when the file's first row gives none (screens.FILTER_EXAMPLE).
const example = "price > 100 and city = 'Paris'"

func newInput() textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.SetVirtualCursor(false) // the terminal's own cursor; no blink timer
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
	return ti
}

// Value is the text in the box.
func (f *Filter) Value() string { return f.in.Value() }

// Err is the inline error, "" if none.
func (f *Filter) Err() string { return f.err }

// Place implements kit.Placed.
func (f *Filter) Place(_, y int) { f.y = y }

// Pos is the row the bar was drawn on last.
func (f *Filter) Pos() int { return f.y }

// Focus implements kit.Focusable.
func (f *Filter) Focus() tea.Cmd {
	f.focused = true
	f.histPos = len(f.history)
	f.in.CursorEnd()
	return f.in.Focus()
}

// Blur implements kit.Focusable.
func (f *Filter) Blur() {
	f.focused = false
	f.in.Blur()
}

// TypingFocused implements kit.Inputs.
func (f *Filter) TypingFocused() bool { return f.focused }

// Keys implements kit.Pane (Python's KEYS["filter"]).
func (f *Filter) Keys() []kit.KeyHint {
	return []kit.KeyHint{{Key: "enter", Help: "apply"}, {Key: "esc", Help: "back"}, {Key: "ctrl+x", Help: "clear"},
		{Key: "↑↓", Help: "history"}, {Key: "select … from t", Help: "full query"}}
}

// promptW is the width of "› " (or "sql › ") before the input.
func (f *Filter) prompt() string {
	if IsSQL(f.in.Value()) {
		return "sql › "
	}
	return "› "
}

// Cursor implements kit.Cursored.
func (f *Filter) Cursor() *tea.Cursor {
	if !f.focused {
		return nil
	}
	c := f.in.Cursor()
	if c == nil {
		return nil
	}
	c.Position.X += ansi.StringWidth(f.prompt())
	if v := f.in.Value(); fmtx.HasControls(v, false) {
		// the box shows the text sanitized (View): measure what it shows
		r := []rune(v)
		c.Position.X = ansi.StringWidth(f.prompt()) + ansi.StringWidth(fmtx.Sanitize(string(r[:min(f.in.Position(), len(r))]), false))
	}
	c.Color = nil
	return c
}

// View implements kit.Pane: the prompt, the input (or the hint), and an
// error after it.
func (f *Filter) View(w, h int) string {
	look := f.env.Look
	p := f.prompt()
	avail := max(1, w-ansi.StringWidth(p)-1)
	e := ""
	if f.err != "" {
		e = "  ✗ " + f.err
		avail = max(10, avail-min(ansi.StringWidth(e), avail/2))
		avail = min(avail, ansi.StringWidth(f.in.Value())+1)
	}
	if f.w != avail {
		f.w = avail
		f.in.SetWidth(avail)
	}
	line := look.Render(styled.New(p, look.Style("accent")))
	switch {
	case fmtx.HasControls(f.in.Value(), false):
		// text set by another part (a value of the file, quoted) can hold
		// control characters: shown as symbols, never as they are
		line += ansi.Truncate(fmtx.Sanitize(f.in.Value(), false), avail, "…")
	case f.focused || f.in.Value() != "":
		line += f.in.View()
	default:
		line += look.Render(styled.New(ansi.Truncate(f.hint, max(0, w-ansi.StringWidth(p)), "…"), look.Style("dim")))
	}
	if e != "" {
		if room := w - ansi.StringWidth(line); room > 6 {
			line += look.Render(styled.New(ansi.Truncate(e, room, "…"), look.Style("error")))
		}
	}
	line = fit(line, w)
	if h <= 1 {
		return line
	}
	return line + strings.Repeat("\n"+strings.Repeat(" ", w), h-1)
}

func fit(s string, w int) string {
	n := ansi.StringWidth(s)
	if n > w {
		return ansi.Truncate(s, w, "")
	}
	return s + strings.Repeat(" ", w-n)
}

// Update implements kit.Pane.
func (f *Filter) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		var cmds []tea.Cmd
		if !f.hintSet {
			f.hintSet = true
			cmds = append(cmds, f.startHint())
			if w := strings.TrimSpace(f.env.Opts.Where); w != "" && f.st.View.Plain() {
				f.in.SetValue(w)
				cmds = append(cmds, f.apply(f.viewFor(w), -1))
			}
		}
		return tea.Batch(cmds...)
	case tea.KeyPressMsg:
		return f.onKey(msg)
	case tea.PasteMsg:
		return f.edit(msg)
	case tea.MouseClickMsg:
		return nil // the root focuses the bar on a click
	case kit.SetViewMsg:
		f.in.SetValue(viewText(msg.View))
		f.in.CursorEnd()
		f.err = ""
		return f.apply(msg.View, msg.KeepFileRow)
	case kit.DoneMsg:
		switch r := msg.Msg.(type) {
		case validated:
			return f.onValidated(r)
		case counted:
			return f.onCounted(r)
		case hinted:
			if r.hint != "" {
				f.hint = r.hint
				f.in.Placeholder = r.hint
			}
		}
	}
	return nil
}

// body is the part focus goes back to after a view is applied: the grid on
// the Data tab, else the tab's body.
func (f *Filter) body() string {
	return [...]string{"grid", "schema", "stats", "plot", "meta"}[f.st.Tab]
}

// viewText is what the box shows for a view.
func viewText(v data.View) string {
	if v.IsSQL() {
		return v.SQL
	}
	return v.Where
}

// viewFor is the view for text typed in the box: a full query, or a filter
// keeping the current sort.
func (f *Filter) viewFor(text string) data.View {
	if IsSQL(text) {
		return data.View{SQL: text}
	}
	v := data.View{Where: text}
	if !f.st.View.IsSQL() {
		v.OrderBy = f.st.View.OrderBy
	}
	return v
}

func (f *Filter) edit(msg tea.Msg) tea.Cmd {
	before := f.in.Value()
	var cmd tea.Cmd
	f.in, cmd = f.in.Update(msg)
	if f.in.Value() != before {
		f.err = ""
	}
	return cmd
}

func (f *Filter) onKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "enter":
		text := strings.TrimSpace(f.in.Value())
		if text != "" && (len(f.history) == 0 || f.history[len(f.history)-1] != text) {
			f.history = append(f.history, text)
		}
		f.histPos = len(f.history)
		return f.apply(f.viewFor(text), -1)
	case "ctrl+x":
		if f.in.Value() != "" && f.st.View.Plain() {
			f.in.SetValue("") // only typed, never applied: just empty the box
			f.err = ""
			return nil
		}
		if f.st.View.Plain() {
			return nil
		}
		// through the root, so the grid keeps the cursor's record
		return kit.Send(kit.SetViewMsg{View: data.View{}, KeepFileRow: f.st.FileRow})
	case "up":
		if f.histPos > 0 {
			if f.histPos == len(f.history) {
				f.histDraft = f.in.Value()
			}
			f.histPos--
			f.in.SetValue(f.history[f.histPos])
			f.in.CursorEnd()
		}
		return nil
	case "down":
		if f.histPos < len(f.history) {
			f.histPos++
			if f.histPos == len(f.history) {
				f.in.SetValue(f.histDraft)
			} else {
				f.in.SetValue(f.history[f.histPos])
			}
			f.in.CursorEnd()
		}
		return nil
	}
	return f.edit(k)
}

type validated struct {
	req  kit.SetViewMsg
	cols []data.Column
	err  error
}

type counted struct {
	view data.View
	n    int64
	secs float64
	err  error
}

// apply checks view with the dataset ("validate"), then shows it.
func (f *Filter) apply(v data.View, keep int64) tea.Cmd {
	if sameView(v, f.st.View) {
		f.pending = nil
		f.env.Tasks.Cancel("validate")
		var cmds []tea.Cmd
		if f.focused {
			cmds = append(cmds, kit.Send(kit.FocusMsg{Pane: f.body()}))
		}
		if f.st.Total < 0 && !f.env.Tasks.Running("count") {
			cmds = append(cmds, f.count(v)) // its count was cancelled: count again
		}
		return tea.Batch(cmds...)
	}
	if err := f.env.DS.SetupErr(); err != nil && !v.Plain() {
		return f.fail(err)
	}
	req := kit.SetViewMsg{View: v, KeepFileRow: keep}
	f.pending = &req
	ds := f.env.DS
	return f.env.Tasks.Run("validate", "checking the filter", false, func(ctx context.Context) tea.Msg {
		cols, err := ds.Validate(ctx, v)
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return validated{req: req, cols: cols, err: err}
	})
}

func (f *Filter) onValidated(r validated) tea.Cmd {
	f.pending = nil
	if r.err != nil {
		if errors.Is(r.err, context.Canceled) {
			return nil
		}
		return f.fail(r.err)
	}
	st := f.st
	v := r.req.View
	wasSQL := st.View.IsSQL()
	f.env.Tasks.Cancel("count") // the old view's: stale (a failure would land on the new one)
	st.View = v
	if v.IsSQL() || wasSQL || len(st.Columns) != len(r.cols) {
		st.Hidden = map[string]bool{}
	}
	if len(r.cols) > 0 {
		st.Columns = r.cols
	}
	cmds := []tea.Cmd{kit.Send(kit.ViewChangedMsg{})}
	if v.Plain() {
		st.Total = f.env.DS.NumRows()
	} else if n, ok := f.counts[countKey(v)]; ok {
		st.Total = n
		cmds = append(cmds, kit.Send(kit.TotalMsg{}))
	} else {
		st.Total = -1
		cmds = append(cmds, f.count(v))
	}
	if f.focused {
		cmds = append(cmds, kit.Send(kit.FocusMsg{Pane: f.body()}))
	}
	f.err = ""
	return tea.Sequence(kit.Send(kit.StatusMsg{}), tea.Batch(cmds...))
}

// fail shows why a view can't be applied: inline, in the status line and
// as a toast (Python's _show_error).
func (f *Filter) fail(err error) tea.Cmd {
	first, hint := describe(err)
	f.err = first
	return tea.Batch(
		kit.Send(kit.StatusMsg{Severity: kit.Error, Text: "Query failed   reason: " + first + "   → " + hint + "  ·  previous view kept"}),
		kit.Send(kit.NotifyMsg{Severity: kit.Error, Title: "✗ Query failed", Text: fmtx.Sanitize(trunc(err.Error(), 600), true),
			Timeout: 8 * time.Second}))
}

var (
	errPrefix  = regexp.MustCompile(`^(Binder|Parser|Catalog|Conversion|Invalid Input|Out of Range) Error:\s*`)
	notFound   = regexp.MustCompile(`Referenced column ("[^"]+") not found in FROM clause!?`)
	candidates = regexp.MustCompile(`Candidate bindings: "(?:[^".\n]+\.)?([^"\n]+)"`)
)

// describe is an error's first line, shortened as Python's _show_error
// does, and a hint ("did you mean …?").
func describe(err error) (string, string) {
	msg := strings.TrimSpace(err.Error())
	first, _, _ := strings.Cut(msg, "\n")
	first = errPrefix.ReplaceAllString(first, "")
	first = notFound.ReplaceAllString(first, "unknown column $1")
	first = strings.TrimRight(first, "!")
	hint := "edit with /"
	if m := candidates.FindStringSubmatch(msg); m != nil {
		hint = `did you mean "` + fmtx.Sanitize(trunc(m[1], 80), false) + `"?`
	}
	return fmtx.Sanitize(trunc(first, 160), false), hint
}

func trunc(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// count counts the rows of view ("count").
func (f *Filter) count(v data.View) tea.Cmd {
	ds := f.env.DS
	return f.env.Tasks.Run("count", "counting rows", false, func(ctx context.Context) tea.Msg {
		t0 := time.Now()
		n, err := ds.Count(ctx, v)
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return counted{view: v, n: n, secs: time.Since(t0).Seconds(), err: err}
	})
}

func (f *Filter) onCounted(r counted) tea.Cmd {
	if !sameView(r.view, f.st.View) {
		return nil
	}
	if r.err != nil {
		if errors.Is(r.err, context.Canceled) {
			return nil
		}
		// A toast only: the status line is the reads' (the grid's). A filter
		// that can't be counted can't be read either, and the read's error
		// (with the previous view kept) must not be replaced by this one,
		// whichever arrives first.
		return kit.Send(kit.NotifyMsg{Severity: kit.Error, Title: "✗ Count failed", Text: fmtx.Sanitize(trunc(r.err.Error(), 600), true)})
	}
	f.counts[countKey(r.view)] = r.n
	f.st.Total = r.n
	return kit.Send(kit.TotalMsg{})
}

// countKey is what a view's row count depends on (not its sort).
func countKey(v data.View) string {
	if v.IsSQL() {
		return "sql\x00" + v.SQL
	}
	return "where\x00" + strings.TrimSpace(v.Where)
}

func sameView(a, b data.View) bool {
	if a.Where != b.Where || a.SQL != b.SQL || len(a.OrderBy) != len(b.OrderBy) {
		return false
	}
	for i := range a.OrderBy {
		if a.OrderBy[i] != b.OrderBy[i] {
			return false
		}
	}
	return true
}
