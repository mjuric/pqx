package grid

// tests/test_lazycols.py: the grid reads only the columns it needs. (The
// details pane's tests are WP12's.)

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/mjuric/pqx/go/internal/cells"
	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

const (
	lazyCols = 120
	lazyRows = 3000
)

// checkCache checks that every value cached is the truth for its file row,
// and every cell drawn shows it or a placeholder.
func checkCache(t *testing.T, h *harness) {
	t.Helper()
	g := h.g
	ds := h.ds.(*fakeDS)
	for name, col := range g.v.vals {
		for r, v := range col {
			fr, ok := g.v.fileRow[r]
			if !ok {
				continue
			}
			if _, bad := v.(failedCell); bad {
				continue
			}
			if want := ds.value(name, fr); v != want {
				t.Fatalf("%s at row %d (file row %d) = %v, want %v", name, r, fr, v, want)
			}
		}
	}
	h.grid()
	for _, s := range g.layout() {
		c := g.cols[s.col]
		for r := g.top; r < g.top+int64(g.bodyH()); r++ {
			tx, ok := g.cellText(s.col, r)
			if !ok {
				continue
			}
			v, have := g.v.cell(c.Name, r)
			switch {
			case !have && tx.plain != cells.Placeholder:
				t.Fatalf("%s row %d not read but shows %q", c.Name, r, tx.plain)
			case have:
				if _, bad := v.(failedCell); bad {
					continue
				}
				if want := fmtx.Format(v, c.kind, g.opts(c)); tx.plain != want {
					t.Fatalf("%s row %d shows %q, want %q", c.Name, r, tx.plain, want)
				}
			}
		}
	}
}

// visibleMissing are the columns on screen with a cell not read in a row read.
func visibleMissing(g *Grid) []string {
	var out []string
	first, last, _, _ := g.colWindow()
	for i := first; i <= last; i++ {
		name := g.cols[i].Name
		for r := g.top; r < g.top+int64(g.bodyH()); r++ {
			if _, ok := g.v.cell(name, r); g.v.loaded(r) && !ok {
				out = append(out, name)
				break
			}
		}
	}
	return out
}

func TestLoadsOnlyTheColumnsNearTheView(t *testing.T) {
	ds := newFake(lazyRows, lazyCols)
	h := newHarness(t, ds, 150, 42)
	calls := ds.log()
	first := calls[0]
	if first.kind != "fetch" || len(first.cols) <= 10 || len(first.cols) >= lazyCols/2 {
		t.Fatalf("first read: %s of %d columns", first.kind, len(first.cols))
	}
	if !slices.Equal(first.cols, names(ds.cols)[:len(first.cols)]) {
		t.Errorf("first read's columns aren't from the left edge in order: %v", first.cols)
	}
	fetched := map[string]bool{}
	for _, c := range first.cols {
		fetched[c] = true
	}
	for _, c := range calls[1:] {
		for _, n := range c.cols {
			if c.kind != "columns" || fetched[n] {
				t.Errorf("a later read asked again for %s (%s)", n, c.kind)
			}
			fetched[n] = true
		}
	}
	if len(fetched) == lazyCols {
		t.Error("every column was read")
	}
	if m := visibleMissing(h.g); len(m) > 0 {
		t.Errorf("visible columns not read: %v", m)
	}
	checkCache(t, h)
}

func TestScrollingRightFetchesAndShowsColumns(t *testing.T) {
	const wide = 300 // (cells are narrow: 120 columns are two reads)
	ds := newFake(lazyRows, wide)
	h := newHarness(t, ds, 150, 42)
	g := h.g
	ds.clearLog()
	h.press("end")
	if g.curCol != wide-1 {
		t.Fatalf("End: column %d", g.curCol)
	}
	calls := ds.log()
	if len(calls) == 0 {
		t.Fatal("End read nothing")
	}
	for _, c := range calls {
		if c.kind != "columns" {
			t.Errorf("End read rows again: %+v", c)
		}
	}
	if m := visibleMissing(g); len(m) > 0 {
		t.Errorf("visible columns not read: %v", m)
	}
	checkCache(t, h)

	// stepping back left a column at a time reads about once a screen
	ds.clearLog()
	for i := 0; i < 100; i++ {
		h.send(kp("left"))
		h.settle()
	}
	if n := len(ds.log()); n < 1 || n > 6 {
		t.Errorf("100 steps left read %d times", n)
	}
	if m := visibleMissing(g); len(m) > 0 {
		t.Errorf("missing: %v", m)
	}
	checkCache(t, h)

	// a jump reads lazily again, around the view
	ds.clearLog()
	h.send(kit.GotoMsg{Row: 2500})
	h.settle()
	calls = ds.log()
	if calls[0].kind != "fetch" || len(calls[0].cols) >= wide || !slices.Contains(calls[0].cols, g.curName()) {
		t.Errorf("read after g: %s of %v", calls[0].kind, calls[0].cols)
	}
	checkCache(t, h)
}

func TestPinnedColumnsAlwaysLoad(t *testing.T) {
	ds := newFake(lazyRows, lazyCols)
	h := newHarness(t, ds, 150, 42)
	h.press("right", "right", "p", "end")
	ds.clearLog()
	h.send(kit.GotoMsg{Row: 1500})
	h.settle()
	calls := ds.fetches()
	if h.g.pinned() != 3 || len(calls) == 0 {
		t.Fatalf("pinned %d, reads %d", h.g.pinned(), len(calls))
	}
	if !slices.Equal(calls[0].cols[:3], names(ds.cols)[:3]) || !slices.Contains(calls[0].cols, ds.cols[lazyCols-1].Name) {
		t.Errorf("read after g: %v", calls[0].cols)
	}
	checkCache(t, h)
}

// A column read overtaken by a new window must not write its rows into it.
func TestStaleColumnFetchIsDiscarded(t *testing.T) {
	ds := newFake(lazyRows, lazyCols)
	h := newHarness(t, ds, 150, 42)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	ds.colsHook = func(ctx context.Context, cols []string) error {
		first := false
		once.Do(func() { first = true })
		if first {
			close(started)
			<-release // read late, whatever ctx says (as if interrupting came too late)
		}
		return nil
	}
	h.send(kp("end"))
	h.waitFor("the column read", func() bool {
		select {
		case <-started:
			return true
		default:
			return false
		}
	})
	h.send(kp("home"))
	h.send(kp("ctrl+end")) // a new window around the first column
	h.settle()
	close(release)
	h.settle()
	if h.g.curCol != 0 || h.g.curRow != lazyRows-1 {
		t.Errorf("cursor (%d,%d)", h.g.curRow, h.g.curCol)
	}
	checkCache(t, h) // every value is its own row's
}

// y and F read the cursor's column first if need be.
func TestActionsGetValuesOfUnloadedColumns(t *testing.T) {
	ds := newFake(lazyRows, lazyCols)
	h := newHarness(t, ds, 150, 42)
	g := h.g
	release := make(chan struct{})
	var once sync.Once
	ds.colsHook = func(ctx context.Context, cols []string) error {
		first := false
		once.Do(func() { first = true })
		if first { // the scroll's own read hangs; the action's goes through
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	h.press("end", "down", "down")
	name := g.curName()
	if _, ok := g.v.cell(name, 2); ok {
		t.Fatal("the column is read already")
	}
	h.press("y")
	h.waitFor("the copy", func() bool { return len(h.copies) > 0 })
	if want := fmtx.Format(truth(name, 2), fmtx.KindFloat, fmtx.Opts{Raw: true, Unsafe: true}); h.copies[0] != want {
		t.Errorf("copied %q, want %q", h.copies[0], want)
	}
	close(release)
	h.settle()
	checkCache(t, h)
}

func TestViewsWithoutRowIDsFetchEveryColumn(t *testing.T) {
	ds := newFake(lazyRows, lazyCols)
	h := newHarness(t, ds, 150, 42)
	h.filterWith("select * from t where c000 > 5000")
	calls := ds.log()
	last := calls[len(calls)-1]
	if !h.env.State.View.IsSQL() || last.kind != "fetch" || !slices.Equal(last.cols, []string{"id", "name"}) {
		t.Errorf("SQL view read %+v", last)
	}
	ds.clearLog()
	h.press("end", "pgdown")
	for _, c := range ds.log() {
		if c.kind != "fetch" {
			t.Errorf("a SQL view read columns: %+v", c)
		}
	}
	if gl := h.grid(); !strings.HasPrefix(strings.TrimSpace(gl[headerRows]), itoa(h.g.top)+" ") {
		t.Errorf("SQL rows are labelled by position: %q", gl[headerRows])
	}

	// a filtered view of a file with its own file_row_number column has no
	// row numbers either
	ds2 := newFake(lazyRows, lazyCols)
	ds2.cols[3].Name = "File_Row_Number"
	h2 := newHarness(t, ds2, 150, 42, hopts{where: "id % 2 = 0"})
	if h2.g.v.ids || h2.env.State.View.Where != "id % 2 = 0" {
		t.Fatalf("ids %v, view %+v", h2.g.v.ids, h2.env.State.View)
	}
	calls = ds2.fetches()
	if last := calls[len(calls)-1]; len(last.cols) != lazyCols {
		t.Errorf("read %d columns, want all", len(last.cols))
	}
}

func TestFilteredViewsLoadLazily(t *testing.T) {
	ds := newFake(lazyRows, lazyCols)
	h := newHarness(t, ds, 150, 42, hopts{where: "id % 3 = 0"})
	calls := ds.fetches()
	if len(calls) == 0 || len(calls[0].cols) >= lazyCols {
		t.Fatalf("filtered view read %+v", calls)
	}
	h.press("end")
	checkCache(t, h)
	for r, fr := range h.g.v.fileRow {
		if fr%3 != 0 || fr != 3*r {
			t.Fatalf("row %d is file row %d", r, fr)
		}
	}
	// FetchColumns not built yet in the data layer: the view's rows are read
	// by position instead
	ds2 := newFake(lazyRows, lazyCols)
	ds2.noColumnsAPI = true
	h2 := newHarness(t, ds2, 150, 42, hopts{where: "id % 3 = 0"})
	h2.press("end")
	if m := visibleMissing(h2.g); len(m) > 0 {
		t.Errorf("columns not read without FetchColumns: %v", m)
	}
	checkCache(t, h2)
}

func TestWidthsDoNotJumpWhenColumnsArrive(t *testing.T) {
	ds := newFake(lazyRows, lazyCols)
	// the footer's statistics: each int column's min and max
	for _, c := range ds.cols {
		if c.Name[0] == 'c' {
			ds.footer = append(ds.footer, data.ChunkSummary{Path: c.Name, HasStats: true,
				Min: truth(c.Name, 0), Max: truth(c.Name, lazyRows-1)})
		}
	}
	h := newHarness(t, ds, 150, 42)
	g := h.g
	release := make(chan struct{})
	ds.colsHook = func(ctx context.Context, cols []string) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	h.press("end")
	if !strings.Contains(strings.Join(h.grid()[headerRows:], "\n"), cells.Placeholder) {
		t.Fatal("no placeholders while the columns load")
	}
	missing := visibleMissing(g)
	before := map[string]int{}
	for _, c := range g.cols {
		before[c.Name] = g.colWidth(c.Name)
	}
	left := g.left
	close(release)
	h.settle()
	h.grid()
	for _, c := range g.cols {
		after := g.colWidth(c.Name)
		if after < before[c.Name] {
			t.Errorf("%s narrowed: %d -> %d", c.Name, before[c.Name], after)
		}
		if slices.Contains(missing, c.Name) && c.Name[0] == 'c' && after != before[c.Name] {
			t.Errorf("%s: reserved %d, now %d (statistics give ints their width)", c.Name, before[c.Name], after)
		}
	}
	if g.left != left || g.curCol != lazyCols-1 || !g.cursorInView() {
		t.Errorf("view moved: left %d (was %d), cursor %d, in view %v", g.left, left, g.curCol, g.cursorInView())
	}
	if strings.Contains(strings.Join(h.grid()[headerRows:], "\n"), cells.Placeholder) {
		t.Error("placeholders remain")
	}
	drawnCellsFit(t, h, "after the columns arrived")
	checkCache(t, h)
}

func TestRawOnALazyPage(t *testing.T) {
	ds := newFake(lazyRows, lazyCols)
	h := newHarness(t, ds, 150, 42)
	h.press("f") // raw while the far columns aren't read
	h.press("end")
	if !h.env.State.Raw {
		t.Fatal("not raw")
	}
	checkCache(t, h)
	i := h.g.byName["f115"]
	c := h.g.cols[i]
	if tx, ok := h.g.cellText(i, 0); !ok || tx.plain != fmtx.Format(truth("f115", 0), c.kind, fmtx.Opts{Raw: true, Width: fmtx.DefaultWidth}) {
		t.Errorf("raw f115 = %+v", tx)
	}
}

// Esc while the scrolled-to columns load: the next move loads them.
func TestColumnsLoadAfterEscCancelsTheirFetch(t *testing.T) {
	ds := newFake(lazyRows, lazyCols)
	ds.interruptErr = errors.New("INTERRUPT Error: cancelled")
	h := newHarness(t, ds, 150, 42)
	started := make(chan struct{})
	var once sync.Once
	ds.colsHook = func(ctx context.Context, cols []string) error {
		first := false
		once.Do(func() { first = true })
		if first {
			close(started)
			<-ctx.Done()
			return ds.interruptErr
		}
		return nil
	}
	h.send(kp("end"))
	h.waitFor("the column read", func() bool {
		select {
		case <-started:
			return true
		default:
			return false
		}
	})
	h.press("esc")
	if len(visibleMissing(h.g)) == 0 {
		t.Fatal("the cancelled columns loaded by themselves")
	}
	if h.noted("Couldn't load") {
		t.Errorf("a cancel counted as a failure: %+v", h.notes)
	}
	h.press("left", "down")
	if m := visibleMissing(h.g); len(m) > 0 {
		t.Errorf("still missing after a move: %v", m)
	}
	checkCache(t, h)
}

// A window read cancelled with Esc: the rows read before stay, and the next
// move reads what is missing.
func TestColumnsLoadAfterACancelledPageLoad(t *testing.T) {
	ds := newFake(lazyRows, lazyCols)
	h := newHarness(t, ds, 150, 42)
	ds.gate = make(chan struct{})
	h.send(kp("ctrl+end"))
	h.send(kp("esc"))
	h.settle()
	if h.env.Tasks.Running("page") {
		t.Fatal("Esc didn't stop the read")
	}
	close(ds.gate)
	h.press("ctrl+home", "end")
	if m := visibleMissing(h.g); len(m) > 0 {
		t.Errorf("missing: %v", m)
	}
	checkCache(t, h)
}

func TestColumnsThatFailShowSoAndAreNotRetried(t *testing.T) {
	ds := newFake(lazyRows, lazyCols)
	h := newHarness(t, ds, 150, 42)
	var mu sync.Mutex
	n := 0
	ds.colsHook = func(ctx context.Context, cols []string) error {
		mu.Lock()
		n++
		mu.Unlock()
		return errors.New("boom")
	}
	h.press("end")
	if n != 1 || !h.noted("Couldn't load") {
		t.Fatalf("reads %d, notes %+v", n, h.notes)
	}
	gl := h.grid()
	if !strings.Contains(gl[headerRows], cells.FailedMark) || strings.Contains(gl[headerRows], cells.Placeholder) {
		t.Errorf("failed cells: %q", gl[headerRows])
	}
	if raw := h.gridRaw()[headerRows]; !strings.Contains(raw, h.g.styledText(cells.FailedMark, h.env.Look.Style("error"))) {
		t.Errorf("✗ isn't drawn as an error: %q", raw)
	}
	h.notes = nil
	h.press("down", "down", "left", "right")
	if n != 1 || h.noted("Couldn't load") {
		t.Errorf("retried: reads %d, notes %+v", n, h.notes)
	}
	h.press("y")
	if len(h.copies) > 0 || !h.noted("couldn't be loaded for these rows") {
		t.Errorf("y on a failed cell: copies %v, notes %+v", h.copies, h.notes)
	}
}

// y then F on a column still loading: both wait for one read.
func TestActionsOnALoadingColumnAllHappen(t *testing.T) {
	ds := newFake(lazyRows, lazyCols)
	h := newHarness(t, ds, 150, 42)
	release := make(chan struct{})
	var mu sync.Mutex
	var calls [][]string
	ds.colsHook = func(ctx context.Context, cols []string) error {
		mu.Lock()
		calls = append(calls, cols)
		mu.Unlock()
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	h.press("end")
	name := h.g.curName()
	if _, ok := h.g.v.cell(name, 0); ok {
		t.Fatal("read already")
	}
	h.press("y", "F")
	close(release)
	h.settle()
	if len(h.copies) != 1 || len(h.dialogs) != 1 || !strings.HasPrefix(h.dialogs[0], "format "+name) {
		t.Errorf("copies %v, dialogs %v", h.copies, h.dialogs)
	}
	single := 0
	for _, c := range calls {
		if slices.Equal(c, []string{name}) {
			single++
		}
	}
	if single > 1 {
		t.Errorf("%d reads of %s alone", single, name)
	}
}
