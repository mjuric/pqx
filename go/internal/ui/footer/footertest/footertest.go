// Package footertest is test support for the Schema and Metadata tabs: a
// dataset over a fixture whose footer methods (FooterSummary, RowGroupInfo,
// Encodings, KeyValueMetadata, Info, and the columns' units and
// descriptions) answer from Python pqx's golden files, so the tabs can be
// tested before and independently of the data layer's footer code.
package footertest

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/mjuric/pqx/go/internal/data"
	"github.com/mjuric/pqx/go/internal/golden"
	"github.com/mjuric/pqx/go/internal/styled"
	"github.com/mjuric/pqx/go/internal/ui/app"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// DS is a fixture with its footer from the golden file.
type DS struct {
	data.Dataset
	Cols    []data.Column
	Summary []data.ChunkSummary
	RGs     []data.RowGroup
	Enc     map[string][]string
	KV      []data.KeyValue
	FInfo   data.FileInfo

	// Gate, if set, holds the footer pass until it is closed (or its
	// context is cancelled); Err makes it fail.
	Gate  chan struct{}
	Err   error
	Calls atomic.Int32 // FooterSummary calls
}

// Open opens fixture name ("demo", "odd", …) with the golden file
// data_<name>.json.
func Open(t testing.TB, name string) *DS {
	t.Helper()
	ds, err := data.Open(golden.Fixture(name+".parquet"), data.Options{Threads: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ds.Close() })
	g := golden.Load(t, "data_"+name+".json")
	f := &DS{Dataset: ds, Enc: map[string][]string{}}

	var cols []struct {
		Name, Unit, Description string
	}
	decode := func(sec string, v any) {
		recs := g.Section(t, sec)
		b, _ := json.Marshal(recs)
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatalf("%s: %v", sec, err)
		}
	}
	decode("columns", &cols)
	f.Cols = append([]data.Column(nil), ds.Columns()...)
	for i := range f.Cols {
		for _, c := range cols {
			if c.Name == f.Cols[i].Name {
				f.Cols[i].Unit, f.Cols[i].Description = c.Unit, c.Description
			}
		}
	}

	var summ []struct {
		Path, Physical, Logical, Compression string
		Compressed, Uncompressed, Nulls      int64
		Min, Max                             golden.Value
		HasStats                             bool `json:"has_stats"`
	}
	decode("column_chunk_summary", &summ)
	for _, s := range summ {
		logical := s.Logical
		if logical == "None" {
			logical = ""
		}
		f.Summary = append(f.Summary, data.ChunkSummary{Path: s.Path, Physical: s.Physical, Logical: logical,
			Compression: s.Compression, Compressed: s.Compressed, Uncompressed: s.Uncompressed,
			Min: Value(s.Min), Max: Value(s.Max), Nulls: s.Nulls, HasStats: s.HasStats})
	}
	var rgs []struct{ Index, Start, Rows, Compressed, Uncompressed int64 }
	decode("row_groups", &rgs)
	for _, r := range rgs {
		f.RGs = append(f.RGs, data.RowGroup{Index: int(r.Index), Start: r.Start, Rows: r.Rows,
			Compressed: r.Compressed, Uncompressed: r.Uncompressed})
	}
	var encs []struct {
		Path string
		Out  []string
	}
	decode("column_encodings", &encs)
	for _, e := range encs {
		f.Enc[e.Path] = e.Out
	}
	var kvs []struct{ Key, Value string }
	decode("key_value_metadata", &kvs)
	for _, kv := range kvs {
		f.KV = append(f.KV, data.KeyValue{Key: kv.Key, Value: kv.Value})
	}
	var file []struct {
		NumLeaf   int    `json:"num_leaf_columns"`
		CreatedBy string `json:"created_by"`
	}
	decode("file", &file)
	f.FInfo = data.FileInfo{Path: ds.Path(), Size: ds.Info().Size, FooterSize: footerSize(t, ds.Path()),
		FormatVersion: "2.6", CreatedBy: file[0].CreatedBy, NumLeaves: file[0].NumLeaf}
	for _, r := range f.RGs {
		f.FInfo.Compressed += r.Compressed
		f.FInfo.Uncompressed += r.Uncompressed
	}
	return f
}

// footerSize is the length of the file's footer (the 4 bytes before the
// trailing "PAR1").
func footerSize(t testing.TB, path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil || len(b) < 8 {
		t.Fatalf("footer of %s: %v", path, err)
	}
	return int64(binary.LittleEndian.Uint32(b[len(b)-8:]))
}

// Value converts a golden value of the kinds footer statistics have.
func Value(v golden.Value) data.Value {
	switch v.Kind {
	case golden.KindInt:
		n, _ := v.Int64()
		return n
	case golden.KindUint:
		n, _ := v.Uint64()
		return n
	case golden.KindF64:
		return v.Float
	case golden.KindF32:
		return v.Float32
	case golden.KindBool:
		return v.Bool
	case golden.KindStr:
		return v.Str
	case golden.KindBytes:
		return v.Bytes
	case golden.KindTS:
		unit := map[string]time.Duration{"s": time.Second, "ms": time.Millisecond, "us": time.Microsecond,
			"ns": time.Nanosecond}[v.Unit]
		return data.Timestamp{T: v.Time, Zoned: v.TZ != "", Unit: unit}
	case golden.KindDate:
		return data.Date(v.Time.Unix() / 86400)
	}
	return nil
}

// Columns, Info, KeyValueMetadata and Encodings answer from the golden file.
func (f *DS) Columns() []data.Column            { return f.Cols }
func (f *DS) Info() data.FileInfo               { return f.FInfo }
func (f *DS) KeyValueMetadata() []data.KeyValue { return f.KV }
func (f *DS) Encodings(path string) []string    { return f.Enc[path] }

// FooterSummary waits for Gate, then answers from the golden file.
func (f *DS) FooterSummary(ctx context.Context) ([]data.ChunkSummary, error) {
	f.Calls.Add(1)
	if f.Gate != nil {
		select {
		case <-f.Gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.Err != nil {
		return nil, f.Err
	}
	return f.Summary, nil
}

// RowGroupInfo answers from the golden file.
func (f *DS) RowGroupInfo(ctx context.Context) ([]data.RowGroup, error) { return f.RGs, nil }

// Env is an environment for ds with the basic look.
func Env(ds data.Dataset) *kit.Env {
	return &kit.Env{DS: ds, Look: Look{}, State: &kit.State{Total: ds.NumRows(), Columns: ds.Columns()},
		Tasks: kit.NewTasks()}
}

// Look is a look whose Render is the plain text, so tests can compare
// screens as text.
type Look struct{}

// Style implements kit.Look as app.BasicLook does.
func (Look) Style(role string) styled.Style {
	switch role {
	case "dim":
		return styled.Style{Dim: true}
	case "cursor", "selection":
		return styled.Style{Reverse: true}
	case "accent", "border-focus":
		return styled.Style{Fg: "accent"}
	}
	return styled.Style{}
}

// DarkBG implements kit.Look.
func (Look) DarkBG() bool { return true }

// Render implements kit.Look: plain text (styles are checked on the
// styled.Text, not the output).
func (Look) Render(t styled.Text) string { return t.Plain }

// Lines is s split into lines with trailing blanks dropped.
func Lines(s string) []string {
	l := strings.Split(s, "\n")
	for i := range l {
		l[i] = strings.TrimRight(l[i], " ")
	}
	return l
}

// App runs the root model with parts the way Bubble Tea does, for tests:
// commands run on their own goroutines and their messages come back through
// a channel, so a footer pass held by Gate doesn't hold up the test.
type App struct {
	t  testing.TB
	A  *app.App
	ch chan tea.Msg
}

// NewApp makes the root model over env with parts, sized w × h.
func NewApp(t testing.TB, env *kit.Env, parts app.Parts, w, h int) *App {
	a := &App{t: t, A: app.New(env, parts), ch: make(chan tea.Msg, 1024)}
	a.Send(tea.WindowSizeMsg{Width: w, Height: h})
	return a
}

// Send gives msg to the model, starts the commands it returns and draws
// the screen (which lays out the parts for the mouse), as Bubble Tea does.
func (a *App) Send(msg tea.Msg) {
	_, cmd := a.A.Update(msg)
	a.A.View()
	a.exec(cmd)
}

func (a *App) exec(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		msg := cmd()
		if b, ok := msg.(tea.BatchMsg); ok {
			for _, c := range b {
				a.exec(c)
			}
			return
		}
		if msg != nil {
			a.ch <- msg
		}
	}()
}

// Until handles messages until cond holds, failing after 10 s.
func (a *App) Until(cond func() bool) {
	a.t.Helper()
	deadline := time.After(10 * time.Second)
	for !cond() {
		select {
		case m := <-a.ch:
			a.Send(m)
		case <-time.After(5 * time.Millisecond): // cond may watch a command's goroutine
		case <-deadline:
			a.t.Fatal("timed out")
		}
	}
}

// Settle handles messages until none comes for 50 ms (ticks further away
// than that are left pending).
func (a *App) Settle() {
	for {
		select {
		case m := <-a.ch:
			a.Send(m)
		case <-time.After(50 * time.Millisecond):
			return
		}
	}
}

// Press sends keys: names ("down", "enter", "tab", "shift+tab", "esc") or
// single characters.
func (a *App) Press(keys ...string) {
	for _, k := range keys {
		a.Send(Key(k))
	}
}

// Key is a key press by name.
func Key(k string) tea.KeyPressMsg {
	codes := map[string]rune{"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight,
		"enter": tea.KeyEnter, "tab": tea.KeyTab, "esc": tea.KeyEscape, "pgdown": tea.KeyPgDown,
		"pgup": tea.KeyPgUp, "home": tea.KeyHome, "end": tea.KeyEnd}
	if k == "shift+tab" {
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	}
	if k == "ctrl+right" {
		return tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModCtrl}
	}
	if c, ok := codes[k]; ok {
		return tea.KeyPressMsg{Code: c}
	}
	r := []rune(k)
	return tea.KeyPressMsg{Code: r[0], Text: k}
}

// Screen is the screen's lines, trailing blanks dropped.
func (a *App) Screen() []string { return Lines(ansi.Strip(a.A.View().Content)) }

// Raw is the screen as drawn, escape sequences and all.
func (a *App) Raw() string { return a.A.View().Content }
