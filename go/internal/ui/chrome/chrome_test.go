package chrome

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/ui/app"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

type fakeDS struct {
	data.Unimplemented
	path     string
	rows     int64
	setupErr error
}

func (f fakeDS) Path() string    { return f.path }
func (f fakeDS) NumRows() int64  { return f.rows }
func (f fakeDS) SetupErr() error { return f.setupErr }
func (f fakeDS) Columns() []data.Column {
	return []data.Column{{Name: "a"}, {Name: "b"}, {Name: "mag"}}
}
func (f fakeDS) RowGroups() []int64  { return []int64{f.rows / 2, f.rows - f.rows/2} }
func (f fakeDS) Info() data.FileInfo { return data.FileInfo{Size: 1 << 20} }

// clock is a settable clock.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func setup(ds fakeDS) (*Chrome, *kit.Env, *clock) {
	env := &kit.Env{DS: ds, Look: app.BasicLook{}, Opts: kit.Options{Version: "0.1.0"},
		State: &kit.State{Total: ds.rows, Hidden: map[string]bool{}}, Tasks: kit.NewTasks()}
	c := New(env)
	clk := &clock{time.Now()} // (tasks start by the real clock)
	c.now = clk.now
	return c, env, clk
}

func demo() fakeDS { return fakeDS{path: "/data/demo.parquet", rows: 20_000} }

func plain(s string) string { return ansi.Strip(s) }

// msgs runs cmd and returns the messages it yields, in order, going into
// batches and sequences (tea's sequenceMsg is unexported: any []tea.Cmd).
// Don't give it ticks: they sleep.
func msgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	m := cmd()
	if m == nil {
		return nil
	}
	if v := reflect.ValueOf(m); v.Kind() == reflect.Slice && v.Type().Elem() == reflect.TypeFor[tea.Cmd]() {
		var out []tea.Msg
		for i := 0; i < v.Len(); i++ {
			out = append(out, msgs(v.Index(i).Interface().(tea.Cmd))...)
		}
		return out
	}
	return []tea.Msg{m}
}

// tests/test_branding.py::test_titlebar_and_help_show_version (the title
// bar's part)
func TestTitleBarShowsVersionDimmed(t *testing.T) {
	c, _, _ := setup(demo())
	bar := c.TitleBar(150)
	if got := plain(bar); !strings.HasPrefix(got, " pqx 0.1.0  ·  demo.parquet  ·  20,000 rows  ·  3 columns  ·  ") ||
		!strings.HasSuffix(got, "  ·  2 row groups") {
		t.Fatalf("title %q", got)
	}
	// the version is dimmed: an SGR "faint" (2) just before it
	i := strings.Index(bar, "0.1.0")
	if i < 0 || !strings.Contains(bar[max(0, i-12):i], "2m") {
		t.Fatalf("version not dim: %q", bar)
	}
}

// tests/test_branding.py::test_titlebar_degrades_gracefully
func TestTitleBarDegradesGracefully(t *testing.T) {
	const long = "0.2.1.dev123+g1a2b3c4d.d20261007"
	name := "yellow_tripdata_2024-01.parquet"
	c, env, _ := setup(fakeDS{path: "/x/" + name, rows: 2})
	env.Opts.Version = long
	if got := plain(c.TitleBar(150)); !strings.HasPrefix(got, " pqx "+long+"  ·  "+name+"  ·  2 rows") {
		t.Fatalf("150: %q", got)
	}
	// Python's widths are the bar's (the screen's minus 4); here the bar
	// is the screen's minus 2
	for _, tc := range []struct {
		width  int
		prefix string
	}{{78, "pqx " + long + "  ·  " + name}, {58, "pqx  ·  " + name + "  ·  "}, {38, "pqx  ·  yellow_tripdata"}} {
		line := strings.TrimPrefix(plain(c.TitleBar(tc.width)), " ")
		if !strings.HasPrefix(line, tc.prefix) || !strings.HasSuffix(line, "…") || ansi.StringWidth(line) > tc.width-2 {
			t.Errorf("%d: %q", tc.width, line)
		}
		if strings.Contains(line, "\n") {
			t.Errorf("%d: more than one line", tc.width)
		}
	}
}

// tests/test_security.py::test_widgets_show_no_control_characters (the
// title bar)
func TestTitleBarSanitizesName(t *testing.T) {
	c, _, _ := setup(fakeDS{path: "/x/f\x1b]0;FN\x07.parquet", rows: 2})
	bar := plain(c.TitleBar(150))
	if !strings.Contains(bar, "␛") || strings.ContainsAny(bar, "\x1b\x07") {
		t.Fatalf("%q", bar)
	}
}

func TestStatusCounts(t *testing.T) {
	c, env, clk := setup(demo())
	st := env.State
	if got := plain(c.StatusLine(150)); got != "✓ 20,000 rows  ·  row 0" {
		t.Fatalf("%q", got)
	}
	st.Row = 1234
	st.Raw = true
	if got := plain(c.StatusLine(150)); got != "✓ 20,000 rows  ·  raw values  ·  row 1,234" {
		t.Fatalf("%q", got)
	}
	st.Raw = false
	// a filter, sorted, counted in 1.5 s
	st.View = data.View{Where: "mag < 19", OrderBy: []data.Sort{{Column: "mag", Desc: true}}}
	st.Total = -1
	c.Update(kit.ViewChangedMsg{})
	clk.t = clk.t.Add(1500 * time.Millisecond)
	st.Total = 1234
	c.Update(kit.TotalMsg{})
	got := plain(c.StatusLine(150))
	if !strings.HasPrefix(got, "✓ 1,234 rows  ·  ") || !strings.Contains(got, " of ") ||
		!strings.Contains(got, "  ·  sorted mag ↓  ·  1.50 s  ·  row 1,234") {
		t.Fatalf("%q", got)
	}
	// a SQL result
	st.View = data.View{SQL: "select 1 from t"}
	st.Total = 6
	c.Update(kit.ViewChangedMsg{})
	if got := plain(c.StatusLine(150)); got != "✓ 6 rows  ·  SQL result  ·  0.00 s  ·  row 1,234" {
		t.Fatalf("%q", got)
	}
	// nothing matches
	st.Total = 0
	if got := plain(c.StatusLine(150)); got != "! No matching rows   → x clears the filter" {
		t.Fatalf("%q", got)
	}
	// cut with an ellipsis
	st.Total = 20_000
	if got := plain(c.StatusLine(10)); ansi.StringWidth(got) != 10 || !strings.HasSuffix(got, "…") {
		t.Fatalf("%q", got)
	}
}

// tests/test_app.py::test_filter_sql_and_errors and test_error_hint_and_look
// (the status line)
func TestStatusQueryError(t *testing.T) {
	c, env, _ := setup(demo())
	err := errors.New("Binder Error: Referenced column \"magg\" not found in FROM clause!\n" +
		"Candidate bindings: \"t.mag\", \"t.a\"\nLINE 1: ...")
	var notes []kit.NotifyMsg
	for _, m := range msgs(QueryError(err)) {
		if n, ok := m.(kit.NotifyMsg); ok {
			notes = append(notes, n)
		}
		c.Update(m)
	}
	got := plain(c.StatusLine(200))
	if !strings.HasPrefix(got, "✗ Query failed   reason: ") || !strings.Contains(got, `unknown column "magg"`) ||
		!strings.Contains(got, `→ did you mean "mag"?  ·  previous view kept`) {
		t.Fatalf("%q", got)
	}
	if len(notes) != 1 || notes[0].Title != "✗ Query failed" || !strings.Contains(notes[0].Text, "Candidate bindings") {
		t.Fatalf("notes %+v", notes)
	}
	// without a candidate: "edit with /"
	c.Update(kit.StatusMsg{Severity: kit.Error, Text: "syntax error at end of input"})
	if got := plain(c.StatusLine(200)); !strings.HasSuffix(got, "→ edit with /  ·  previous view kept") {
		t.Fatalf("%q", got)
	}
	// the error outranks a running task, and goes with the next view
	env.Tasks.Run("count", "counting rows", false, func(context.Context) tea.Msg { return nil })
	if got := plain(c.StatusLine(200)); !strings.HasPrefix(got, "✗ Query failed") {
		t.Fatalf("%q", got)
	}
	c.Update(kit.ViewChangedMsg{})
	if got := plain(c.StatusLine(200)); !strings.HasPrefix(got, "⠋ Counting rows") {
		t.Fatalf("%q", got)
	}
}

// tests/test_security.py::test_duckdb_error_with_markup_is_shown_as_text
func TestErrorMarkupShownAsText(t *testing.T) {
	c, _, _ := setup(demo())
	text := "[@click=app.quit]CLICK ME[/]"
	err := errors.New("Conversion Error: Could not convert string '" + text + "' to INT32")
	for _, m := range msgs(QueryError(err)) {
		c.Update(m)
	}
	if got := plain(c.StatusLine(200)); !strings.Contains(got, text) {
		t.Fatalf("%q", got)
	}
	found := false
	for _, o := range c.Toasts(160, 40) {
		found = found || strings.Contains(plain(o.Content), "'"+text+"'")
	}
	if !found {
		t.Fatal("the toast doesn't show the message literally")
	}
}

func TestStatusSetupError(t *testing.T) {
	ds := demo()
	ds.setupErr = errors.New("IO Error: Failed to read Parquet file '/x/demo.parquet': bad magic\nmore")
	c, _, _ := setup(ds)
	if got := plain(c.StatusLine(200)); !strings.HasPrefix(got, "✓") {
		t.Fatalf("before any error: %q", got)
	}
	c.Update(kit.StatusMsg{Severity: kit.Error, Text: "whatever"})
	want := "✗ DuckDB can't read this file   reason: bad magic   → Schema (2) and Metadata (5) still work"
	if got := plain(c.StatusLine(200)); got != want {
		t.Fatalf("%q", got)
	}
}

func TestStatusBusyAndSpinner(t *testing.T) {
	c, env, clk := setup(demo())
	env.State.Total = -1
	env.Tasks.Run("stats", "profiling \x1bra", true, func(context.Context) tea.Msg { return nil })
	cmd := c.Update(kit.CursorMsg{}) // any message while busy starts the ticks
	if cmd == nil || !c.Ticking() {
		t.Fatal("no tick while busy")
	}
	if got := plain(c.StatusLine(100)); got != "⠋ Profiling ␛ra" {
		t.Fatalf("%q", got)
	}
	if c.Update(kit.CursorMsg{}) != nil {
		t.Fatal("a second ticker started")
	}
	c.Update(tickMsg{})
	c.Update(tickMsg{})
	clk.t = env.Tasks.List()[0].Started.Add(65 * time.Second)
	env.State.Total = 20_000
	if got := plain(c.StatusLine(100)); got != "⠹ Profiling ␛ra   20,000 rows  ·  01:05 elapsed" {
		t.Fatalf("%q", got)
	}
	// idle: the tick that arrives doesn't schedule another
	env.Tasks.CancelAll()
	if cmd := c.Update(tickMsg{}); cmd != nil || c.Ticking() {
		t.Fatal("still ticking when idle")
	}
	if got := plain(c.StatusLine(100)); !strings.HasPrefix(got, "✓ 20,000 rows") {
		t.Fatalf("%q", got)
	}
}

// tests/test_app.py::test_linked_columns_hidden (the status line)
func TestStatusHiddenColumnHint(t *testing.T) {
	c, env, _ := setup(demo())
	st := env.State
	st.Hidden["b"] = true
	st.Current = "b"
	c.Update(kit.ColumnChangedMsg{From: "schema"})
	if got := plain(c.StatusLine(200)); !strings.HasSuffix(got, "   ! b is hidden · c to show") {
		t.Fatalf("%q", got)
	}
	c.Update(kit.CursorMsg{})
	if got := plain(c.StatusLine(200)); strings.Contains(got, "hidden") {
		t.Fatalf("%q", got)
	}
}

func TestKeyBar(t *testing.T) {
	c, _, _ := setup(demo())
	// a tab's keys get the global ones
	got := plain(c.KeyBar(150, []kit.KeyHint{{Key: "↑↓", Help: "column"}, {Key: "enter", Help: "stats"}, {Key: "/", Help: "filter"}}))
	if got != " ↑↓ column   enter stats   / filter   1-5 tabs   ? help   q quit" {
		t.Fatalf("%q", got)
	}
	// placed by the part: not repeated
	data := []kit.KeyHint{{Key: "/", Help: "filter"}, {Key: "x", Help: "clear filter"}, {Key: "1-5", Help: "tabs"},
		{Key: "?", Help: "help"}, {Key: "q", Help: "quit"}, {Key: "s", Help: "sort"}, {Key: "=", Help: "match cell"},
		{Key: "d", Help: "detail"}, {Key: "c", Help: "columns"}, {Key: "g", Help: "go to"}, {Key: "e", Help: "export"},
		{Key: "< > F", Help: "format"}}
	want := " / filter   x clear filter   1-5 tabs   ? help   q quit   s sort   = match cell   d detail   c columns   g go to   e export   < > F format"
	if got := plain(c.KeyBar(150, data)); got != want {
		t.Fatalf("%q", got)
	}
	// the filter's keys stand alone
	filter := []kit.KeyHint{{Key: "enter", Help: "apply"}, {Key: "esc", Help: "back"}}
	if got := plain(c.KeyBar(150, filter)); got != " enter apply   esc back" {
		t.Fatalf("%q", got)
	}
	// keys bold, help dim
	if bar := c.KeyBar(150, filter); !strings.Contains(bar, "1m") || !strings.Contains(bar, "2m") {
		t.Fatalf("%q", bar)
	}
}

// tests/test_detail_keys.py::test_detail_key_line_fits_80_columns
func TestKeyBarDetailFits80(t *testing.T) {
	c, _, _ := setup(demo())
	detail := []kit.KeyHint{{Key: "↑↓", Help: "column"}, {Key: "=", Help: "match"}, {Key: "y", Help: "copy"},
		{Key: "i", Help: "stats"}, {Key: "esc", Help: "close"}, {Key: "?", Help: "help"}, {Key: "q", Help: "quit"},
		{Key: "tab", Help: "grid"}}
	line := plain(c.KeyBar(80, detail))
	if ansi.StringWidth(line) > 80 || !strings.Contains(line, "q quit") || !strings.Contains(line, "= match") ||
		!strings.Contains(line, "esc close") {
		t.Fatalf("%q", line)
	}
	// what doesn't fit goes by whole words (Python's key bar wraps and shows
	// its first line)
	if got := plain(c.KeyBar(20, detail)); got != " ↑↓ column   =" { // (the key is a word too)
		t.Fatalf("%q", got)
	}
	if got := plain(c.KeyBar(21, detail)); got != " ↑↓ column   = match" {
		t.Fatalf("%q", got)
	}
}

func TestToasts(t *testing.T) {
	c, _, _ := setup(demo())
	if c.Toasts(100, 30) != nil {
		t.Fatal("toasts before any notice")
	}
	if c.Update(kit.NotifyMsg{Text: "✓ Wrote 1.2k rows · 3 MiB · 0.4 s\n→ /tmp/x.parquet"}) == nil {
		t.Fatal("no expiry scheduled")
	}
	c.Update(kit.NotifyMsg{Severity: kit.Error, Title: "✗ Query failed",
		Text: "bad thing happened with a fairly long message that should wrap around the toast width"})
	ts := c.Toasts(100, 30)
	if len(ts) != 2 {
		t.Fatalf("%d toasts", len(ts))
	}
	old, cur := ts[0], ts[1]
	ol, cl := strings.Split(plain(old.Content), "\n"), strings.Split(plain(cur.Content), "\n")
	// the newest at the bottom, just above the key bar; one row between
	if cur.Y+len(cl) != 29 || old.Y+len(ol) != cur.Y-1 {
		t.Fatalf("rows: old %d+%d new %d+%d", old.Y, len(ol), cur.Y, len(cl))
	}
	// 48 wide (half the screen less its padding and a scroll bar's gutter),
	// its right edge 4 cells from the screen's (Textual's ToastRack)
	for _, l := range append(ol, cl...) {
		if ansi.StringWidth(l) != 48 {
			t.Fatalf("width %d: %q", ansi.StringWidth(l), l)
		}
	}
	if cur.X != 100-3-48 || !strings.Contains(cl[1], "✗ Query failed") || !strings.Contains(ol[2], "→ /tmp/x.parquet") {
		t.Fatalf("%d\n%s\n%s", cur.X, strings.Join(ol, "\n"), strings.Join(cl, "\n"))
	}
	if len(cl) != 6 { // border, title, three lines of text, border
		t.Fatalf("%q", cl)
	}
	// the error's border is red (ANSI colour 1)
	if top := strings.Split(cur.Content, "\n")[0]; !strings.Contains(top, "31m") && !strings.Contains(top, "38;5;1m") {
		t.Fatalf("%q", cur.Content)
	}
	// expiry
	c.Update(expireMsg{1})
	if ts := c.Toasts(100, 30); len(ts) != 1 || !strings.Contains(plain(ts[0].Content), "Query failed") {
		t.Fatalf("%v", ts)
	}
	// a task's notice
	c.Update(kit.DoneMsg{Tag: "export", Msg: kit.NotifyMsg{Text: "done"}})
	if ts := c.Toasts(100, 30); len(ts) != 2 {
		t.Fatalf("%d toasts", len(ts))
	}
	// those that don't fit the height are left out, oldest first
	for i := 0; i < 10; i++ {
		c.Update(kit.NotifyMsg{Text: "n"})
	}
	ts = c.Toasts(100, 12) // three of 3 rows, with a row between and the key bar's
	if len(ts) != 3 || ts[0].Y != 0 {
		t.Fatalf("%d toasts", len(ts))
	}
}

func TestToastTimeouts(t *testing.T) {
	for _, tc := range []struct {
		m    kit.NotifyMsg
		want time.Duration
	}{{kit.NotifyMsg{Text: "a"}, 3 * time.Second}, {kit.NotifyMsg{Severity: kit.Error, Text: "a"}, 8 * time.Second},
		{kit.NotifyMsg{Severity: kit.Error, Text: "a", Timeout: 2 * time.Second}, 2 * time.Second}} {
		if got := timeout(tc.m); got != tc.want {
			t.Errorf("%+v: %v", tc.m, got)
		}
	}
}

func TestCommas(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567", -1234: "-1,234"} {
		if got := Commas(n); got != want {
			t.Errorf("%d: %q", n, got)
		}
	}
}

// Toast text wraps as Rich wraps it: a word that doesn't fit goes to the
// next line, one longer than a line is folded, leading spaces stay.
func TestToastWrap(t *testing.T) {
	got := wrap("✓ Wrote 23 rows\n→ /a/very/long/path/name.csv\n    ^", 12)
	want := []string{"✓ Wrote 23", "rows", "→", "/a/very/long", "/path/name.c", "sv", "    ^"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("%q", got)
	}
}
