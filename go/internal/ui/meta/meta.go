// Package meta is the Metadata tab (Python pqx's _init_meta_tab,
// _render_meta_overview, _build_meta_footer): on the left the file's
// overview and key-value metadata, on the right a table of its row groups
// and the footer pass's status. Tab steps between the two panels.
package meta

import (
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/app"
	"github.com/mjuric/pqx/go/internal/ui/footer"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// Python pqx's limits: the row-group table shows the first MaxRowGroups,
// added Batch at a time every BatchEvery; key-value values this long or
// longer are cut instead of pretty-printed.
const (
	MaxRowGroups = 5000
	Batch        = 250
	BatchEvery   = 10 * time.Millisecond
	MaxKV        = 4000
)

// Pane is the Metadata tab's body.
type Pane struct {
	env *kit.Env

	kv      []styled.Text // the key-value metadata section, unwrapped
	rgs     []data.RowGroup
	summary []data.ChunkSummary
	read    bool        // the footer pass is done
	failed  styled.Text // it failed

	table   *footer.Table
	pending [][]styled.Text // row-group rows not added yet
	gen     int             // the current batch ticks

	inner   int // the panel with focus: 0 the file, 1 the row groups
	top     int // the file panel's scroll
	focused bool
	w, h    int

	wrapW int // width the file panel's lines were wrapped for
	lines []string
}

type batchMsg struct {
	p   *Pane
	gen int
}

var (
	_ app.Paneled    = (*Pane)(nil)
	_ app.InnerFocus = (*Pane)(nil)
	_ kit.Focusable  = (*Pane)(nil)
)

// New makes the Metadata tab. It shows the footer pass's result (task
// footer.Tag) when it arrives.
func New(env *kit.Env) *Pane {
	p := &Pane{env: env, w: 80, h: 24}
	p.table = footer.NewTable([]string{"#", "first row", "rows", "compressed", "", "ratio"},
		[]bool{true, true, true, true, false, true})
	p.kv = p.kvSection()
	return p
}

func (p *Pane) dim() styled.Style { return p.env.Look.Style("dim") }

// Keys implements kit.Pane (Python's KEYS["tab-meta"]; the chrome adds the
// tabs, help and quit).
func (p *Pane) Keys() []kit.KeyHint {
	return []kit.KeyHint{{Key: "↑↓", Help: "scroll"}, {Key: "tab", Help: "next panel"}}
}

// Focus implements kit.Focusable.
func (p *Pane) Focus() tea.Cmd { p.focused = true; return nil }

// Blur implements kit.Focusable.
func (p *Pane) Blur() { p.focused = false }

// CycleFocus implements app.InnerFocus: Tab goes from the file to the row
// groups, then on to the filter (and comes back to the file); Shift+Tab the
// other way.
func (p *Pane) CycleFocus(d int) bool {
	if d > 0 {
		if p.inner == 0 {
			p.inner = 1
			return true
		}
		p.inner = 0
		return false
	}
	if p.inner == 1 {
		p.inner = 0
		return true
	}
	p.inner = 1
	return false
}

// Update implements kit.Pane.
func (p *Pane) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case kit.DoneMsg:
		if m.Tag == footer.Tag {
			if r, ok := m.Msg.(footer.Result); ok {
				return p.onFooter(r)
			}
		}
	case batchMsg:
		if m.p == p && m.gen == p.gen {
			return p.addBatch()
		}
	case tea.KeyPressMsg:
		p.onKey(m.String())
	case tea.MouseMsg:
		p.onMouse(m)
	}
	return nil
}

func (p *Pane) onFooter(r footer.Result) tea.Cmd {
	if r.Err != nil {
		var t styled.Text
		t.Append("✗", styled.Style{Fg: "red"})
		t.Append(" Couldn't read the footer's statistics", styled.Style{})
		t.Append("   "+fmtx.Sanitize(r.Err.Error(), false), p.dim())
		p.failed = t
		return nil
	}
	p.read, p.rgs, p.summary = true, r.RowGroups, r.Summary
	p.wrapW = 0 // the overview changed
	p.pending = p.rowGroupRows()
	p.gen++
	return p.addBatch()
}

// addBatch adds the next Batch rows to the table, and asks for the next
// batch while rows are left (Python's _add_rowgroup_rows).
func (p *Pane) addBatch() tea.Cmd {
	n := min(Batch, len(p.pending))
	p.table.Add(p.pending[:n]...)
	p.pending = p.pending[n:]
	if len(p.pending) == 0 {
		p.pending = nil
		return nil
	}
	gen := p.gen
	return tea.Tick(BatchEvery, func(time.Time) tea.Msg { return batchMsg{p, gen} })
}

// rowGroupRows are the table's rows (Python's _rowgroup_rows).
func (p *Pane) rowGroupRows() [][]styled.Text {
	d := p.dim()
	right := func(s string, st styled.Style) styled.Text {
		return styled.Text{Plain: s, Style: st, Justify: styled.Right}
	}
	var cmax int64
	for _, r := range p.rgs {
		cmax = max(cmax, r.Compressed)
	}
	if cmax == 0 {
		cmax = 1
	}
	var rows [][]styled.Text
	for _, r := range p.rgs[:min(len(p.rgs), MaxRowGroups)] {
		n := int(math.RoundToEven(10 * float64(r.Compressed) / float64(cmax)))
		n = max(0, min(10, n))
		var bar styled.Text
		bar.Append(strings.Repeat("▰", n), styled.Style{})
		bar.Append(strings.Repeat("▱", 10-n), d)
		ratio := ""
		if r.Compressed != 0 {
			ratio = strconv.FormatFloat(float64(r.Uncompressed)/float64(r.Compressed), 'f', 2, 64) + "×"
		}
		rows = append(rows, []styled.Text{
			right(strconv.Itoa(r.Index), d),
			right(footer.Commas(r.Start), styled.Style{}),
			right(footer.Commas(r.Rows), styled.Style{}),
			right(fmtx.HumanBytes(float64(r.Compressed)), styled.Style{}),
			bar,
			right(ratio, d),
		})
	}
	return rows
}

// kvSection is the key-value metadata as Python's _init_meta_tab shows it:
// each value that is JSON pretty-printed under its key, other values after
// it on one line, ARROW:schema summarised. Everything from the file is
// sanitized.
func (p *Pane) kvSection() []styled.Text {
	d := p.dim()
	cyan := styled.Style{Fg: "cyan"}
	parts := []styled.Text{{}, styled.New("key-value metadata", styled.Style{Bold: true})}
	S := func(s string) string { return fmtx.Sanitize(s, false) }
	for _, kv := range p.env.DS.KeyValueMetadata() {
		if kv.Key == "ARROW:schema" {
			var t styled.Text
			t.Append(kv.Key, cyan)
			t.Append("   "+fmtx.HumanBytes(float64(len(kv.Value)))+" · decoded in Schema", d)
			parts = append(parts, t)
			continue
		}
		v := []rune(kv.Value)
		if len(v) < MaxKV {
			if js, ok := footer.PrettyJSON(kv.Value); ok {
				parts = append(parts, styled.New(S(kv.Key), cyan),
					styled.New(fmtx.Sanitize(js, true), styled.Style{}))
				continue
			}
		}
		val := kv.Value
		if len(v) >= MaxKV {
			val = string(v[:MaxKV]) + " …"
		}
		var t styled.Text
		t.Append(S(kv.Key), cyan)
		t.Append("   "+fmtx.Sanitize(val, true), styled.Style{})
		parts = append(parts, t)
	}
	if len(parts) == 2 {
		parts = append(parts, styled.New("none", d))
	}
	return parts
}

// overview is the file's overview, as (label, value) rows (Python's
// _render_meta_overview).
func (p *Pane) overview() [][2]styled.Text {
	ds, d := p.env.DS, p.dim()
	info := ds.Info()
	nrg := int64(len(ds.RowGroups()))
	var groups, dataT styled.Text
	groups.Append(footer.Commas(nrg), styled.Style{})
	if !p.read {
		dataT.Append("…", d)
	} else {
		var rows, comp, unc int64
		for _, r := range p.rgs {
			rows += r.Rows
			comp += r.Compressed
			unc += r.Uncompressed
		}
		avg := 0.0
		if len(p.rgs) > 0 {
			avg = float64(rows) / float64(len(p.rgs))
		}
		groups.Append("   ~"+footer.CommasF(avg)+" rows each", d)
		dataT.Append(fmtx.HumanBytes(float64(comp)), styled.Style{})
		rest := "   compressed  ·  " + fmtx.HumanBytes(float64(unc)) + " raw"
		if comp != 0 {
			rest += "  ·  " + strconv.FormatFloat(float64(unc)/float64(comp), 'f', 2, 64) + "×"
		}
		dataT.Append(rest, d)
	}
	path := ds.Path()
	var pathT, sizeT, colsT styled.Text
	pathT.Append(fmtx.Sanitize(filepath.Base(path), false), styled.Style{Fg: "cyan"})
	pathT.Append("   "+fmtx.Sanitize(filepath.Dir(path), false)+"/", d)
	sizeT.Append(fmtx.HumanBytes(float64(info.Size)), styled.Style{})
	sizeT.Append("   "+footer.Commas(info.Size)+" bytes", d)
	colsT.Append(strconv.Itoa(len(ds.Columns())), styled.Style{})
	colsT.Append("   "+strconv.Itoa(info.NumLeaves)+" leaf", d)
	created := info.CreatedBy
	if created == "" {
		created = "?"
	}
	plain := func(s string) styled.Text { return styled.New(s, styled.Style{}) }
	l := func(s string) styled.Text { return styled.New(s, d) }
	return [][2]styled.Text{
		{l("path"), pathT},
		{l("file size"), sizeT},
		{l("rows"), plain(footer.Commas(ds.NumRows()))},
		{l("columns"), colsT},
		{l("row groups"), groups},
		{l("data"), dataT},
		{l("format"), plain(info.FormatVersion)},
		{l("created by"), plain(fmtx.Sanitize(created, false))},
		{l("footer"), plain(fmtx.HumanBytes(float64(info.FooterSize)))},
	}
}

// fileLines are the file panel's lines for an inside of w cells: the
// overview (labels in a column, values wrapped beside them), then the
// key-value metadata, wrapped.
func (p *Pane) fileLines(w int) []string {
	if p.wrapW == w && p.lines != nil {
		return p.lines
	}
	look := p.env.Look
	var out []string
	ov := p.overview()
	kw := 0
	for _, r := range ov {
		kw = max(kw, footer.Width(r[0].Plain))
	}
	vw := max(1, w-kw-2)
	for _, r := range ov {
		for i, l := range footer.Wrap(r[1], vw) {
			k := ""
			if i == 0 {
				k = look.Render(r[0].Lines()[0])
				k = footer.Pad(k, footer.Width(r[0].Plain), kw, styled.Left)
			} else {
				k = strings.Repeat(" ", kw)
			}
			out = append(out, k+"  "+look.Render(l))
		}
	}
	for _, t := range p.kv {
		for _, l := range footer.Wrap(t, w) {
			out = append(out, look.Render(l))
		}
	}
	p.wrapW, p.lines = w, out
	return out
}

func (p *Pane) fileInner() int { return max(1, p.leftW()-6) }
func (p *Pane) fileRows() int  { return max(1, p.h-2) }
func (p *Pane) leftW() int     { return max(1, (p.w-1)/2) }
func (p *Pane) tableRows() int { return max(1, p.h-2-2-1) } // borders, status, header

func (p *Pane) scrollFile(d int) {
	n := len(p.fileLines(p.fileInner()))
	p.top = max(0, min(p.top+d, n-p.fileRows()))
}

func (p *Pane) onKey(k string) {
	if p.inner == 1 {
		p.table.Key(k, p.tableRows(), max(1, p.w-p.leftW()-1-4))
		return
	}
	switch k {
	case "up", "k":
		p.scrollFile(-1)
	case "down", "j":
		p.scrollFile(1)
	case "pgup":
		p.scrollFile(-p.fileRows())
	case "pgdown", "space":
		p.scrollFile(p.fileRows())
	case "home", "ctrl+home":
		p.top = 0
	case "end", "ctrl+end":
		p.scrollFile(math.MaxInt32)
	}
}

func (p *Pane) onMouse(msg tea.MouseMsg) {
	m := msg.Mouse()
	panel := 0
	if m.X > p.leftW() {
		panel = 1
	} else if m.X == p.leftW() {
		return // the gap
	}
	switch msg.(type) {
	case tea.MouseWheelMsg:
		d := 0
		switch m.Button {
		case tea.MouseWheelUp:
			d = -1
		case tea.MouseWheelDown:
			d = 1
		}
		if panel == 0 {
			p.scrollFile(d)
		} else {
			p.table.ScrollBy(d, p.tableRows())
		}
	case tea.MouseClickMsg:
		if m.Button != tea.MouseLeft {
			return
		}
		p.inner = panel
		if panel == 1 && m.Y >= 2 {
			if row := p.table.Top + m.Y - 2; row < p.table.Len() {
				p.table.MoveTo(row, p.tableRows())
			}
		}
	}
}

// status is the line under the row-group table.
func (p *Pane) status() styled.Text {
	if p.failed.Plain != "" {
		return p.failed
	}
	d := p.dim()
	info := p.env.DS.Info()
	n := footer.Commas(int64(len(p.env.DS.RowGroups())))
	var t styled.Text
	if !p.read {
		t.Append("Reading the footer …", styled.Style{})
		t.Append("   "+fmtx.HumanBytes(float64(info.FooterSize))+"  ·  "+n+" row groups", d)
		return t
	}
	stats := 0
	for _, s := range p.summary {
		if s.HasStats {
			stats++
		}
	}
	t.Append("✓", styled.Style{Fg: "green"})
	t.Append(" Footer read", styled.Style{})
	t.Append("   "+fmtx.HumanBytes(float64(info.FooterSize))+"  ·  "+n+" row groups  ·  stats on "+
		strconv.Itoa(stats)+"/"+strconv.Itoa(info.NumLeaves)+" columns", d)
	return t
}

// View implements kit.Pane; the root draws Panels instead.
func (p *Pane) View(w, h int) string { return p.Panels(w+4, h+2)[0].Content }

// Panels implements app.Paneled: the file on the left, the row groups on
// the right, one column apart (Python's #meta-file and #meta-rg-panel).
func (p *Pane) Panels(w, h int) []app.Panel {
	p.w, p.h = w, h
	look := p.env.Look
	lw := p.leftW()
	rw := max(1, w-lw-1)

	// the file: two cells of padding (one more than the root's)
	iw := p.fileInner()
	lines := p.fileLines(iw)
	p.top = max(0, min(p.top, len(lines)-p.fileRows()))
	var left []string
	for i := p.top; i < len(lines) && len(left) < p.fileRows(); i++ {
		left = append(left, " "+footer.Fit(lines[i], iw)+" ")
	}

	// the row groups: the table, a blank line, the status
	tw := max(1, rw-4)
	th := max(1, h-2-2)
	cur := styled.Style{}
	if p.table.Len() > 0 {
		cur = look.Style("cursor")
	}
	right := p.table.View(look, tw, th, cur)
	right = append(right, "", look.Render(footer.Wrap(p.status(), tw)[0]))

	nrg := len(p.env.DS.RowGroups())
	if p.read {
		nrg = len(p.rgs)
	}
	return []app.Panel{
		{X: 0, Y: 0, W: lw, H: h, Focused: p.inner == 0, Content: strings.Join(left, "\n")},
		{X: lw + 1, Y: 0, W: rw, H: h, Focused: p.inner == 1,
			Title:   styled.New("row groups  "+footer.Commas(int64(nrg)), p.dim()),
			Content: strings.Join(right, "\n")},
	}
}

// RowGroups is the number of rows in the row-group table so far.
func (p *Pane) RowGroups() int { return p.table.Len() }

// RowGroupCell is the text of row i's cell j in the row-group table.
func (p *Pane) RowGroupCell(i, j int) string { return p.table.Row(i)[j].Plain }

// Cursor is the row-group table's cursor row.
func (p *Pane) Cursor() int { return p.table.Cursor }

// Status is the line under the row-group table.
func (p *Pane) Status() string { return p.status().Plain }

// FocusedPanel reports whether panel i (0 the file, 1 the row groups) has
// the focus within the tab.
func (p *Pane) FocusedPanel(i int) bool { return p.inner == i }

// FileText is the file panel's lines for an inside of w cells, unstyled.
func (p *Pane) FileText(w int) []string {
	var out []string
	for _, l := range p.fileLines(w) {
		out = append(out, ansi.Strip(l))
	}
	return out
}
