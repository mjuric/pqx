package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/data"
)

// harness runs a Model the way Bubble Tea does: every command in its own
// goroutine, its message fed back to Update on the test goroutine.
type harness struct {
	t     *testing.T
	m     *Model
	ds    *fakeDS
	msgs  chan tea.Msg
	out   int // commands still running
	quits int
}

func newHarness(t *testing.T, ds *fakeDS, w, h int) *harness {
	t.Helper()
	m := New(ds)
	m.tick = func() tea.Cmd { return nil }
	hs := &harness{t: t, m: m, ds: ds, msgs: make(chan tea.Msg, 100)}
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

func (h *harness) send(msg tea.Msg) {
	_, cmd := h.m.Update(msg)
	h.exec(cmd)
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
		h.send(msg)
	}
}

// settle feeds messages back until no command is running, or until those
// still running (blocked fetches or counts) have been quiet for a while.
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
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "ctrl+x":
		return tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
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

// screen is the rendered screen without escape sequences.
func (h *harness) screen() string { return ansi.Strip(h.m.Render()) }

func (h *harness) line(i int) string { return strings.Split(h.screen(), "\n")[i] }

func TestFirstScreen(t *testing.T) {
	ds := newFake(1000, 4)
	h := newHarness(t, ds, 80, 20)
	calls := ds.calls()
	if len(calls) != 1 {
		t.Fatalf("want one fetch, got %d: %+v", len(calls), calls)
	}
	bodyH := h.m.bodyH()
	c := calls[0]
	if c.start != 0 || c.n != 2*bodyH || !c.view.Plain() {
		t.Errorf("first fetch = %+v, want rows [0, %d) of the plain view", c, 2*bodyH)
	}
	if strings.Join(c.cols, ",") != "id,name,c2,c3" {
		t.Errorf("columns fetched = %v", c.cols)
	}
	s := h.screen()
	for _, want := range []string{"test.parquet", "id", "BIGINT", "VARCHAR", "r0", "c3:5",
		"row 1 of 1,000", "/ filter", "q quit", "where"} {
		if !strings.Contains(s, want) {
			t.Errorf("screen lacks %q:\n%s", want, s)
		}
	}
	if lines := strings.Split(s, "\n"); len(lines) != 20 {
		t.Errorf("screen has %d lines, want 20", len(lines))
	}
	for i, l := range strings.Split(s, "\n") {
		if w := ansi.StringWidth(l); w > 80 {
			t.Errorf("line %d is %d cells wide: %q", i, w, l)
		}
	}
}

func TestCursorKeys(t *testing.T) {
	ds := newFake(1000, 6)
	h := newHarness(t, ds, 80, 20)
	m := h.m
	n := int64(m.bodyH())
	h.press("down", "down", "right")
	if m.curRow != 2 || m.curCol != 1 {
		t.Fatalf("cursor at (%d,%d), want (2,1)", m.curRow, m.curCol)
	}
	h.press("up", "up", "up", "left", "left")
	if m.curRow != 0 || m.curCol != 0 {
		t.Fatalf("cursor not clamped at the top-left: (%d,%d)", m.curRow, m.curCol)
	}
	h.press("pgdown")
	if m.curRow != n || m.top != n {
		t.Errorf("after PgDn: row %d top %d, want %d %d", m.curRow, m.top, n, n)
	}
	h.press("pgup")
	if m.curRow != 0 || m.top != 0 {
		t.Errorf("after PgUp: row %d top %d", m.curRow, m.top)
	}
	h.press("end")
	if m.curCol != 5 {
		t.Errorf("End: column %d, want 5", m.curCol)
	}
	h.press("home")
	if m.curCol != 0 {
		t.Errorf("Home: column %d, want 0", m.curCol)
	}
	h.press("ctrl+end")
	if m.curRow != 999 || m.top != 1000-n {
		t.Errorf("Ctrl+End: row %d top %d", m.curRow, m.top)
	}
	if !strings.Contains(h.screen(), "row 1,000 of 1,000") || !strings.Contains(h.screen(), "r999") {
		t.Errorf("last row not shown:\n%s", h.screen())
	}
	h.press("down")
	if m.curRow != 999 {
		t.Errorf("cursor moved past the last row: %d", m.curRow)
	}
	h.press("ctrl+home")
	if m.curRow != 0 || m.top != 0 {
		t.Errorf("Ctrl+Home: row %d top %d", m.curRow, m.top)
	}
	h.press("q")
	if h.quits != 1 {
		t.Errorf("q did not quit")
	}
	h.press("ctrl+c")
	if h.quits != 2 {
		t.Errorf("ctrl+c did not quit")
	}
}

func TestPgDnPastWindowFetches(t *testing.T) {
	ds := newFake(100000, 3)
	h := newHarness(t, ds, 80, 20)
	n := h.m.bodyH()
	before := len(ds.calls())
	h.press("pgdown") // rows [n, 2n) are cached already (the first fetch read 2n)
	// the new screen was loaded by the first fetch, but the margin below it
	// was not: PgDn past the loaded window starts a fetch
	if calls := ds.calls(); len(calls) != before+1 || calls[len(calls)-1].start+int64(calls[len(calls)-1].n) <= int64(2*n) {
		t.Errorf("PgDn past the loaded window: fetches %+v", calls[before:])
	}
	h.press("pgdown", "pgdown")
	calls := ds.calls()
	last := calls[len(calls)-1]
	top := h.m.top
	if last.start > top || last.start+int64(last.n) < top+int64(n) {
		t.Errorf("last fetch %+v doesn't cover the screen at row %d", last, top)
	}
	if !strings.Contains(h.screen(), "r"+itoa(top)) {
		t.Errorf("row %d not shown after PgDn:\n%s", top, h.screen())
	}
	if strings.Contains(h.line(bodyTop), "·") {
		t.Errorf("placeholders remain after the fetch: %q", h.line(bodyTop))
	}
}

func itoa(n int64) string { return strings.ReplaceAll(commas(n), ",", "") }

func TestSupersededFetchIsCancelled(t *testing.T) {
	ds := newFake(1_000_000, 3)
	ds.gate = make(chan struct{})
	h := newHarness(t, ds, 80, 20)
	if len(ds.calls()) != 1 || h.m.inflight == nil {
		t.Fatalf("want one fetch in flight, got %d", len(ds.calls()))
	}
	// cells not yet loaded show the placeholder
	if !strings.Contains(h.line(bodyTop), "·") {
		t.Errorf("no placeholder while loading: %q", h.line(bodyTop))
	}
	h.press("ctrl+end")
	h.waitFor("the first fetch to be cancelled", func() bool { return ds.cancels() == 1 })
	if len(ds.calls()) != 2 {
		t.Fatalf("want a second fetch, got %d", len(ds.calls()))
	}
	if c := ds.calls()[1]; c.start+int64(c.n) != 1_000_000 {
		t.Errorf("second fetch %+v doesn't reach the end", c)
	}
	close(ds.gate)
	h.waitFor("the second fetch", func() bool { return h.m.inflight == nil })
	h.settle()
	if !strings.Contains(h.screen(), "r999999") {
		t.Errorf("last row not shown:\n%s", h.screen())
	}
	if _, ok := h.m.v.cells["id"][0]; ok {
		t.Errorf("the cancelled fetch stored rows")
	}
}

func TestOldViewResultsAreDropped(t *testing.T) {
	ds := newFake(1000, 3)
	h := newHarness(t, ds, 80, 20)
	oldGen := h.m.v.gen
	ds.countGate = make(chan struct{})
	h.press("/")
	h.typeText("id % 10 = 0")
	h.press("enter")
	if h.m.v.gen == oldGen {
		t.Fatal("the view didn't change")
	}
	// a late result for the plain view must not land in the filtered one
	w, _ := ds.Fetch(t.Context(), data.View{}, 500, 5, []string{"id", "name"})
	h.send(fetchMsg{req: fetchReq{id: 999, gen: oldGen, start: 500, n: 5, cols: []string{"id", "name"}}, win: w})
	if _, ok := h.m.v.cells["name"][500]; ok {
		t.Error("a result for the old view was stored")
	}
	if got := h.m.v.cells["name"][1]; got != "r10" {
		t.Errorf("row 1 of the filtered view = %q, want r10", got)
	}
	close(ds.countGate)
	h.settle()
}

func TestBadFilterKeepsView(t *testing.T) {
	ds := newFake(1000, 3)
	h := newHarness(t, ds, 80, 20)
	gen := h.m.v.gen
	h.press("/")
	if h.m.focus != focusFilter {
		t.Fatal("/ didn't open the filter")
	}
	h.typeText("(id > 3")
	h.press("enter")
	if h.m.v.gen != gen || !h.m.v.view.Plain() {
		t.Error("a rejected filter changed the view")
	}
	if h.m.focus != focusFilter {
		t.Error("the filter bar closed after an error")
	}
	if s := h.screen(); !strings.Contains(s, "✗ unbalanced parentheses") {
		t.Errorf("no inline error:\n%s", s)
	}
	h.typeText(")")
	if h.m.filterErr != "" {
		t.Error("the error stays while typing")
	}
	h.press("esc")
	if h.m.focus != focusGrid || !h.m.v.view.Plain() {
		t.Error("esc didn't leave the filter bar")
	}
}

func TestGoodFilterSwitchesAndCounts(t *testing.T) {
	ds := newFake(1000, 3)
	ds.countGate = make(chan struct{})
	h := newHarness(t, ds, 80, 20)
	h.press("down", "down", "down")
	h.press("/")
	h.typeText("id % 10 = 0")
	h.press("enter")
	m := h.m
	if m.v.view.Where != "id % 10 = 0" || m.focus != focusGrid {
		t.Fatalf("filter not applied: %q focus %d", m.v.view.Where, m.focus)
	}
	if m.curRow != 0 {
		t.Errorf("cursor not at the top: %d", m.curRow)
	}
	if !m.counting {
		t.Fatal("no count running")
	}
	s := h.screen()
	for _, want := range []string{"counting…", "row 1 of ?", "where id % 10 = 0", "r10", "r20"} {
		if !strings.Contains(s, want) {
			t.Errorf("screen lacks %q:\n%s", want, s)
		}
	}
	// the grid works before the count arrives, as far as rows are loaded
	h.press("pgdown")
	if m.curRow == 0 {
		t.Error("PgDn didn't move before the count arrived")
	}
	close(ds.countGate)
	h.waitFor("the count", func() bool { return !m.counting })
	h.settle()
	if m.v.total != 100 {
		t.Errorf("total = %d, want 100", m.v.total)
	}
	s = h.screen()
	if !strings.Contains(s, "of 100") || !strings.Contains(s, "✓ 100 rows match") {
		t.Errorf("count not shown:\n%s", s)
	}
	h.press("ctrl+end")
	if m.curRow != 99 || !strings.Contains(h.screen(), "r990") {
		t.Errorf("Ctrl+End in the filtered view: row %d", m.curRow)
	}
	// row labels are file row numbers
	if !strings.Contains(h.screen(), "990 ") {
		t.Errorf("file row number not shown:\n%s", h.screen())
	}
	// history: up recalls the filter
	h.press("/")
	h.send(kp("ctrl+x"))
	h.settle()
	if !m.v.view.Plain() || m.focus != focusGrid {
		t.Error("ctrl+x in the filter bar didn't clear the filter")
	}
	h.press("/", "up")
	if m.filter.Value() != "id % 10 = 0" {
		t.Errorf("history: %q", m.filter.Value())
	}
	h.press("down")
	if m.filter.Value() != "" {
		t.Errorf("history down: %q", m.filter.Value())
	}
	h.press("esc")
}

func TestEscCancelsCount(t *testing.T) {
	ds := newFake(1000, 3)
	ds.countGate = make(chan struct{})
	h := newHarness(t, ds, 80, 20)
	h.press("/")
	h.typeText("id % 7 = 0")
	h.press("enter")
	if !h.m.counting {
		t.Fatal("no count running")
	}
	h.press("esc")
	h.waitFor("the count to be cancelled", func() bool { return ds.countCancels() == 1 })
	h.settle()
	if h.m.counting || h.m.v.total != -1 {
		t.Errorf("counting=%v total=%d after Esc", h.m.counting, h.m.v.total)
	}
	s := h.screen()
	if strings.Contains(s, "counting…") || !strings.Contains(s, "count cancelled") {
		t.Errorf("status after Esc:\n%s", s)
	}
	if !h.m.v.view.Plain() && h.m.v.view.Where != "id % 7 = 0" {
		t.Error("Esc changed the view")
	}
}

func TestFilterReadErrorReverts(t *testing.T) {
	ds := newFake(1000, 3)
	h := newHarness(t, ds, 80, 20)
	h.press("down", "down")
	h.press("/")
	h.typeText("bad")
	h.press("enter")
	h.settle()
	m := h.m
	if !m.v.view.Plain() {
		t.Errorf("a filter whose read failed was kept: %q", m.v.view.Where)
	}
	if m.curRow != 2 {
		t.Errorf("cursor not restored: %d", m.curRow)
	}
	if m.focus != focusFilter || m.filter.Value() != "bad" {
		t.Errorf("filter bar not reopened with the filter: focus %d %q", m.focus, m.filter.Value())
	}
	if s := h.screen(); !strings.Contains(s, `✗ Binder Error`) {
		t.Errorf("no error shown:\n%s", s)
	}
}

func TestGoTo(t *testing.T) {
	ds := newFake(1000, 3)
	h := newHarness(t, ds, 80, 20)
	for _, c := range []struct {
		spec string
		row  int64
	}{{"50%", 500}, {"-1", 999}, {"1.5k", 999}, {"12", 12}, {"0", 0}} {
		h.press("g")
		if h.m.focus != focusGoto {
			t.Fatal("g didn't open the prompt")
		}
		h.typeText(c.spec)
		if !strings.Contains(h.screen(), "go to row: "+c.spec) {
			t.Errorf("prompt not shown:\n%s", h.screen())
		}
		h.press("enter")
		if h.m.curRow != c.row {
			t.Errorf("g %s: row %d, want %d", c.spec, h.m.curRow, c.row)
		}
		if !strings.Contains(h.screen(), "r"+itoa(c.row)) {
			t.Errorf("g %s: row not shown", c.spec)
		}
	}
	h.press("g")
	h.typeText("abc")
	h.press("enter")
	if h.m.msgKind != msgErr || !strings.Contains(h.screen(), "✗ not a row number: abc") {
		t.Errorf("bad row spec: %q", h.m.msg)
	}
	h.press("g")
	h.typeText("5")
	h.press("esc")
	if h.m.focus != focusGrid || h.m.curRow != 0 {
		t.Errorf("esc in the prompt: focus %d row %d", h.m.focus, h.m.curRow)
	}
}

func TestMouse(t *testing.T) {
	ds := newFake(1000, 4)
	h := newHarness(t, ds, 80, 20)
	h.send(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	h.settle()
	if h.m.top != 3 || h.m.curRow != 3 {
		t.Errorf("wheel down: top %d row %d, want 3 3", h.m.top, h.m.curRow)
	}
	h.send(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	h.settle()
	if h.m.top != 0 {
		t.Errorf("wheel up: top %d", h.m.top)
	}
	// click on the second body row, in the third column
	slots := h.m.layout()
	x := 2 + slots[2].x + 1
	h.send(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: bodyTop + 1})
	h.settle()
	if h.m.curRow != 1 || h.m.curCol != 2 {
		t.Errorf("click: cursor (%d,%d), want (1,2)", h.m.curRow, h.m.curCol)
	}
	h.send(tea.MouseClickMsg{Button: tea.MouseLeft, X: 10, Y: 1})
	if h.m.focus != focusFilter {
		t.Error("a click on the filter bar didn't open it")
	}
}

func TestHorizontalScroll(t *testing.T) {
	ds := newFake(1000, 300)
	h := newHarness(t, ds, 100, 20)
	first := ds.calls()[0]
	if len(first.cols) >= 300 || len(first.cols) < 5 {
		t.Errorf("first fetch asked for %d columns, want those on screen", len(first.cols))
	}
	if !strings.Contains(h.line(gridTop+1), "›") || strings.Contains(h.line(gridTop+1), "‹") {
		t.Errorf("edge markers at the left: %q", h.line(gridTop+1))
	}
	h.press("end")
	if h.m.left == 0 {
		t.Fatal("End didn't scroll right")
	}
	calls := ds.calls()
	last := calls[len(calls)-1]
	if last.start != first.start || last.n != first.n || last.cols[len(last.cols)-1] != "c299" {
		t.Errorf("columns scrolled in weren't fetched for the rows shown: %+v", last)
	}
	hdr := h.line(gridTop + 1)
	if !strings.Contains(hdr, "‹") || strings.Contains(hdr, "›") || !strings.Contains(hdr, "c299") {
		t.Errorf("header after End: %q", hdr)
	}
	if !strings.Contains(h.line(bodyTop), "c299:0") {
		t.Errorf("first row after End: %q", h.line(bodyTop))
	}
}

func TestViewSmallGrid(t *testing.T) {
	ds := newFake(5, 3)
	h := newHarness(t, ds, 60, 14)
	lines := strings.Split(h.screen(), "\n")
	want := []string{
		"╭──────────────────────────────────────────────────────────╮",
		"│ where / to filter: SQL WHERE expression, e.g. id > 0 an… │",
		"╰──────────────────────────────────────────────────────────╯",
		"╭─ test.parquet ───────────────────────────────────────────╮",
		"│        id  name         c2                               │",
		"│    BIGINT  VARCHAR  DOUBLE                               │",
		"│ 0       0  r0         c2:0                               │",
		"│ 1       1  r1         c2:1                               │",
		"│ 2       2  r2         c2:2                               │",
		"│ 3       3  r3         c2:3                               │",
		"│ 4       4  r4         c2:4                               │",
		"╰──────────────────────────────────────────────────────────╯",
		"test.parquet  ·  row 1 of 5",
	}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("line %d:\n got %q\nwant %q", i, lines[i], w)
		}
	}
	if !strings.HasPrefix(lines[13], "/ filter   x clear   g go to   esc cancel   q quit") {
		t.Errorf("key bar: %q", lines[13])
	}
	// the cursor cell is in reverse video, numbers right aligned
	raw := strings.Split(h.m.Render(), "\n")[bodyTop]
	if !strings.Contains(raw, sgrReverse+"      0 "+sgrNoRev) {
		t.Errorf("cursor cell not reversed: %q", raw)
	}
	// secondary text is faint
	if !strings.Contains(strings.Split(h.m.Render(), "\n")[gridTop+2], sgrFaint+"BIGINT") {
		t.Error("types aren't faint")
	}
}

func TestTruncation(t *testing.T) {
	if got := fit("abcdefghij", 5); got != "abcd…" {
		t.Errorf("fit ascii: %q", got)
	}
	if got := fit("äbcdefghij", 5); got != "äbcd…" {
		t.Errorf("fit unicode: %q", got)
	}
	if got := fit("日本語のテキスト", 5); ansi.StringWidth(got) > 5 || !strings.HasSuffix(got, "…") {
		t.Errorf("fit wide: %q", got)
	}
	if got := fit("abc", 5); got != "abc" {
		t.Errorf("fit short: %q", got)
	}
	ds := newFake(3, 3)
	h := newHarness(t, ds, 60, 12)
	h.m.v.cells["name"][0] = strings.Repeat("x", 40)
	h.m.v.colW[1] = maxColWidth
	if !strings.Contains(h.screen(), strings.Repeat("x", maxColWidth-1)+"…") {
		t.Errorf("long cell not truncated:\n%s", h.screen())
	}
}

func TestCommas(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567", -1234: "-1,234"} {
		if got := commas(n); got != want {
			t.Errorf("commas(%d) = %q, want %q", n, got, want)
		}
	}
}
