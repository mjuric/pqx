package dialogs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/ui/app"
	"github.com/mjuric/pqx/go/internal/ui/chrome"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// fakeDS is a file whose Export records its arguments and writes a small
// file (Export is WP2's).
type fakeDS struct {
	data.Unimplemented
	path    string
	cols    []data.Column
	exports []exportCall
	fail    error
}

type exportCall struct {
	view data.View
	path string
	f    data.ExportFormat
	cols []string
}

func (f *fakeDS) Path() string           { return f.path }
func (f *fakeDS) NumRows() int64         { return 20_000 }
func (f *fakeDS) Columns() []data.Column { return f.cols }
func (f *fakeDS) Export(ctx context.Context, v data.View, path string, ef data.ExportFormat, cols []string) (int64, error) {
	f.exports = append(f.exports, exportCall{v, path, ef, cols})
	if f.fail != nil {
		return 0, f.fail
	}
	return 1234, os.WriteFile(path, []byte("PAR1"), 0o644)
}

var demoCols = []data.Column{
	{Name: "diaSourceId", Arrow: arrow.PrimitiveTypes.Int64},
	{Name: "ra", Arrow: arrow.PrimitiveTypes.Float64, Unit: "deg"},
	{Name: "band", Arrow: arrow.BinaryTypes.String},
	{Name: "mag", Arrow: arrow.PrimitiveTypes.Float64},
}

func setup(t *testing.T, path string, cols []data.Column) (*Factory, *kit.Env, *fakeDS) {
	t.Helper()
	ds := &fakeDS{path: path, cols: cols}
	env := &kit.Env{DS: ds, Look: app.BasicLook{}, Opts: kit.Options{Version: "0.1.0"},
		State: &kit.State{Total: 20_000, Columns: cols, Hidden: map[string]bool{}}, Tasks: kit.NewTasks()}
	f := New(env)
	env.Dialogs = f
	return f, env, ds
}

func keyMsg(s string) tea.KeyPressMsg {
	switch s {
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "ctrl+a":
		return tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl}
	case "ctrl+n":
		return tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl}
	case "ctrl+u":
		return tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
	}
	r := []rune(s)
	return tea.KeyPressMsg{Code: r[0], Text: s}
}

// press feeds keys to d and returns the messages the last one's command
// yields.
func press(d kit.Dialog, keys ...string) []tea.Msg {
	var out []tea.Msg
	for _, k := range keys {
		out = msgs(d.Update(keyMsg(k)))
	}
	return out
}

// typeText types s into d.
func typeText(d kit.Dialog, s string) {
	for _, r := range s {
		d.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// msgs runs cmd and returns the messages it yields, in order, going into
// batches and sequences (tea's sequenceMsg is unexported: any []tea.Cmd).
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

func view(d kit.Dialog, w, h int) string {
	dw, dh := d.Size(w, h)
	return ansi.Strip(d.View(dw, dh))
}

// flat is a dialog's text in one line: the borders dropped, the blanks
// collapsed.
func flat(v string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(v, "│", " ")), " ")
}

func closed(ms []tea.Msg) bool {
	return len(ms) > 0 && ms[0] == (kit.CloseDialogMsg{})
}

func notice(ms []tea.Msg) (kit.NotifyMsg, bool) {
	for _, m := range ms {
		if n, ok := m.(kit.NotifyMsg); ok {
			return n, true
		}
	}
	return kit.NotifyMsg{}, false
}

// The dialogs draw exactly their size, a border all round.
func checkBox(t *testing.T, d kit.Dialog, w, h int) {
	t.Helper()
	dw, dh := d.Size(w, h)
	lines := strings.Split(ansi.Strip(d.View(dw, dh)), "\n")
	if len(lines) != dh {
		t.Fatalf("%d lines, size says %d", len(lines), dh)
	}
	for i, l := range lines {
		if ansi.StringWidth(l) != dw {
			t.Fatalf("line %d is %d wide, not %d: %q", i, ansi.StringWidth(l), dw, l)
		}
	}
	if !strings.HasPrefix(lines[0], "┌") || !strings.HasPrefix(lines[dh-1], "└") {
		t.Fatalf("no border:\n%s", strings.Join(lines, "\n"))
	}
}

// tests/test_app.py::test_startup_and_navigation (the go-to part)
func TestGoto(t *testing.T) {
	f, _, _ := setup(t, "/x/demo.parquet", demoCols)
	d := f.Goto(20_000)
	checkBox(t, d, 150, 42)
	if v := view(d, 150, 42); !strings.Contains(v, "Go to row") || !strings.Contains(v, "1234 · 1.5M · 50% · -1 (last)   of 20,000") {
		t.Fatalf("%s", v)
	}
	typeText(d, "12345")
	ms := press(d, "enter")
	if !closed(ms) || len(ms) != 2 || ms[1] != (kit.GotoMsg{Row: 12_345}) {
		t.Fatalf("%v", ms)
	}
	if c := d.(kit.Cursored).Cursor(); c == nil || c.Position.Y != 5 {
		t.Fatalf("cursor %+v", c)
	}

	d = f.Goto(20_000)
	typeText(d, "-1")
	if ms := press(d, "enter"); ms[1] != (kit.GotoMsg{Row: 19_999}) {
		t.Fatalf("%v", ms)
	}
	// a bad spec: closes, says so (sanitized, not markup)
	d = f.Goto(20_000)
	typeText(d, "[b]x")
	ms = press(d, "enter")
	if n, ok := notice(ms); !closed(ms) || !ok || n.Severity != kit.Error || n.Text != "Not a row number: [b]x" {
		t.Fatalf("%v", ms)
	}
	// nothing typed, or Esc: just closes
	if ms := press(f.Goto(10), "enter"); !closed(ms) || len(ms) != 1 {
		t.Fatalf("%v", ms)
	}
	if ms := press(f.Goto(10), "esc"); !closed(ms) || len(ms) != 1 {
		t.Fatalf("%v", ms)
	}
	// while counting
	d = f.Goto(-1)
	if v := flat(view(d, 150, 42)); !strings.Contains(v, "-1 (last) (row count still being computed)") {
		t.Fatalf("%s", v)
	}
	typeText(d, "1.5M")
	if ms := press(d, "enter"); ms[1] != (kit.GotoMsg{Row: 1_500_000}) {
		t.Fatalf("%v", ms)
	}
}

// tests/test_app.py::test_column_formats and test_format_dialog_markup_and_kinds
// (the dialog's part), test_security.py::test_trailing_backslash_in_names_is_not_markup
func TestFormat(t *testing.T) {
	f, _, _ := setup(t, "/x/demo.parquet", demoCols)
	d := f.Format(demoCols[1], fmtx.Override{Digits: 4, Set: true}, 123.456)
	checkBox(t, d, 150, 42)
	v := view(d, 150, 42)
	if !strings.Contains(v, "Format of ra") || !strings.Contains(v, "│ 4 ") || !strings.Contains(v, "empty = automatic") {
		t.Fatalf("%s", v)
	}
	d.Update(keyMsg("backspace"))
	typeText(d, ".2e")
	ms := press(d, "enter")
	if !closed(ms) || ms[1] != (kit.FormatSetMsg{Column: "ra", Override: fmtx.ParseOverride(".2e")}) {
		t.Fatalf("%v", ms)
	}
	// a spec prefilled; emptied resets to automatic
	d = f.Format(demoCols[1], fmtx.Override{Spec: ".3e", Set: true}, 1.0)
	if v := view(d, 150, 42); !strings.Contains(v, "│ .3e ") {
		t.Fatalf("%s", v)
	}
	d.Update(keyMsg("ctrl+u"))
	if ms := press(d, "enter"); ms[1] != (kit.FormatSetMsg{Column: "ra"}) {
		t.Fatalf("%v", ms)
	}
	// hints by kind
	if v := view(f.Format(demoCols[2], fmtx.Override{}, "x"), 150, 42); !strings.Contains(v, ",d · x · >12 · empty = automatic") {
		t.Fatalf("%s", v)
	}
	ts := data.Column{Name: "ingestTime", Arrow: &arrow.TimestampType{Unit: arrow.Microsecond}}
	if fmtx.KindFor(ts.Name, ts.Arrow, "") == fmtx.KindTime {
		if v := view(f.Format(ts, fmtx.Override{}, nil), 150, 42); !strings.Contains(v, "%Y-%m-%d %H:%M · empty") {
			t.Fatalf("%s", v)
		}
	}
	// the name is text, not markup
	d = f.Format(data.Column{Name: "dir\\", Arrow: arrow.PrimitiveTypes.Float64}, fmtx.Override{}, 1.5)
	if v := view(d, 150, 42); !strings.Contains(v, "Format of dir\\ ") {
		t.Fatalf("%s", v)
	}
	d = f.Format(data.Column{Name: "a\x1b[31mb", Arrow: arrow.PrimitiveTypes.Float64}, fmtx.Override{}, 1.5)
	if v := d.View(d.Size(150, 42)); strings.Contains(v, "\x1b[31mb") {
		t.Fatalf("control characters reach the terminal: %q", v)
	}
}

// tests/test_app.py::test_column_formats, test_format_dialog_markup_and_kinds:
// refused formats stay in the dialog with the reason. fmtx.OverrideError
// is WP3's; until it lands this checks only what it can.
func TestFormatRefused(t *testing.T) {
	f, _, _ := setup(t, "/x/demo.parquet", demoCols)
	for _, tc := range []struct {
		col  data.Column
		text string
	}{{demoCols[1], ",d"}, {demoCols[1], "[/b]"}, {demoCols[1], "²"}, {demoCols[1], "100000000"},
		{demoCols[0], "4"}} {
		kind := fmtx.KindFor(tc.col.Name, tc.col.Arrow, tc.col.Unit)
		why := fmtx.OverrideError(fmtx.ParseOverride(tc.text), kind, 1.5)
		if why == "" {
			t.Logf("fmtx.OverrideError accepts %q for %s (starter): skipped", tc.text, kind)
			continue
		}
		d := f.Format(tc.col, fmtx.Override{}, 1.5)
		typeText(d, tc.text)
		if ms := press(d, "enter"); closed(ms) {
			t.Fatalf("%q accepted", tc.text)
		}
		// the inline error is the message, cut with "…" to the dialog's width
		v := view(d, 150, 42)
		i := strings.Index(v, "✗ ")
		if i < 0 {
			t.Fatalf("no error in\n%s", v)
		}
		shown, _, _ := strings.Cut(v[i+len("✗ "):], "\n")
		shown = strings.TrimRight(strings.TrimRight(shown, " │"), " ")
		if cut, ok := strings.CutSuffix(shown, "…"); ok {
			if cut == "" || !strings.HasPrefix(why, cut) {
				t.Fatalf("%q isn't the start of %q\n%s", shown, why, v)
			}
		} else if shown != why {
			t.Fatalf("%q, want %q\n%s", shown, why, v)
		}
	}
}

// tests/test_app.py::test_columns_detail_raw (the picker),
// test_security.py::test_markup_in_names_and_values_is_shown_as_text (the
// picker)
func TestColumns(t *testing.T) {
	cols := append([]data.Column{{Name: "[/]", Arrow: arrow.PrimitiveTypes.Int64}}, demoCols...)
	f, _, _ := setup(t, "/x/demo.parquet", cols)
	d := f.Columns(cols, map[string]bool{"diaSourceId": true}, "ra")
	checkBox(t, d, 150, 42)
	v := view(d, 150, 42)
	for _, want := range []string{"Visible columns  space toggles · Ctrl+A all · Ctrl+N none",
		"▐X▌ [/]  i64", "▐X▌ diaSourceId  i64", "Apply", "Cancel", "type to filter columns…"} {
		if !strings.Contains(v, want) {
			t.Fatalf("no %q in\n%s", want, v)
		}
	}
	// ctrl+a: all of them
	ms := press(d, "ctrl+a", "tab", "tab", "enter") // filter → list → Apply
	if !closed(ms) || !reflect.DeepEqual(ms[1], kit.ColumnsPickedMsg{Visible: []string{"[/]", "diaSourceId", "ra", "band", "mag"}}) {
		t.Fatalf("%v", ms)
	}
	// ctrl+n: none, refused
	d = f.Columns(cols, nil, "ra")
	ms = press(d, "ctrl+n", "tab", "tab", "enter")
	if n, ok := notice(ms); closed(ms) || !ok || n.Text != "Select at least one column" || n.Severity != kit.Warning {
		t.Fatalf("%v", ms)
	}
	// Esc cancels
	if ms := press(d, "esc"); !closed(ms) || len(ms) != 1 {
		t.Fatalf("%v", ms)
	}
	// the filter narrows the list; Enter moves to it; space toggles; the
	// choice outside the filter is kept
	d = f.Columns(cols, nil, "ra")
	typeText(d, "MA")
	if v := view(d, 150, 42); strings.Contains(v, "diaSourceId") || !strings.Contains(v, "▐X▌ mag") {
		t.Fatalf("%s", v)
	}
	press(d, "enter", "space") // to the list, unticks mag
	ms = press(d, "tab", "enter")
	if !reflect.DeepEqual(ms[1], kit.ColumnsPickedMsg{Visible: []string{"[/]", "diaSourceId", "ra", "band"}}) {
		t.Fatalf("%v", ms)
	}
	// landing on the current column; a click toggles the entry under it
	d = f.Columns(cols, nil, "band")
	press(d, "tab", "space") // band
	view(d, 150, 42)
	cd := d.(*columnsDialog)
	d.Update(tea.MouseClickMsg{X: 10, Y: cd.listY, Button: tea.MouseLeft}) // [/]
	ms = msgs(d.Update(tea.MouseClickMsg{X: cd.buttons[0][0], Y: cd.buttonY, Button: tea.MouseLeft}))
	if !reflect.DeepEqual(ms[1], kit.ColumnsPickedMsg{Visible: []string{"diaSourceId", "ra", "mag"}}) {
		t.Fatalf("%v", ms)
	}
}

// tests/test_app.py::test_export_dialog
func TestExport(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	f, env, ds := setup(t, "/data/demo.parquet", demoCols)
	env.State.View = data.View{Where: "band = 'y'"}
	env.State.Total = 3_000
	env.State.Hidden["ra"] = true
	d := f.Export()
	checkBox(t, d, 150, 42)
	v := view(d, 150, 42)
	for _, want := range []string{"Export current view", "where band = 'y' · 3,000 rows · 3 visible columns",
		"▐●▌ Parquet (zstd)", "▐●▌ CSV", "▐●▌ JSON (newline-delimited)",
		"▐X▌ Only the visible columns", "▐X▌ Overwrite if the file exists", "Export", "Cancel"} {
		if !strings.Contains(v, want) {
			t.Fatalf("no %q in\n%s", want, v)
		}
	}
	// the default path (checked as the input's value: a long temporary
	// directory, as on macOS, doesn't fit the input)
	if got := d.(*exportDialog).path.Value(); got != filepath.Join(dir, "demo.subset.parquet") {
		t.Fatalf("default path %q", got)
	}
	// CSV: the extension follows
	press(d, "tab", "down")
	if got := d.(*exportDialog).path.Value(); got != filepath.Join(dir, "demo.subset.csv") {
		t.Fatalf("%q", got)
	}
	ms := press(d, "tab", "tab", "tab", "enter") // the visible-columns box, overwrite, Export
	if !closed(ms) || len(ds.exports) != 1 {
		t.Fatalf("%v %v", ms, ds.exports)
	}
	call := ds.exports[0]
	if call.view.Where != "band = 'y'" || call.path != filepath.Join(dir, "demo.subset.csv") || call.f != data.ExportCSV ||
		!reflect.DeepEqual(call.cols, []string{"diaSourceId", "band", "mag"}) {
		t.Fatalf("%+v", call)
	}
	done, ok := ms[len(ms)-1].(kit.DoneMsg)
	n, _ := done.Msg.(kit.NotifyMsg)
	if !ok || done.Tag != "export" || !strings.HasPrefix(n.Text, "✓ Wrote ") || !strings.HasSuffix(n.Text, "\n→ "+call.path) ||
		n.Timeout.Seconds() != 8 {
		t.Fatalf("%+v", ms)
	}
	// it exists now: refused unless Overwrite is ticked
	d = f.Export()
	press(d, "tab", "down", "tab", "space") // CSV, all columns
	ms = press(d, "shift+tab", "shift+tab", "enter")
	if n, ok := notice(ms); closed(ms) || !ok || n.Severity != kit.Warning ||
		n.Text != filepath.Join(dir, "demo.subset.csv")+" exists — tick 'Overwrite' to replace it" {
		t.Fatalf("%v", ms)
	}
	press(d, "tab", "tab", "tab", "space") // Overwrite
	ms = press(d, "tab", "enter")
	if !closed(ms) || len(ds.exports) != 2 || ds.exports[1].cols != nil {
		t.Fatalf("%v %+v", ms, ds.exports)
	}
	// no name
	d = f.Export()
	d.Update(keyMsg("ctrl+u"))
	if n, ok := notice(press(d, "enter")); !ok || n.Text != "Enter a file name" {
		t.Fatalf("%v", n)
	}
	// an error is reported
	ds.fail = errors.New("IO Error: Cannot open file \"/nope/x.parquet\": No such file")
	d = f.Export()
	d.Update(keyMsg("ctrl+u"))
	typeText(d, "/nope/x.parquet")
	ms = press(d, "enter")
	done = ms[len(ms)-1].(kit.DoneMsg)
	if n := done.Msg.(kit.NotifyMsg); n.Severity != kit.Error || n.Title != "✗ Query failed" || !strings.Contains(n.Text, "Cannot open file") {
		t.Fatalf("%+v", n)
	}
	// Esc
	if ms := press(f.Export(), "esc"); !closed(ms) || len(ms) != 1 {
		t.Fatalf("%v", ms)
	}
}

// tests/test_security.py::test_export_path_with_brackets
func TestExportPathWithBrackets(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	f, _, ds := setup(t, filepath.Join(dir, "x[x=a:b].parquet"), []data.Column{{Name: "a"}})
	d := f.Export()
	ms := press(d, "enter")
	out := filepath.Join(dir, "x[x=a:b].subset.parquet")
	if !closed(ms) || len(ds.exports) != 1 || ds.exports[0].path != out {
		t.Fatalf("%v %+v", ms, ds.exports)
	}
	if n := ms[len(ms)-1].(kit.DoneMsg).Msg.(kit.NotifyMsg); !strings.Contains(n.Text, out) {
		t.Fatalf("%q", n.Text)
	}
	// it exists: the warning names it, brackets and all
	ms = press(f.Export(), "enter")
	if n, ok := notice(ms); !ok || !strings.HasPrefix(n.Text, out+" exists") {
		t.Fatalf("%v", ms)
	}
}

// tests/test_security.py::test_markup_in_names_and_values_is_shown_as_text
// (the export summary)
func TestExportSummaryShowsMarkupAsText(t *testing.T) {
	name := "[bold]mk[/] [@click=app.quit]x[/]"
	f, env, _ := setup(t, "/x/e.parquet", []data.Column{{Name: name}, {Name: "s\x1bx"}})
	env.State.View = data.View{Where: `"` + name + `" = '[/]'`, OrderBy: []data.Sort{{Column: name}}}
	v := flat(view(f.Export(), 150, 42))
	if !strings.Contains(v, "sorted by "+name) || !strings.Contains(v, "'[/]'") {
		t.Fatalf("%s", v)
	}
	env.State.View = data.View{SQL: "select 1"}
	env.State.Total = -1
	if v := view(f.Export(), 150, 42); !strings.Contains(v, "SQL result · row count pending · 2 visible columns") {
		t.Fatalf("%s", v)
	}
	env.State.View = data.View{}
	if v := view(f.Export(), 150, 42); !strings.Contains(v, "all rows · ") {
		t.Fatalf("%s", v)
	}
}

// tests/test_branding.py::test_titlebar_and_help_show_version (the help),
// test_app.py::test_columns_detail_raw (help closes with Esc)
func TestHelp(t *testing.T) {
	f, _, _ := setup(t, "/x/demo.parquet", demoCols)
	d := f.Help()
	checkBox(t, d, 150, 42)
	lines := strings.Split(view(d, 150, 42), "\n")
	head := lines[2]
	if !strings.Contains(head, "pqx 0.1.0") || !strings.Contains(head, "Written by Mario Juric") ||
		!strings.Contains(head, "https://github.com/mjuric/pqx") {
		t.Fatalf("%q", head)
	}
	if !strings.Contains(lines[len(lines)-3], "Esc to close") {
		t.Fatalf("%q", lines[len(lines)-3])
	}
	if !strings.Contains(Help, FilterExample) || strings.Contains(Help, "mag < 21") || strings.Contains(Help, "‵") {
		t.Fatal("help text")
	}
	all := strings.Join(lines, "\n")
	for _, want := range []string{"pqx — Parquet explorer", "Filtering and queries", "• a SQL WHERE expression — price > 100",
		"key", "action", "go to row — 1234, 1.5M, 50%, -1"} {
		if !strings.Contains(all, want) {
			t.Fatalf("no %q in\n%s", want, all)
		}
	}
	if strings.Contains(all, "**") || strings.Contains(all, "`") || strings.Contains(all, "|---") {
		t.Fatalf("markdown left:\n%s", all)
	}
	// scrolls: End shows the end
	press(d, "end")
	if all := view(d, 150, 42); !strings.Contains(all, "PQX_BORDER") {
		t.Fatalf("%s", all)
	}
	for _, k := range []string{"esc", "q", "?"} {
		if ms := press(f.Help(), k); !closed(ms) {
			t.Fatalf("%s didn't close it", k)
		}
	}
}

func TestMarkdownTable(t *testing.T) {
	f, env, _ := setup(t, "/x/demo.parquet", demoCols)
	_ = f
	lines := renderMarkdown(env.Look, "| key | action |\n|---|---|\n| **g** | go to a row far away in the file |\n", 24)
	var got []string
	for _, l := range lines {
		got = append(got, l.Plain)
	}
	want := []string{"key  action", "────────────────────────", "g    go to a row far", "     away in the file"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%q", got)
	}
}

// Every dialog leaves the screen behind it where it was: only its own
// rectangle changes (tests/test_app.py::test_a_dialog_does_not_shift_the_screen_behind_it).
func TestDialogsDoNotShiftTheScreen(t *testing.T) {
	f, env, _ := setup(t, "/x/demo.parquet", demoCols)
	parts := app.Parts{Chrome: chrome.New(env)}
	for _, p := range []*kit.Pane{&parts.Filter, &parts.Grid, &parts.Detail, &parts.Schema, &parts.Stats, &parts.Plot, &parts.Meta} {
		*p = fill{}
	}
	a := app.New(env, parts)
	a.Update(tea.WindowSizeMsg{Width: 150, Height: 42})
	screen := func() []string { // (the compositor drops trailing blanks)
		l := strings.Split(ansi.Strip(a.View().Content), "\n")
		for i := range l {
			l[i] = strings.TrimRight(l[i], " ")
		}
		return l
	}
	before := screen()
	for name, d := range map[string]kit.Dialog{"help": f.Help(), "goto": f.Goto(20_000), "columns": f.Columns(demoCols, nil, "ra"),
		"format": f.Format(demoCols[1], fmtx.Override{}, 1.5), "export": f.Export()} {
		a.Update(kit.OpenDialogMsg{Dialog: d})
		after := screen()
		dw, dh := d.Size(150, 42)
		x, y := (150-dw)/2, (42-dh)/2
		if len(after) != len(before) {
			t.Fatalf("%s: %d lines, was %d", name, len(after), len(before))
		}
		for i := range before {
			if i < y || i >= y+dh {
				if after[i] != before[i] {
					t.Fatalf("%s: line %d moved:\n%q\n%q", name, i, before[i], after[i])
				}
				continue
			}
			trim := func(s string) string { return strings.TrimRight(s, " ") }
			if trim(ansi.Cut(after[i], 0, x)) != trim(ansi.Cut(before[i], 0, x)) ||
				trim(ansi.Cut(after[i], x+dw, 150)) != trim(ansi.Cut(before[i], x+dw, 150)) {
				t.Fatalf("%s: line %d moved beside the dialog:\n%q\n%q", name, i, before[i], after[i])
			}
		}
		if !strings.HasPrefix(before[0], "  pqx 0.1.0  ·  demo.parquet") || after[0] != before[0] {
			t.Fatalf("%s: title bar %q", name, after[0])
		}
		a.Update(kit.CloseDialogMsg{})
	}
	if strings.Join(screen(), "\n") != strings.Join(before, "\n") {
		t.Fatal("screen not restored")
	}
}

// fill is a pane drawn as dots, so a shift would show.
type fill struct{}

func (fill) Update(tea.Msg) tea.Cmd { return nil }
func (fill) Keys() []kit.KeyHint    { return nil }
func (fill) View(w, h int) string {
	l := make([]string, h)
	for i := range l {
		l[i] = strings.Repeat(".:", w/2+1)[:w]
	}
	return strings.Join(l, "\n")
}
