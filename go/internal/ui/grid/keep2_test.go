package grid

// Cases from the independent review of WP11: "=" on doubles DuckDB would
// read as decimals, filters "=" must not widen, keys behind a refused "=",
// "=" pressed twice quickly, Esc while "=" is checked, and the record
// keeping's corners (the rows read around it, failures, the messages).

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/sqllit"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

func TestEqualsOnASixteenDigitDouble(t *testing.T) {
	ds := openFixture(t, "demo")
	vals := demoTruth(t, ds, "trailLength")["trailLength"]
	if vals[37] != 1.9101520992509673 {
		t.Fatalf("trailLength at 37 is %v", vals[37])
	}
	h := newHarness(t, ds, 150, 42)
	h.send(kit.GotoMsg{Row: 37})
	h.settle()
	h.g.curCol = h.g.byName["trailLength"]
	h.exec(h.g.moved())
	h.settle()
	h.press("=")
	if h.env.State.View.Where != "trailLength = 1.9101520992509673e0" || h.env.State.Total != 1 || h.record() != 37 {
		t.Errorf("where %q total %d record %d", h.env.State.View.Where, h.env.State.Total, h.record())
	}
}

// "=" onto a filter with OR not between spaces, or ending in a comment,
// narrows it rather than widening it or being commented out.
func TestEqualsDoesNotWidenOrLoseTheFilter(t *testing.T) {
	ds := openFixture(t, "demo")
	vals := demoTruth(t, ds, "detector", "band")
	for _, f := range []string{"band = 'r' -- note", "band = 'r' or(band = 'g')", "(band = 'r')or(band = 'g')", "band = 'r' OR(band='g')"} {
		h := newHarness(t, ds, 150, 42)
		h.filterWith(f)
		if h.env.State.View.Where != f {
			t.Fatalf("%q not applied: %q", f, h.f.Err())
		}
		h.g.curCol = h.g.byName["detector"]
		h.exec(h.g.moved())
		h.settle()
		fr := h.record()
		h.press("=")
		d := vals["detector"][fr]
		want := int64(0)
		for i := range vals["detector"] {
			b := vals["band"][i]
			if vals["detector"][i] == d && (b == "r" || (!strings.Contains(f, "--") && b == "g")) {
				want++
			}
		}
		if h.env.State.Total != want || h.record() != fr {
			t.Errorf("%q then =: %q has %d rows, want %d (record %d, want %d)", f, h.env.State.View.Where, h.env.State.Total, want, h.record(), fr)
		}
	}
}

// A replayed "=" that is refused leaves nothing waiting: the keys after it
// act at once, and nothing fires on a later view change.
func TestARefusedReplayedEqualsLeavesNothingBehind(t *testing.T) {
	ds := hookFixture(t, "types")
	h := newHarness(t, ds, 200, 42)
	h.send(kit.GotoMsg{Row: 3}) // bin and i64 are set there
	h.settle()
	h.g.curCol = h.g.byName["i64"]
	h.exec(h.g.moved())
	h.settle()
	release := make(chan struct{})
	var once sync.Once
	ds.mu.Lock()
	ds.fetchGate = func(v data.View, start int64) chan struct{} {
		var ch chan struct{}
		if v.Where != "" {
			once.Do(func() { ch = release })
		}
		return ch
	}
	ds.mu.Unlock()
	h.press("=")
	if h.g.kept == nil {
		t.Fatal("no record kept")
	}
	for h.curColumn() != "bin" {
		h.press("right")
	}
	h.press("=", "y")
	close(release)
	h.settle()
	if h.record() != 3 || !h.noted("Can't filter on this value type") || len(h.copies) != 1 {
		t.Fatalf("record %d, copies %q, notes %+v", h.record(), h.copies, h.notes)
	}
	h.press("x")
	if len(h.copies) != 1 || h.g.curRow != 3 {
		t.Errorf("after x: copies %q row %d", h.copies, h.g.curRow)
	}
}

// slowCheck is a dataset whose Validate waits for gate.
type slowCheck struct {
	*hooked
	gate chan struct{}
}

func (s *slowCheck) Validate(ctx context.Context, v data.View) ([]data.Column, error) {
	if err := s.hooked.wait(ctx, s.gate); err != nil {
		return nil, err
	}
	return s.hooked.Validate(ctx, v)
}

// "=" twice before the first view is checked: the second adds to the first
// (Python adds to the box), and only the view applied goes into the history.
func TestEqualsTwiceQuickly(t *testing.T) {
	ds := &slowCheck{hooked: hookFixture(t, "demo")}
	tr := demoTruth(t, ds, "band", "psfFlux")
	h := newHarness(t, ds, 150, 42)
	h.g.curCol = h.g.byName["band"]
	h.exec(h.g.moved())
	h.settle()
	ds.gate = make(chan struct{})
	h.press("=", "right", "=")
	close(ds.gate)
	h.settle()
	first := "band = '" + tr["band"][0].(string) + "'"
	cond, _ := sqllit.Equals("psfFlux", tr["psfFlux"][0])
	want := "(" + first + ") and " + cond
	if w := h.env.State.View.Where; w != want || h.record() != 0 {
		t.Errorf("where %q, want %q", w, want)
	}
	if hist := h.f.History(); len(hist) != 1 || hist[0] != want {
		t.Errorf("history %q", hist)
	}
}

// Esc while the view "=" made is checked: the box shows the filter not
// applied, and says so with its red border.
func TestEscWhileEqualsIsCheckedMarksTheBox(t *testing.T) {
	ds := &slowCheck{hooked: hookFixture(t, "demo")}
	h := newHarness(t, ds, 150, 42)
	h.g.curCol = h.g.byName["band"]
	h.exec(h.g.moved())
	h.settle()
	ds.gate = make(chan struct{})
	h.press("=")
	h.press("esc")
	if !h.env.State.View.Plain() || h.f.Value() == "" || !h.f.BorderError() {
		t.Errorf("view %+v box %q border %v", h.env.State.View, h.f.Value(), h.f.BorderError())
	}
}

// Keys waiting for a record go on waiting when a new view keeps the same
// record, and are dropped (with a notice) when it keeps another.
func TestQueuedKeysFollowTheRecordIntoANewView(t *testing.T) {
	for _, same := range []bool{true, false} {
		ds := hookFixture(t, "demo")
		tr := demoTruth(t, ds, "band", "detector")
		other := int64(15_001)
		for tr["detector"][other].(int64) >= 150 {
			other++
		}
		ds.holdFind = true
		h := newHarness(t, ds, 150, 42)
		h.send(kit.GotoMsg{Row: 15_000})
		h.settle()
		h.g.curCol = h.g.byName["band"]
		h.exec(h.g.moved())
		h.settle()
		h.send(kp("="))
		h.waitFor("the lookup", func() bool { return ds.nFinds() == 1 && h.g.v.loaded(0) && h.env.State.View.Where != "" })
		h.settle()
		h.press("y")
		keep := int64(15_000)
		if !same {
			keep = other
		}
		ds.mu.Lock()
		ds.holdFind = false
		ds.mu.Unlock()
		h.send(kit.SetViewMsg{View: data.View{Where: "detector < 150"}, KeepFileRow: keep})
		h.settle()
		ds.release(0)
		h.settle()
		if same && (h.record() != 15_000 || len(h.copies) != 1 || h.copies[0] != tr["band"][15_000]) {
			t.Errorf("same record: record %d copies %q", h.record(), h.copies)
		}
		if !same && (len(h.copies) != 0 || !h.noted("y not applied: the view changed before the record was found") || h.record() != other) {
			t.Errorf("another record: record %d copies %q notes %+v", h.record(), h.copies, h.notes)
		}
	}
}

// aroundWrap is the fake whose FetchAround is recorded, can start later
// than asked (as the data layer's does when fewer rows precede the record)
// or fail, and can be held.
type aroundWrap struct {
	*fakeDS
	mu    sync.Mutex
	calls [][3]int64 // file row, pos, start asked for
	late  int64
	fail  bool
	gate  chan struct{}
}

func (a *aroundWrap) FetchAround(ctx context.Context, v data.View, fr, pos, start int64, n int, cols []string) (data.Window, error) {
	a.mu.Lock()
	a.calls = append(a.calls, [3]int64{fr, pos, start})
	a.mu.Unlock()
	if err := a.hold(ctx, a.gate); err != nil {
		return data.Window{}, err
	}
	if a.fail {
		return data.Window{}, errors.New("IO Error: no rows")
	}
	w, err := a.Fetch(ctx, v, start+a.late, n-int(a.late), cols)
	w.Start = start + a.late
	return w, err
}

func (a *aroundWrap) hold(ctx context.Context, ch chan struct{}) error {
	if ch == nil {
		return nil
	}
	return a.fakeDS.hold(ctx, ch)
}

// The rows around the record: the window holds it at its screen row with a
// screen above and below, and a window starting later than asked lands it
// right.
func TestTheRowsAroundTheRecord(t *testing.T) {
	for _, late := range []int64{0, 7} {
		ds := &aroundWrap{fakeDS: newFake(30_000, 6), late: late}
		h := newHarness(t, ds, 150, 42)
		h.send(kit.GotoMsg{Row: 27_000})
		h.settle()
		h.g.top = h.g.curRow - 10
		h.exec(h.g.moved())
		h.settle()
		h.send(kit.SetViewMsg{View: data.View{Where: "id % 3 = 0"}, KeepFileRow: 27_000})
		h.settle()
		pos, n := int64(9000), int64(h.g.bodyH())
		if len(ds.calls) != 1 || ds.calls[0][0] != 27_000 || ds.calls[0][1] != pos || ds.calls[0][2] != pos-10-n {
			t.Errorf("late %d: reads around %v", late, ds.calls)
		}
		if h.record() != 27_000 || h.g.curRow != pos || h.screenRow() != 10 || h.g.kept != nil {
			t.Errorf("late %d: record %d row %d screen row %d", late, h.record(), h.g.curRow, h.screenRow())
		}
		for r := pos - 10; r < pos+n-10; r++ {
			if h.g.fileRowAt(r) != 3*r {
				t.Fatalf("late %d: row %d holds file row %d", late, r, h.g.fileRowAt(r))
			}
		}
	}
}

// The rows around the record failing, or Esc while they are read, give up
// on it.
func TestTheRowsAroundTheRecordFailOrAreCancelled(t *testing.T) {
	for _, esc := range []bool{false, true} {
		ds := &aroundWrap{fakeDS: newFake(30_000, 6), fail: !esc}
		if esc {
			ds.gate = make(chan struct{})
		}
		h := newHarness(t, ds, 150, 42)
		h.send(kit.GotoMsg{Row: 27_000})
		h.settle()
		h.send(kit.SetViewMsg{View: data.View{Where: "id % 3 = 0"}, KeepFileRow: 27_000, Keys: []tea.KeyPressMsg{kp("y")}})
		h.settle()
		if esc {
			if h.g.kept == nil || h.g.kept.phase != "seeking" {
				t.Fatalf("not seeking: %+v", h.g.kept)
			}
			h.press("esc")
		}
		if h.g.kept != nil || !h.noted("y not applied: its page didn't load") || len(h.copies) != 0 || h.g.curRow != 0 {
			t.Errorf("esc %v: kept %+v copies %q notes %+v row %d", esc, h.g.kept, h.copies, h.notes, h.g.curRow)
		}
	}
}

// Sorting or jumping while the record is looked up gives it up, saying why.
func TestSortOrJumpWhileLookingDropsTheKeys(t *testing.T) {
	for _, how := range []string{"s", "g"} {
		ds := hookFixture(t, "demo")
		ds.holdFind = true
		h := newHarness(t, ds, 150, 42)
		h.send(kit.GotoMsg{Row: 15_000})
		h.settle()
		h.send(kit.SetViewMsg{View: data.View{Where: "band = 'r'"}, KeepFileRow: 15_001, Keys: []tea.KeyPressMsg{kp("y")}})
		h.waitFor("the lookup", func() bool { return ds.nFinds() == 1 })
		h.settle()
		want := "y not applied: the sort changed before the record was found"
		if how == "s" {
			h.press("s")
		} else {
			h.send(kit.GotoMsg{Row: 100})
			h.settle()
			want = "y not applied: the view moved before the record was found"
		}
		if !h.noted(want) || h.g.kept != nil || h.env.Tasks.Running("locate") {
			t.Errorf("%s: notes %+v", how, h.notes)
		}
	}
}

// A view other than the one asked for (keeping a record) keeps nothing.
func TestTheRecordIsKeptOnlyForTheViewAskedFor(t *testing.T) {
	ds := newFake(5000, 4)
	h := newHarness(t, ds, 150, 42)
	h.send(kit.GotoMsg{Row: 3000})
	h.settle()
	h.g.onSetView(kit.SetViewMsg{View: data.View{Where: "id % 3 = 0"}, KeepFileRow: 3000})
	h.env.State.View = data.View{Where: "id % 2 = 0"}
	h.send(kit.ViewChangedMsg{})
	h.settle()
	if h.g.kept != nil || h.g.curRow != 0 {
		t.Errorf("kept %+v row %d", h.g.kept, h.g.curRow)
	}
}

// A view asked for while a record is on its way: the same record keeps the
// screen row it is going to and the values it came with; another takes the
// cursor's screen row and its values from the rows read.
func TestAViewAskedForReusesTheRecordOnItsWayOnlyForTheSameRow(t *testing.T) {
	h := newHarness(t, newFake(5000, 4), 150, 42)
	g := h.g
	g.curRow, g.top = 5, 2
	g.kept = &keeping{fileRow: 100, screenRow: 7, values: map[string]data.Value{"id": "X"}, phase: "locating"}
	g.onSetView(kit.SetViewMsg{View: data.View{Where: "id % 3 = 0"}, KeepFileRow: 100})
	if g.next.screenRow != 7 || g.next.values["id"] != "X" {
		t.Errorf("same record: %+v", g.next)
	}
	g.onSetView(kit.SetViewMsg{View: data.View{Where: "id % 3 = 0"}, KeepFileRow: 5})
	if g.next.screenRow != 3 || g.next.values["id"] != int64(5) || g.next.values["name"] != "r5" {
		t.Errorf("another record: %+v", g.next)
	}
	g.kept = nil
	if v := g.recordValues(-1); len(v) != 0 {
		t.Errorf("values of no record: %v", v)
	}
	h.filterWith("select id, name from t") // rows without file rows
	if v := g.recordValues(-1); len(v) != 0 {
		t.Errorf("values of a SQL row: %v", v)
	}
}

// While the rows around the record are read, other reads neither replace
// that read nor decide about the record.
func TestWhileSeekingOtherReadsWait(t *testing.T) {
	ds := &aroundWrap{fakeDS: newFake(30_000, 6), gate: make(chan struct{})}
	h := newHarness(t, ds, 150, 42)
	h.send(kit.GotoMsg{Row: 27_000})
	h.settle()
	h.send(kit.SetViewMsg{View: data.View{Where: "id % 3 = 0"}, KeepFileRow: 27_000})
	h.settle()
	g := h.g
	if g.kept == nil || g.kept.phase != "seeking" || g.page == nil || !g.page.around {
		t.Fatalf("not seeking: %+v", g.kept)
	}
	g.top = 5000 // rows not read
	if cmd := g.ensureRows(); cmd != nil || !g.page.around {
		t.Error("a read replaced the one around the record")
	}
	g.top = 0
	// a read that isn't the one around the record, holding nothing of it
	if cmd := g.keepOnPage(fetchReq{n: 5}, 0, data.Window{Len: 5, FileRows: []int64{0, 3, 6, 9, 12}}); cmd != nil || g.kept == nil || g.kept.phase != "seeking" {
		t.Errorf("an unrelated read decided: %+v", g.kept)
	}
	close(ds.gate)
	h.settle()
	if h.record() != 27_000 || g.kept != nil {
		t.Errorf("record %d kept %+v", h.record(), g.kept)
	}
}
