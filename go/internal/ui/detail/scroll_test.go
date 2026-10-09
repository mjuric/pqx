package detail

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

func exactH(p *Pane, i int) int { return entryHeight(*p.textOf(i), p.nameW, p.cw) }

// The scroll position and the selection under random keys, wheel, clicks,
// record changes (entries changing height) and grid moves, against an
// exact model of the lines: the position stays on the entries with no blank
// below the end, the selection stays in view after a move (and after a
// record change if it was in view), lineAt agrees, every line is w wide.
// (From the review's probe; it found a record change shrinking a top entry
// partly scrolled off, which dropped the selection off the bottom.)
func TestScrollInvariants(t *testing.T) {
	rnd := rand.New(rand.NewSource(3))
	fails := 0
	for trial := 0; trial < 100 && fails < 5; trial++ {
		n := 1 + rnd.Intn(120)
		w, h := 8+rnd.Intn(60), 1+rnd.Intn(35)
		var cols []data.Column
		for i := 0; i < n; i++ {
			cols = append(cols, col(fmt.Sprintf("c%d", i), str, ""))
		}
		mk := func() map[string]data.Value {
			v := map[string]data.Value{}
			for i := 0; i < n; i++ {
				v[fmt.Sprintf("c%d", i)] = strings.Repeat("ab ", rnd.Intn(4)*rnd.Intn(40))
			}
			return v
		}
		r := newRig(t, cols, kit.Record{Row: 0, FileRow: 0, Values: mk()}, w, h)
		r.p.Focus()
		r.p.View(w, h)
		for step := 0; step < 80 && fails < 5; step++ {
			p := r.p
			op := rnd.Intn(10)
			var name string
			wasIn := p.sel >= 0 && p.inView(p.sel)
			bti, bto, bcw, bbar := p.ti, p.to, p.cw, p.bar
			switch op {
			case 0, 1, 2, 3, 4, 5:
				name = []string{"up", "down", "pgup", "pgdown", "home", "end"}[op]
				r.press(name)
			case 6:
				name = "wheel"
				p.scroll(rnd.Intn(9) - 4)
			case 7:
				name = "record"
				r.g.rec = kit.Record{Row: int64(step), FileRow: int64(step), Values: mk()}
				r.send(kit.CursorMsg{})
			case 8:
				name = "grid column"
				r.env.State.Current = fmt.Sprintf("c%d", rnd.Intn(n))
				r.send(kit.ColumnChangedMsg{From: "grid"})
			case 9:
				name = "click"
				r.send(tea.MouseClickMsg{X: rnd.Intn(w), Y: rnd.Intn(h), Button: tea.MouseLeft})
			}
			out := p.View(w, h)
			// exact model
			hs := make([]int, len(p.entries))
			total, top := 0, p.to
			for i := range p.entries {
				hs[i] = exactH(p, i)
				total += hs[i]
				if i < p.ti {
					top += hs[i]
				}
			}
			bad := ""
			if p.to < 0 || (len(hs) > 0 && p.to >= hs[p.ti]) {
				bad = "to out of entry"
			} else if top < 0 || top > max(0, total-h) {
				bad = fmt.Sprintf("top %d total %d", top, total)
			}
			if bad == "" && p.sel >= 0 && (op <= 5 || op == 8 || (op == 7 && wasIn)) {
				a := 0
				for i := 0; i < p.sel; i++ {
					a += hs[i]
				}
				b := a + hs[p.sel]
				if hs[p.sel] <= h && (a < top || b > top+h) {
					bad = fmt.Sprintf("selection %d lines [%d,%d) not in view [%d,%d)", p.sel, a, b, top, top+h)
				}
				if hs[p.sel] > h && a != top && op <= 5 {
					bad = fmt.Sprintf("tall selection %d at %d, top %d", p.sel, a, top)
				}
			}
			ls := strings.Split(out, "\n")
			if bad == "" && len(ls) != h && h > 0 {
				bad = fmt.Sprintf("%d lines", len(ls))
			}
			for _, l := range ls {
				if bad == "" && h > 0 && ansi.StringWidth(l) != w {
					bad = fmt.Sprintf("line width %d", ansi.StringWidth(l))
				}
			}
			for y := 0; bad == "" && y < h; y++ {
				l, want := top+y, -1
				for i, acc := 0, 0; i < len(hs); i++ {
					if l < acc+hs[i] {
						want = i
						break
					}
					acc += hs[i]
				}
				if got := p.lineAt(y); got != want {
					bad = fmt.Sprintf("lineAt(%d) %d want %d", y, got, want)
				}
			}
			if bad != "" {
				fails++
				t.Errorf("trial %d (%d entries %dx%d) step %d %s: %s; before ti %d to %d cw %d bar %v; after ti %d to %d cw %d bar %v sel %d", trial, n, w, h, step, name, bad, bti, bto, bcw, bbar, p.ti, p.to, p.cw, p.bar, p.sel)
			}
		}
	}
}

// heightsRig: entries c00…c29 whose values take the given lines (value
// column 24 wide: a line is a 20-letter word).
func heightsRig(t *testing.T, w, h int, lines map[int]int) *rig {
	var cols []data.Column
	vals := map[string]data.Value{}
	for i := 0; i < 30; i++ {
		n := fmt.Sprintf("c%02d", i)
		cols = append(cols, col(n, str, ""))
		k := max(1, lines[i])
		vals[n] = strings.TrimSpace(strings.Repeat(strings.Repeat("x", 20)+" ", k))
	}
	return newRig(t, cols, kit.Record{Row: 0, FileRow: 0, Values: vals}, w, h)
}

// An entry partly scrolled off the top isn't in view; moving to it shows
// it whole, at the top.
func TestPartlyScrolledEntry(t *testing.T) {
	r := heightsRig(t, 30, 6, map[int]int{2: 4})
	r.p.View(30, 6)
	r.p.Focus()
	r.send(tea.MouseWheelMsg{Button: tea.MouseWheelDown}) // 2 lines: c02 starts at line 2
	r.send(tea.MouseWheelMsg{Button: tea.MouseWheelDown}) // its first 2 lines off the top
	if r.p.ti != 2 || r.p.to != 2 || r.p.inView(2) || !r.p.inView(3) {
		t.Fatalf("ti %d to %d", r.p.ti, r.p.to)
	}
	r.env.State.Current = "c02"
	r.send(kit.ColumnChangedMsg{From: "grid"})
	if r.p.ti != 2 || r.p.to != 0 {
		t.Fatalf("ti %d to %d", r.p.ti, r.p.to)
	}
	// the wheel through a tall entry, one line at a time and past its end
	r.p.scroll(3)
	if r.p.ti != 2 || r.p.to != 3 {
		t.Fatalf("ti %d to %d", r.p.ti, r.p.to)
	}
	r.p.scroll(1)
	if r.p.ti != 3 || r.p.to != 0 {
		t.Fatalf("ti %d to %d", r.p.ti, r.p.to)
	}
}

// Down to an entry below the screen puts its last line on the screen's
// last line, the entry above partly shown.
func TestScrollToTheBottom(t *testing.T) {
	r := heightsRig(t, 30, 6, map[int]int{5: 3})
	r.p.View(30, 6)
	r.p.Focus()
	r.env.State.Current = "c06"
	r.send(kit.ColumnChangedMsg{From: "grid"})
	// lines: c00–c04 0–4, c05 5–7, c06 8: view [3, 9)
	if r.p.Top() != 3 || r.p.ti != 3 || r.p.to != 0 {
		t.Fatalf("top %d ti %d to %d", r.p.Top(), r.p.ti, r.p.to)
	}
	r.env.State.Current = "c07"
	r.send(kit.ColumnChangedMsg{From: "grid"})
	if r.p.Top() != 4 || r.p.lineAt(5) != 7 {
		t.Fatalf("top %d", r.p.Top())
	}
	r.p.scroll(2) // c05's first line off the top
	r.p.scroll(-1)
	if r.p.ti != 5 || r.p.to != 0 {
		t.Fatalf("ti %d to %d", r.p.ti, r.p.to)
	}
	// the entry above doesn't fit whole: it shows in part, the least scroll
	r = heightsRig(t, 30, 6, map[int]int{4: 3, 5: 3})
	r.p.View(30, 6)
	r.env.State.Current = "c06"
	r.send(kit.ColumnChangedMsg{From: "grid"})
	// lines: c04 4–6, c05 7–9, c06 10: view [5, 11)
	if r.p.ti != 4 || r.p.to != 1 || r.p.Top() != 5 {
		t.Fatalf("ti %d to %d", r.p.ti, r.p.to)
	}
}

// A column asked for again by another part shows, even if the wheel had
// scrolled the selection away.
func TestColumnAskedForAgain(t *testing.T) {
	r := heightsRig(t, 30, 6, nil)
	r.p.View(30, 6)
	for i := 0; i < 5; i++ {
		r.send(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	}
	if r.p.inView(0) {
		t.Fatal("still in view")
	}
	r.send(kit.CursorMsg{}) // a new row: left where it was scrolled to
	if r.p.inView(0) {
		t.Fatal("scrolled back on a new row")
	}
	r.send(kit.ColumnChangedMsg{From: "schema"})
	if !r.p.inView(0) {
		t.Fatal("not shown")
	}
}

// The scrollbar is Textual's for the exact lines, whatever has been drawn
// (files with up to exactBar columns); beyond, entries not measured count
// as a line.
func TestScrollbarIsExact(t *testing.T) {
	r := heightsRig(t, 49, 10, map[int]int{3: 2, 17: 3, 25: 4})
	r.p.View(49, 10)
	r.press("end")
	r.p.View(49, 10)
	got := r.p.scrollbar(r.env.Look.Style("border")) // (before measuring here)
	total := 0
	for i := range r.p.entries {
		total += r.p.height(i)
	}
	want := scrollbar(10, total, r.p.Top(), r.env.Look.Style("border"))
	for y := range want {
		if want[y].Plain != got[y].Plain || want[y].Style != got[y].Style {
			t.Fatalf("row %d: %q %+v, want %q %+v (total %d)", y, got[y].Plain, got[y].Style, want[y].Plain, want[y].Style, total)
		}
	}
	// a wide file: only what was drawn is measured; the thumb is about a
	// screen in 600 lines
	var cols []data.Column
	vals := map[string]data.Value{}
	for i := 0; i < exactBar+100; i++ {
		n := fmt.Sprintf("c%03d", i)
		cols = append(cols, col(n, str, ""))
		vals[n] = "v"
	}
	r = newRig(t, cols, kit.Record{Row: 0, FileRow: 0, Values: vals}, 49, 10)
	r.p.View(49, 10)
	thumb := 0
	for _, x := range r.p.scrollbar(r.env.Look.Style("border")) {
		if x.Style.Reverse || x.Plain != " " {
			thumb++
		}
	}
	if thumb != 1 || r.p.entries[len(cols)-1].h != 0 {
		t.Fatalf("thumb %d", thumb)
	}
}

// A view change forgets the columns' kinds: a column of the same name may
// be another type now.
func TestKindsAfterAViewChange(t *testing.T) {
	r := newRig(t, []data.Column{col("ra", f64, "deg")}, kit.Record{Row: 0, FileRow: 0,
		Values: map[string]data.Value{"ra": 12.5}}, 49, 10)
	if !strings.Contains(r.value("ra"), "00h50m00.000s") {
		t.Fatal(r.value("ra"))
	}
	r.env.State.Columns = []data.Column{col("ra", i64, "")}
	r.g.rec.Values["ra"] = int64(12)
	r.send(kit.ViewChangedMsg{})
	if v := r.value("ra"); strings.Contains(v, "h") {
		t.Fatal(v)
	}
}

// A read of several groups that fails for one goes on with the others.
func TestPartlyFailedRead(t *testing.T) {
	r := newRig(t, demoCols(), kit.Record{Row: -1, FileRow: -1}, 49, 20)
	r.g.read = func() (kit.View, [][]int64, [][]string, bool) {
		return kit.View{Gen: 1}, [][]int64{{3}, {3, 4}, {4}}, [][]string{{"ra"}, {"dec"}, {"band"}}, true
	}
	r.ds.fetch = func(ctx context.Context, rows []int64, cs []string) (data.Window, error) {
		if cs[0] == "dec" {
			return data.Window{}, errors.New("boom")
		}
		w := data.Window{Len: len(rows), FileRows: rows, Cols: map[string][]data.Value{}}
		w.Cols[cs[0]] = make([]data.Value, len(rows))
		return w, nil
	}
	r.g.rec = kit.Record{Row: 3, FileRow: 3, Values: map[string]data.Value{}, Missing: []string{"ra"}}
	r.send(kit.CursorMsg{})
	if r.ds.calls != 3 || len(r.g.merged) != 3 {
		t.Fatalf("%d reads, %d merged", r.ds.calls, len(r.g.merged))
	}
	if w := r.g.merged[1]; w.Failed["dec"] == nil || len(w.FileRows) != 2 {
		t.Fatalf("%+v", w)
	}
	if w := r.g.merged[2]; len(w.Failed) != 0 || len(w.Cols["band"]) != 1 {
		t.Fatalf("%+v", w)
	}
	if m, ok := last[kit.NotifyMsg](r); !ok || m.Text != "Couldn't load 1 column: boom" {
		t.Fatalf("%v", r.msgs)
	}
}
