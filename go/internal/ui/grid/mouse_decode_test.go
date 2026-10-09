package grid

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/mjuric/pqx/go/internal/ui/app"
	"github.com/mjuric/pqx/go/internal/ui/filter"
	"github.com/mjuric/pqx/go/internal/ui/kit"
)

// These tests feed raw terminal input through a real tea.Program (its
// ultraviolet input decoder) to see what Bubble Tea makes of the mouse
// reports GNU screen 4.x sends. Screen 4.x doesn't know SGR mouse (1006),
// which Bubble Tea asks for, so it reports in the X10 encoding:
// ESC [ M Cb Cx Cy, each a single byte with 32 added, and 1 added to the
// 0-based coordinates. Columns above 94 give bytes above 127, which on their
// own are not valid UTF-8.

type recorder struct {
	got []tea.Msg
}

func (r *recorder) Init() tea.Cmd { return nil }
func (r *recorder) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.MouseMsg:
		r.got = append(r.got, msg)
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+q" {
			return r, tea.Quit
		}
		r.got = append(r.got, msg)
	}
	return r, nil
}
func (r *recorder) View() tea.View { return tea.NewView("") }

// decode runs input through a tea.Program and returns the mouse and key
// messages it produced, as strings.
func decode(t *testing.T, input []byte) []string {
	t.Helper()
	pr, pw := io.Pipe()
	rec := &recorder{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p := tea.NewProgram(rec, tea.WithInput(pr), tea.WithOutput(io.Discard),
		tea.WithWindowSize(250, 200), tea.WithContext(ctx), tea.WithoutSignals())
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	// one write, as a terminal would deliver one report; then ctrl+q to quit
	go func() {
		_, _ = pw.Write(input)
		time.Sleep(50 * time.Millisecond)
		_, _ = pw.Write([]byte{0x11})
	}()
	if err := <-done; err != nil {
		t.Fatalf("program: %v", err)
	}
	var out []string
	for _, m := range rec.got {
		switch m := m.(type) {
		case tea.MouseClickMsg:
			out = append(out, fmt.Sprintf("click %v %d,%d", m.Button, m.X, m.Y))
		case tea.MouseReleaseMsg:
			out = append(out, fmt.Sprintf("release %d,%d", m.X, m.Y))
		case tea.MouseWheelMsg:
			out = append(out, fmt.Sprintf("wheel %v %d,%d", m.Button, m.X, m.Y))
		case tea.MouseMotionMsg:
			out = append(out, fmt.Sprintf("motion %d,%d", m.X, m.Y))
		case tea.KeyPressMsg:
			out = append(out, "key "+m.String())
		}
	}
	return out
}

// x10 is an X10 mouse report for button code cb at 0-based (x, y).
func x10(cb, x, y int) []byte {
	return []byte{0x1b, '[', 'M', byte(32 + cb), byte(32 + 1 + x), byte(32 + 1 + y)}
}

func TestX10MouseDecoding(t *testing.T) {
	cases := []struct {
		name  string
		input []byte
		want  string
	}{
		{"click, small coordinates", x10(0, 10, 5), "click left 10,5"},
		{"click at column 100 (byte 133)", x10(0, 100, 5), "click left 100,5"},
		{"click at column 200, row 150 (bytes 233, 183)", x10(0, 200, 150), "click left 200,150"},
		{"largest X10 coordinate (byte 255)", x10(0, 222, 222), "click left 222,222"},
		{"release at column 120", x10(3, 120, 7), "release 120,7"},
		{"wheel down at column 120", x10(65, 120, 7), "wheel wheeldown 120,7"},
		{"wheel up at column 96", x10(64, 96, 0), "wheel wheelup 96,0"},
		{"SGR click at column 100", []byte("\x1b[<0;101;6M"), "click left 100,5"},
		{"urxvt (1015) click at column 100", []byte("\x1b[32;101;6M"), "?"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// a key after the report checks that the stream stays in step
			got := decode(t, append(append([]byte{}, c.input...), 'z'))
			t.Logf("%q -> %v", c.input, got)
			if c.want == "?" {
				return // reported, not asserted
			}
			if len(got) != 2 || got[0] != c.want || got[1] != "key z" {
				t.Errorf("got %v, want [%s key z]", got, c.want)
			}
		})
	}
}

func TestX10MouseSplitAcrossReads(t *testing.T) {
	// a report split after ESC [ M, as a slow link might deliver it (5 ms
	// apart, less than the reader's 50 ms escape timeout)
	pr, pw := io.Pipe()
	rec := &recorder{}
	p := tea.NewProgram(rec, tea.WithInput(pr), tea.WithOutput(io.Discard),
		tea.WithWindowSize(250, 200), tea.WithoutSignals())
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	r := x10(0, 150, 9)
	_, _ = pw.Write(r[:3])
	time.Sleep(5 * time.Millisecond)
	_, _ = pw.Write(r[3:])
	time.Sleep(50 * time.Millisecond)
	_, _ = pw.Write([]byte{0x11})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var s []string
	for _, m := range rec.got {
		s = append(s, fmt.Sprint(m))
	}
	// Found 2026-10-08 with Bubble Tea v2.0.10 / ultraviolet 20260811: the
	// decoder returns UnknownCsiEvent for a bare ESC [ M, which the scanner
	// (unlike UnknownEvent) doesn't wait on, so the rest of the report arrives
	// as key presses: here "space" and "*" (byte 183 is dropped). A report
	// split this way could type any key, q included (column 80). This test
	// records the behaviour rather than asserting it.
	t.Logf("X10 report split after ESC [ M -> %v", s)
	if len(rec.got) == 1 {
		if c, ok := rec.got[0].(tea.MouseClickMsg); ok && c.X == 150 && c.Y == 9 {
			t.Log("split reports are decoded correctly now; update the PR notes")
		}
	}
}

// TestTerminalModes checks what Bubble Tea writes to the terminal for our
// view: SGR mouse (1006) with button-event tracking (1002), never SGR-pixel
// (1016); and on exit, what it does with the alternate screen.
func TestTerminalModes(t *testing.T) {
	ds := newFake(100, 3)
	env := &kit.Env{DS: ds, Look: app.BasicLook{}, Tasks: kit.NewTasks(),
		State: &kit.State{Total: ds.NumRows(), Columns: ds.Columns()}}
	m := app.New(env, app.Parts{Grid: New(env), Filter: filter.New(env)})
	var out bytes.Buffer
	pr, pw := io.Pipe()
	p := tea.NewProgram(m, tea.WithInput(pr), tea.WithOutput(&out),
		tea.WithWindowSize(80, 20), tea.WithoutSignals())
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	time.Sleep(100 * time.Millisecond)
	_, _ = pw.Write([]byte("q"))
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{"\x1b[?1049h", "\x1b[?1002h", "\x1b[?1006h", "\x1b[?1049l", "\x1b[?1002l", "\x1b[?1006l"} {
		if !strings.Contains(s, want) {
			t.Errorf("output lacks %q", want)
		}
	}
	if strings.Contains(s, "?1016") {
		t.Error("SGR-pixel mouse mode (1016) was touched")
	}
	// The alternate screen is erased before it is left (the root blanks the
	// last frame; Bubble Tea itself doesn't erase it).
	i := strings.LastIndex(s, "\x1b[?1049l")
	t.Logf("around leaving the alternate screen: %q", s[max(0, i-60):])
	if j := strings.LastIndex(s[:i], "\x1b[2J"); j < 0 || strings.Contains(s[j:i], "row") {
		t.Error("the alternate screen isn't erased before it is left")
	}
}

type clipModel struct{}

func (clipModel) Init() tea.Cmd { return tea.SetClipboard("héllo") }
func (c clipModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "q" {
		return c, tea.Quit
	}
	return c, nil
}
func (clipModel) View() tea.View { return tea.NewView("x") }

// TestOSC52 checks that tea.SetClipboard writes an OSC 52 sequence (system
// clipboard, base64 text, BEL-terminated). It isn't wrapped for tmux or
// screen passthrough.
func TestOSC52(t *testing.T) {
	var out bytes.Buffer
	pr, pw := io.Pipe()
	p := tea.NewProgram(clipModel{}, tea.WithInput(pr), tea.WithOutput(&out),
		tea.WithWindowSize(40, 5), tea.WithoutSignals())
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	time.Sleep(100 * time.Millisecond)
	_, _ = pw.Write([]byte("q"))
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	want := "\x1b]52;c;aMOpbGxv\a" // base64("héllo")
	if !strings.Contains(out.String(), want) {
		t.Errorf("no OSC 52 write %q in %q", want, out.String())
	}
}
