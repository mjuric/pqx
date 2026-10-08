package plot

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/apache/arrow-go/v18/arrow"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/golden"
	"github.com/mjuric/pqx/go/internal/ui/app"
	"github.com/mjuric/pqx/go/internal/ui/kit"
	"github.com/mjuric/pqx/go/internal/ui/uitest"
)

func setup(t *testing.T, ds *uitest.FakeDS) (*uitest.Driver, *Pane) {
	t.Helper()
	env := uitest.Env(ds)
	p := New(env)
	d := uitest.NewDriver(t, env, app.Parts{Plot: p}, 150, 42)
	return d, p
}

// wide is test_app's wide_path: ra, dec and 198 flux columns.
func wide(t *testing.T) *uitest.FakeDS {
	ds := uitest.Demo(t)
	f64 := arrow.PrimitiveTypes.Float64
	ds.Cols = []data.Column{{Name: "ra", Arrow: f64}, {Name: "dec", Arrow: f64}}
	for i := 0; i < 198; i++ {
		ds.Cols = append(ds.Cols, data.Column{Name: fmt.Sprintf("flux_%03d", i), Arrow: f64})
	}
	return ds
}

func lastCall(ds *uitest.FakeDS) uitest.Call {
	c := ds.Calls()
	if len(c) == 0 {
		return uitest.Call{}
	}
	return c[len(c)-1]
}

// key sends a key to the pane itself: the root takes Tab for its focus
// cycle (a contract change is pending), so Tab goes straight to the pane.
func key(d *uitest.Driver, p *Pane, keys ...string) {
	for _, k := range keys {
		d.SendTo(p, uitest.Key(k))
	}
}

// test_tabs_stats_plots (Plot part).
func TestPlotTab(t *testing.T) {
	ds := uitest.Demo(t)
	d, p := setup(t, ds)
	d.Press("4")
	if c := lastCall(ds); c.Method != "sky" || !reflect.DeepEqual(c.Cols, []string{"ra", "dec"}) || c.Opts.(float64) != SkyRes {
		t.Fatalf("first plot %+v", c)
	}
	if !strings.HasPrefix(p.Status(), "✓ Binned ra × dec") || !strings.HasSuffix(p.Status(), "all rows") {
		t.Fatalf("status %q", p.Status())
	}
	if p.Value("colour") != "magma" || !p.focused {
		t.Fatalf("colour %q focused %v", p.Value("colour"), p.focused)
	}
	scr := strings.Join(d.Screen(), "\n")
	if !strings.Contains(scr, "mode sky ▾    lon ra ▾    lat dec ▾    centre 0° ▾    colour magma ▾") {
		t.Fatalf("settings line:\n%s", scr)
	}
	d.Press("left") // mode field: sky -> xy
	if p.Value("mode") != "xy" || lastCall(ds).Method != "xy" || !strings.HasPrefix(p.Status(), "✓ Binned") {
		t.Fatalf("mode %q last %+v status %q", p.Value("mode"), lastCall(ds), p.Status())
	}
	// xy: 2·pw × 2·ph bins for the area
	w, h := p.size()
	if got := lastCall(ds).Opts.([2]int); got != [2]int{2 * max(10, w-12), 2 * max(4, h-5)} {
		t.Fatalf("xy bins %v for %d × %d", got, w, h)
	}
	if !strings.Contains(p.Status(), " bins  ·  ") {
		t.Fatalf("status %q", p.Status())
	}
	key(d, p, "tab", "tab", "tab", "right") // colour field: magma -> viridis
	if p.Value("colour") != "viridis" {
		t.Fatalf("colour %q", p.Value("colour"))
	}
	n := len(ds.Calls())
	d.Press("m") // sampling toggle re-runs the plot
	if !d.Env.State.Sampling || len(ds.Calls()) != n+1 || !strings.HasPrefix(p.Status(), "! Binned ra × dec, sampled") ||
		!strings.HasSuffix(p.Status(), "m scans everything") {
		t.Fatalf("sampling %v status %q", d.Env.State.Sampling, p.Status())
	}
	if lastCall(ds).Sample.Rows != kit.SampleRows {
		t.Fatalf("sample %+v", lastCall(ds).Sample)
	}
}

// test_plot_field_dropdown.
func TestFieldDropdown(t *testing.T) {
	ds := wide(t)
	d, p := setup(t, ds)
	d.Press("4")
	if p.Value("x") != "ra" || p.Value("y") != "dec" {
		t.Fatalf("x %q y %q", p.Value("x"), p.Value("y"))
	}
	// keyboard: tab to the lat field, enter opens the list, typing narrows it
	key(d, p, "tab", "tab", "enter")
	dd, ok := d.Dialog().(*Dropdown)
	if !ok || !dd.Searchable() {
		t.Fatalf("dialog %T", d.Dialog())
	}
	d.Type("flux_17")
	if len(dd.Shown()) != 10 || dd.Title() != "lat  10 of 200" {
		t.Fatalf("shown %d title %q", len(dd.Shown()), dd.Title())
	}
	scr := strings.Join(d.Screen(), "\n")
	if !strings.Contains(scr, "lat  10 of 200") || !strings.Contains(scr, "flux_179") {
		t.Fatalf("drop-down not drawn:\n%s", scr)
	}
	d.Press("down", "enter")
	if d.Count(kit.CloseDialogMsg{}) != 1 || p.Value("y") != "flux_171" {
		t.Fatalf("closed %d y %q", d.Count(kit.CloseDialogMsg{}), p.Value("y"))
	}
	if c := lastCall(ds); !reflect.DeepEqual(c.Cols, []string{"ra", "flux_171"}) {
		t.Fatalf("replot %+v", c)
	}

	// mouse: clicking the lon field's value opens it; esc leaves it unchanged
	x, y := d.Find("ra ▾")
	d.Click(x+1, y)
	if dd2, ok := d.Dialog().(*Dropdown); !ok || dd2 == dd || dd2.Title() != "lon  200" {
		t.Fatalf("click opened %T", d.Dialog())
	}
	_, ty := d.Find("lon  200")
	if ty != y+1 {
		t.Fatalf("drop-down at row %d, field at %d", ty, y)
	}
	d.Press("esc")
	if d.Count(kit.CloseDialogMsg{}) != 2 || p.Value("x") != "ra" {
		t.Fatalf("esc: closed %d x %q", d.Count(kit.CloseDialogMsg{}), p.Value("x"))
	}

	// a short list (colour) opens without a filter line and picks by click
	x, y = d.Find("magma ▾")
	d.Click(x+1, y)
	cd := d.Dialog().(*Dropdown)
	if cd.Searchable() || cd.Highlighted() != "magma" {
		t.Fatalf("colour list searchable %v highlighted %q", cd.Searchable(), cd.Highlighted())
	}
	ix, iy := d.Find("viridis")
	d.Click(ix, iy)
	if p.Value("colour") != "viridis" || d.Count(kit.CloseDialogMsg{}) != 3 {
		t.Fatalf("colour %q", p.Value("colour"))
	}
	// a click outside closes it
	x, y = d.Find("viridis ▾")
	d.Click(x+1, y)
	d.Click(2, 40)
	if d.Count(kit.CloseDialogMsg{}) != 4 || p.Value("colour") != "viridis" {
		t.Fatal("click outside didn't close")
	}
}

type pick struct{ v string }

// The drop-down's keys, filter line and placement.
func TestDropdownKeys(t *testing.T) {
	var opts []string
	for i := 0; i < 30; i++ {
		opts = append(opts, fmt.Sprintf("Col_%02d", i))
	}
	dd := NewDropdown(app.BasicLook{}, "x", opts, "Col_05", 140, 5, func(v string) tea.Msg { return pick{v} })
	send := func(k string) []tea.Msg { return uitest.Drain(dd.Update(uitest.Key(k))) }
	if dd.Highlighted() != "Col_05" {
		t.Fatalf("highlighted %q", dd.Highlighted())
	}
	send("pgdown")
	send("down")
	if dd.Highlighted() != "Col_16" {
		t.Fatalf("pgdown down: %q", dd.Highlighted())
	}
	send("pgup")
	send("pgup")
	if dd.Highlighted() != "Col_00" {
		t.Fatalf("pgup: %q", dd.Highlighted())
	}
	for _, r := range "col_2" { // case-insensitive
		dd.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if len(dd.Shown()) != 10 || dd.Highlighted() != "Col_20" {
		t.Fatalf("filtered %v highlighted %q", dd.Shown(), dd.Highlighted())
	}
	send("backspace")
	if len(dd.Shown()) != 30 || dd.Title() != "x  30 of 30" {
		t.Fatalf("backspace: %d %q", len(dd.Shown()), dd.Title())
	}
	if c := dd.Cursor(); c == nil || c.Position.X != 2+4 || c.Position.Y != 1 {
		t.Fatalf("cursor %+v", c)
	}
	msgs := send("enter")
	if len(msgs) != 2 || msgs[1] != (pick{"Col_05"}) {
		t.Fatalf("enter: %v", msgs)
	}
	// placement: kept on screen, and above the field when there's no room below
	w, h := dd.Size(150, 42)
	if x, y := dd.Position(150, 42); x != 150-w || y != 5 || h != 16+1+2 {
		t.Fatalf("at %d,%d size %d×%d", x, y, w, h)
	}
	low := NewDropdown(app.BasicLook{}, "x", opts, "", 3, 30, func(v string) tea.Msg { return pick{v} })
	if _, y := low.Position(150, 42); y != 30-19-1 {
		t.Fatalf("opened at row %d", y)
	}
	// esc closes without a pick; a short list has no filter line
	short := NewDropdown(app.BasicLook{}, "mode", []string{"sky", "xy"}, "sky", 0, 0, func(v string) tea.Msg { return pick{v} })
	if m := uitest.Drain(short.Update(uitest.Key("esc"))); len(m) != 1 || m[0] != (kit.CloseDialogMsg{}) {
		t.Fatalf("esc: %v", m)
	}
	short.Update(uitest.Key("x")) // no filter: ignored
	short.Update(uitest.Key("end"))
	if m := uitest.Drain(short.Update(uitest.Key("space"))); len(m) != 2 || m[1] != (pick{"xy"}) {
		t.Fatalf("space: %v", m)
	}
	if _, h := short.Size(150, 42); h != 4 {
		t.Fatalf("short height %d", h)
	}
}

// r rotates the sky; the centre field is there only in sky mode.
func TestRotate(t *testing.T) {
	ds := uitest.Demo(t)
	d, p := setup(t, ds)
	d.Press("4", "r")
	if p.Value("centre") != "180°" || !strings.Contains(strings.Join(d.Screen(), "\n"), "centre 180° ▾") {
		t.Fatalf("centre %q", p.Value("centre"))
	}
	d.Press("r")
	if p.Value("centre") != "0°" {
		t.Fatalf("centre %q", p.Value("centre"))
	}
	key(d, p, "left")
	if strings.Contains(strings.Join(d.Screen(), "\n"), "centre") || len(p.ctl.fields()) != 4 {
		t.Fatal("centre shown in xy mode")
	}
}

// Plots are computed only while the tab is shown; x and y stay across views
// while they are still there.
func TestViewsAndVisibility(t *testing.T) {
	ds := uitest.Demo(t)
	d, p := setup(t, ds)
	st := d.Env.State
	st.View = data.View{Where: "mag < 20"}
	d.Send(kit.ViewChangedMsg{})
	d.Send(kit.SamplingChangedMsg{})
	if len(ds.Calls()) != 0 {
		t.Fatalf("plotted while hidden: %+v", ds.Calls())
	}
	d.Press("4")
	if c := lastCall(ds); c.View.Where != "mag < 20" || len(ds.Calls()) != 1 {
		t.Fatalf("calls %+v", ds.Calls())
	}
	if !strings.HasSuffix(p.Status(), "where mag < 20") {
		t.Fatalf("status %q", p.Status())
	}
	// back to the tab, nothing changed: not binned again
	d.Press("1", "4")
	if len(ds.Calls()) != 1 {
		t.Fatalf("re-plotted: %+v", ds.Calls())
	}
	// a view that keeps ra and dec keeps the settings
	key(d, p, "tab", "right") // lon: ra -> dec
	if p.Value("x") != "dec" {
		t.Fatalf("x %q", p.Value("x"))
	}
	st.View = data.View{Where: "mag < 19"}
	d.Send(kit.ViewChangedMsg{})
	if p.Value("x") != "dec" || p.Value("y") != "dec" || lastCall(ds).View.Where != "mag < 19" {
		t.Fatalf("x %q y %q last %+v", p.Value("x"), p.Value("y"), lastCall(ds))
	}
	// a query without them: defaults again
	st.View = data.View{SQL: "select mag, snr, band from t"}
	st.Columns = []data.Column{ds.Cols[10], ds.Cols[11], ds.Cols[7]}
	d.Send(kit.ViewChangedMsg{})
	if p.Value("mode") != "xy" || p.Value("x") != "mag" || p.Value("y") != "snr" {
		t.Fatalf("defaults %q %q %q", p.Value("mode"), p.Value("x"), p.Value("y"))
	}
	if !strings.HasSuffix(p.Status(), "SQL result") {
		t.Fatalf("status %q", p.Status())
	}
	// nothing numeric
	st.Columns = []data.Column{ds.Cols[7]}
	n := len(ds.Calls())
	d.Send(kit.ViewChangedMsg{})
	if len(ds.Calls()) != n || !strings.Contains(strings.Join(d.Screen(), "\n"), "no numeric columns to plot") {
		t.Fatalf("no numeric: calls %d screen\n%s", len(ds.Calls())-n, strings.Join(d.Screen(), "\n"))
	}
	if !strings.Contains(strings.Join(d.Screen(), "\n"), "x — ▾") {
		t.Fatal("empty fields not shown as —")
	}
}

// A resize replots after its debounce, at the new size.
func TestResize(t *testing.T) {
	ds := uitest.Demo(t)
	d, p := setup(t, ds)
	d.Press("4", "left")
	before := lastCall(ds).Opts.([2]int)
	d.Send(tea.WindowSizeMsg{Width: 120, Height: 30})
	after := lastCall(ds).Opts.([2]int)
	if before == after {
		t.Fatalf("not re-binned for the new size: %v", after)
	}
	_ = p
}

func TestGuessSkyColumns(t *testing.T) {
	for _, r := range golden.Load(t, "data_demo.json").Section(t, "guess_sky_columns") {
		var names []string
		var out [2]*string
		r.Decode(t, "names", &names)
		r.Decode(t, "out", &out)
		lon, lat := GuessSkyColumns(names)
		if lon != *out[0] || lat != *out[1] {
			t.Errorf("%v: %q %q, want %q %q", names, lon, lat, *out[0], *out[1])
		}
	}
	for _, c := range []struct {
		names    []string
		lon, lat string
	}{
		{[]string{"RA", "Dec"}, "RA", "Dec"},
		{[]string{"x", "coord_ra", "coord_dec"}, "coord_ra", "coord_dec"},
		{[]string{"raErr", "decErr", "ra_icrs", "dec_icrs"}, "ra_icrs", "dec_icrs"},
		{[]string{"objRa", "objDec"}, "objRa", "objDec"},
		{[]string{"raDot", "decDot"}, "", ""},
		{[]string{"glon", "glat"}, "glon", "glat"},
		{[]string{"mag", "snr"}, "", ""},
		{[]string{"ra"}, "", ""},
	} {
		lon, lat := GuessSkyColumns(c.names)
		if lon != c.lon || lat != c.lat {
			t.Errorf("%v: %q %q, want %q %q", c.names, lon, lat, c.lon, c.lat)
		}
	}
}
