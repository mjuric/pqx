package fmtx

import (
	"fmt"
	"math"
	"math/big"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/apache/arrow-go/v18/arrow"

	"github.com/mjuric/pqx/go/internal/data"
)

// Ports of tests/test_fmt_plots.py (fmt parts), tests/test_security.py
// (sanitize parts) and tests/test_config.py::test_parse_override.

var (
	f64 = arrow.PrimitiveTypes.Float64
	f32 = arrow.PrimitiveTypes.Float32
)

func fv(v data.Value, k Kind) string { return Format(v, k, Opts{Width: DefaultWidth}) }

func eq[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// test_fmt_plots.py::test_kinds
func TestKinds(t *testing.T) {
	eq(t, "midpointMjdTai", KindFor("midpointMjdTai", f64, ""), KindMJD)
	eq(t, "ra", KindFor("ra", f64, ""), KindAngle)
	eq(t, "coord_dec", KindFor("coord_dec", f64, ""), KindAngle)
	eq(t, "raErr", KindFor("raErr", f32, ""), KindErr)
	eq(t, "psfFlux", KindFor("psfFlux", f32, ""), KindFlux)
	eq(t, "gMag", KindFor("gMag", f32, ""), KindMag)
	eq(t, "x deg", KindFor("x", f32, "deg"), KindAngle)
	eq(t, "radius", KindFor("radius", f64, ""), KindFloat)
	eq(t, "diaSourceId", KindFor("diaSourceId", arrow.PrimitiveTypes.Int64, ""), KindInt)
	eq(t, "t", KindFor("t", &arrow.TimestampType{Unit: arrow.Millisecond}, ""), KindTime)
	eq(t, "nil type", KindFor("x", nil, ""), KindStr)
}

// test_fmt_plots.py::test_format_values
func TestFormatValues(t *testing.T) {
	eq(t, "None", fv(nil, KindFloat), Null)
	eq(t, "nan", fv(math.NaN(), KindFloat), "NaN")
	eq(t, "mjd", fv(60800.123456789, KindMJD), "60800.1234568")
	eq(t, "angle", fv(12.3456789, KindAngle), "12.345679")
	eq(t, "mag", fv(21.123456, KindMag), "21.123")
	eq(t, "flux", fv(28561.3, KindFlux), "28560")
	eq(t, "err", fv(2.14e-5, KindErr), "2.14e-05")
	eq(t, "int", fv(int64(170000000000000123), KindInt), "170000000000000123")
	eq(t, "raw", Format(0.1, KindFloat, Opts{Raw: true}), "0.1")
	eq(t, "bytes", strings.HasPrefix(fv([]byte{1, 2}, KindBinary), "0x0102"), true)
	l := make(data.List, 30)
	for i := range l {
		l[i] = int64(i)
	}
	eq(t, "list", strings.HasSuffix(fv(l, KindNested), "(30)"), true)
	eq(t, "ts", fv(data.Timestamp{T: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Zoned: true}, KindTime), "2026-01-01 00:00:00Z")
}

// test_fmt_plots.py::test_derived (F.shortest: a float32 shown raw)
func TestDerived(t *testing.T) {
	eq(t, "mjd", Derived("midpointMjdTai", KindMJD, 60676.0, ""), "2025-01-01 00:00:00.000")
	eq(t, "ra", Derived("ra", KindAngle, 180.0, ""), "12h00m00.000s")
	eq(t, "dec", Derived("dec", KindAngle, -30.5, ""), "-30°30′00.00″")
	eq(t, "raErr", strings.HasSuffix(Derived("raErr", KindErr, 1e-6, ""), "mas"), true)
	eq(t, "hms", DegToHMS(359.99999999), "00h00m00.000s")
	eq(t, "shortest", Format(float32(0.1), KindFloat32, Opts{Raw: true}), "0.1")
}

// test_fmt_plots.py::test_derived_angles
func TestDerivedAngles(t *testing.T) {
	d := func(name string, v float64) string { return Derived(name, KindAngle, v, "") }
	for _, n := range []string{"ra", "RA", "raJ2000", "coord_ra", "coordRa", "ra_deg", "ra_icrs"} {
		eq(t, n, d(n, 180), "12h00m00.000s")
	}
	for _, n := range []string{"dec", "decl", "decJ2000", "coordDec", "lat", "glat", "elat", "beta", "pickup_lat"} {
		eq(t, n, d(n, -30.5), "-30°30′00.00″")
		eq(t, n, d(n, 45.25), "+45°15′00.00″")
		eq(t, n, d(n, 120), "")
	}
	eq(t, "pickup_lon", d("pickup_lon", -74.000439), "-74°00′01.58″")
	for _, n := range []string{"lon", "glon", "elon", "lambda", "pickup_lon"} {
		eq(t, n, d(n, 285.5), "285°30′00.00″")
		eq(t, n, d(n, 12), "12°00′00.00″")
		eq(t, n, d(n, -170.25), "-170°15′00.00″")
	}
	eq(t, "lon h", strings.Contains(d("lon", 30), "h"), false)
	eq(t, "pickup_longitude", d("pickup_longitude", -73.5), "-73°30′00.00″")
	eq(t, "pickup_latitude", d("pickup_latitude", 40.75), "+40°45′00.00″")
	eq(t, "lambda 5000", d("lambda", 5000), "")
	eq(t, "lon 1e20", d("lon", 1e20), "")
	eq(t, "lambda 360", d("lambda", 360), "360°00′00.00″")
	eq(t, "lambda deg", Derived("lambda", KindAngle, 5000.0, "deg"), "5000°00′00.00″")
	for _, n := range []string{"RAJ2000", "RA_ICRS", "RA"} {
		eq(t, n, KindFor(n, f64, ""), KindAngle)
		eq(t, n, d(n, 180), "12h00m00.000s")
	}
	for _, n := range []string{"DEJ2000", "DE_ICRS", "DE"} {
		eq(t, n, KindFor(n, f64, ""), KindAngle)
		eq(t, n, d(n, -30.5), "-30°30′00.00″")
	}
	for _, n := range []string{"RATIO", "RANGE", "DEPTH", "altitude"} {
		if KindFor(n, f64, "") == KindAngle {
			t.Errorf("%s is an angle", n)
		}
	}
	eq(t, "latitude", KindFor("latitude", f64, ""), KindAngle)
	eq(t, "lon 359.999999999", d("lon", 359.999999999), "00°00′00.00″")
	eq(t, "lon 360", d("lon", 360), "360°00′00.00″")
	eq(t, "lon -1e-9", d("lon", -1e-9), "00°00′00.00″")
	eq(t, "dec -1e-9", d("dec", -1e-9), "+00°00′00.00″")
	eq(t, "dec -0.0000013", d("dec", -0.0000013), "+00°00′00.00″")
	eq(t, "dms -0.000002", DegToDMS(-0.000002, true), "-00°00′00.01″")
	eq(t, "dms 10.999999999", DegToDMS(10.999999999, true), "+11°00′00.00″")
	eq(t, "dms 1e20", DegToDMS(1e20, false), "100000000000000004287°30′38.72″") // as Python: 1e20 * 360000 is inexact
}

// test_fmt_plots.py::test_percent
func TestPercent(t *testing.T) {
	eq(t, "all", Percent(1_290_773, 1_290_773), "100%")
	eq(t, "none", Percent(0, 10), "0%")
	eq(t, "70", Percent(350_100, 500_000), "70%")
	eq(t, "tiny", Percent(1, 1e6), "<0.01%")
	eq(t, "almost", Percent(999_999, 1e6), ">99.99%")
	eq(t, "99.95", Percent(9_995, 10_000), "99.95%")
	eq(t, "0.105", Percent(21, 20_000), "0.105%")
	eq(t, "of 0", Percent(5, 0), "")
}

// test_fmt_plots.py::test_human
func TestHuman(t *testing.T) {
	eq(t, "count", HumanCount(4_213_882_112), "4.21B")
	eq(t, "bytes", HumanBytes(35.4*1024*1024), "35.4 MiB")
	eq(t, "dict", ShortType(&arrow.DictionaryType{IndexType: arrow.PrimitiveTypes.Int32, ValueType: arrow.BinaryTypes.String}), "dict<str>")
	eq(t, "-0", HumanCount(math.Copysign(0, -1)), "0")
}

func digitsOv(n int) Override  { return Override{Digits: n, Set: true} }
func specOv(s string) Override { return Override{Spec: s, Set: true} }

func fo(v data.Value, k Kind, o Override) string {
	return Format(v, k, Opts{Width: DefaultWidth, Override: o})
}

// test_fmt_plots.py::test_overrides
func TestOverrides(t *testing.T) {
	eq(t, "angle 2", fo(12.3456789, KindAngle, digitsOv(2)), "12.35")
	eq(t, "flux 2", fo(28561.3, KindFlux, digitsOv(2)), "29000")
	eq(t, "flux .2e", fo(28561.3, KindFlux, specOv(".2e")), "2.86e+04")
	eq(t, "int ,d", fo(int64(1234567), KindInt, specOv(",d")), "1,234,567")
	eq(t, "int 3", fo(int64(1234567), KindInt, digitsOv(3)), "1234567")
	eq(t, "float ,d", fo(1.5, KindFloat, specOv(",d")), "1.5")
	eq(t, "raw wins", Format(1.5, KindFloat, Opts{Raw: true, Override: specOv(".3f")}), "1.5")
	eq(t, "null", fo(nil, KindFloat, specOv(".3f")), Null)
	eq(t, "nan", fo(math.NaN(), KindFloat, specOv(".3f")), "NaN")

	eq(t, "step angle", StepOverride(Override{}, KindAngle, -1), digitsOv(5))
	eq(t, "step flux", StepOverride(Override{}, KindFlux, 1), digitsOv(5))
	eq(t, "step mag 0", StepOverride(digitsOv(0), KindMag, -1), digitsOv(0))
	eq(t, "step err 1", StepOverride(digitsOv(1), KindErr, -1), digitsOv(1))
	eq(t, "step .3e", StepOverride(specOv(".3e"), KindFloat, 1), specOv(".4e"))
	eq(t, "step ,d", StepOverride(specOv(",d"), KindFlux, 1), digitsOv(5))
	eq(t, "step int", StepOverride(Override{}, KindInt, 1), Override{})
	eq(t, "step str", StepOverride(Override{}, KindStr, 1), Override{})

	eq(t, "describe .4f", DescribeOverride(digitsOv(4), KindAngle), ".4f")
	eq(t, "describe sig", DescribeOverride(digitsOv(4), KindFlux), "4 sig")
	eq(t, "describe spec", DescribeOverride(specOv(".2e"), KindFlux), ".2e")
	eq(t, "error .2f", OverrideError(specOv(".2f"), KindFloat, 1.5), "")
	eq(t, "error ,d", OverrideError(specOv(",d"), KindFloat, 1.5) != "", true)
	eq(t, "error ,d no kind", OverrideError(specOv(",d"), "", nil), "")
	eq(t, "error .2f time", OverrideError(specOv(".2f"), KindTime, nil) != "", true)
	eq(t, "error %Y-%m time", OverrideError(specOv("%Y-%m"), KindTime, nil), "")
	eq(t, "error %Y-%m", OverrideError(specOv("%Y-%m"), "", nil), "")
	eq(t, "error 4 int", OverrideError(digitsOv(4), KindInt, nil) != "", true)
	eq(t, "error 4 flux", OverrideError(digitsOv(4), KindFlux, nil), "")
	eq(t, "error 18", OverrideError(digitsOv(18), KindFlux, nil) != "", true)
	eq(t, "error huge", OverrideError(specOv(".100000000f"), "", nil) != "", true)
	eq(t, "error bool", OverrideError(specOv(".2f"), KindBool, nil) != "", true)
	eq(t, "error .2q", strings.Contains(OverrideError(specOv(".2q"), "", nil), "Unknown format code"), true)
	eq(t, "error strftime", OverrideError(specOv("%Y-2026 (UTC+0100)"), "", nil), "")
	eq(t, "error .100%", OverrideError(specOv(".100%"), "", nil) != "", true)
	eq(t, "zero override", OverrideError(Override{}, KindFloat, nil), "")
}

// test_fmt_plots.py::test_override_edge_cases
func TestOverrideEdgeCases(t *testing.T) {
	dec := data.Decimal{Unscaled: big.NewInt(123456), Scale: 5, Precision: 10}
	eq(t, "decimal 3", fo(dec, KindFloat, digitsOv(3)), "1.23")
	eq(t, "huge digits", fo(1.0, KindFloat, digitsOv(100_000_000)), fo(1.0, KindFloat, digitsOv(MaxDigits)))
	eq(t, "negative digits", fo(1.25, KindAngle, digitsOv(-3)), "1")
	eq(t, "bool spec", fo(true, KindBool, specOv(".2f")), "✓")
	eq(t, "cut", utf8.RuneCountInString(fo("x", KindStr, specOv("<200"))), 40)
	eq(t, "strftime", fo(data.Timestamp{T: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}, KindTime, specOv("%Y-%m")), "2026-01")
}

// test_config.py::test_parse_override
func TestParseOverride(t *testing.T) {
	eq(t, "3", ParseOverride(" 3 "), digitsOv(3))
	eq(t, "²", ParseOverride("²"), specOv("²"))
	eq(t, ".2e", ParseOverride(".2e"), specOv(".2e"))
	eq(t, "empty", ParseOverride(""), Override{})
	eq(t, "spaces", ParseOverride(" \t "), Override{})
	eq(t, "huge", OverrideError(ParseOverride("99999999999999999999999"), "", nil) != "", true)
}

// test_security.py: the hostile values

const esc = "\x1b"

var (
	oscTitle = esc + "]0;PWNED-TITLE\x07"
	osc52    = esc + "]52;c;ZWNobyBQV05FRAo=" + esc + "\\"
	osc8     = esc + "]8;;https://evil.example/" + esc + "\\click" + esc + "]8;;" + esc + "\\"
	c1CSI    = "\u009b"
	markup   = "[bold red]MARKUP[/] [@click=app.quit]clickme[/] [link=https://evil.example]lnk[/link]"
)

func evilValues() []string {
	return []string{
		"it's \"quoted\" \\ back'slash",
		"x' ); COPY (SELECT 1) TO '/x'; --",
		oscTitle + "TITLE-VAL",
		osc52 + "CLIP-VAL",
		osc8,
		markup,
		"[/]",
		esc + "[2J" + esc + "[31mRED",
		c1CSI + "31mC1" + "\u009d0;C1-TITLE\x07",
		"tab\there\nnew line",
	}
}

// controls are the C0 (but tab and newline), DEL and C1 characters of s.
func controls(s string) []rune {
	var out []rune
	for _, r := range s {
		if (r < 0x20 && r != '\t' && r != '\n') || (r >= 0x7F && r < 0xA0) {
			out = append(out, r)
		}
	}
	return out
}

func anyControls(s string) bool {
	for _, r := range s {
		if r < 0x20 || (r >= 0x7F && r < 0xA0) {
			return true
		}
	}
	return false
}

// test_security.py::test_sanitize_shows_controls_visibly
func TestSanitizeShowsControlsVisibly(t *testing.T) {
	s := "abc"
	if Sanitize(s, false) != s {
		t.Error("abc changed")
	}
	eq(t, "osc", Sanitize(esc+"]0;x\x07", false), "␛]0;x␇")
	eq(t, "c1", Sanitize("a\u009bb\x7f\x00", false), "a\\x9bb␡␀")
	eq(t, "ws", Sanitize("a\tb\nc", false), "a␉b␊c")
	eq(t, "keep ws", Sanitize("a\tb\nc\x1b", true), "a\tb\nc␛")
	eq(t, "unicode", Sanitize("é ✓ 漢字  ", false), "é ✓ 漢字  ")
	for _, v := range evilValues() {
		if c := controls(Sanitize(v, true)); len(c) > 0 {
			t.Errorf("%q: controls %q", v, c)
		}
		if anyControls(Sanitize(v, false)) {
			t.Errorf("%q: controls", v)
		}
	}
	eq(t, "has esc", HasControls(esc, false), true)
	eq(t, "keep ws has", HasControls("a\tb\n", true), false)
	eq(t, "tab has", HasControls("a\tb", false), true)
	eq(t, "bidi", Sanitize("abc\u202edcba", false), "abc⟨U+202E⟩dcba")
	for _, c := range "\u202a\u202b\u202c\u202d\u202e\u2066\u2067\u2068\u2069\u200e\u200f\u061c\u200b\u200c\u200d\u2060\ufeff" {
		eq(t, fmt.Sprintf("U+%04X", c), Sanitize("x"+string(c), true), fmt.Sprintf("x⟨U+%04X⟩", c))
		eq(t, fmt.Sprintf("has U+%04X", c), HasControls(string(c), false), true)
	}
}

// test_security.py::test_format_value_sanitizes_strings_and_nested_values
func TestFormatSanitizesStringsAndNestedValues(t *testing.T) {
	for _, v := range evilValues() {
		for _, raw := range []bool{false, true} {
			if c := controls(Format(v, KindStr, Opts{Raw: raw})); len(c) > 0 {
				t.Errorf("%q raw=%v: %q", v, raw, c)
			}
		}
		if c := controls(fo(v, KindStr, specOv(">30"))); len(c) > 0 {
			t.Errorf("%q >30: %q", v, c)
		}
	}
	nested := data.Struct{{Name: "k" + esc + "]0;K", Value: data.List{esc + "[2J", data.Struct{{Name: "x" + c1CSI, Value: "y\u009d"}}}}}
	for _, raw := range []bool{false, true} {
		if c := controls(Format(nested, KindNested, Opts{Raw: raw})); len(c) > 0 {
			t.Errorf("nested raw=%v: %q", raw, c)
		}
	}
	eq(t, "unsafe", strings.Contains(Format(esc+"x", KindStr, Opts{Unsafe: true}), esc), true)
	st := arrow.StructOf(arrow.Field{Name: "f" + esc + "]0;T", Type: arrow.PrimitiveTypes.Int32, Nullable: true})
	eq(t, "short type", anyControls(ShortType(st)), false)
	eq(t, "cell", len(controls(Cell(osc52, KindStr, Opts{Width: DefaultWidth}).Plain)), 0)
	// bytes that aren't UTF-8 (Python's strings can't hold them) are shown as \xff
	eq(t, "invalid utf8", Format("a\xffb\x9b", KindStr, Opts{}), "a\\xffb\\x9b")
}

func TestCellStyles(t *testing.T) {
	c := Cell(true, KindBool, Opts{})
	eq(t, "true", c.Style.Bold && c.Justify == 2 && c.Plain == "✓", true)
	c = Cell(false, KindBool, Opts{})
	eq(t, "false", c.Style.Dim && c.Justify == 2 && c.Plain == "·", true)
	c = Cell(float32(math.Inf(-1)), KindFloat32, Opts{})
	eq(t, "-inf", c.Style.Dim && c.Justify == 1 && c.Plain == "-∞", true)
	c = Cell(nil, KindStr, Opts{})
	eq(t, "null", c.Style.Dim && c.Justify == 0, true)
}

func TestOtherValues(t *testing.T) {
	eq(t, "uuid", fv(data.UUID{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 1, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}, KindStr),
		"01234567-89ab-cdef-0123-456789abcdef")
	eq(t, "duration", fv(data.Duration(1500*time.Millisecond), KindStr), "1.5s")
	eq(t, "interval", fv(data.Interval{Months: 1, Days: 2, Nanos: 3_723_000_004_000}, KindStr), "32 days, 1:02:03.000004")
	eq(t, "neg interval", fv(data.Interval{Nanos: -1000}, KindStr), "-1 day, 23:59:59.999999")
	eq(t, "ns ts", fv(data.Timestamp{T: time.Date(2020, 9, 13, 12, 26, 40, 123456789, time.UTC)}, KindTime), "2020-09-13 12:26:40.123456789")
	eq(t, "ns time", fv(data.TimeOfDay(5*3600e9+7), KindTime), "05:00:00.000000007")
	// wider than 38 digits: a float in the grid, as Python pqx has it; exact raw
	wide := data.Decimal{Unscaled: new(big.Int).Exp(big.NewInt(10), big.NewInt(60), nil), Scale: 10, Precision: 76}
	eq(t, "wide decimal", fv(wide, KindFloat), "1e+50")
	eq(t, "wide decimal angle", fv(wide, KindAngle), "100000000000000007629769841091887003294964970946560.000000")
	eq(t, "wide decimal spec", fo(wide, KindFloat, specOv(".3e")), "1.000e+50")
	eq(t, "wide decimal raw", Format(wide, KindFloat, Opts{Raw: true}), "1"+strings.Repeat("0", 50)+".0000000000")
	at38 := data.Decimal{Unscaled: big.NewInt(-30), Scale: 2, Precision: 38}
	eq(t, "38 digits", fv(at38, KindFloat), "-0.30")
	eq(t, "decimal sci", fv(data.Decimal{Unscaled: big.NewInt(1), Scale: 7}, KindFloat), "1E-7")
	eq(t, "map", fv(data.Map{{Key: "a", Value: int64(1)}}, KindNested), "[[a, 1]]")
}
