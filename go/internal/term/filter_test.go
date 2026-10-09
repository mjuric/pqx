package term

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// x10 is an X10 mouse report for button code b at 1-based (x, y), as
// tests/test_terminal.py builds it.
func x10(b, x, y int) string {
	return "\x1b[M" + string([]byte{byte(32 + b), byte(32 + x), byte(32 + y)})
}

// translate runs whole sequences through a fresh translator.
func translate(t *testing.T, last int, in string) string {
	t.Helper()
	tr := &translator{lastButton: last}
	out := tr.feed([]byte(in))
	return string(out) + string(tr.expire())
}

// Port of test_terminal.py::test_translation.
func TestTranslation(t *testing.T) {
	cases := []struct {
		in   string
		last int
		want string
	}{
		{x10(0, 51, 13), 0, "\x1b[<0;51;13M"},
		{x10(2, 51, 13), 0, "\x1b[<2;51;13M"},   // right button
		{x10(3, 51, 13), 2, "\x1b[<2;51;13m"},   // release of the last button
		{x10(35, 51, 13), 0, "\x1b[<35;51;13M"}, // motion
		{x10(65, 51, 13), 0, "\x1b[<65;51;13M"}, // wheel down
		{"\x1b[32;51;13M", 0, "\x1b[<0;51;13M"}, // urxvt
		{"\x1b[<0;51;13M", 0, "\x1b[<0;51;13M"}, // already SGR: unchanged
		{"\x1b[A", 0, "\x1b[A"},                 // not a mouse report
		{x10(3+4+16, 7, 8), 1, "\x1b[<21;7;8m"}, // release keeps shift and ctrl
		{x10(0, 0, 5), 0, ""},                   // column 0: dropped, as Python
		{"\x1b[32;0;5M", 0, ""},
		{"\x1b[1;2;3M", 0, ""},                    // urxvt with a button code below 32
		{x10(0, 222, 222), 0, "\x1b[<0;222;222M"}, // largest X10 coordinate (byte 254)
		{"\x1b[1;5A\x1b[3~", 0, "\x1b[1;5A\x1b[3~"},
		// xterm sends 0 for a coordinate beyond 223: dropped, as x10_to_sgr
		// drops x < 1, and the stream stays in step
		{"\x1b[M\x00\x00\x00z", 0, "z"},
		{"\x1b[M #\x00z", 0, "z"},
		{"\x1b[M\x1b[Az", 0, "z"},
		{"\x1b[32;123456;5M", 0, "\x1b[<0;123456;5M"},   // six digits: a report
		{"\x1b[32;1234567;5M", 0, "\x1b[32;1234567;5M"}, // seven: not one
	}
	for _, c := range cases {
		if got := translate(t, c.last, c.in); got != c.want {
			t.Errorf("%q (last button %d) -> %q, want %q", c.in, c.last, got, c.want)
		}
	}
	// the button pressed is remembered for the release
	tr := &translator{}
	got := string(tr.feed([]byte(x10(2, 9, 9) + x10(3, 10, 9))))
	if got != "\x1b[<2;9;9M\x1b[<2;10;9m" {
		t.Errorf("press then release: %q", got)
	}
}

// Every way of cutting a report, a key sequence or a paste into two reads
// gives the same bytes as one read.
func TestSplitAnywhere(t *testing.T) {
	inputs := []string{
		x10(0, 80, 5) + x10(3, 80, 5),
		x10(0, 150, 9) + "z",
		"\x1b[32;101;6M" + "q",
		"\x1b[<0;80;5M\x1b[<0;80;5m",
		"\x1b[1;5Aé✓\x1b[3~",
		"\x1b[200~paste \x1b[M!!! é\x1b[201~" + x10(0, 100, 3),
		"a\xc3\xa9b\xe2\x9c\x93",
	}
	for _, in := range inputs {
		want := translate(t, 0, in)
		for cut := 1; cut < len(in); cut++ {
			tr := &translator{}
			got := string(tr.feed([]byte(in[:cut]))) + string(tr.feed([]byte(in[cut:])))
			got += string(tr.expire())
			if got != want {
				t.Errorf("%q cut at %d -> %q, want %q", in, cut, got, want)
			}
		}
	}
}

// Port of test_terminal.py::test_input_decoding_survives_invalid_utf8.
func TestLenientUTF8(t *testing.T) {
	cases := []struct{ in, want string }{
		// an X10 click at column 120: 0x98 is not valid UTF-8
		{"\x1b[M \x98-", "\x1b[<0;120;13M"},
		{"é✓", "é✓"},      // real UTF-8 is unaffected
		{"a\xffb", "aÿb"}, // an invalid byte is the Latin-1 character
		{"a\xe9b", "aéb"}, // (Latin-1 é)
		{"a\x9bb", "ab"},  // a C1 control is dropped
		{"\xc3", "Ã"},     // cut off and never completed
		{"\xe2\x9c", "â"}, // â, then 0x9c dropped
	}
	for _, c := range cases {
		if got := translate(t, 0, c.in); got != c.want {
			t.Errorf("%q -> %q, want %q", c.in, got, c.want)
		}
	}
	// a character cut by a read is waited for, not mangled
	tr := &translator{}
	if got := string(tr.feed([]byte("x\xe2\x9c"))); got != "x" {
		t.Errorf("held: %q", got)
	}
	if got := string(tr.feed([]byte("\x93y"))); got != "✓y" {
		t.Errorf("completed: %q", got)
	}
}

func TestPasteIsNotTranslated(t *testing.T) {
	in := "\x1b[200~" + x10(0, 51, 13) + "\xff\x1b[201~" + x10(0, 51, 13)
	want := "\x1b[200~" + x10(0, 51, 13)[:3] + " S-" + "ÿ\x1b[201~" + "\x1b[<0;51;13M"
	if got := translate(t, 0, in); got != want {
		t.Errorf("%q -> %q, want %q", in, got, want)
	}
}

func TestHeldSequencesExpire(t *testing.T) {
	cases := []struct{ in, want string }{
		{"\x1b", "\x1b[27u"},         // a lone Esc: a key Bubble Tea doesn't wait on
		{"\x1b\x1b", "\x1b\x1b[27u"}, // Alt+Esc
		{"\x1b[", "\x1b["},
		{"\x1b[M", "\x1b[M"},
		{"\x1b[32;5", "\x1b[32;5"},
		{"\x1b[200", "\x1b[200"},
	}
	for _, c := range cases {
		tr := &translator{}
		if got := tr.feed([]byte(c.in)); len(got) != 0 {
			t.Errorf("%q passed on %q at once", c.in, got)
		}
		if got := string(tr.expire()); got != c.want {
			t.Errorf("%q expired as %q, want %q", c.in, got, c.want)
		}
	}
	// ordinary sequences aren't held
	for _, in := range []string{"\x1bO", "\x1b[A", "\x1bx", "\x1b[1;5", "q", "\x1b[<0;5"} {
		tr := &translator{}
		got := string(tr.feed([]byte(in)))
		if in == "\x1b[1;5" { // could still be urxvt: held
			got += string(tr.expire())
		}
		if got != in {
			t.Errorf("%q -> %q", in, got)
		}
	}
}

// ---- through Bubble Tea, as pqx runs it

type recorder struct{ got []string }

func (r *recorder) Init() tea.Cmd { return nil }
func (r *recorder) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.MouseClickMsg:
		r.got = append(r.got, fmt.Sprintf("click %v %d,%d", m.Button, m.X, m.Y))
	case tea.MouseReleaseMsg:
		r.got = append(r.got, fmt.Sprintf("release %v %d,%d", m.Button, m.X, m.Y))
	case tea.MouseWheelMsg:
		r.got = append(r.got, fmt.Sprintf("wheel %v %d,%d", m.Button, m.X, m.Y))
	case tea.MouseMotionMsg:
		r.got = append(r.got, fmt.Sprintf("motion %d,%d", m.X, m.Y))
	case tea.PasteMsg:
		r.got = append(r.got, fmt.Sprintf("paste %q", m.Content))
	case tea.KeyPressMsg:
		if m.String() == "ctrl+q" {
			return r, tea.Quit
		}
		r.got = append(r.got, "key "+m.String())
	}
	return r, nil
}
func (r *recorder) View() tea.View { return tea.NewView("") }

// run feeds the chunks to a tea.Program through a Filter, pause apart, and
// returns the messages it produced.
func run(t *testing.T, pause time.Duration, chunks ...string) []string {
	t.Helper()
	pr, pw := io.Pipe()
	f := mustFilter(t, pr)
	rec := &recorder{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := tea.NewProgram(rec, tea.WithInput(f.File()), tea.WithOutput(io.Discard),
		tea.WithWindowSize(250, 200), tea.WithContext(ctx), tea.WithoutSignals())
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	go func() {
		for _, c := range chunks {
			_, _ = pw.Write([]byte(c))
			time.Sleep(pause)
		}
		time.Sleep(150 * time.Millisecond)
		_, _ = pw.Write([]byte{0x11}) // ctrl+q
	}()
	if err := <-done; err != nil {
		t.Fatalf("program: %v", err)
	}
	return rec.got
}

// Port of test_terminal.py::test_parser_understands_x10_and_urxvt (Bubble
// Tea's coordinates are 0-based).
func TestProgramUnderstandsX10AndUrxvt(t *testing.T) {
	got := run(t, 0, x10(0, 121, 13)+x10(3, 121, 13)+"\x1b[32;5;6M")
	want := []string{"click left 120,12", "release left 120,12", "click left 4,5"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %v, want %v", got, want)
	}
}

// The case found with the prototype: a report split after ESC [ M used to
// arrive as key presses, and a click at column 80 typed q. Coordinates
// above 95 are bytes above 127.
func TestSplitReportsDontType(t *testing.T) {
	for _, c := range []struct {
		x, y int
		cut  int
	}{
		{80, 5, 3}, {80, 5, 4}, {80, 5, 5}, {80, 5, 1}, {80, 5, 2},
		{150, 9, 3}, {200, 150, 4}, {222, 222, 5},
	} {
		r := x10(0, c.x+1, c.y+1)
		got := run(t, 20*time.Millisecond, r[:c.cut], r[c.cut:], "z")
		want := []string{fmt.Sprintf("click left %d,%d", c.x, c.y), "key z"}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("click at %d,%d cut after %d bytes: got %v, want %v", c.x, c.y, c.cut, got, want)
		}
	}
	// urxvt, cut in its digits
	got := run(t, 20*time.Millisecond, "\x1b[32;8", "1;6M", "z")
	if strings.Join(got, "|") != "click left 80,5|key z" {
		t.Errorf("urxvt split: %v", got)
	}
	// and the release after a split press keeps its button
	got = run(t, 20*time.Millisecond, x10(2, 81, 6)[:3], x10(2, 81, 6)[3:]+x10(3, 81, 6))
	if strings.Join(got, "|") != "click right 80,5|release right 80,5" {
		t.Errorf("right click and release: %v", got)
	}
}

func TestKeysAndPastesUnaffected(t *testing.T) {
	cases := []struct {
		chunks []string
		want   string
	}{
		{[]string{"q"}, "key q"},
		{[]string{"\x1b[A", "\x1b[1;5B", "\x1b[3~", "\x1bOP"}, "key up|key ctrl+down|key delete|key f1"},
		{[]string{"é✓"}, "key é|key ✓"},
		{[]string{"\x1b[200~select * from t\x1b[201~"}, `paste "select * from t"`},
		{[]string{"\x1b[200~sel", "ect é", "\x1b[20", "1~"}, `paste "select é"`},
		{[]string{"\x1b[200~a\xffb\x1b[201~"}, `paste "aÿb"`},
		{[]string{"\x1b[<0;81;6M", "\x1b[<0;81;6m"}, "click left 80,5|release left 80,5"},
		{[]string{"\x1b[<65;81;6M"}, "wheel wheeldown 80,5"},
		{[]string{"\x1bx"}, "key alt+x"},
		{[]string{"a\xffb"}, "key a|key ÿ|key b"},
	}
	for _, c := range cases {
		got := run(t, 10*time.Millisecond, c.chunks...)
		if strings.Join(got, "|") != c.want {
			t.Errorf("%q: got %v, want %s", c.chunks, got, c.want)
		}
	}
}

// Esc arrives about as soon as without the filter (Bubble Tea waits 50 ms
// for the rest of a sequence after a lone ESC).
func TestEscTiming(t *testing.T) {
	pr, pw := io.Pipe()
	f := mustFilter(t, pr)
	got := make(chan stamp, 10)
	p := tea.NewProgram(keyTimer{got}, tea.WithInput(f.File()), tea.WithOutput(io.Discard),
		tea.WithWindowSize(80, 20), tea.WithoutSignals())
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	time.Sleep(100 * time.Millisecond)
	var lat []time.Duration
	for i := 0; i < 5; i++ {
		t0 := time.Now()
		_, _ = pw.Write([]byte{0x1b})
		s := <-got
		if s.key != "esc" {
			t.Fatalf("got %q, want esc", s.key)
		}
		lat = append(lat, s.at.Sub(t0))
		time.Sleep(80 * time.Millisecond)
	}
	_, _ = pw.Write([]byte{0x11})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	t.Logf("Esc latency: %v", lat)
	for _, l := range lat {
		if l < EscTimeout || l > EscTimeout+40*time.Millisecond {
			t.Errorf("Esc took %v, want about %v", l, EscTimeout)
		}
	}
	// and a key after a lone Esc (but within the timeout) is Alt+key, as
	// without the filter
	if g := run(t, 10*time.Millisecond, "\x1b", "x"); strings.Join(g, "|") != "key alt+x" {
		t.Errorf("Esc then x within 10 ms: %v", g)
	}
	if g := run(t, 120*time.Millisecond, "\x1b", "x"); strings.Join(g, "|") != "key esc|key x" {
		t.Errorf("Esc then x after 120 ms: %v", g)
	}
}

type stamp struct {
	key string
	at  time.Time
}

type keyTimer struct{ c chan<- stamp }

func (k keyTimer) Init() tea.Cmd { return nil }
func (k keyTimer) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m, ok := msg.(tea.KeyPressMsg); ok {
		if m.String() == "ctrl+q" {
			return k, tea.Quit
		}
		k.c <- stamp{m.String(), time.Now()}
	}
	return k, nil
}
func (k keyTimer) View() tea.View { return tea.NewView("") }

func mustFilter(t testing.TB, src io.Reader) *Filter {
	t.Helper()
	f, err := NewFilter(src)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

type failing struct {
	data string
	err  error
}

func (r *failing) Read(p []byte) (int, error) {
	if r.data == "" {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func TestFilterEOFAndClose(t *testing.T) {
	f := mustFilter(t, strings.NewReader("ab\x1b"))
	b, err := io.ReadAll(f)
	if err != nil || string(b) != "ab\x1b[27u" {
		t.Errorf("ReadAll = %q, %v", b, err)
	}
	// the source's error, after what was read before it
	boom := errors.New("boom")
	f = mustFilter(t, &failing{"xy", boom})
	b, err = io.ReadAll(f)
	if !errors.Is(err, boom) || string(b) != "xy" {
		t.Errorf("failing source: %q, %v", b, err)
	}
	pr, _ := io.Pipe()
	f = mustFilter(t, pr)
	errc := make(chan error)
	go func() { _, err := f.Read(make([]byte, 10)); errc <- err }()
	time.Sleep(10 * time.Millisecond)
	f.Close()
	if err := <-errc; err != io.EOF {
		t.Errorf("Read after Close: %v", err)
	}
}

// readWithin reads what the filter passes on within d.
func readWithin(t *testing.T, f *Filter, d time.Duration) string {
	t.Helper()
	_ = f.r.SetReadDeadline(time.Now().Add(d))
	defer f.r.SetReadDeadline(time.Time{})
	buf := make([]byte, 256)
	n, err := f.r.Read(buf)
	if err != nil {
		t.Fatalf("nothing within %v: %v", d, err)
	}
	return string(buf[:n])
}

// The hold timer is armed when a sequence is cut off: a lone Esc arrives
// without more input.
func TestHeldEscArrivesAlone(t *testing.T) {
	pr, pw := io.Pipe()
	f := mustFilter(t, pr)
	for i := 0; i < 3; i++ {
		_, _ = pw.Write([]byte{0x1b})
		if got := readWithin(t, f, 10*EscTimeout); got != "\x1b[27u" {
			t.Fatalf("got %q", got)
		}
	}
	_, _ = pw.Write([]byte("\x1b[M"))
	if got := readWithin(t, f, 10*SeqTimeout); got != "\x1b[M" {
		t.Fatalf("a partial report after its timeout: %q", got)
	}
}

// A read that ends in the middle of a sequence arms the hold timer: it
// fires within the timeout (checked on the timer itself).
func TestHoldTimerArmed(t *testing.T) {
	for _, c := range []struct {
		in   string
		wait time.Duration
	}{{"\x1b", EscTimeout}, {"\x1b[M", SeqTimeout}, {"a\xe2\x9c", SeqTimeout}} {
		f, err := newFilter()
		if err != nil {
			t.Fatal(err)
		}
		f.onData([]byte(c.in), true)
		select {
		case <-f.timer.C:
		case <-time.After(c.wait + 100*time.Millisecond):
			t.Errorf("%q: the hold timer isn't armed", c.in)
		}
		f.Close()
	}
}

// When the timer and the rest of a sequence are both ready, the rest goes
// first: the sequence isn't passed on cut.
func TestRestBeatsTimer(t *testing.T) {
	for i := 0; i < 200; i++ {
		f, err := newFilter()
		if err != nil {
			t.Fatal(err)
		}
		f.data = make(chan []byte, 1)
		r := x10(0, 81, 6)
		f.send(f.t.feed([]byte(r[:3])))
		f.data <- []byte(r[3:])
		f.onExpire() // the timer fired with the rest waiting
		if got := readWithin(t, f, time.Second); got != "\x1b[<0;81;6M" {
			t.Fatalf("got %q", got)
		}
		f.Close()
	}
}

// BenchmarkFilterLatency is the time from a key written to the pipe until
// the filter's reader has it: the filter's cost on every key press.
func BenchmarkFilterLatency(b *testing.B) {
	pr, pw := io.Pipe()
	f := mustFilter(b, pr)
	buf := make([]byte, 64)
	key := []byte("\x1b[B")
	for b.Loop() {
		go func() { _, _ = pw.Write(key) }()
		n := 0
		for n < len(key) {
			m, _ := f.Read(buf[n:])
			n += m
		}
	}
}

// BenchmarkDirectLatency is the same through a bare pipe, for comparison.
func BenchmarkDirectLatency(b *testing.B) {
	pr, pw := io.Pipe()
	buf := make([]byte, 64)
	key := []byte("\x1b[B")
	for b.Loop() {
		go func() { _, _ = pw.Write(key) }()
		n := 0
		for n < len(key) {
			m, _ := pr.Read(buf[n:])
			n += m
		}
	}
}

// BenchmarkTranslate is the translation of a busy read: mouse motion,
// keys, UTF-8.
func BenchmarkTranslate(b *testing.B) {
	var in bytes.Buffer
	for i := 0; i < 50; i++ {
		in.WriteString(x10(35, 100+i, 20))
		in.WriteString("\x1b[<35;100;20M")
		in.WriteString("abc é✓\x1b[A")
	}
	p := in.Bytes()
	b.SetBytes(int64(len(p)))
	tr := &translator{}
	for b.Loop() {
		tr.feed(p)
	}
}
