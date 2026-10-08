package ui

import (
	"io"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/mjuric/pqx/go/internal/data"
)

// benchModel is a 200x50 screen over 300 columns, with every column cached
// for rows [0, 400) (open question 6 of docs/design/native-port.md).
func benchModel(b *testing.B) *Model {
	ds := newFake(1_000_000, 300)
	m := New(ds)
	m.tick = func() tea.Cmd { return nil }
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	cols := make([]string, len(ds.cols))
	for i, c := range ds.cols {
		cols[i] = c.Name
	}
	w, err := ds.Fetch(b.Context(), data.View{}, 0, 400, cols)
	if err != nil {
		b.Fatal(err)
	}
	m.store(fetchReq{gen: m.v.gen, start: 0, n: 400, cols: cols}, w)
	m.cancelFetch()
	m.curRow, m.curCol = 20, 10
	m.scrollToCursor()
	return m
}

// BenchmarkRender is the time to build one frame's text (Model.View).
func BenchmarkRender(b *testing.B) {
	m := benchModel(b)
	i := 0
	b.ReportAllocs()
	for b.Loop() {
		i++
		m.curCol = 10 + i%2 // keep the cursor moving
		_ = m.View()
	}
}

// BenchmarkFrame adds what Bubble Tea's renderer does with the text each
// frame: parse it into a cell buffer and write the difference from the last
// frame. The cursor moves one cell per frame (arrow keys).
func BenchmarkFrame(b *testing.B) {
	benchFrame(b, func(m *Model, i int) { m.curCol = 10 + i%2 })
}

// BenchmarkFramePage is the same with every row changing per frame (PgDn and
// PgUp in turn over cached rows).
func BenchmarkFramePage(b *testing.B) {
	benchFrame(b, func(m *Model, i int) {
		m.top = int64(i%2) * 100
		m.curRow = m.top
	})
}

func benchFrame(b *testing.B, step func(*Model, int)) {
	m := benchModel(b)
	buf := uv.NewScreenBuffer(200, 50)
	scr := uv.NewTerminalRenderer(io.Discard, []string{"TERM=xterm-256color"})
	scr.SetFullscreen(true)
	scr.SetRelativeCursor(false)
	i := 0
	b.ReportAllocs()
	for b.Loop() {
		i++
		step(m, i)
		content := uv.NewStyledString(m.View().Content)
		buf.Clear()
		content.Draw(buf, buf.Bounds())
		scr.Render(buf.RenderBuffer)
		if err := scr.Flush(); err != nil {
			b.Fatal(err)
		}
	}
}
