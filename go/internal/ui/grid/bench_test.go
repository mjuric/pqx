package grid

import (
	"io"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"

	"github.com/mjuric/pqx/go/internal/data"
)

// benchHarness is a 200x50 screen over 300 columns, every column cached
// for rows [0, 400) (docs/design/go-port.md: a frame on a 300-column
// screen ≤ 5 ms).
func benchHarness(b *testing.B) *harness {
	ds := newFake(1_000_000, 300)
	h := newHarness(b, ds, 200, 50)
	cols := names(ds.cols)
	w, err := ds.Fetch(b.Context(), data.View{}, 0, 400, cols)
	if err != nil {
		b.Fatal(err)
	}
	h.g.store(0, 400, w)
	h.g.curRow, h.g.curCol = 20, 10
	h.g.scrollToCursor()
	h.app.View()
	return h
}

// BenchmarkView is the time to draw the grid pane (Grid.View).
func BenchmarkView(b *testing.B) {
	h := benchHarness(b)
	g := h.g
	i := 0
	b.ReportAllocs()
	for b.Loop() {
		i++
		g.curCol = 10 + i%2 // keep the cursor moving
		_ = g.View(g.w, g.h)
	}
}

// BenchmarkFrame is a whole frame: the root's View, and what Bubble Tea's
// renderer does with its text (parse it into a cell buffer and write the
// difference from the last frame). The cursor moves one cell per frame.
func BenchmarkFrame(b *testing.B) {
	benchFrame(b, func(g *Grid, i int) { g.curCol = 10 + i%2 })
}

// BenchmarkFramePage is the same with every row changing per frame (PgDn
// and PgUp in turn over cached rows).
func BenchmarkFramePage(b *testing.B) {
	benchFrame(b, func(g *Grid, i int) {
		g.top = int64(i%2) * 100
		g.curRow = g.top
	})
}

// BenchmarkFrameScroll moves a column sideways each frame (→ at the edge).
func BenchmarkFrameScroll(b *testing.B) {
	benchFrame(b, func(g *Grid, i int) {
		g.left = 10 + i%2
		g.curCol = g.left
	})
}

func benchFrame(b *testing.B, step func(*Grid, int)) {
	h := benchHarness(b)
	buf := uv.NewScreenBuffer(200, 50)
	scr := uv.NewTerminalRenderer(io.Discard, []string{"TERM=xterm-256color"})
	scr.SetFullscreen(true)
	scr.SetRelativeCursor(false)
	i := 0
	b.ReportAllocs()
	for b.Loop() {
		i++
		step(h.g, i)
		content := uv.NewStyledString(h.app.View().Content)
		buf.Clear()
		content.Draw(buf, buf.Bounds())
		scr.Render(buf.RenderBuffer)
		if err := scr.Flush(); err != nil {
			b.Fatal(err)
		}
	}
}
