package grid

// tests/test_viewport.py and the record-keeping parts of
// tests/test_detail_keys.py: a new view of the same columns keeps the
// leftmost column, and "=" (or the pane's "=", or clearing) keeps the
// cursor on its record, on its screen row; keys pressed while the record is
// looked up wait for it.

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// FindRow and FetchAround for the fake: the position of a file row in a
// "id % K = 0" view (sorted descending or not).
func (f *fakeDS) FindRow(ctx context.Context, v data.View, fr int64) (int64, bool, error) {
	if v.IsSQL() {
		return 0, false, nil
	}
	k, total, err := f.viewRows(v)
	if err != nil {
		return 0, false, err
	}
	if fr < 0 || fr >= f.rows || fr%k != 0 {
		return 0, false, nil
	}
	pos := fr / k
	if len(v.OrderBy) > 0 && v.OrderBy[0].Desc {
		pos = total - 1 - pos
	}
	return pos, true, nil
}

func (f *fakeDS) FetchAround(ctx context.Context, v data.View, fr, pos, start int64, n int, cols []string) (data.Window, error) {
	return f.Fetch(ctx, v, start, n, cols)
}

// hooked is a dataset whose FindRow, FetchAround and Fetch can be held by
// a test (Python's hold_find_row and the tests' patched fetches); calls
// held count for the harness's settle.
type hooked struct {
	data.Dataset
	held atomic.Int32

	mu          sync.Mutex
	holdFind    bool
	finds       []chan struct{} // one per FindRow call while holdFind
	findCalls   []int64
	aroundGate  chan struct{}
	aroundCalls [][2]int64 // (file row, position)
	fetchGate   func(v data.View, start int64) chan struct{}
	failPlain   bool
}

func (h *hooked) heldCalls() int { return int(h.held.Load()) }

func (h *hooked) wait(ctx context.Context, ch chan struct{}) error {
	if ch == nil {
		return nil
	}
	h.held.Add(1)
	defer h.held.Add(-1)
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *hooked) FindRow(ctx context.Context, v data.View, fr int64) (int64, bool, error) {
	h.mu.Lock()
	h.findCalls = append(h.findCalls, fr)
	var ch chan struct{}
	if h.holdFind {
		ch = make(chan struct{})
		h.finds = append(h.finds, ch)
	}
	h.mu.Unlock()
	if err := h.wait(ctx, ch); err != nil {
		return 0, false, err
	}
	return h.Dataset.FindRow(ctx, v, fr)
}

func (h *hooked) FetchAround(ctx context.Context, v data.View, fr, pos, start int64, n int, cols []string) (data.Window, error) {
	h.mu.Lock()
	h.aroundCalls = append(h.aroundCalls, [2]int64{fr, pos})
	ch := h.aroundGate
	h.mu.Unlock()
	if err := h.wait(ctx, ch); err != nil {
		return data.Window{}, err
	}
	return h.Dataset.FetchAround(ctx, v, fr, pos, start, n, cols)
}

func (h *hooked) Fetch(ctx context.Context, v data.View, start int64, n int, cols []string) (data.Window, error) {
	h.mu.Lock()
	gate, fail := h.fetchGate, h.failPlain
	h.mu.Unlock()
	if gate != nil {
		if err := h.wait(ctx, gate(v, start)); err != nil {
			return data.Window{}, err
		}
	}
	if fail && v.Plain() && n > 1 {
		return data.Window{}, errors.New("IO Error: no page")
	}
	return h.Dataset.Fetch(ctx, v, start, n, cols)
}

func (h *hooked) release(i int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	close(h.finds[i])
}

func (h *hooked) nFinds() int { h.mu.Lock(); defer h.mu.Unlock(); return len(h.finds) }

func hookFixture(t *testing.T, name string) *hooked {
	return &hooked{Dataset: openFixture(t, name)}
}

// demoTruth is columns of demo.parquet, all rows.
func demoTruth(t *testing.T, ds data.Dataset, cols ...string) map[string][]data.Value {
	t.Helper()
	w, err := ds.Fetch(context.Background(), data.View{}, 0, int(ds.NumRows()), cols)
	if err != nil {
		t.Fatal(err)
	}
	return w.Cols
}

// matching are the file rows whose value of col is v, in order.
func matching(vals []data.Value, v data.Value) []int64 {
	var out []int64
	for i, x := range vals {
		if x == v {
			out = append(out, int64(i))
		}
	}
	return out
}

// position is file row fr's position in the view of rows whose value is v.
func position(vals []data.Value, v data.Value, fr int64) int64 {
	n := int64(0)
	for _, x := range vals[:fr] {
		if x == v {
			n++
		}
	}
	return n
}

// leftmost is the leftmost scrollable column shown.
func (h *harness) leftmost() string {
	h.app.View()
	return h.g.cols[h.g.left].Name
}

// record is the file row under the cursor.
func (h *harness) record() int64 { return h.g.fileRowAt(h.g.curRow) }

// screenRow is the cursor's row on the screen.
func (h *harness) screenRow() int { return int(h.g.curRow - h.g.top) }

// place puts the cursor on (file row fr, column) of the plain view, the
// view scrolled so left is the leftmost column (or as near as the grid
// allows: it returns the one shown) and the cursor screenRow rows down.
func (h *harness) place(fr int64, column, left string, screenRow int) string {
	h.t.Helper()
	g := h.g
	h.send(kit.GotoMsg{Row: fr})
	h.settle()
	g.curCol = g.byName[column]
	g.left = g.byName[left]
	g.top = g.curRow - int64(screenRow)
	h.exec(g.moved())
	h.settle()
	l := h.leftmost()
	if h.record() != fr || h.screenRow() != screenRow || g.left <= g.pinned() || !g.cursorInView() {
		h.t.Fatalf("placed on file row %d at screen row %d, leftmost %q (cursor in view %v)", h.record(), h.screenRow(), l, g.cursorInView())
	}
	return l
}

func (h *harness) curColumn() string { return h.g.curName() }

const vpW, vpH = 100, 40 // narrow: the demo's 16 columns need scrolling

func TestEqualsKeepsTheLeftmostColumnAndScreenRow(t *testing.T) {
	ds := hookFixture(t, "demo")
	det := demoTruth(t, ds, "detector")["detector"]
	h := newHarness(t, ds, vpW, vpH)
	left := h.place(15_000, "detector", "psfFlux", 15)
	h.press("=")
	d := det[15_000].(int64)
	st := h.env.State
	if st.View.Where != "detector = "+strconv.FormatInt(d, 10) {
		t.Fatalf("where %q", st.View.Where)
	}
	if pos := position(det, d, 15_000); h.g.curRow != pos || pos < 15 {
		t.Errorf("row %d, want %d", h.g.curRow, pos)
	}
	if h.record() != 15_000 || h.curColumn() != "detector" || h.leftmost() != left || h.screenRow() != 15 {
		t.Errorf("record %d, column %q, leftmost %q, screen row %d", h.record(), h.curColumn(), h.leftmost(), h.screenRow())
	}
	h.press("x") // back to the plain view: still there, on the same record
	if !st.View.Plain() || h.g.curRow != 15_000 || h.leftmost() != left || h.screenRow() != 15 {
		t.Errorf("after x: view %+v, row %d, leftmost %q, screen row %d", st.View, h.g.curRow, h.leftmost(), h.screenRow())
	}
	if h.f.Value() != "" {
		t.Errorf("box %q after x", h.f.Value())
	}
}

// The details pane's "=" sends a SetViewMsg keeping the record (WP12);
// the grid, narrowed by the pane, keeps its viewport.
func TestEqualsFromThePaneKeepsTheViewport(t *testing.T) {
	ds := hookFixture(t, "demo")
	det := demoTruth(t, ds, "detector")["detector"]
	h := newHarness(t, ds, 150, vpH)
	h.send(kit.ToggleDetailMsg{})
	h.settle()
	before := h.g.cols[h.g.byName["detector"]-1].Name
	left := h.place(15_000, "detector", before, 15)
	where := "detector = " + strconv.FormatInt(det[15_000].(int64), 10)
	h.send(kit.SetViewMsg{View: data.View{Where: where}, KeepFileRow: 15_000})
	h.settle()
	if h.env.State.View.Where != where || h.record() != 15_000 || h.curColumn() != "detector" {
		t.Fatalf("view %+v, record %d, column %q", h.env.State.View, h.record(), h.curColumn())
	}
	if h.leftmost() != left || h.screenRow() != 15 {
		t.Errorf("leftmost %q (want %q), screen row %d", h.leftmost(), left, h.screenRow())
	}
	if h.f.Value() != where || h.f.History()[len(h.f.History())-1] != where {
		t.Errorf("box %q, history %q", h.f.Value(), h.f.History())
	}
}

func TestARecordNearTheTopOfTheViewShowsAtItsPosition(t *testing.T) {
	ds := hookFixture(t, "demo")
	det := demoTruth(t, ds, "detector")["detector"]
	fr := matching(det, det[15_000])[5] // the 6th record of its view
	h := newHarness(t, ds, vpW, vpH)
	left := h.place(fr, "detector", "psfFlux", 15)
	h.press("=")
	if h.record() != fr || h.g.curRow != 5 || h.screenRow() != 5 || h.g.top != 0 || h.leftmost() != left {
		t.Errorf("record %d row %d, screen row %d, top %d, leftmost %q", h.record(), h.g.curRow, h.screenRow(), h.g.top, h.leftmost())
	}
}

func TestARecordNearTheEndOfTheViewFillsTheScreen(t *testing.T) {
	ds := hookFixture(t, "demo")
	band := demoTruth(t, ds, "band")["band"]
	rows := matching(band, band[15_000])
	h := newHarness(t, ds, vpW, vpH)
	n := int64(h.g.bodyH())
	if int64(len(rows)) <= 2*2*n {
		t.Fatalf("only %d rows", len(rows))
	}
	fr := rows[len(rows)-3] // the third from the end of its view
	left := h.place(fr, "band", "ra", 15)
	h.press("=")
	if h.record() != fr || h.g.curRow != int64(len(rows)-3) || h.env.State.Total != int64(len(rows)) {
		t.Fatalf("record %d row %d total %d", h.record(), h.g.curRow, h.env.State.Total)
	}
	// not row 15: the screen ends at the view's end
	if h.screenRow() != h.g.bodyH()-3 || h.leftmost() != left {
		t.Errorf("screen row %d (body %d), leftmost %q", h.screenRow(), h.g.bodyH(), h.leftmost())
	}
	if len(ds.findCalls) != 1 || len(ds.aroundCalls) != 1 || ds.aroundCalls[0] != [2]int64{fr, int64(len(rows) - 3)} {
		t.Errorf("lookups %v, reads around %v", ds.findCalls, ds.aroundCalls)
	}
}

// The record is among the view's first rows read, but too near their end
// to show 15 rows down with a screen of rows below it: the rows below are
// read, and no lookup is needed.
func TestARecordNearTheFirstReadsEndIsShownAtItsScreenRow(t *testing.T) {
	ds := hookFixture(t, "demo")
	band := demoTruth(t, ds, "band")["band"]
	h := newHarness(t, ds, vpW, vpH)
	p := 2*h.g.bodyH() - 5
	fr := matching(band, band[15_000])[p]
	left := h.place(fr, "band", "ra", 15)
	h.press("=")
	if h.record() != fr || h.g.curRow != int64(p) || h.screenRow() != 15 || h.leftmost() != left {
		t.Errorf("record %d row %d (want %d), screen row %d, leftmost %q", h.record(), h.g.curRow, p, h.screenRow(), h.leftmost())
	}
	if len(ds.findCalls) != 0 {
		t.Errorf("looked up: %v", ds.findCalls)
	}
}

func TestSortAndClearKeepTheLeftmostColumn(t *testing.T) {
	ds := hookFixture(t, "demo")
	h := newHarness(t, ds, vpW, vpH)
	g := h.g
	left := h.place(15_000, "detector", "psfFlux", 15)
	h.press("s")
	st := h.env.State
	if len(st.View.OrderBy) != 1 || st.View.OrderBy[0] != (data.Sort{Column: "detector"}) || g.curRow != 0 {
		t.Fatalf("sorted: %+v, row %d", st.View, g.curRow)
	}
	if h.leftmost() != left || h.curColumn() != "detector" {
		t.Errorf("leftmost %q, column %q", h.leftmost(), h.curColumn())
	}
	g.curRow, g.top = 40, 40-12
	h.exec(g.moved())
	h.settle()
	fr := h.record()
	h.press("x") // the plain view, on the same record and screen row
	if !st.View.Plain() || g.curRow != fr || h.leftmost() != left || h.screenRow() != 12 {
		t.Errorf("after x: %+v row %d (want %d), leftmost %q, screen row %d", st.View, g.curRow, fr, h.leftmost(), h.screenRow())
	}
	h.filterWith("band = 'r'") // a typed filter: its top, same columns
	if st.View.Where != "band = 'r'" || g.curRow != 0 || h.leftmost() != left {
		t.Errorf("typed filter: %+v row %d leftmost %q", st.View, g.curRow, h.leftmost())
	}
}

func TestAQueryOfOtherColumnsStartsAtTheLeft(t *testing.T) {
	ds := hookFixture(t, "demo")
	h := newHarness(t, ds, vpW, vpH)
	left := h.place(15_000, "detector", "psfFlux", 15)
	h.filterWith("select ssObjectId, ra, dec, raErr, decErr, midpointMjdTai, band, psfFlux, " +
		"psfFluxErr, mag, snr, trailLength, isDipole, detector, ingestTime from t")
	g := h.g
	if !h.env.State.View.IsSQL() || g.cols[0].Name != "ssObjectId" {
		t.Fatalf("view %+v, first column %q", h.env.State.View, g.cols[0].Name)
	}
	// not kept: the view scrolls from the left only as far as the cursor's
	// column needs
	if _, last, _, _ := g.colWindow(); h.curColumn() != "detector" || last != g.curCol || h.leftmost() == left {
		t.Errorf("column %q, last shown %d (cursor %d), leftmost %q", h.curColumn(), last, g.curCol, h.leftmost())
	}
}

func TestPagingKeepsTheLeftmostColumn(t *testing.T) {
	ds := hookFixture(t, "demo")
	h := newHarness(t, ds, vpW, vpH)
	left := h.place(100, "mag", "psfFlux", 15)
	h.send(kit.GotoMsg{Row: 15_000})
	h.settle()
	if h.g.curRow != 15_000 || h.leftmost() != left {
		t.Errorf("row %d leftmost %q", h.g.curRow, h.leftmost())
	}
}

func TestARecordFoundLaterGetsItsScreenRow(t *testing.T) {
	ds := hookFixture(t, "demo")
	band := demoTruth(t, ds, "band")["band"]
	ds.holdFind = true
	h := newHarness(t, ds, vpW, vpH)
	left := h.place(15_000, "band", "ra", 15)
	h.send(kp("="))
	h.waitFor("the lookup", func() bool { return ds.nFinds() == 1 && h.env.State.View.Where != "" && h.g.v.loaded(0) })
	h.settle()
	// the first rows, at the top, while the lookup runs
	if h.g.curRow != 0 || h.leftmost() != left || !h.env.Tasks.Running("locate") {
		t.Fatalf("while looking: row %d leftmost %q", h.g.curRow, h.leftmost())
	}
	rec, ok := h.g.pendingRecord()
	if !ok || rec.FileRow != 15_000 || rec.Values["band"] != band[15_000] || rec.Row != 0 || h.g.Record().FileRow != 15_000 {
		t.Errorf("pending record %+v", rec)
	}
	ds.release(0)
	h.settle()
	pos := position(band, band[15_000], 15_000)
	if h.record() != 15_000 || h.g.curRow != pos || pos <= 2*int64(h.g.bodyH()) || h.screenRow() != 15 || h.leftmost() != left {
		t.Errorf("record %d row %d (want %d), screen row %d, leftmost %q", h.record(), h.g.curRow, pos, h.screenRow(), h.leftmost())
	}
	if _, ok := h.g.pendingRecord(); ok {
		t.Error("still pending")
	}
}

// pinnedPlace: three columns pinned, the cursor on a pinned one
// (ssObjectId), mag the leftmost scrollable.
func pinnedPlace(t *testing.T, h *harness) {
	g := h.g
	h.send(kit.GotoMsg{Row: 15_004})
	h.settle()
	g.curCol = g.byName["ra"]
	h.exec(g.moved())
	h.press("p")
	if g.pinned() != 3 {
		t.Fatalf("pinned %d", g.pinned())
	}
	g.curCol = g.byName["ssObjectId"]
	g.left = g.byName["mag"]
	h.exec(g.moved())
	h.settle()
	if h.leftmost() != "mag" {
		t.Fatalf("leftmost %q", h.leftmost())
	}
}

func TestACursorInAPinnedColumnKeepsTheLeftmostColumn(t *testing.T) {
	ds := hookFixture(t, "demo")
	h := newHarness(t, ds, vpW, vpH)
	pinnedPlace(t, h)
	for _, keys := range [][]string{{"down"}, {"pgdown"}, {"goto"}, {"up"}} {
		if keys[0] == "goto" {
			h.send(kit.GotoMsg{Row: 2000})
			h.settle()
		} else {
			h.press(keys...)
		}
		if h.leftmost() != "mag" || h.curColumn() != "ssObjectId" {
			t.Errorf("%v: leftmost %q, column %q", keys, h.leftmost(), h.curColumn())
		}
	}
	fr := h.record()
	h.press("=")
	st := h.env.State
	if !strings.HasPrefix(st.View.Where, "ssObjectId") || h.record() != fr || h.leftmost() != "mag" {
		t.Errorf("=: %+v record %d (want %d) leftmost %q", st.View, h.record(), fr, h.leftmost())
	}
	h.press("x")
	if !st.View.Plain() || h.g.curRow != fr || h.leftmost() != "mag" {
		t.Errorf("x: row %d leftmost %q", h.g.curRow, h.leftmost())
	}
	h.press("s")
	if len(st.View.OrderBy) != 1 || st.View.OrderBy[0].Column != "ssObjectId" || h.leftmost() != "mag" {
		t.Errorf("s: %+v leftmost %q", st.View, h.leftmost())
	}
}

// A plain view whose first read fails leaves nothing of the record kept
// for it behind to place a later jump (Python's _page_failed forgets the
// screen row).
func TestAFailedPageForgetsTheKeptRecord(t *testing.T) {
	ds := hookFixture(t, "demo")
	h := newHarness(t, ds, vpW, vpH)
	h.place(15_000, "detector", "psfFlux", 15)
	h.press("=")
	if h.screenRow() != 15 {
		t.Fatalf("screen row %d", h.screenRow())
	}
	ds.failPlain = true
	h.press("x") // the plain view's first read fails
	if h.g.kept != nil || h.g.next != nil || !h.env.State.View.Plain() || h.status.Severity != kit.Error {
		t.Errorf("after the failed page: kept %+v, next %+v, view %+v, status %+v", h.g.kept, h.g.next, h.env.State.View, h.status)
	}
	ds.failPlain = false
	h.send(kit.GotoMsg{Row: 5000})
	h.settle()
	if h.g.curRow != 5000 || h.screenRow() != h.g.bodyH()/2 {
		t.Errorf("goto: row %d, screen row %d", h.g.curRow, h.screenRow())
	}
}

func TestAnEmptyResultKeepsTheLeftmostColumn(t *testing.T) {
	ds := hookFixture(t, "demo")
	h := newHarness(t, ds, vpW, vpH)
	left := h.place(15_000, "detector", "psfFlux", 15)
	h.filterWith("band = 'nope'")
	if h.env.State.Total != 0 {
		t.Fatalf("total %d", h.env.State.Total)
	}
	h.filterWith("band = 'r'")
	if h.env.State.View.Where != "band = 'r'" || h.leftmost() != left {
		t.Errorf("%+v leftmost %q", h.env.State.View, h.leftmost())
	}
}

func TestHidingAColumnKeepsTheLeftmostColumn(t *testing.T) {
	ds := hookFixture(t, "demo")
	h := newHarness(t, ds, vpW, vpH)
	left := h.place(15_000, "mag", "psfFlux", 15)
	h.press("-") // mag: the leftmost stays
	if _, ok := h.g.byName["mag"]; ok || h.leftmost() != left {
		t.Errorf("leftmost %q", h.leftmost())
	}
	h.g.curCol = h.g.byName[left]
	h.exec(h.g.moved())
	h.settle()
	next := h.g.cols[h.g.byName[left]+1].Name
	h.press("-") // the leftmost one: the next one right of it is
	if h.leftmost() != next {
		t.Errorf("leftmost %q, want %q", h.leftmost(), next)
	}
}

// --- tests/test_detail_keys.py: = keeping the record, keys queued

func TestGridEqualsKeepsTheRecord(t *testing.T) {
	ds := hookFixture(t, "demo")
	h := newHarness(t, ds, 150, 42)
	for _, c := range []struct {
		fr  int64
		col string
	}{{12_345, "band"}, {40, "detector"}} { // the lookup, then the first rows
		h.send(kit.GotoMsg{Row: c.fr})
		h.settle()
		h.g.curCol = h.g.byName[c.col]
		h.exec(h.g.moved())
		h.settle()
		h.press("=")
		if !h.g.focused || h.record() != c.fr || h.curColumn() != c.col {
			t.Errorf("%d: record %d, column %q", c.fr, h.record(), h.curColumn())
		}
		h.press("x")
		if h.g.curRow != c.fr {
			t.Errorf("%d: after x row %d", c.fr, h.g.curRow)
		}
	}
}

// = on a NULL in a sorted view: IS NULL, the sort kept, the record found by
// its place in the filtered, sorted view.
func TestEqualsOnNullInASortedView(t *testing.T) {
	ds := hookFixture(t, "demo")
	tr := demoTruth(t, ds, "ssObjectId", "mag")
	h := newHarness(t, ds, 150, 42)
	h.g.curCol = h.g.byName["mag"]
	h.press("s")
	// a NULL ssObjectId well down the sorted view
	w, err := ds.Fetch(context.Background(), h.env.State.View, 600, 400, []string{"ssObjectId"})
	if err != nil {
		t.Fatal(err)
	}
	i := 0
	for w.Cols["ssObjectId"][i] != nil {
		i++
	}
	pos, fr := 600+int64(i), w.FileRows[i]
	h.send(kit.GotoMsg{Row: pos})
	h.settle()
	h.g.curCol = h.g.byName["ssObjectId"]
	h.exec(h.g.moved())
	h.settle()
	if h.record() != fr {
		t.Fatalf("record %d, want %d", h.record(), fr)
	}
	h.press("=")
	st := h.env.State
	if st.View.Where != "ssObjectId IS NULL" || len(st.View.OrderBy) != 1 || st.View.OrderBy[0].Column != "mag" {
		t.Fatalf("view %+v", st.View)
	}
	// its place among the NULLs sorted by mag (ties in file order)
	want := int64(0)
	m := tr["mag"][fr].(float32)
	for r, v := range tr["ssObjectId"] {
		if v != nil {
			continue
		}
		if x := tr["mag"][r].(float32); x < m || (x == m && int64(r) < fr) {
			want++
		}
	}
	if h.record() != fr || h.g.curRow != want {
		t.Errorf("record %d row %d, want %d", h.record(), h.g.curRow, want)
	}
}

func TestLookupDoesNotYankACursorMovedMeanwhile(t *testing.T) {
	ds := hookFixture(t, "demo")
	ds.holdFind = true
	h := newHarness(t, ds, 150, 42)
	h.send(kit.GotoMsg{Row: 15_000})
	h.settle()
	h.g.curCol = h.g.byName["band"]
	h.send(kp("="))
	h.waitFor("the lookup", func() bool { return ds.nFinds() == 1 && h.g.v.loaded(0) && h.env.State.View.Where != "" })
	h.settle()
	if h.g.curRow != 0 || !h.env.Tasks.BusyUser() {
		t.Fatalf("row %d while looking", h.g.curRow)
	}
	h.press("down", "down")
	ds.release(0)
	h.settle()
	if h.g.curRow != 2 || h.g.kept != nil {
		t.Errorf("row %d, kept %+v", h.g.curRow, h.g.kept)
	}
}

func TestLookupCancelledByEsc(t *testing.T) {
	ds := hookFixture(t, "demo")
	ds.holdFind = true // a lookup that runs until cancelled
	h := newHarness(t, ds, 150, 42)
	h.send(kit.GotoMsg{Row: 15_000})
	h.settle()
	h.g.curCol = h.g.byName["band"]
	h.press("d")
	h.send(kp("="))
	h.waitFor("the lookup", func() bool { return h.env.Tasks.Running("locate") })
	h.send(kp("y")) // waits for the record
	h.send(kp("esc"))
	h.settle()
	if h.g.curRow != 0 || h.env.Tasks.Busy() || h.env.State.View.Where == "" || !h.env.State.DetailOpen {
		t.Errorf("row %d busy %v view %+v pane open %v", h.g.curRow, h.env.Tasks.Busy(), h.env.State.View, h.env.State.DetailOpen)
	}
	if !h.noted("y not applied: finding the record was cancelled") || len(h.copies) != 0 {
		t.Errorf("notes %+v, copies %q", h.notes, h.copies)
	}
	h.press("esc") // nothing running: now it closes the pane
	if h.env.State.DetailOpen || !h.g.focused {
		t.Error("the second Esc didn't close the pane")
	}
}

func TestEqualsWithoutRowIDsGoesToTheTop(t *testing.T) {
	ds := hookFixture(t, "odd")
	h := newHarness(t, ds, 150, 42)
	h.send(kit.GotoMsg{Row: 600})
	h.settle()
	h.g.curCol = h.g.byName["weird name"]
	h.exec(h.g.moved())
	h.settle()
	h.press("=")
	if w := h.env.State.View.Where; !strings.HasPrefix(w, `"weird name" = `) || h.g.curRow != 0 || h.g.v.ids {
		t.Errorf("where %q, row %d", w, h.g.curRow)
	}
	if len(ds.findCalls) != 0 {
		t.Errorf("looked up %v", ds.findCalls)
	}
}

func TestPlainViewKeepsTheRecordAfterClearing(t *testing.T) {
	ds := hookFixture(t, "demo")
	h := newHarness(t, ds, 150, 42, hopts{where: "band = 'r'"})
	h.send(kit.GotoMsg{Row: 700})
	h.settle()
	h.g.curCol = h.g.byName["detector"]
	h.exec(h.g.moved())
	h.settle()
	fr := h.record()
	h.press("=")
	if h.record() != fr {
		t.Fatalf("record %d, want %d", h.record(), fr)
	}
	h.press("x")
	if !h.env.State.View.Plain() || h.g.curRow != fr {
		t.Errorf("view %+v row %d", h.env.State.View, h.g.curRow)
	}
}

// = on detector, then = and y before the record lands: they act on the
// record (its detector, not that of the row the cursor waits on).
func TestKeysWhileTheRecordIsOnItsWayActOnIt(t *testing.T) {
	ds := hookFixture(t, "demo")
	tr := demoTruth(t, ds, "band", "detector")
	ds.holdFind = true
	h := newHarness(t, ds, 150, 42)
	h.send(kit.GotoMsg{Row: 15_000})
	h.settle()
	h.g.curCol = h.g.byName["band"]
	h.exec(h.g.moved())
	h.settle()
	band, det := tr["band"][15_000].(string), tr["detector"][15_000].(int64)
	h.send(kp("="))
	h.waitFor("the lookup", func() bool { return ds.nFinds() == 1 && h.g.v.loaded(0) && h.env.State.View.Where != "" })
	h.settle()
	if h.g.curRow != 0 || h.record() == 15_000 {
		t.Fatalf("row %d record %d", h.g.curRow, h.record())
	}
	rec, _ := h.g.pendingRecord()
	if !rec.Pending || rec.Values["band"] != band || rec.Values["detector"] != det {
		t.Errorf("pending record %+v", rec)
	}
	h.press("right") // (a column move keeps the record coming)
	for h.curColumn() != "detector" {
		h.press("right")
	}
	h.send(kp("="))
	h.send(kp("y"))
	h.settle()
	if h.env.State.View.Where != "band = '"+band+"'" || len(h.copies) != 0 {
		t.Fatalf("not waiting: %+v, copies %q", h.env.State.View, h.copies)
	}
	ds.mu.Lock()
	ds.holdFind = false // (a lookup in the next view isn't held)
	ds.mu.Unlock()
	ds.release(0)
	h.settle()
	want := "(band = '" + band + "') and detector = " + strconv.FormatInt(det, 10)
	if h.env.State.View.Where != want {
		t.Errorf("where %q, want %q", h.env.State.View.Where, want)
	}
	if len(h.copies) != 1 || h.copies[0] != strconv.FormatInt(det, 10) {
		t.Errorf("copies %q", h.copies)
	}
	if h.record() != 15_000 || h.curColumn() != "detector" || h.g.kept != nil {
		t.Errorf("record %d column %q", h.record(), h.curColumn())
	}
	if len(h.f.History()) < 2 || h.f.History()[len(h.f.History())-1] != want {
		t.Errorf("history %q", h.f.History())
	}
}

func TestKeysWaitingForTheRecordAreDroppedWhenTheUserMoves(t *testing.T) {
	ds := hookFixture(t, "demo")
	ds.holdFind = true
	h := newHarness(t, ds, 150, 42)
	h.send(kit.GotoMsg{Row: 15_000})
	h.settle()
	h.g.curCol = h.g.byName["band"]
	h.send(kp("="))
	h.waitFor("the lookup", func() bool { return ds.nFinds() == 1 && h.g.v.loaded(0) && h.env.State.View.Where != "" })
	h.settle()
	h.press("y", "down") // y waits; down a row
	if h.g.kept != nil || h.g.curRow != 1 || len(h.copies) != 0 || h.env.Tasks.Running("locate") {
		t.Errorf("kept %+v row %d copies %q", h.g.kept, h.g.curRow, h.copies)
	}
	if !h.noted("y not applied: the cursor moved before the record was found") {
		t.Errorf("notes %+v", h.notes)
	}
	ds.release(0)
	h.settle()
	if h.g.curRow != 1 || len(h.copies) != 0 {
		t.Errorf("row %d copies %q", h.g.curRow, h.copies)
	}
}

func TestAJumpBeforeTheFirstPageDropsTheKeptRecord(t *testing.T) {
	ds := hookFixture(t, "demo")
	h := newHarness(t, ds, 150, 42)
	h.send(kit.GotoMsg{Row: 15_000})
	h.settle()
	h.g.curCol = h.g.byName["band"]
	release := make(chan struct{})
	var once sync.Once
	ds.mu.Lock()
	ds.fetchGate = func(v data.View, start int64) chan struct{} {
		var ch chan struct{}
		if v.Where != "" {
			once.Do(func() { ch = release }) // the filtered view's first read hangs (only it)
		}
		return ch
	}
	ds.mu.Unlock()
	h.press("=")
	if h.g.kept == nil {
		t.Fatal("no record kept")
	}
	h.send(kit.GotoMsg{Row: 100})
	h.settle()
	if h.g.kept != nil || h.g.curRow != 100 {
		t.Errorf("kept %+v row %d", h.g.kept, h.g.curRow)
	}
	close(release) // the first read comes too late (it was cancelled)
	h.settle()
	if h.g.kept != nil || h.g.curRow != 100 {
		t.Errorf("kept %+v row %d", h.g.kept, h.g.curRow)
	}
}

func TestAStaleLookupLeavesTheNewerOneRunning(t *testing.T) {
	ds := hookFixture(t, "demo")
	ds.holdFind = true
	h := newHarness(t, ds, 150, 42)
	h.send(kit.GotoMsg{Row: 15_000})
	h.settle()
	h.g.curCol = h.g.byName["band"]
	h.send(kp("="))
	h.waitFor("the lookup", func() bool { return ds.nFinds() == 1 })
	// another view keeping the same record
	h.send(kit.SetViewMsg{View: data.View{Where: "detector < 150"}, KeepFileRow: 15_000})
	h.waitFor("the second lookup", func() bool { return ds.nFinds() == 2 })
	ds.release(0) // the first lookup ends (cancelled) while the second runs
	h.settle()
	if !h.env.Tasks.Running("locate") || h.g.kept == nil {
		t.Fatal("the newer lookup stopped")
	}
	ds.release(1)
	h.settle()
	if h.record() != 15_000 || h.env.State.View.Where != "detector < 150" {
		t.Errorf("record %d view %+v", h.record(), h.env.State.View)
	}
}

func TestRecordRowsFoundByFileRowAndNotYankedAfterAMove(t *testing.T) {
	ds := hookFixture(t, "demo")
	ds.aroundGate = make(chan struct{})
	h := newHarness(t, ds, 150, 42)
	h.send(kit.GotoMsg{Row: 15_000})
	h.settle()
	h.g.curCol = h.g.byName["band"]
	h.send(kp("="))
	h.waitFor("the read around the record", func() bool { ds.mu.Lock(); defer ds.mu.Unlock(); return len(ds.aroundCalls) > 0 })
	h.settle()
	if ds.aroundCalls[0][0] != 15_000 || h.g.kept == nil || h.g.kept.phase != "seeking" {
		t.Fatalf("reads around %v, kept %+v", ds.aroundCalls, h.g.kept)
	}
	h.press("down")
	if h.g.kept != nil {
		t.Error("still kept after a move")
	}
	close(ds.aroundGate)
	h.settle()
	if h.g.curRow != 1 {
		t.Errorf("row %d: the record's rows came too late", h.g.curRow)
	}
	ds.aroundGate = nil
	h.g.curRow = 0
	h.exec(h.g.moved())
	h.press("x")
	h.send(kit.GotoMsg{Row: 15_000})
	h.settle()
	h.press("=")
	if h.record() != 15_000 || h.g.kept != nil {
		t.Errorf("record %d kept %+v", h.record(), h.g.kept)
	}
}

// = on a column not read yet for the cursor's row waits for it.
func TestEqualsOnAnUnloadedValue(t *testing.T) {
	ds := newFake(2000, 300)
	h := newHarness(t, ds, 150, 42)
	h.g.curRow = 5
	h.exec(h.g.moved())
	h.settle()
	last := h.g.cols[len(h.g.cols)-1].Name
	if _, ok := h.g.v.cell(last, 5); ok {
		t.Fatalf("%s is read already", last)
	}
	h.send(kp("end"))
	h.send(kp("=")) // before the column is read
	h.settle()
	want := last + " = " + strconv.FormatInt(truth(last, 5).(int64), 10)
	if h.env.State.View.Where != want || h.record() != 5 {
		t.Errorf("where %q (want %q), record %d", h.env.State.View.Where, want, h.record())
	}
}

// A plain-view landing replays the keys queued, and a view without file
// rows says they weren't applied.
func TestQueuedKeysInViewsThatDoOrDontKeepTheRecord(t *testing.T) {
	ds := newFake(5000, 6)
	h := newHarness(t, ds, 150, 42)
	h.filterWith("id % 3 = 0")
	h.send(kit.GotoMsg{Row: 400})
	h.settle()
	fr := h.record()
	h.send(kit.SetViewMsg{View: data.View{}, KeepFileRow: fr, Keys: []tea.KeyPressMsg{kp("y")}})
	h.settle()
	if h.g.curRow != fr || len(h.copies) != 1 || h.copies[0] != strconv.FormatInt(fr, 10) {
		t.Errorf("row %d (want %d), copies %q", h.g.curRow, fr, h.copies)
	}
	h.send(kit.SetViewMsg{View: data.View{SQL: "select id, name from t"}, KeepFileRow: fr, Keys: []tea.KeyPressMsg{kp("y")}})
	h.settle()
	if !h.noted("y not applied: the record isn't kept in this view") || h.g.curRow != 0 {
		t.Errorf("notes %+v row %d", h.notes, h.g.curRow)
	}
}

func TestEqualsOnASQLResultIsRefused(t *testing.T) {
	ds := newFake(100, 4)
	h := newHarness(t, ds, 150, 42)
	h.filterWith("select id, name from t")
	h.press("=")
	if !h.noted("= filtering works on the table, not on SQL results") || !h.env.State.View.IsSQL() {
		t.Errorf("notes %+v", h.notes)
	}
}
