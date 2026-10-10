package fmtx

import (
	"math"
	"math/big"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mjuric/pqx/go/internal/data"
)

var allKinds = []Kind{KindInt, KindFloat, KindFloat32, KindErr, KindMJD, KindAngle, KindMag, KindFlux, KindBool,
	KindTime, KindBinary, KindNested, KindStr}

// fuzzValues are values of every kind built from the fuzzer's inputs.
func fuzzValues(s string, f float64, n int64) []data.Value {
	b := []byte(s)
	return []data.Value{nil, s, b, f, float32(f), n, uint64(n), n%2 == 0,
		data.Timestamp{T: time.Unix(n%(1<<34), n%1e9).UTC(), Zoned: n%3 == 0},
		data.Date(n % 3_000_000), data.TimeOfDay(uint64(n) % (86400 * 1e9)), data.Duration(n),
		data.Interval{Months: int32(n), Days: int32(n >> 8), Nanos: n},
		data.Decimal{Unscaled: big.NewInt(n), Scale: int32(uint64(n) % 40), Precision: 38},
		data.UUID{1, 2, 3}, data.List{s, f, n, data.List{s}}, data.Struct{{Name: s, Value: s}, {Name: "x", Value: f}},
		data.Map{{Key: s, Value: n}}}
}

func noControls(t *testing.T, what, s string) {
	t.Helper()
	if !utf8.ValidString(s) {
		t.Fatalf("%s: not UTF-8: %q", what, s)
	}
	for _, r := range s {
		if (r < 0x20 && r != '\t' && r != '\n') || (r >= 0x7F && r < 0xA0) || isBidiOrZW(r) {
			t.Fatalf("%s: control %U in %q", what, r, s)
		}
	}
}

func isBidiOrZW(r rune) bool {
	return (r >= 0x202A && r < 0x202F) || (r >= 0x2066 && r < 0x206A) || (r >= 0x200B && r < 0x2010) ||
		r == 0x061C || r == 0x2060 || r == 0xFEFF
}

// FuzzFormat: Format never panics, and its safe output has no control
// characters (but tab and newline), whatever the value, kind and spec.
func FuzzFormat(f *testing.F) {
	for _, s := range []string{"", "abc", ".2f", "%Y-%m", ",d", "\x1b]0;x\x07", "\u009b", "0=+#020,.5%", "é>12.3s", "\xff"} {
		f.Add(s, s, 1.5, int64(7), 40, false)
	}
	f.Add(".3e", "x", math.Inf(1), int64(-1), 0, true)
	f.Add("<99999", "\u202e", math.NaN(), int64(math.MinInt64), 3, false)
	f.Fuzz(func(t *testing.T, spec, s string, x float64, n int64, width int, raw bool) {
		width = max(0, width%200)
		for _, v := range fuzzValues(s, x, n) {
			for _, k := range allKinds {
				for _, o := range []Override{{}, {Spec: spec, Set: true}, {Digits: int(n % 40), Set: true}} {
					out := Format(v, k, Opts{Raw: raw, Width: width, Override: o})
					noControls(t, "Format", out)
					c := Cell(v, k, Opts{Raw: raw, Width: width, Override: o})
					noControls(t, "Cell", c.Plain)
					_ = OverrideError(o, k, v)
				}
				_ = Derived(s, k, v, s)
			}
		}
		_ = StepOverride(Override{Spec: spec, Set: true}, KindFloat, 1)
		_ = ParseOverride(spec)
	})
}

// FuzzSanitize: the safe text has no controls, keeps tab and newline only
// when asked, and HasControls agrees with Sanitize.
func FuzzSanitize(f *testing.F) {
	for _, s := range []string{"", "abc", "a\tb\nc", "\x1b[2J", "\u009b31m", "\u202e", "\xff\xfe", "é ✓ 漢字"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, keep := range []bool{false, true} {
			out := Sanitize(s, keep)
			noControls(t, "Sanitize", out)
			if !keep && strings.ContainsAny(out, "\t\n") {
				t.Fatalf("tab or newline kept: %q", out)
			}
			if HasControls(s, keep) != (out != s) {
				t.Fatalf("HasControls(%q, %v) disagrees", s, keep)
			}
			if Sanitize(out, keep) != out {
				t.Fatalf("not idempotent: %q", s)
			}
		}
	})
}

func BenchmarkFormatFloat(b *testing.B) {
	o := Opts{Width: DefaultWidth}
	vals := []float64{12.3456789, -0.000123456, 60800.123456789, 1e-7, 28561.3, 2.675}
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		Format(vals[i%len(vals)], KindFloat, o)
	}
}

func BenchmarkFormatAngle(b *testing.B) {
	o := Opts{Width: DefaultWidth}
	b.ReportAllocs()
	for b.Loop() {
		Format(123.456789012, KindAngle, o)
	}
}

func BenchmarkFormatFloat32(b *testing.B) {
	o := Opts{Width: DefaultWidth}
	b.ReportAllocs()
	for b.Loop() {
		Format(float32(21.123456), KindFloat32, o)
	}
}

func BenchmarkFormatInt(b *testing.B) {
	o := Opts{Width: DefaultWidth}
	b.ReportAllocs()
	for b.Loop() {
		Format(int64(170000000000000123), KindInt, o)
	}
}

func BenchmarkFormatString(b *testing.B) {
	o := Opts{Width: DefaultWidth}
	b.ReportAllocs()
	for b.Loop() {
		Format("2026-01-02 obs r band visit 12345", KindStr, o)
	}
}

func BenchmarkFormatLongString(b *testing.B) {
	o := Opts{Width: DefaultWidth}
	s := strings.Repeat("日本語テキスト", 20)
	b.ReportAllocs()
	for b.Loop() {
		Format(s, KindStr, o)
	}
}

func BenchmarkFormatTimestamp(b *testing.B) {
	o := Opts{Width: DefaultWidth}
	v := data.Timestamp{T: time.Date(2026, 1, 2, 3, 4, 5, 123000000, time.UTC), Zoned: true, Unit: time.Millisecond}
	b.ReportAllocs()
	for b.Loop() {
		Format(v, KindTime, o)
	}
}

func BenchmarkFormatSpec(b *testing.B) {
	o := Opts{Width: DefaultWidth, Override: Override{Spec: ",.3f", Set: true}}
	b.ReportAllocs()
	for b.Loop() {
		Format(1234567.891, KindFloat, o)
	}
}

func BenchmarkCellFloat(b *testing.B) {
	o := Opts{Width: DefaultWidth}
	b.ReportAllocs()
	for b.Loop() {
		Cell(12.3456789, KindFloat, o)
	}
}
