// Package schema is the Schema tab (Python pqx's _init_schema_tab,
// _build_schema_tab, schema_row): a table of the file's columns with their
// type, unit, nulls, min and max, size and compression ratio from the
// footer, and below it the current column's description. The table follows
// the linked current column and moves it; Enter shows the column's
// statistics.
package schema

import (
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/app"
	"github.com/mjuric/pqx/go/internal/ui/footer"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// info is what the description needs of a column once the footer is read.
type info struct {
	index int
	size  int64
	ratio string // "12.6×", or "" for an empty column
	comp  string // codec, lower case
	leaf  bool   // a leaf column (encodings are per leaf)
}

// Pane is the Schema tab's body.
type Pane struct {
	env    *kit.Env
	reader *footer.Reader
	cols   []data.Column

	table  *footer.Table
	info   map[string]info
	built  bool
	failed styled.Text // the footer couldn't be read
	encs   map[string]string

	focused bool
	w, h    int // the body's size when last drawn
}

var (
	_ app.Paneled   = (*Pane)(nil)
	_ kit.Focusable = (*Pane)(nil)
)

// New makes the Schema tab. It passes every message it gets to reader,
// which starts the footer pass (the Metadata tab uses its result too).
func New(env *kit.Env, reader *footer.Reader) *Pane {
	labels := []string{"#", "column", "type", "unit", "nulls", "null %", "min", "max", "size", "ratio"}
	right := []bool{true, false, false, false, true, true, true, true, true, true}
	return &Pane{env: env, reader: reader, cols: env.DS.Columns(),
		table: footer.NewTable(labels, right), info: map[string]info{}, encs: map[string]string{},
		w: 80, h: 24}
}

func (p *Pane) dim() styled.Style { return p.env.Look.Style("dim") }

// Keys implements kit.Pane (Python's KEYS["tab-schema"]; the chrome adds
// the tabs, help and quit).
func (p *Pane) Keys() []kit.KeyHint {
	return []kit.KeyHint{{Key: "↑↓", Help: "column"}, {Key: "enter", Help: "stats"}, {Key: "/", Help: "filter"}}
}

// Focus implements kit.Focusable: the tab is shown, so the table moves to
// the current column.
func (p *Pane) Focus() tea.Cmd {
	p.focused = true
	p.sync()
	return nil
}

// Blur implements kit.Focusable.
func (p *Pane) Blur() { p.focused = false }

// Update implements kit.Pane.
func (p *Pane) Update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	if p.reader != nil {
		cmd = p.reader.Update(msg)
	}
	switch m := msg.(type) {
	case kit.DoneMsg:
		if m.Tag == footer.Tag {
			if r, ok := m.Msg.(footer.Result); ok {
				p.onFooter(r)
			}
		}
	case kit.ColumnChangedMsg:
		if m.From != "schema" {
			p.sync()
		}
	case tea.KeyPressMsg:
		return tea.Batch(cmd, p.onKey(m.String()))
	case tea.MouseMsg:
		return tea.Batch(cmd, p.onMouse(m))
	}
	return cmd
}

func (p *Pane) onFooter(r footer.Result) {
	if r.Err != nil {
		var t styled.Text
		t.Append("✗", styled.Style{Fg: "red"})
		t.Append(" Couldn't read the footer's statistics", styled.Style{})
		t.Append("   "+fmtx.Sanitize(r.Err.Error(), false), p.dim())
		p.failed = t
		return
	}
	p.build(r.Summary)
}

// build fills the table (Python's _build_schema_tab). Building doesn't move
// the current column; the table goes to it.
func (p *Pane) build(summary []data.ChunkSummary) {
	d := p.dim()
	summ := make(map[string]data.ChunkSummary, len(summary))
	for _, s := range summary {
		summ[s.Path] = s
	}
	dash := func() styled.Text { return styled.Text{Plain: "–", Style: d, Justify: styled.Right} }
	right := func(s string, st styled.Style) styled.Text {
		return styled.Text{Plain: s, Style: st, Justify: styled.Right}
	}
	rows := make([][]styled.Text, 0, len(p.cols))
	numRows := p.env.DS.NumRows()
	for i, c := range p.cols {
		s, leaf := summ[c.Name]
		var size, usize int64
		var mn, mx data.Value
		nulls := int64(-1)
		comp := ""
		if !leaf { // nested: add up the leaves
			first := true
			for _, v := range summary {
				if top, _, _ := strings.Cut(v.Path, "."); top == c.Name {
					if first {
						comp, first = v.Compression, false
					}
					size += v.Compressed
					usize += v.Uncompressed
				}
			}
		} else {
			size, usize = s.Compressed, s.Uncompressed
			mn, mx = s.Min, s.Max
			if s.HasStats {
				nulls = s.Nulls
			}
			comp = s.Compression
		}
		ratio := ""
		if size != 0 {
			ratio = strconv.FormatFloat(float64(usize)/float64(size), 'f', 1, 64) + "×"
		}
		p.info[c.Name] = info{index: i, size: size, ratio: ratio, comp: strings.ToLower(comp), leaf: leaf}

		var nullN, nullP styled.Text
		switch {
		case nulls < 0:
			nullN = dash()
		case nulls > 0:
			nullN = right(footer.Commas(nulls), styled.Style{})
			nullP = right(fmtx.Percent(float64(nulls), float64(numRows)), styled.Style{})
		default:
			nullN = right("0", d)
		}
		unit := styled.New("–", d)
		if c.Unit != "" {
			unit = styled.New(fmtx.Sanitize(c.Unit, false), styled.Style{})
		}
		kind := fmtx.KindFor(c.Name, c.Arrow, c.Unit)
		mm := func(v data.Value) styled.Text {
			if v == nil {
				return dash()
			}
			return fmtx.Cell(v, kind, fmtx.Opts{Width: fmtx.DefaultWidth})
		}
		rows = append(rows, []styled.Text{
			right(strconv.Itoa(i), d),
			styled.New(fmtx.Sanitize(c.Name, false), styled.Style{Bold: true}),
			styled.New(fmtx.ShortType(c.Arrow), d),
			unit, nullN, nullP, mm(mn), mm(mx),
			right(fmtx.HumanBytes(float64(size)), styled.Style{}),
			right(ratio, d),
		})
	}
	p.table.Add(rows...)
	p.built = true
	p.sync()
}

// sync puts the cursor on the current column, if it is the file's (a SQL
// result's own columns leave the table where it is). It doesn't change the
// current column.
func (p *Pane) sync() {
	if !p.built {
		return
	}
	if inf, ok := p.info[p.env.State.Current]; ok && p.table.Cursor != inf.index {
		p.table.MoveTo(inf.index, p.tableRows())
	}
}

// current is the column under the cursor, if the table is built.
func (p *Pane) current() (data.Column, bool) {
	if !p.built || p.table.Len() == 0 {
		return data.Column{}, false
	}
	return p.cols[p.table.Cursor], true
}

// moved announces the cursor's column as the current one if the cursor moved.
func (p *Pane) moved(before int) tea.Cmd {
	c, ok := p.current()
	if !ok || p.table.Cursor == before {
		return nil
	}
	p.env.State.Current = c.Name
	return kit.Send(kit.ColumnChangedMsg{From: "schema"})
}

func (p *Pane) onKey(k string) tea.Cmd {
	if !p.built {
		return nil
	}
	if k == "enter" {
		if c, ok := p.current(); ok {
			return kit.Send(kit.ColumnStatsMsg{Column: c.Name})
		}
		return nil
	}
	before := p.table.Cursor
	if p.table.Key(k, p.tableRows(), max(1, p.w-4)) {
		return p.moved(before)
	}
	return nil
}

func (p *Pane) onMouse(msg tea.MouseMsg) tea.Cmd {
	m := msg.Mouse()
	tableH, _ := p.split()
	if m.Y >= tableH || !p.built {
		return nil
	}
	switch msg.(type) {
	case tea.MouseWheelMsg:
		switch m.Button {
		case tea.MouseWheelUp:
			p.table.ScrollBy(-1, p.tableRows())
		case tea.MouseWheelDown:
			p.table.ScrollBy(1, p.tableRows())
		}
	case tea.MouseClickMsg:
		if m.Button != tea.MouseLeft {
			return nil
		}
		row := p.table.Top + m.Y - 2 // the border, the header
		if m.Y < 2 || row >= p.table.Len() {
			return nil
		}
		if row == p.table.Cursor { // a click on the cursor row selects it
			return p.onKey("enter")
		}
		before := p.table.Cursor
		p.table.MoveTo(row, p.tableRows())
		return p.moved(before)
	}
	return nil
}

// desc is the description panel's text and title.
func (p *Pane) desc() (styled.Text, styled.Text) {
	d := p.dim()
	var t styled.Text
	if !p.built {
		if p.failed.Plain != "" {
			return p.failed, styled.Text{}
		}
		info := p.env.DS.Info()
		t.Append("Reading sizes and statistics from the footer …", styled.Style{})
		t.Append("   "+footer.Commas(int64(len(p.env.DS.RowGroups())))+" row groups × "+
			footer.Commas(int64(info.NumLeaves))+" columns", d)
		return t, styled.Text{}
	}
	c, ok := p.current()
	if !ok {
		return t, styled.Text{}
	}
	inf := p.info[c.Name]
	S := func(s string) string { return fmtx.Sanitize(s, false) }
	t.Append(S(c.Name), styled.Style{Bold: true, Fg: "cyan"})
	t.Append("   "+S(arrowName(c.Arrow)), styled.Style{})
	if c.Unit != "" {
		t.Append("   ["+S(c.Unit)+"]", styled.Style{})
	}
	null := "not null"
	if c.Nullable {
		null = "nullable"
	}
	rest := "   " + null + "  ·  " + fmtx.HumanBytes(float64(inf.size))
	if inf.ratio != "" {
		rest += "  ·  " + inf.ratio + " " + inf.comp
	}
	if enc := p.encodings(c.Name, inf); enc != "" {
		rest += "  ·  " + enc
	}
	t.Append(rest, d)
	t.Append("\n", styled.Style{})
	if c.Description != "" {
		t.Append(fmtx.Sanitize(c.Description, true), styled.Style{})
	} else {
		t.Append("no description in the file's field metadata", d)
	}
	t.Append("\n→ enter opens statistics  ·  i from the data grid", d)
	return t, styled.New("column "+strconv.Itoa(inf.index), d)
}

// encodings is a leaf column's encodings, "dict, plain, rle", read once.
func (p *Pane) encodings(name string, inf info) string {
	if !inf.leaf {
		return ""
	}
	if e, ok := p.encs[name]; ok {
		return e
	}
	var l []string
	for _, e := range p.env.DS.Encodings(name) {
		l = append(l, strings.ToLower(strings.ReplaceAll(e, "RLE_DICTIONARY", "dict")))
	}
	slices.Sort(l)
	e := fmtx.Sanitize(strings.Join(l, ", "), false)
	p.encs[name] = e
	return e
}

// descLines is the description wrapped to the panel's inside.
func (p *Pane) descLines(w int) []styled.Text {
	t, _ := p.desc()
	return footer.Wrap(t, max(1, w-4))
}

// split is the heights of the table's and the description's panels in the
// body (one blank row between them): the description is as tall as its
// text, at least 5 rows with its border (Python's #schema-desc), and leaves
// the table at least 4.
func (p *Pane) split() (int, int) {
	descH := max(5, len(p.descLines(p.w))+2)
	descH = max(3, min(descH, p.h-1-4))
	return max(1, p.h-descH-1), descH
}

// tableRows is the number of rows the table shows (borders and header not
// counted).
func (p *Pane) tableRows() int {
	th, _ := p.split()
	return max(1, th-3)
}

// View implements kit.Pane; the root draws Panels instead.
func (p *Pane) View(w, h int) string {
	ps := p.Panels(w, h+2)
	return ps[0].Content
}

// Panels implements app.Paneled.
func (p *Pane) Panels(w, h int) []app.Panel {
	p.w, p.h = w, h
	look := p.env.Look
	tableH, descH := p.split()
	cur := styled.Style{}
	if p.built {
		cur = look.Style("cursor")
	}
	lines := p.table.View(look, max(1, w-4), max(1, tableH-2), cur)
	var dl []string
	for _, l := range p.descLines(w) {
		dl = append(dl, look.Render(l))
	}
	_, title := p.desc()
	return []app.Panel{
		{X: 0, Y: 0, W: w, H: tableH, Focused: true, Content: strings.Join(lines, "\n")},
		{X: 0, Y: h - descH, W: w, H: descH, Title: title, Content: strings.Join(dl, "\n")},
	}
}

// Built reports whether the table has been built from the footer.
func (p *Pane) Built() bool { return p.built }

// Rows is the number of rows in the table.
func (p *Pane) Rows() int { return p.table.Len() }

// Cursor is the cursor's row.
func (p *Pane) Cursor() int { return p.table.Cursor }

// Description is the description panel's text.
func (p *Pane) Description() string { t, _ := p.desc(); return t.Plain }

// Cell is the text of column col's cell under header label (tests).
func (p *Pane) Cell(col, label string) string {
	inf, ok := p.info[col]
	if !ok {
		return ""
	}
	for j, h := range p.table.Header {
		if h.Plain == label {
			return p.table.Row(inf.index)[j].Plain
		}
	}
	return ""
}
