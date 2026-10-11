// Package filter is the filter bar (Python pqx's #filterbox): a one-line
// input for a WHERE expression or a full query, with completion of column
// names and keywords (→), history (↑↓), Enter to check the text with the
// dataset and apply it, and errors shown by a red border and in the status
// line. It applies every view change, its own and those other parts ask for
// with kit.SetViewMsg ("=", sort, clear, the grid's revert): it sets
// State.View, Columns and Total, broadcasts ViewChangedMsg, and counts the
// rows of a filtered view in the background (TotalMsg). Its own changes go
// out as a SetViewMsg too, so the grid sees every view change coming.
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
	"github.com/mjuric/pqx/go/internal/ui/chrome"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// Part is the filter's name for focus.
const Part = "filter"

// IsSQL reports whether text is a full query rather than a WHERE expression.
func IsSQL(text string) bool { return data.IsSQLQuery(text) }

// Filter is the filter bar. It implements kit.Pane, kit.Focusable,
// kit.Inputs, kit.Cursored and kit.Placed.
type Filter struct {
	env *kit.Env
	st  *kit.State

	in      textinput.Model
	focused bool
	sel     bool   // all the text is selected (on focus, Textual's select_on_focus)
	keepSel bool   // the next Focus doesn't select (the box gets focus back after a failure)
	err     string // why the last view failed, while the text is unchanged (red border)

	sugg  string    // the completion offered for the text (Python's Suggester)
	words []wordSQL // names and keywords to complete, made on first use

	history []string
	histPos int

	hint    string
	hintSet bool

	pending   *kit.SetViewMsg // the view being checked ("validate")
	own       *data.View      // a SetViewMsg the filter sent itself, on its way
	typed     bool            // the view on its way was typed (Enter): a failure focuses the box again
	typedView *data.View      // the view typed (Enter) until checked: still typed when the grid sends it again
	remember  string          // the filter "=" made, for the history once it applies
	mark      string          // the error to show once the view on its way applies (a revert)
	noted     bool            // DuckDB can't read the file: said once

	counts map[string]int64
	w      int // the input's width
	y      int // the row the root draws it on (kit.Placed)
}

// New makes the filter bar; opts.Where, if set, is applied at the first
// window size.
func New(env *kit.Env) *Filter {
	f := &Filter{env: env, st: env.State, counts: map[string]int64{}}
	f.in = newInput()
	f.hint = Placeholder(nil, nil)
	f.in.Placeholder = f.hint
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
	km.AcceptSuggestion = key.NewBinding(key.WithDisabled()) // completion is the filter's own (→)
	ti.KeyMap = km
	return ti
}

// Value is the text in the box.
func (f *Filter) Value() string { return f.in.Value() }

// Err is why the last view failed ("" if it didn't, or the text changed
// since): the box's border is red meanwhile.
func (f *Filter) Err() string { return f.err }

// BorderError tells the root to draw the box's border red.
func (f *Filter) BorderError() bool { return f.err != "" }

// History is the filters applied (typed or made by "="), oldest first.
func (f *Filter) History() []string { return f.history }

// Suggestion is the completion offered for the text, "" if none.
func (f *Filter) Suggestion() string { return f.sugg }

// Place implements kit.Placed.
func (f *Filter) Place(_, y int) { f.y = y }

// Pos is the row the bar was drawn on last.
func (f *Filter) Pos() int { return f.y }

// Focus implements kit.Focusable. The text is selected, as Textual's
// Input does on focus (typing replaces it), unless the box gets focus back
// after its filter failed (it never left it in Python).
func (f *Filter) Focus() tea.Cmd {
	f.focused = true
	f.in.CursorEnd()
	f.sel = f.in.Value() != "" && !f.keepSel
	f.keepSel = false
	f.sugg = "" // (Textual drops the suggestion on focus)
	return f.in.Focus()
}

// Blur implements kit.Focusable.
func (f *Filter) Blur() {
	f.focused = false
	f.sel = false
	f.in.Blur()
}

// TypingFocused implements kit.Inputs.
func (f *Filter) TypingFocused() bool { return f.focused }

// Keys implements kit.Pane (Python's KEYS["filter"]).
func (f *Filter) Keys() []kit.KeyHint {
	return []kit.KeyHint{{Key: "enter", Help: "apply"}, {Key: "esc", Help: "back"}, {Key: "ctrl+x", Help: "clear"},
		{Key: "↑↓", Help: "history"}, {Key: "→", Help: "complete"}, {Key: "select … from t", Help: "full query"}}
}

// prompt is "› ", or "sql › " while the text is a full query.
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

// View implements kit.Pane: the prompt and the input (or the hint), with
// the completion offered after the text, dim.
func (f *Filter) View(w, h int) string {
	look := f.env.Look
	p := f.prompt()
	avail := max(1, w-ansi.StringWidth(p)-1)
	if f.w != avail {
		f.w = avail
		f.in.SetWidth(avail)
	}
	v := f.in.Value()
	// the mode label in the accent colour, then a plain space (Python's
	// #filter-mode with margin-right 1)
	// ($primary: the accent, or a named theme's primary colour, which the
	// focused border has too)
	line := look.Render(styled.New(strings.TrimSuffix(p, " "), look.Style("border-focus"))) + " "
	switch {
	case fmtx.HasControls(v, false):
		// text set by another part (a value of the file, quoted) can hold
		// control characters: shown as symbols, never as they are
		line += ansi.Truncate(fmtx.Sanitize(v, false), avail, "…")
	case v == "":
		// the hint, faint whatever --dim says (Textual's placeholder style),
		// the rest of the line plain
		// faint whatever --dim says (Textual's placeholder style; a named
		// theme's Paint blends it), word-wrapped as Rich does, its first
		// line shown
		if h := firstLine(f.hint, max(0, w-ansi.StringWidth(p))); h != "" {
			line += "\x1b[2m" + h + "\x1b[m"
		}
	case f.sel && f.focused && ansi.StringWidth(v) < f.w:
		line += look.Render(styled.New(v, look.Style("selection")))
	case f.showSuggestion():
		// (the text fits: a suggestion is only shown then)
		rest := string([]rune(f.sugg)[len([]rune(v)):])
		line += v + look.Render(styled.New(ansi.Truncate(fmtx.Sanitize(rest, false), max(0, avail-ansi.StringWidth(v)), ""), look.Style("dim")))
	default:
		line += f.in.View()
	}
	line = fit(line, w)
	if h <= 1 {
		return line
	}
	return line + strings.Repeat("\n"+strings.Repeat(" ", w), h-1)
}

// showSuggestion reports whether the completion is drawn: the box has
// focus, the suggestion is longer than the text, and the text fits.
func (f *Filter) showSuggestion() bool {
	v := f.in.Value()
	return f.focused && f.sugg != "" && len([]rune(f.sugg)) > len([]rune(v)) && ansi.StringWidth(v) < f.w
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
				f.in.CursorEnd()
				cmds = append(cmds, f.send(f.viewFor(w), -1))
			}
		}
		return tea.Batch(cmds...)
	case tea.KeyPressMsg:
		return f.onKey(msg)
	case tea.PasteMsg:
		return f.edit(msg)
	case tea.MouseClickMsg:
		return nil // the root focuses the bar on a click
	case startCount:
		if sameView(msg.view, f.st.View) && f.st.Total < 0 && !f.env.Tasks.Running("count") {
			return f.count(msg.view)
		}
		return nil
	case kit.SetViewMsg:
		return f.onSetView(msg)
	case kit.CancelledMsg:
		for _, t := range msg.Tags {
			if t == "validate" && f.pending != nil {
				// Esc stopped the check: the box holds a filter not applied,
				// and says so (red border) until it is edited
				f.pending = nil
				if !sameView(f.viewFor(f.in.Value()), f.st.View) {
					f.err = "the query was cancelled"
				}
			}
		}
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

// send asks for view v through the root, as the other parts do, so the grid
// sees it coming.
func (f *Filter) send(v data.View, keep int64) tea.Cmd {
	f.own = &v
	return kit.Send(kit.SetViewMsg{View: v, KeepFileRow: keep})
}

// KeepRevert is the KeepFileRow of the grid's SetViewMsg going back to the
// previous view after a filter failed on its first read: no record is
// kept, and the box keeps the filter typed, marked as failed (Python keeps
// the text).
const KeepRevert = -2

// onSetView applies a view asked for: the box shows its text. A view "="
// made (a new filter, not one the filter sent itself, nor the grid's
// revert to the previous view) goes into the history, as Python pqx's
// _filter_value does.
func (f *Filter) onSetView(m kit.SetViewMsg) tea.Cmd {
	v := m.View
	own := f.own != nil && sameView(*f.own, v)
	f.own = nil
	if f.typedView != nil && sameView(*f.typedView, v) {
		// the typed view, sent again by the grid after one of its own
		// (a quick s): still the filter's own, still typed
		own, f.typed = true, true
	}
	f.remember = ""
	if !own {
		f.typed = false
		where := strings.TrimSpace(v.Where)
		if !v.IsSQL() && where != "" && where != strings.TrimSpace(f.st.View.Where) && m.KeepFileRow != KeepRevert {
			f.remember = where // (once it applies: one replaced meanwhile isn't kept)
		}
	}
	if m.KeepFileRow == KeepRevert {
		f.typed = false
		f.mark = "the query failed reading its first rows" // (the grid reports why)
		return f.apply(v, -1)
	}
	f.setText(viewText(v))
	return f.apply(v, m.KeepFileRow)
}

// addHistory adds text to the history unless it is the last entry.
func (f *Filter) addHistory(text string) {
	if text != "" && (len(f.history) == 0 || f.history[len(f.history)-1] != text) {
		f.history = append(f.history, text)
	}
	f.histPos = len(f.history)
}

// setText puts text in the box, the cursor at its end.
func (f *Filter) setText(s string) {
	f.in.SetValue(s)
	f.in.CursorEnd()
	f.changed()
}

// changed runs when the text changed: the error is gone (Python's
// Input.Changed) and the completion is looked up again.
func (f *Filter) changed() {
	f.err = ""
	f.sugg = f.suggest(f.in.Value())
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
	if f.sel {
		// the text is selected: typing or pasting replaces it, deleting
		// deletes it, moving ends the selection (Textual's Input)
		f.sel = false
		switch m := msg.(type) {
		case tea.PasteMsg:
			f.setText("")
		case tea.KeyPressMsg:
			switch m.String() {
			case "backspace", "delete", "ctrl+h", "ctrl+d", "ctrl+u", "ctrl+k", "ctrl+w", "alt+backspace", "alt+d", "alt+delete", "ctrl+backspace", "ctrl+delete":
				f.setText("")
				return nil
			case "left", "home":
				f.in.CursorStart()
				return nil
			case "right", "end":
				return nil // (the cursor is at the end)
			}
			if m.Text != "" && m.Mod&(tea.ModCtrl|tea.ModAlt) == 0 {
				f.setText("")
			}
		}
	}
	before := f.in.Value()
	var cmd tea.Cmd
	f.in, cmd = f.in.Update(msg)
	if f.in.Value() != before {
		f.changed()
	}
	return cmd
}

func (f *Filter) onKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "enter":
		f.sel = false
		text := strings.TrimSpace(f.in.Value())
		f.addHistory(text)
		f.typed = true
		v := f.viewFor(text)
		f.typedView = &v
		// focus leaves the box at once: keys typed while the query is
		// checked are commands, not text (it comes back if it fails)
		return tea.Sequence(kit.Send(kit.FocusMsg{Pane: f.body()}), f.send(v, -1))
	case "ctrl+x":
		f.sel = false
		if f.in.Value() != "" && f.st.View.Plain() {
			f.setText("") // only typed, never applied: just empty the box
			return nil
		}
		return f.ClearFilter()
	case "up", "down":
		f.sel = false
		if len(f.history) == 0 {
			return nil
		}
		if k.String() == "up" {
			f.histPos = max(0, f.histPos-1)
		} else {
			f.histPos = min(len(f.history), f.histPos+1)
		}
		if f.histPos < len(f.history) {
			f.setText(f.history[f.histPos])
		} else {
			f.setText("")
		}
		return nil
	case "right":
		if f.sugg != "" && f.in.Position() >= len([]rune(f.in.Value())) {
			f.setText(f.sugg)
			return nil
		}
	}
	return f.edit(k)
}

// ClearFilter is x and Ctrl+X (Python's action_clear_filter): back to the
// whole file, the sort dropped with the filter, the cursor on the same
// record (the one on its way, if it is being looked up); the box empties
// and the grid (or the tab's body) gets focus.
func (f *Filter) ClearFilter() tea.Cmd {
	if f.in.Value() == "" && f.st.View.Plain() {
		return nil
	}
	fr := f.st.FileRow
	if f.env.Grid != nil {
		fr = f.env.Grid.Record().FileRow
	}
	f.setText("")
	cmds := []tea.Cmd{kit.Send(kit.SetViewMsg{View: data.View{}, KeepFileRow: fr})}
	if f.focused {
		cmds = append(cmds, kit.Send(kit.FocusMsg{Pane: f.body()}))
	}
	return tea.Batch(cmds...)
}

type validated struct {
	req      kit.SetViewMsg
	typed    bool
	remember string
	mark     string
	cols     []data.Column
	err      error
}

type counted struct {
	view data.View
	n    int64
	secs float64
	err  error
}

// apply checks view with the dataset ("validate"), then shows it.
func (f *Filter) apply(v data.View, keep int64) tea.Cmd {
	typed, remember, mark := f.typed, f.remember, f.mark
	f.typed, f.remember, f.mark = false, "", ""
	if sameView(v, f.st.View) {
		f.pending = nil
		f.env.Tasks.Cancel("validate")
		if f.st.Total < 0 && !f.env.Tasks.Running("count") {
			return f.count(v) // its count was cancelled: count again
		}
		return nil
	}
	if err := f.env.DS.SetupErr(); err != nil && !v.Plain() {
		return f.setupFailed(err)
	}
	req := kit.SetViewMsg{View: v, KeepFileRow: keep}
	f.pending = &req
	ds := f.env.DS
	return f.env.Tasks.Run("validate", "checking the filter", true, func(ctx context.Context) tea.Msg {
		cols, err := ds.Validate(ctx, v)
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return validated{req: req, typed: typed, remember: remember, mark: mark, cols: cols, err: err}
	})
}

func (f *Filter) onValidated(r validated) tea.Cmd {
	f.pending = nil
	if f.typedView != nil && sameView(*f.typedView, r.req.View) {
		f.typedView = nil // checked: whatever comes next wasn't typed
	}
	if r.err != nil {
		if errors.Is(r.err, context.Canceled) {
			return nil
		}
		return f.fail(r.err, r.typed)
	}
	if r.remember != "" {
		f.addHistory(r.remember)
	}
	st := f.st
	v := r.req.View
	wasSQL, old := st.View.IsSQL(), st.View
	f.env.Tasks.Cancel("count") // the old view's: stale (a failure would land on the new one)
	st.View = v
	if v.IsSQL() || wasSQL || len(st.Columns) != len(r.cols) {
		st.Hidden = map[string]bool{}
	}
	if len(r.cols) > 0 {
		st.Columns = r.cols
	}
	cmds := []tea.Cmd{kit.Send(kit.ViewChangedMsg{})}
	sortOnly := !v.IsSQL() && !wasSQL && strings.TrimSpace(v.Where) == strings.TrimSpace(old.Where) && st.Total >= 0
	if v.Plain() {
		st.Total = f.env.DS.NumRows()
	} else if sortOnly {
		// only the sort changed: the count stands (Python's _sort_by keeps
		// its total), not counted nor timed again
	} else if n, ok := f.counts[countKey(v)]; ok {
		st.Total = n
		cmds = append(cmds, kit.Send(kit.TotalMsg{}))
	} else {
		st.Total = -1
		// counted after the view reaches the grid: its first rows start
		// first, as Python's _set_view loads the window before counting
		// (the status line names the oldest work: "Loading rows", then
		// "Counting rows")
		f.err = r.mark
		return tea.Sequence(kit.Send(kit.StatusMsg{}), tea.Batch(cmds...), kit.Send(startCount{v}))
	}
	f.err = r.mark
	return tea.Sequence(kit.Send(kit.StatusMsg{}), tea.Batch(cmds...))
}

// fail shows why a view can't be applied: the box's border turns red, the
// status line says why with a hint, and a toast has the whole message
// (Python's _show_error with mark_input). A typed filter gets the box
// back to edit it.
func (f *Filter) fail(err error, typed bool) tea.Cmd {
	f.err, _ = describe(errors.New(data.FullError(err)))
	cmds := []tea.Cmd{chrome.QueryError(err)}
	if typed {
		f.keepSel = true // (Python's box never lost focus: nothing is selected)
		cmds = append(cmds, kit.Send(kit.FocusMsg{Pane: Part}))
	}
	return tea.Batch(cmds...)
}

// setupFailed: DuckDB can't read the file, so no query can run; the status
// line says why and a toast says it once (Python's _show_error for the
// setup error).
func (f *Filter) setupFailed(err error) tea.Cmd {
	cmds := []tea.Cmd{kit.Send(kit.StatusMsg{Severity: kit.Error, Text: chrome.SetupReason(err)})}
	if !f.noted {
		f.noted = true
		cmds = append(cmds, kit.Send(kit.NotifyMsg{Severity: kit.Error, Title: "✗ DuckDB can't read this file",
			Text:    fmtx.Sanitize(trunc(strings.TrimSpace(err.Error()), 600), true) + "\n\nSchema and Metadata (from the footer) still work.",
			Timeout: 12 * time.Second}))
	}
	return tea.Batch(cmds...)
}

var (
	errPrefix  = regexp.MustCompile(`^(Binder|Parser|Catalog|Conversion|Invalid Input|Out of Range) Error:\s*`)
	notFound   = regexp.MustCompile(`Referenced column ("[^"]+") not found in FROM clause!?`)
	candidates = regexp.MustCompile(`Candidate bindings: "(?:[^".\n]+\.)?([^"\n]+)"`)
)

// describe is an error's first line, shortened as Python's _show_error
// does (chrome.QueryError builds the status line the same way), and a hint.
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
// startCount starts the count of view v once its ViewChangedMsg is out.
type startCount struct{ view data.View }

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
		return kit.Send(kit.NotifyMsg{Severity: kit.Error, Title: "✗ Count failed",
			Text: fmtx.Sanitize(trunc(r.err.Error(), 600), true), Timeout: 8 * time.Second}) // (Python's _show_error)
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

// firstLine is the first line of s word-wrapped at w cells, as Rich wraps
// Textual's placeholder (at the input's width plus one) and the input
// shows its first line, cut to w: no ellipsis, a word that doesn't fit
// left out but the space before it kept (one longer than the line is cut).
func firstLine(s string, w int) string {
	if w <= 0 {
		return ""
	}
	out := ""
	for _, word := range strings.Split(s, " ") {
		next := word
		if out != "" {
			next = out + " " + word
		}
		if ansi.StringWidth(next) > w+1 {
			if out == "" {
				return ansi.Truncate(word, w, "")
			}
			out += " " // (Rich keeps the space a line breaks at)
			break
		}
		out = next
	}
	return ansi.Truncate(out, w, "")
}
