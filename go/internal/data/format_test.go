package data

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

// noControls: nothing in s can act on a terminal.
func noControls(s string, keepWS bool) bool {
	for _, r := range s {
		if keepWS && (r == '\t' || r == '\n') {
			continue
		}
		if r < 0x20 || (r >= 0x7F && r < 0xA0) || r == utf8RuneError || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

const utf8RuneError = '�'

// pqx's test_sanitize_shows_controls_visibly
func TestSanitize(t *testing.T) {
	cases := []struct{ in, want, keepWS string }{
		{"abc", "abc", "abc"},
		{"\x1b]0;x\x07", "␛]0;x␇", "␛]0;x␇"},
		{"a\u009bb\x7f\x00", "a\\x9bb␡␀", "a\\x9bb␡␀"},
		{"a\tb\nc", "a␉b␊c", "a\tb\nc"},
		{"a\tb\nc\x1b", "a␉b␊c␛", "a\tb\nc␛"},
		{"é ✓ 漢字  ", "é ✓ 漢字  ", "é ✓ 漢字  "},
		{"abc‮dcba", "abc⟨U+202E⟩dcba", "abc⟨U+202E⟩dcba"},
		{"bad \x9b byte \xff", "bad \\x9b byte \\xff", "bad \\x9b byte \\xff"},
	}
	for _, c := range cases {
		if got := Sanitize(c.in); got != c.want {
			t.Errorf("Sanitize(%q) = %q, want %q", c.in, got, c.want)
		}
		if got := SanitizeKeepWS(c.in); got != c.keepWS {
			t.Errorf("SanitizeKeepWS(%q) = %q, want %q", c.in, got, c.keepWS)
		}
	}
	for _, c := range []rune{0x202a, 0x202b, 0x202c, 0x202d, 0x202e, 0x2066, 0x2067, 0x2068, 0x2069, 0x200e, 0x200f, 0x061c, 0x200b, 0x200c, 0x200d, 0x2060, 0xfeff} {
		want := fmt.Sprintf("x⟨U+%04X⟩", c)
		if got := SanitizeKeepWS("x" + string(c)); got != want || !HasControls(string(c)) {
			t.Errorf("%U: %q", c, got)
		}
	}
	if !HasControls("\x1b") || HasControls("plain é") || !HasControls("a\tb") {
		t.Error("HasControls")
	}
	// every C0, C1 and DEL, alone and in text
	for r := rune(0); r < 0xA0; r++ {
		if r >= 0x20 && r < 0x7F {
			continue
		}
		s := "a" + string(r) + "b"
		if !noControls(Sanitize(s), false) || !noControls(SanitizeKeepWS(s), true) {
			t.Errorf("%U gets through: %q", r, Sanitize(s))
		}
	}
	for _, s := range hostile {
		if !noControls(Sanitize(s), false) {
			t.Errorf("%q gets through: %q", s, Sanitize(s))
		}
	}
	// the usual string isn't copied
	s := strings.Repeat("abc", 10)
	if got := Sanitize(s); got != s {
		t.Error("changed")
	}
	if a := testing.AllocsPerRun(100, func() { Sanitize(s) }); a != 0 {
		t.Errorf("Sanitize of a plain string allocates %v times", a)
	}
}

func TestFormatCell(t *testing.T) {
	mem := memory.DefaultAllocator
	f64 := array.NewFloat64Builder(mem)
	f64.AppendValues([]float64{1.5, 1234567.0, 1e-7, math.NaN(), math.Inf(-1), 0}, nil)
	f64.AppendNull()
	a := f64.NewArray()
	defer a.Release()
	want := []string{"1.5", "1.23457e+06", "1e-07", "NaN", "-Inf", "0", Null}
	for i, w := range want {
		if got := FormatCell(a, i); got != w {
			t.Errorf("float64 %d: %q, want %q", i, got, w)
		}
	}
	f32 := array.NewFloat32Builder(mem)
	f32.Append(0.1)
	b := f32.NewArray()
	defer b.Release()
	if got := FormatCell(b, 0); got != "0.1" {
		t.Errorf("float32: %q", got)
	}
	ts := array.NewTimestampBuilder(mem, &arrow.TimestampType{Unit: arrow.Nanosecond, TimeZone: "America/New_York"})
	t0 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	ts.Append(arrow.Timestamp(t0.UnixNano()))
	ts.Append(arrow.Timestamp(t0.UnixNano() + 1000))
	ts.Append(arrow.Timestamp(t0.UnixNano() + 1))
	c := ts.NewArray()
	defer c.Release()
	for i, w := range []string{"2026-01-02 03:04:05Z", "2026-01-02 03:04:05.000001Z", "2026-01-02 03:04:05.000000001Z"} {
		if got := FormatCell(c, i); got != w {
			t.Errorf("timestamp %d: %q, want %q", i, got, w)
		}
	}
	bin := array.NewBinaryBuilder(mem, arrow.BinaryTypes.Binary)
	bin.Append([]byte{0x1b, 0, 0xff})
	bin.Append([]byte(strings.Repeat("x", 20)))
	d := bin.NewArray()
	defer d.Release()
	if got := FormatCell(d, 0); got != "0x1b00ff (3 B)" {
		t.Errorf("binary: %q", got)
	}
	if got := FormatCell(d, 1); got != "0x"+strings.Repeat("78", 16)+"… (20 B)" {
		t.Errorf("binary: %q", got)
	}
	lb := array.NewListBuilder(mem, arrow.BinaryTypes.String)
	vb := lb.ValueBuilder().(*array.StringBuilder)
	lb.Append(true)
	vb.Append("a\x1b[31m")
	vb.Append("b")
	e := lb.NewArray()
	defer e.Release()
	if got := FormatCell(e, 0); !noControls(got, false) || !strings.Contains(got, "\\u001b[31m") {
		t.Errorf("list: %q", got)
	}
}
