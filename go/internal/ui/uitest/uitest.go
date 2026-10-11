// Package uitest helps test UI parts: a dataset fake fed from the golden
// files (Python pqx's outputs on the demo fixture), an Env, and a driver
// that runs the root model and its commands synchronously. Only tests
// import it.
package uitest

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/fmtx"
	"github.com/mjuric/pqx/go/internal/golden"
	"github.com/mjuric/pqx/go/internal/ui/app"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// Call is one call of a fake's analysis method.
type Call struct {
	Method string // "stats", "hist", "sky", "xy"
	Cols   []string
	View   data.View
	Sample data.Sample
	Opts   any
}

// FakeDS is the demo fixture as Python pqx sees it: columns, column stats,
// histograms, sky and x-y counts from go/testdata/golden/data_demo.json.
// Stats, histograms and counts the golden file lacks are made up. Calls
// are recorded.
type FakeDS struct {
	data.Unimplemented
	Cols  []data.Column
	stats []statsRec
	xy    []xyRec
	sky   []skyRec
	// Err, if set, is returned by every analysis call.
	Err error

	mu    sync.Mutex
	calls []Call
}

type statsRec struct {
	where, sql, col string
	sample          int64
	st              data.ColumnStats
}

type xyRec struct {
	where, x, y string
	nx, ny      int
	g           data.Grid2D
}

type skyRec struct {
	where string
	res   float64
	g     data.Grid2D
}

// Path implements data.Dataset.
func (f *FakeDS) Path() string { return "/x/demo.parquet" }

// NumRows implements data.Dataset.
func (f *FakeDS) NumRows() int64 { return 20000 }

// Columns implements data.Dataset.
func (f *FakeDS) Columns() []data.Column { return f.Cols }

// Calls are the analysis calls so far.
func (f *FakeDS) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// StatsCalls are the columns profiled so far, in order.
func (f *FakeDS) StatsCalls() []string {
	var out []string
	for _, c := range f.Calls() {
		if c.Method == "stats" {
			out = append(out, c.Cols[0])
		}
	}
	return out
}

func (f *FakeDS) record(c Call) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
}

// arrowType maps the golden file's PyArrow type names to Arrow types.
func arrowType(s string) arrow.DataType {
	switch s {
	case "int64":
		return arrow.PrimitiveTypes.Int64
	case "int16":
		return arrow.PrimitiveTypes.Int16
	case "double":
		return arrow.PrimitiveTypes.Float64
	case "float":
		return arrow.PrimitiveTypes.Float32
	case "bool":
		return arrow.FixedWidthTypes.Boolean
	case "string":
		return arrow.BinaryTypes.String
	case "timestamp[us, tz=UTC]":
		return &arrow.TimestampType{Unit: arrow.Microsecond, TimeZone: "UTC"}
	case "timestamp[ms, tz=UTC]":
		return &arrow.TimestampType{Unit: arrow.Millisecond, TimeZone: "UTC"}
	}
	if strings.HasPrefix(s, "dictionary<values=string") {
		return &arrow.DictionaryType{IndexType: arrow.PrimitiveTypes.Int32, ValueType: arrow.BinaryTypes.String}
	}
	panic("uitest: no Arrow type for " + s)
}

// Value converts a golden value (the scalar kinds) to a data.Value.
func Value(v golden.Value) data.Value {
	switch v.Kind {
	case golden.KindNull:
		return nil
	case golden.KindInt:
		return v.Int.Int64()
	case golden.KindUint:
		return v.Int.Uint64()
	case golden.KindF64:
		return v.Float
	case golden.KindF32:
		return v.Float32
	case golden.KindBool:
		return v.Bool
	case golden.KindStr:
		return v.Str
	case golden.KindTS:
		return data.Timestamp{T: v.Time, Zoned: v.TZ != "", Unit: time.Microsecond}
	}
	panic("uitest: no conversion for " + v.Kind)
}

func pf(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

func fptr(v golden.Value) *float64 {
	if v.IsNull() {
		return nil
	}
	x := v.Float
	return &x
}

// Demo is the fake of the demo fixture.
func Demo(t testing.TB) *FakeDS {
	t.Helper()
	g := golden.Load(t, "data_demo.json")
	f := &FakeDS{}
	for _, r := range g.Section(t, "columns") {
		f.Cols = append(f.Cols, data.Column{Name: r.String(t, "name"), Type: r.String(t, "duckdb_type"),
			Arrow: arrowType(r.String(t, "arrow_type")), Nullable: r.Bool(t, "nullable"),
			Unit: r.String(t, "unit"), Description: r.String(t, "description"), SQLName: r.String(t, "sql_name")})
	}
	type view struct {
		Where string `json:"where"`
		SQL   string `json:"sql"`
	}
	for _, r := range g.Section(t, "column_stats") {
		var v view
		r.Decode(t, "view", &v)
		var sample int64
		if !r.IsNull("sample") {
			sample = int64(r.Int(t, "sample"))
		}
		var o struct {
			Name      string               `json:"name"`
			Count     int64                `json:"count"`
			Nulls     int64                `json:"nulls"`
			NaNs      *int64               `json:"nans"`
			Distinct  *int64               `json:"distinct"`
			Min       golden.Value         `json:"min"`
			Max       golden.Value         `json:"max"`
			Mean      golden.Value         `json:"mean"`
			Std       golden.Value         `json:"std"`
			Quantiles [][2]json.RawMessage `json:"quantiles"`
			Top       [][2]json.RawMessage `json:"top"`
			Sampled   bool                 `json:"sampled"`
		}
		r.Decode(t, "out", &o)
		st := data.ColumnStats{Name: o.Name, Count: o.Count, Nulls: o.Nulls, NaNs: -1, Distinct: -1,
			Min: Value(o.Min), Max: Value(o.Max), Mean: fptr(o.Mean), Std: fptr(o.Std), Sampled: o.Sampled,
			Quantiles: map[float64]float64{}}
		if o.NaNs != nil {
			st.NaNs = *o.NaNs
		}
		if o.Distinct != nil {
			st.Distinct = *o.Distinct
		}
		for _, q := range o.Quantiles {
			var p float64
			var gv golden.Value
			_ = json.Unmarshal(q[0], &p)
			_ = json.Unmarshal(q[1], &gv)
			st.Quantiles[p] = gv.Float
		}
		for _, tv := range o.Top {
			var gv golden.Value
			var n int64
			_ = json.Unmarshal(tv[0], &gv)
			_ = json.Unmarshal(tv[1], &n)
			st.Top = append(st.Top, data.ValueCount{Value: Value(gv), Count: n})
		}
		st.DistinctExact = len(st.Top) > 0 && len(st.Top) < 10
		f.stats = append(f.stats, statsRec{v.Where, v.SQL, r.String(t, "column"), sample, st})
	}
	for _, r := range g.Section(t, "xy_counts") {
		var v view
		r.Decode(t, "view", &v)
		var o struct {
			Grid [][]int64 `json:"grid"`
			XLim [2]string `json:"xlim"`
			YLim [2]string `json:"ylim"`
		}
		r.Decode(t, "out", &o)
		f.xy = append(f.xy, xyRec{v.Where, r.String(t, "x"), r.String(t, "y"), r.Int(t, "nx"), r.Int(t, "ny"),
			data.Grid2D{Counts: o.Grid, X: [2]float64{pf(o.XLim[0]), pf(o.XLim[1])}, Y: [2]float64{pf(o.YLim[0]), pf(o.YLim[1])}}})
	}
	for _, r := range g.Section(t, "sky_counts") {
		var v view
		r.Decode(t, "view", &v)
		var counts [][]int64
		r.Decode(t, "out", &counts)
		f.sky = append(f.sky, skyRec{v.Where, r.Float(t, "res_deg"), data.Grid2D{Counts: counts, X: [2]float64{0, 360}, Y: [2]float64{-90, 90}}})
	}
	return f
}

// ColumnStats implements data.Dataset: the golden profile of col in the
// view (any sample if none matches), or a made-up one.
func (f *FakeDS) ColumnStats(ctx context.Context, v data.View, col string, s data.Sample) (data.ColumnStats, error) {
	f.record(Call{Method: "stats", Cols: []string{col}, View: v, Sample: s})
	if f.Err != nil {
		return data.ColumnStats{}, f.Err
	}
	var best *statsRec
	for i := range f.stats {
		r := &f.stats[i]
		if r.col == col && r.where == v.Where && r.sql == v.SQL {
			if best == nil || r.sample == s.Rows {
				best = r
			}
		}
	}
	if best != nil {
		st := best.st
		if s.Rows > 0 {
			st.Sampled = true
		}
		return st, nil
	}
	mean, std := 1.5, 0.5
	return data.ColumnStats{Name: col, Count: 100, NaNs: -1, Distinct: 100, Min: 1.0, Max: 2.0, Mean: &mean, Std: &std,
		Quantiles: map[float64]float64{0.01: 1, 0.05: 1.05, 0.25: 1.25, 0.5: 1.5, 0.75: 1.75, 0.95: 1.95, 0.99: 1.99},
		Sampled:   s.Rows > 0}, nil
}

// Histogram implements data.Dataset: o.Bins bins of a made-up shape.
func (f *FakeDS) Histogram(ctx context.Context, v data.View, col string, o data.HistOptions) (data.Histogram, error) {
	f.record(Call{Method: "hist", Cols: []string{col}, View: v, Sample: o.Sample, Opts: o})
	if f.Err != nil {
		return data.Histogram{}, f.Err
	}
	n := max(1, o.Bins)
	h := data.Histogram{Edges: make([]float64, n+1), Counts: make([]int64, n)}
	for i := range h.Edges {
		h.Edges[i] = float64(i)
	}
	for i := range h.Counts {
		x := (float64(i) - float64(n)/2) / (float64(n) / 6)
		h.Counts[i] = int64(1000 * math.Exp(-x*x/2))
	}
	return h, nil
}

// SkyCounts implements data.Dataset: the golden 10° counts, each in one
// cell of the requested grid.
func (f *FakeDS) SkyCounts(ctx context.Context, v data.View, lon, lat string, res float64, s data.Sample) (data.Grid2D, error) {
	f.record(Call{Method: "sky", Cols: []string{lon, lat}, View: v, Sample: s, Opts: res})
	if f.Err != nil {
		return data.Grid2D{}, f.Err
	}
	nx, ny := int(math.Round(360/res)), int(math.Round(180/res))
	g := data.Grid2D{Counts: make([][]int64, ny), X: [2]float64{0, 360}, Y: [2]float64{-90, 90}}
	for i := range g.Counts {
		g.Counts[i] = make([]int64, nx)
	}
	for _, r := range f.sky {
		if r.where != v.Where || r.res != 10 {
			continue
		}
		for iy, row := range r.g.Counts {
			for ix, c := range row {
				g.Counts[min(ny-1, int(float64(iy)*10/res))][min(nx-1, int(float64(ix)*10/res))] += c
			}
		}
		break
	}
	return g, nil
}

// XYCounts implements data.Dataset: the golden mag × snr counts if they
// fit, else a made-up nx × ny grid.
func (f *FakeDS) XYCounts(ctx context.Context, v data.View, x, y string, nx, ny int, s data.Sample, xlim, ylim *[2]float64) (data.Grid2D, error) {
	f.record(Call{Method: "xy", Cols: []string{x, y}, View: v, Sample: s, Opts: [2]int{nx, ny}})
	if f.Err != nil {
		return data.Grid2D{}, f.Err
	}
	for _, r := range f.xy {
		if r.where == v.Where && r.x == x && r.y == y && r.nx == nx && r.ny == ny {
			return r.g, nil
		}
	}
	g := data.Grid2D{Counts: make([][]int64, ny), X: [2]float64{0, 1}, Y: [2]float64{0, 1}}
	for i := range g.Counts {
		g.Counts[i] = make([]int64, nx)
		for j := range g.Counts[i] {
			g.Counts[i][j] = int64((i + j) % 7)
		}
	}
	return g, nil
}

// Env is an Env over ds with the basic look, its columns and row count.
func Env(ds data.Dataset) *kit.Env {
	return &kit.Env{DS: ds, Look: app.BasicLook{}, Tasks: kit.NewTasks(),
		State: &kit.State{Columns: ds.Columns(), Total: ds.NumRows(), Formats: map[string]fmtx.Override{}}}
}

// Driver runs the root model with its commands synchronously (ticks
// included: a debounce waits its time), drawing after each message.
type Driver struct {
	T    testing.TB
	App  *app.App
	Env  *kit.Env
	Msgs []tea.Msg // every message delivered, in order
}

// NewDriver makes the root model over env and parts, sized w × h.
func NewDriver(t testing.TB, env *kit.Env, parts app.Parts, w, h int) *Driver {
	d := &Driver{T: t, App: app.New(env, parts), Env: env}
	d.Send(tea.WindowSizeMsg{Width: w, Height: h})
	return d
}

// Send delivers msg and everything its commands yield.
func (d *Driver) Send(msg tea.Msg) {
	queue := []tea.Msg{msg}
	for n := 0; len(queue) > 0; n++ {
		if n > 10000 {
			d.T.Fatal("uitest: too many messages")
		}
		m := queue[0]
		queue = queue[1:]
		d.Msgs = append(d.Msgs, m)
		_, cmd := d.App.Update(m)
		d.App.View() // drawn after each update, as Bubble Tea does
		queue = append(queue, Drain(cmd)...)
	}
}

// Drain runs cmd and the commands it batches, returning their messages.
func Drain(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if b, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range b {
			out = append(out, Drain(c)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

// Key is the key press for a key name ("esc", "tab", "shift+tab", "enter",
// "space", "up", "down", "left", "right", "pgup", "pgdown", "home", "end",
// "backspace", "ctrl+right", "ctrl+left") or a character.
func Key(s string) tea.KeyPressMsg {
	codes := map[string]rune{"esc": tea.KeyEscape, "tab": tea.KeyTab, "enter": tea.KeyEnter, "space": tea.KeySpace,
		"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight, "pgup": tea.KeyPgUp,
		"pgdown": tea.KeyPgDown, "home": tea.KeyHome, "end": tea.KeyEnd, "backspace": tea.KeyBackspace}
	if c, ok := codes[s]; ok {
		k := tea.KeyPressMsg{Code: c}
		if s == "space" {
			k.Text = " "
		}
		return k
	}
	switch s {
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "ctrl+right":
		return tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModCtrl}
	case "ctrl+left":
		return tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModCtrl}
	}
	r := []rune(s)
	if len(r) != 1 {
		panic(fmt.Sprintf("uitest: unknown key %q", s))
	}
	return tea.KeyPressMsg{Code: r[0], Text: s}
}

// Press sends key presses.
func (d *Driver) Press(keys ...string) {
	for _, k := range keys {
		d.Send(Key(k))
	}
}

// Type sends each character of s as a key press.
func (d *Driver) Type(s string) {
	for _, r := range s {
		d.Send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// Click clicks screen cell (x, y).
func (d *Driver) Click(x, y int) {
	d.Send(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
}

// Screen is the screen's text, one string per row, trailing blanks dropped.
func (d *Driver) Screen() []string {
	l := strings.Split(ansi.Strip(d.App.View().Content), "\n")
	for i := range l {
		l[i] = strings.TrimRight(l[i], " ")
	}
	return l
}

// Find is the first screen row containing s and its column (in cells),
// or -1, -1.
func (d *Driver) Find(s string) (x, y int) {
	for i, l := range d.Screen() {
		if j := strings.Index(l, s); j >= 0 {
			return ansi.StringWidth(l[:j]), i
		}
	}
	return -1, -1
}

// Notices are the texts of the NotifyMsgs delivered so far.
func (d *Driver) Notices() []string {
	var out []string
	for _, m := range d.Msgs {
		if n, ok := m.(kit.NotifyMsg); ok {
			out = append(out, n.Text)
		}
	}
	return out
}

// SendTo gives msg to pane p directly (bypassing the root's key routing),
// then delivers what its commands yield through the root.
func (d *Driver) SendTo(p kit.Pane, msg tea.Msg) {
	for _, m := range Drain(p.Update(msg)) {
		d.Send(m)
	}
}

// Dialog is the dialog opened last (nil if none was).
func (d *Driver) Dialog() kit.Dialog {
	for i := len(d.Msgs) - 1; i >= 0; i-- {
		if m, ok := d.Msgs[i].(kit.OpenDialogMsg); ok {
			return m.Dialog
		}
	}
	return nil
}

// Count is how many messages of msg's type were delivered.
func (d *Driver) Count(msg tea.Msg) int {
	n := 0
	for _, m := range d.Msgs {
		if fmt.Sprintf("%T", m) == fmt.Sprintf("%T", msg) {
			n++
		}
	}
	return n
}
